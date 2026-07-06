package vad

import (
	"context"
	"testing"

	"cross-livetranslate/internal/audio"
)

// frame builds a 1600-sample chunk of constant amplitude(양수 상수 → RMS=amp).
func frame(amp float32) audio.Chunk {
	c := make(audio.Chunk, audio.ChunkSamples)
	for i := range c {
		c[i] = amp
	}
	return c
}

const silent = 0.001 // RMS < threshold
const loud = 0.2     // RMS > threshold

// 무음만 넣으면 아무것도 통과하지 않고 speaking=false.
func TestSilenceNeverForwards(t *testing.T) {
	g := NewGate(Config{})
	for i := 0; i < 20; i++ {
		fwd, sp := g.Process(frame(silent))
		if len(fwd) != 0 {
			t.Fatalf("frame %d: silence forwarded %d chunks", i, len(fwd))
		}
		if sp {
			t.Fatalf("frame %d: silence marked speaking", i)
		}
	}
}

// 지속 발화는 onset 확정(MinSpeechFrames) 후 통과하고 speaking=true가 된다.
func TestSpeechForwards(t *testing.T) {
	g := NewGate(Config{})
	total := 0
	for i := 0; i < 10; i++ {
		fwd, _ := g.Process(frame(loud))
		total += len(fwd)
	}
	if !g.Speaking() {
		t.Fatal("should be speaking after sustained loud frames")
	}
	if total < 10 {
		t.Fatalf("expected >=10 forwarded frames, got %d", total)
	}
}

// pre-roll: onset 직전 무음 프레임이 확정 시 함께 flush된다.
func TestPreRollPreserved(t *testing.T) {
	cfg := Config{PreRollFrames: 2, MinSpeechFrames: 2}
	g := NewGate(cfg)
	// 무음 3프레임(pre-roll 링에 최근 2개 보관).
	for i := 0; i < 3; i++ {
		if fwd, _ := g.Process(frame(silent)); len(fwd) != 0 {
			t.Fatalf("silence should not forward")
		}
	}
	// 고에너지 프레임1: onset 미확정(speechRun=1) → 통과 없음.
	if fwd, _ := g.Process(frame(loud)); len(fwd) != 0 {
		t.Fatalf("first loud frame should be buffered, got %d", len(fwd))
	}
	// 고에너지 프레임2: onset 확정 → pre-roll(2) + pending(2) flush = 4.
	fwd, sp := g.Process(frame(loud))
	if !sp {
		t.Fatal("should be speaking after confirmation")
	}
	if len(fwd) != 4 {
		t.Fatalf("onset flush expected 2 pre-roll + 2 onset = 4, got %d", len(fwd))
	}
}

// hangover: 발화 후 무음이 와도 HangoverFrames 동안 계속 통과한다.
func TestHangover(t *testing.T) {
	cfg := Config{MinSpeechFrames: 1, HangoverFrames: 3, PreRollFrames: 0}
	g := NewGate(cfg)
	g.Process(frame(loud)) // onset 확정(min=1), speaking=true, hangover=3
	if !g.Speaking() {
		t.Fatal("should be speaking")
	}
	// 무음 프레임들: hangover 카운트다운(3,2,1) 동안 통과, 그 후 종료.
	forwarded := 0
	for i := 0; i < 5; i++ {
		fwd, _ := g.Process(frame(silent))
		forwarded += len(fwd)
	}
	if g.Speaking() {
		t.Fatal("should have stopped speaking after hangover expired")
	}
	// hangover=3 프레임 동안 통과(3번째에서 종료).
	if forwarded != 3 {
		t.Fatalf("hangover should forward exactly 3 silent frames, got %d", forwarded)
	}
}

// bypass: WrapSource(enabled=false)는 원본을 그대로 반환한다.
func TestWrapSourceBypass(t *testing.T) {
	base := &fakeSource{}
	got := WrapSource(base, false)
	if got != audio.Source(base) {
		t.Fatal("bypass should return the original source unchanged")
	}
}

// WrapSource(enabled=true)는 발화 청크만 onChunk로 전달한다.
func TestWrapSourceGates(t *testing.T) {
	base := &fakeSource{}
	wrapped := WrapSource(base, true)
	var received int
	_ = wrapped.Start(context.Background(), func(audio.Chunk) { received++ })
	// 무음 5 → 통과 없음.
	for i := 0; i < 5; i++ {
		base.emit(frame(silent))
	}
	if received != 0 {
		t.Fatalf("silence should not reach onChunk, got %d", received)
	}
	// 지속 발화 → 통과.
	for i := 0; i < 5; i++ {
		base.emit(frame(loud))
	}
	if received == 0 {
		t.Fatal("speech should reach onChunk")
	}
	_ = wrapped.Stop()
	if !base.stopped {
		t.Fatal("Stop should propagate to underlying source")
	}
}

// fakeSource is a controllable audio.Source for tests.
type fakeSource struct {
	onChunk func(audio.Chunk)
	stopped bool
}

func (f *fakeSource) Start(_ context.Context, onChunk func(audio.Chunk)) error {
	f.onChunk = onChunk
	return nil
}
func (f *fakeSource) Stop() error { f.stopped = true; return nil }
func (f *fakeSource) emit(c audio.Chunk) {
	if f.onChunk != nil {
		f.onChunk(c)
	}
}
