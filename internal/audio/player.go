//go:build cgo

// player.go — A3(Wave2): 번역 출력 오디오(24kHz mono Int16 LE PCM) 실시간 재생.
//
// 원본 이식: liveTranslate/Sources/Audio/TranslatedAudioPlayer.swift
//   (AVAudioEngine+AVAudioPlayerNode 스트리밍, in-flight 백프레셔, 40청크 dedup 윈도우,
//    interrupt flush, 소프트 게인 + tanh 소프트 리미터).
// 여기서는 malgo(miniaudio) **playback** 디바이스 + 링버퍼로 옮긴다:
//   - Enqueue(int16LE): dedup → 백프레셔 검사 → Int16→Float32(게인+리미터) → 링버퍼 write.
//   - malgo 재생 콜백(F32 mono)이 링버퍼에서 read 해 출력. 언더런은 무음(0)으로 패딩.
//
// cgo가 필요하므로 `//go:build cgo`로 격리한다(capture_malgo.go와 동일 규약). CGO 비활성
// 크로스빌드에서는 제외되어 audio 패키지의 순수 부분만 컴파일된다.
//
// 피드백 차단 주의: 재생된 번역 음성이 마이크/루프백 입력으로 재유입되면 무한 재번역이
// 발생한다. 현재 mic 입력에서는 사용자 볼륨/헤드폰 사용이 책임이며, 무설치 시스템 탭(P2b)
// 도입 시 자기 프로세스 오디오를 캡처에서 제외해 근본 차단할 예정이다(spec 012 F10).
package audio

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/gen2brain/malgo"
)

const (
	// PlaybackSampleRate is the Gemini 출력 오디오 샘플레이트(24kHz mono).
	PlaybackSampleRate = 24000
	// maxInFlightSamples caps buffered(미재생) 샘플 수 ≈ 3초(24kHz) — 원본 maxInFlightFrames.
	// 초과 시 신규 Enqueue를 드롭해 수신>재생 드리프트(재생 지연 누적)를 막는다.
	maxInFlightSamples = PlaybackSampleRate * 3
	// ringCapacitySamples: 링버퍼 물리 용량(임계 + 1초 여유). 백프레셔가 임계에서 드롭하므로
	// 정상 경로는 이 용량을 넘지 않는다.
	ringCapacitySamples = PlaybackSampleRate * 4
	// dedupWindow: 최근 청크 슬라이딩 윈도우 크기(원본 recentChunkWindow=40).
	dedupWindow = 40
)

// ringBuffer is a fixed-capacity, mutex-guarded float32 ring buffer. Enqueue 경로
// (write)와 실시간 재생 콜백(read)이 공유하므로 짧은 임계구역으로 보호한다.
type ringBuffer struct {
	mu   sync.Mutex
	buf  []float32
	head int // 다음 read 위치
	size int // 유효 샘플 수
}

func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{buf: make([]float32, capacity)}
}

// length returns the number of buffered(미재생) samples.
func (r *ringBuffer) length() int {
	r.mu.Lock()
	n := r.size
	r.mu.Unlock()
	return n
}

// write appends samples, dropping any that don't fit (백프레셔는 호출자가 선검사).
func (r *ringBuffer) write(samples []float32) {
	r.mu.Lock()
	capacity := len(r.buf)
	for _, s := range samples {
		if r.size >= capacity {
			break // 용량 초과분은 폐기(정상 경로는 임계 선검사로 도달하지 않음).
		}
		w := (r.head + r.size) % capacity
		r.buf[w] = s
		r.size++
	}
	r.mu.Unlock()
}

// read fills out from the buffer and returns the number of samples copied
// (부족분은 호출자가 무음으로 패딩). 실시간 재생 콜백에서 호출.
func (r *ringBuffer) read(out []float32) int {
	r.mu.Lock()
	capacity := len(r.buf)
	n := len(out)
	if n > r.size {
		n = r.size
	}
	for i := 0; i < n; i++ {
		out[i] = r.buf[(r.head+i)%capacity]
	}
	r.head = (r.head + n) % capacity
	r.size -= n
	r.mu.Unlock()
	return n
}

// clear discards all buffered samples (flush/stop).
func (r *ringBuffer) clear() {
	r.mu.Lock()
	r.head = 0
	r.size = 0
	r.mu.Unlock()
}

