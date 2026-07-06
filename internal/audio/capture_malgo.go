//go:build cgo

// capture_malgo.go — malgo(miniaudio) 기반 마이크 캡처.
//
// cgo가 필요하므로 `//go:build cgo`로 격리한다. CGO 비활성 크로스빌드(예: mingw
// 없는 GOOS=windows)에서는 이 파일이 제외되어 audio 패키지의 순수 부분만 컴파일된다.
// 덕분에 이 패키지를 임포트하는 순수 패키지(pipeline 등)도 크로스빌드가 가능하다.
package audio

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/gen2brain/malgo"
)

// MalgoSource is a malgo/miniaudio-backed capture Source. It configures the
// device as 16kHz / mono / Float32 (miniaudio가 내부 리샘플링) and emits
// ChunkSamples-sized Chunks. It implements Source.
//
// deviceType selects the miniaudio device type: malgo.Capture (기본 마이크/입력장치)
// or malgo.Loopback (시스템 출력 캡처 — WASAPI, windows 전용). deviceID(비어있지 않으면)
// 로 특정 캡처 장치를 선택한다(EnumerateDevices의 DeviceInfo.ID와 매칭).
type MalgoSource struct {
	mu         sync.Mutex
	mctx       *malgo.AllocatedContext
	device     *malgo.Device
	ch         chan Chunk
	done       chan struct{}
	chunker    *chunker
	dropped    atomic.Uint64
	started    bool
	deviceType malgo.DeviceType // 0 → Capture (기본)
	deviceID   string           // "" → 기본 장치
}

// NewMalgoSource returns an unstarted malgo capture Source on the default
// input device (마이크).
func NewMalgoSource() *MalgoSource { return &MalgoSource{deviceType: malgo.Capture} }

// NewMalgoSourceForDevice returns an unstarted malgo capture Source bound to a
// specific capture device (EnumerateDevices의 DeviceInfo.ID). id가 ""이면 기본 장치.
func NewMalgoSourceForDevice(id string) *MalgoSource {
	return &MalgoSource{deviceType: malgo.Capture, deviceID: id}
}

// effectiveDeviceType returns Capture when unset (zero value 안전).
func (s *MalgoSource) effectiveDeviceType() malgo.DeviceType {
	if s.deviceType == 0 {
		return malgo.Capture
	}
	return s.deviceType
}

// Start opens the capture device and begins delivering Chunks to onChunk.
// The realtime malgo callback only enqueues to a bounded channel (non-blocking:
// full → drop + counter); a dispatch goroutine drains it into onChunk so onChunk
// never runs on the audio thread. Cancelling ctx stops capture.
func (s *MalgoSource) Start(ctx context.Context, onChunk func(Chunk)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return fmt.Errorf("audio: source already started")
	}

	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return fmt.Errorf("audio: init malgo context: %w", err)
	}

	s.ch = make(chan Chunk, 32)
	s.done = make(chan struct{})
	s.chunker = newChunker(ChunkSamples)

	deviceConfig := malgo.DefaultDeviceConfig(s.effectiveDeviceType())
	deviceConfig.Capture.Format = malgo.FormatF32
	deviceConfig.Capture.Channels = 1
	deviceConfig.SampleRate = SampleRate

	// 특정 캡처 장치 선택: EnumerateDevices의 DeviceInfo.ID(malgo DeviceID hex)와 매칭해
	// 그 장치의 malgo.DeviceID 포인터를 Capture.DeviceID로 지정한다.
	// Go 1.26 cgocheck는 C(InitDevice)로 넘어가는 구조체 내부의 Go 포인터를 거부하므로
	// runtime.Pinner로 &selectedID를 핀해 전달을 허용한다(Start 반환 시 Unpin). miniaudio는
	// InitDevice 시점에 값을 복사하므로 그 이후엔 핀이 필요 없다. deviceID가 비면 기본 장치(nil).
	var selectedID malgo.DeviceID
	var pinner runtime.Pinner
	defer pinner.Unpin()
	if s.deviceID != "" {
		devs, derr := mctx.Devices(malgo.Capture)
		if derr != nil {
			_ = mctx.Uninit()
			mctx.Free()
			return fmt.Errorf("audio: enumerate for device select: %w", derr)
		}
		found := false
		for i := range devs {
			if devs[i].ID.String() == s.deviceID {
				selectedID = devs[i].ID
				found = true
				break
			}
		}
		if !found {
			_ = mctx.Uninit()
			mctx.Free()
			return fmt.Errorf("audio: capture device %q not found", s.deviceID)
		}
		pinner.Pin(&selectedID)
		deviceConfig.Capture.DeviceID = unsafe.Pointer(&selectedID)
	}

	onRecv := func(_, inputSamples []byte, _ uint32) {
		// inputSamples: N*4 bytes (Float32 mono little-endian).
		n := len(inputSamples) / 4
		if n == 0 {
			return
		}
		buf := make([]float32, n)
		for i := 0; i < n; i++ {
			bits := binary.LittleEndian.Uint32(inputSamples[i*4:])
			buf[i] = math.Float32frombits(bits)
		}
		for _, frame := range s.chunker.push(buf) {
			// 논블로킹: 채널이 가득 차면 드롭하고 카운터만 증가(백프레셔 계약).
			select {
			case s.ch <- frame:
			default:
				s.dropped.Add(1)
			}
		}
	}

	device, err := malgo.InitDevice(mctx.Context, deviceConfig, malgo.DeviceCallbacks{Data: onRecv})
	// selectedID는 pinner가 Start 반환까지 핀 유지(InitDevice가 값 복사 완료 후 안전).
	if err != nil {
		_ = mctx.Uninit()
		mctx.Free()
		return fmt.Errorf("audio: init capture device: %w", err)
	}

	// dispatch goroutine: 채널 → onChunk. ctx/Stop로 종료.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.done:
				return
			case c := <-s.ch:
				onChunk(c)
			}
		}
	}()

	if err := device.Start(); err != nil {
		device.Uninit()
		_ = mctx.Uninit()
		mctx.Free()
		return fmt.Errorf("audio: start capture device: %w", err)
	}

	s.mctx = mctx
	s.device = device
	s.started = true

	// ctx 취소 시 자동 정리.
	go func() {
		<-ctx.Done()
		_ = s.Stop()
	}()

	return nil
}

// Stop tears down the capture device and dispatch goroutine. Idempotent.
func (s *MalgoSource) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return nil
	}
	s.started = false
	close(s.done)
	if s.device != nil {
		s.device.Uninit()
		s.device = nil
	}
	if s.mctx != nil {
		_ = s.mctx.Uninit()
		s.mctx.Free()
		s.mctx = nil
	}
	return nil
}

// Dropped returns the number of Chunks dropped due to backpressure.
func (s *MalgoSource) Dropped() uint64 { return s.dropped.Load() }
