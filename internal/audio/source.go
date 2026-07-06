package audio

import "context"

// P1 오디오 파이프라인 계약 상수. 값은 원본 liveTranslate AppConfig.swift에서 이식:
//   SampleRate   = 16000  (AppConfig.audioSampleRate)
//   ChunkMillis  = 100    (AppConfig.audioChunkMilliseconds)
//   ChunkSamples = 1600   (AppConfig.audioChunkSampleCount = 16000*100/1000)
const (
	// SampleRate is the Gemini Live 송신 샘플레이트(16kHz mono).
	SampleRate = 16000
	// ChunkMillis is the 청크 길이(ms).
	ChunkMillis = 100
	// ChunkSamples is 100ms 청크당 샘플 수(16kHz mono) = 1600.
	ChunkSamples = SampleRate * ChunkMillis / 1000
)

// Chunk is a 100ms mono frame of Float32 PCM in [-1, 1].
// Length is ChunkSamples (1600) for frames emitted by a Source.
type Chunk []float32

// Source captures microphone audio as 16kHz mono Float32 and delivers it in
// ChunkSamples-sized Chunks via onChunk.
//
// Start is non-blocking: it wires up the capture backend and returns once the
// device is running. onChunk is invoked from a dispatch goroutine (never the
// realtime audio callback) so consumers may perform light work without risking
// audio glitches. Backpressure is handled by the implementation (drop + count).
// Stop tears down the device. Cancelling ctx also stops capture.
type Source interface {
	Start(ctx context.Context, onChunk func(Chunk)) error
	Stop() error
}