// Player renders translated output audio via a malgo playback device.
// 24kHz mono, Float32 출력(게인/리미터를 Enqueue 단계에서 Float32에 적용해 저장).
type Player struct {
	mu       sync.Mutex
	mctx     *malgo.AllocatedContext
	device   *malgo.Device
	ring     *ringBuffer
	started  bool
	deviceID string // "" → 시스템 기본 출력

	// gainBits holds the current soft gain(소프트 볼륨 × 덕킹 게인보상) as float64 bits.
	// 실시간 콜백은 read하지 않고 Enqueue(단일 소비 goroutine)만 읽으므로 atomic으로 충분.
	gainBits atomic.Uint64

	// 진단 카운터(Stats — controller가 로깅해 데이터 흐름 검증).
	enqueuedBytes atomic.Uint64
	droppedChunks atomic.Uint64 // 백프레셔 드롭
	dupSkipped    atomic.Uint64 // dedup 윈도우 skip

	// dedup 슬라이딩 윈도우(원본 recentChunks). Enqueue(단일 goroutine) + reset(Start/Stop/Flush)
	// 에서 접근 → dedupMu로 보호.
	dedupMu    sync.Mutex
	recent     []dedupEntry
	recentHash map[uint64]int

	// scratch is a reusable read buffer for the realtime callback(단일 오디오 스레드 전용).
	scratch []float32
}

type dedupEntry struct {
	hash uint64
	data []byte
}

// NewPlayer returns an unstarted Player on the system default output device.
func NewPlayer() *Player {
	p := &Player{
		ring:       newRingBuffer(ringCapacitySamples),
		recentHash: make(map[uint64]int),
	}
	p.gainBits.Store(math.Float64bits(1.0))
	return p
}

// SetOutputDevice selects the playback output device by ID (EnumerateOutputDevices의
// DeviceInfo.ID). ""이면 시스템 기본 출력. 재생 중이면 다음 Start()에 반영되도록 재구성한다.
func (p *Player) SetOutputDevice(id string) {
	p.mu.Lock()
	if p.deviceID == id {
		p.mu.Unlock()
		return
	}
	p.deviceID = id
	wasStarted := p.started
	p.mu.Unlock()
	if wasStarted {
		// 재생 중 장치 변경: 정지 후 재시작(멱등). 실패해도 stop 상태로 안전 수렴.
		_ = p.Stop()
		_ = p.Start()
	}
}

// SetGain sets the software playback gain (소프트 볼륨 × 덕킹 게인보상). 1.0=무증폭.
// g>1이면 Enqueue에서 tanh 소프트 리미터로 피크를 ±1 내로 압축한다(원본 정책).
func (p *Player) SetGain(g float64) {
	if g < 0 {
		g = 0
	}
	p.gainBits.Store(math.Float64bits(g))
}

func (p *Player) gain() float64 { return math.Float64frombits(p.gainBits.Load()) }

// Start opens the playback device and begins draining the ring buffer. Idempotent.
func (p *Player) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return nil
	}

	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return fmt.Errorf("audio: init malgo context (player): %w", err)
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Playback)
	deviceConfig.Playback.Format = malgo.FormatF32
	deviceConfig.Playback.Channels = 1
	deviceConfig.SampleRate = PlaybackSampleRate

	// 출력 장치 선택: capture_malgo.go와 동일하게 runtime.Pinner로 &selectedID를 핀해
	// C(InitDevice)로 넘긴다(Go 1.26 cgocheck 준수). miniaudio가 값 복사하므로 이후 핀 불필요.
	var selectedID malgo.DeviceID
	var pinner runtime.Pinner
	defer pinner.Unpin()
	if p.deviceID != "" {
		devs, derr := mctx.Devices(malgo.Playback)
		if derr == nil {
			for i := range devs {
				if devs[i].ID.String() == p.deviceID {
					selectedID = devs[i].ID
					pinner.Pin(&selectedID)
					deviceConfig.Playback.DeviceID = unsafe.Pointer(&selectedID)
					break
				}
			}
		}
		// 미발견/열거 실패 시 조용히 기본 출력으로 폴백(DeviceID nil 유지).
	}

	onSend := func(pOutput, _ []byte, framecount uint32) {
		n := int(framecount)
		if cap(p.scratch) < n {
			p.scratch = make([]float32, n)
		}
		buf := p.scratch[:n]
		got := p.ring.read(buf)
		for i := 0; i < n; i++ {
			var v float32
			if i < got {
				v = buf[i]
			}
			binary.LittleEndian.PutUint32(pOutput[i*4:], math.Float32bits(v))
		}
	}

	device, err := malgo.InitDevice(mctx.Context, deviceConfig, malgo.DeviceCallbacks{Data: onSend})
	if err != nil {
		_ = mctx.Uninit()
		mctx.Free()
		return fmt.Errorf("audio: init playback device: %w", err)
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		_ = mctx.Uninit()
		mctx.Free()
		return fmt.Errorf("audio: start playback device: %w", err)
	}

	p.mctx = mctx
	p.device = device
	p.started = true
	p.ring.clear()
	p.resetDedup()
	return nil
}

