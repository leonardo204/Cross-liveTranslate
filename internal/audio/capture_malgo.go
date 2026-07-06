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
	"sync"
	"sync/atomic"

	"github.com/gen2brain/malgo"
)

// MalgoSource is a malgo/miniaudio-backed microphone Source. It configures the
// capture device as 16kHz / mono / Float32 (miniaudio가 내부 리샘플링) and emits
// ChunkSamples-sized Chunks. It implements Source.
type MalgoSource struct {
	mu      sync.Mutex
	mctx    *malgo.AllocatedContext
	device  *malgo.Device
	ch      chan Chunk
	done    chan struct{}
	chunker *chunker
	dropped atomic.Uint64
	started bool
}

// NewMalgoSource returns an unstarted malgo capture Source.
func NewMalgoSource() *MalgoSource { return &MalgoSource{} }

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

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatF32
	deviceConfig.Capture.Channels = 1
	deviceConfig.SampleRate = SampleRate

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
