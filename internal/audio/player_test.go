//go:build cgo

package audio

import (
	"encoding/binary"
	"math"
	"testing"
)

// int16LE builds a little-endian Int16 PCM byte slice from samples.
func int16LE(samples ...int16) []byte {
	b := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

// newStartedPlayer returns a Player marked started without opening a real device
// (테스트에서 malgo 재생 디바이스 없이 Enqueue 경로만 검증). ring/ dedup만 채워진다.
func newStartedPlayer() *Player {
	p := NewPlayer()
	p.started = true
	return p
}

func TestRingBufferWriteReadClear(t *testing.T) {
	r := newRingBuffer(4)
	r.write([]float32{1, 2, 3, 4, 5}) // 5번째는 용량 초과 → 폐기.
	if got := r.length(); got != 4 {
		t.Fatalf("length after over-write = %d, want 4", got)
	}
	out := make([]float32, 3)
	if n := r.read(out); n != 3 {
		t.Fatalf("read n = %d, want 3", n)
	}
	if out[0] != 1 || out[1] != 2 || out[2] != 3 {
		t.Fatalf("read values = %v, want [1 2 3]", out)
	}
	if got := r.length(); got != 1 {
		t.Fatalf("length after read = %d, want 1", got)
	}
	// 언더런: 요청 > 잔여 → 잔여만 채우고 나머지 out은 호출자 책임.
	out2 := make([]float32, 3)
	if n := r.read(out2); n != 1 {
		t.Fatalf("underrun read n = %d, want 1", n)
	}
	r.write([]float32{9})
	r.clear()
	if got := r.length(); got != 0 {
		t.Fatalf("length after clear = %d, want 0", got)
	}
}

func TestEnqueueGainUnityLinear(t *testing.T) {
	p := newStartedPlayer()
	// 전체 스케일 양/음 + 0. gain=1 → 선형(v/32768).
	p.Enqueue(int16LE(16384, -16384, 0))
	out := make([]float32, 3)
	if n := p.ring.read(out); n != 3 {
		t.Fatalf("ring read n = %d, want 3", n)
	}
	if math.Abs(float64(out[0])-0.5) > 1e-4 {
		t.Errorf("out[0] = %f, want ~0.5", out[0])
	}
	if math.Abs(float64(out[1])+0.5) > 1e-4 {
		t.Errorf("out[1] = %f, want ~-0.5", out[1])
	}
	if out[2] != 0 {
		t.Errorf("out[2] = %f, want 0", out[2])
	}
	if p.Stats().EnqueuedBytes != 6 {
		t.Errorf("EnqueuedBytes = %d, want 6", p.Stats().EnqueuedBytes)
	}
}

func TestEnqueueGainTanhLimiter(t *testing.T) {
	p := newStartedPlayer()
	p.SetGain(4.0)
	// 전체 스케일 입력에 gain=4 → tanh 소프트 리미터로 ±1 미만 압축(하드 클립 아님).
	p.Enqueue(int16LE(32767, -32767))
	out := make([]float32, 2)
	p.ring.read(out)
	if out[0] <= 0.9 || out[0] >= 1.0 {
		t.Errorf("tanh limited positive = %f, want in (0.9,1.0)", out[0])
	}
	if out[1] >= -0.9 || out[1] <= -1.0 {
		t.Errorf("tanh limited negative = %f, want in (-1.0,-0.9)", out[1])
	}
}

func TestEnqueueDedupWindow(t *testing.T) {
	p := newStartedPlayer()
	chunk := int16LE(100, 200, 300)
	p.Enqueue(chunk)
	p.Enqueue(chunk) // 동일 바이트 → dedup skip.
	if got := p.Stats().DupSkipped; got != 1 {
		t.Fatalf("DupSkipped = %d, want 1", got)
	}
	if got := p.ring.length(); got != 3 {
		t.Fatalf("ring length = %d, want 3 (second skipped)", got)
	}
	// 다른 청크는 통과.
	p.Enqueue(int16LE(101, 200, 300))
	if got := p.ring.length(); got != 6 {
		t.Fatalf("ring length = %d, want 6", got)
	}
}

func TestEnqueueBackpressureDrop(t *testing.T) {
	p := newStartedPlayer()
	// 임계(maxInFlightSamples) 이상 차면 신규 청크 드롭. 서로 다른 내용으로 dedup 회피.
	chunk := make([]int16, PlaybackSampleRate) // 1초(24000 샘플).
	for i := range chunk {
		chunk[i] = int16(i % 1000)
	}
	// 3청크 = 72000 = 임계. 4번째부터 드롭.
	for i := 0; i < 5; i++ {
		c := make([]int16, len(chunk))
		copy(c, chunk)
		c[0] = int16(i) // 청크마다 구분(dedup 회피).
		p.Enqueue(int16LE(c...))
	}
	if got := p.Stats().DroppedChunks; got == 0 {
		t.Fatalf("DroppedChunks = 0, want >0 (백프레셔 미동작)")
	}
	if got := p.ring.length(); got > ringCapacitySamples {
		t.Fatalf("ring length %d exceeds capacity %d", got, ringCapacitySamples)
	}
}

func TestFlushClearsRing(t *testing.T) {
	p := newStartedPlayer()
	p.Enqueue(int16LE(1, 2, 3, 4))
	if p.ring.length() == 0 {
		t.Fatal("ring empty before flush")
	}
	p.Flush()
	if got := p.ring.length(); got != 0 {
		t.Fatalf("ring length after flush = %d, want 0", got)
	}
}

func TestEnqueueDropsWhenNotStarted(t *testing.T) {
	p := NewPlayer() // started=false.
	p.Enqueue(int16LE(1, 2, 3))
	if p.ring.length() != 0 {
		t.Fatalf("enqueue while stopped wrote %d samples, want 0", p.ring.length())
	}
}

func TestNoopDuckerSafe(t *testing.T) {
	d := noopDucker{}
	if d.IsSupported() {
		t.Error("noopDucker.IsSupported() = true, want false")
	}
	d.Duck(0.3) // no panic.
	d.Restore() // no panic.
}