// Stop tears down the playback device and clears buffers. Idempotent.
func (p *Player) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return nil
	}
	p.started = false
	if p.device != nil {
		p.device.Uninit()
		p.device = nil
	}
	if p.mctx != nil {
		_ = p.mctx.Uninit()
		p.mctx.Free()
		p.mctx = nil
	}
	p.ring.clear()
	p.resetDedup()
	return nil
}

// Flush discards queued(미재생) audio without stopping the device (서버 interrupted 대응).
// 진행 중 번역 오디오를 끊되 재생은 계속 가능한 상태로 둔다.
func (p *Player) Flush() {
	p.ring.clear()
	p.resetDedup()
}

// Enqueue converts a 24kHz mono Int16 LE PCM chunk to Float32(게인+리미터 적용) and
// writes it to the ring buffer. 재생 중이 아니거나 빈 데이터면 무시. 백프레셔/ dedup 드롭.
func (p *Player) Enqueue(int16LE []byte) {
	p.mu.Lock()
	started := p.started
	p.mu.Unlock()
	if !started || len(int16LE) < 2 {
		return
	}

	// dedup: 최근 40청크 윈도우에 바이트가 완전히 동일한 청크가 있으면 skip(원본 수정4).
	// hash로 빠르게 거른 뒤 바이트 동등성까지 확인해 해시 충돌을 방어한다.
	h := fnv.New64a()
	_, _ = h.Write(int16LE)
	chunkHash := h.Sum64()
	if p.isDuplicate(chunkHash, int16LE) {
		p.dupSkipped.Add(1)
		return
	}

	// 백프레셔: 미재생 샘플이 임계(≈3초)를 넘으면 신규 청크 드롭.
	if p.ring.length() >= maxInFlightSamples {
		p.droppedChunks.Add(1)
		return
	}

	g := p.gain()
	nSamples := len(int16LE) / 2
	out := make([]float32, nSamples)
	limit := g > 1.0
	for i := 0; i < nSamples; i++ {
		s := int16(binary.LittleEndian.Uint16(int16LE[2*i:]))
		f := float64(s) / 32768.0 * g
		if limit {
			// 게인 보상: tanh 소프트 리미터로 피크를 ±1 내로 부드럽게 압축(하드 클리핑 방지).
			out[i] = float32(math.Tanh(f))
		} else {
			// 무증폭/감쇠: 선형 + 하드 클램프(왜곡 없음).
			if f > 1 {
				f = 1
			} else if f < -1 {
				f = -1
			}
			out[i] = float32(f)
		}
	}

	p.ring.write(out)
	p.enqueuedBytes.Add(uint64(len(int16LE)))
	p.recordChunk(chunkHash, int16LE)
}

// isDuplicate reports whether an identical chunk exists in the dedup window.
func (p *Player) isDuplicate(hash uint64, data []byte) bool {
	p.dedupMu.Lock()
	defer p.dedupMu.Unlock()
	if p.recentHash[hash] == 0 {
		return false
	}
	for i := range p.recent {
		if p.recent[i].hash == hash && bytesEqual(p.recent[i].data, data) {
			return true
		}
	}
	return false
}

// recordChunk appends a chunk to the dedup window, evicting the oldest past the cap.
func (p *Player) recordChunk(hash uint64, data []byte) {
	cp := make([]byte, len(data))
	copy(cp, data)
	p.dedupMu.Lock()
	p.recent = append(p.recent, dedupEntry{hash: hash, data: cp})
	p.recentHash[hash]++
	for len(p.recent) > dedupWindow {
		old := p.recent[0]
		p.recent = p.recent[1:]
		if c := p.recentHash[old.hash]; c <= 1 {
			delete(p.recentHash, old.hash)
		} else {
			p.recentHash[old.hash] = c - 1
		}
	}
	p.dedupMu.Unlock()
}

// resetDedup clears the dedup window (start/stop/flush).
func (p *Player) resetDedup() {
	p.dedupMu.Lock()
	p.recent = p.recent[:0]
	p.recentHash = make(map[uint64]int)
	p.dedupMu.Unlock()
}

// PlayerStats is a snapshot of playback diagnostics (controller가 로깅).
type PlayerStats struct {
	EnqueuedBytes uint64
	DroppedChunks uint64 // 백프레셔 드롭
	DupSkipped    uint64 // dedup 윈도우 skip
	BufferedMS    int    // 현재 링버퍼 잔여(ms)
}

// Stats returns a diagnostics snapshot for logging/verification.
func (p *Player) Stats() PlayerStats {
	return PlayerStats{
		EnqueuedBytes: p.enqueuedBytes.Load(),
		DroppedChunks: p.droppedChunks.Load(),
		DupSkipped:    p.dupSkipped.Load(),
		BufferedMS:    p.ring.length() * 1000 / PlaybackSampleRate,
	}
}

// bytesEqual reports byte-wise equality (avoids importing bytes just for this).
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
