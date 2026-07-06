package audio

import "testing"

// 1600 경계에서 정확히 프레임이 잘리는지 검증한다.
func TestChunker_Boundary(t *testing.T) {
	c := newChunker(ChunkSamples)

	// 1599개: 아직 프레임 없음.
	if frames := c.push(make([]float32, ChunkSamples-1)); len(frames) != 0 {
		t.Fatalf("1599 push: got %d frames want 0", len(frames))
	}
	// 1개 추가 → 정확히 1프레임(1600).
	frames := c.push(make([]float32, 1))
	if len(frames) != 1 {
		t.Fatalf("1600 경계: got %d frames want 1", len(frames))
	}
	if len(frames[0]) != ChunkSamples {
		t.Fatalf("프레임 길이: got %d want %d", len(frames[0]), ChunkSamples)
	}
}

// 큰 입력이 여러 프레임으로 분할되고 잔여가 다음으로 이월되는지 검증한다.
func TestChunker_MultipleAndRemainder(t *testing.T) {
	c := newChunker(ChunkSamples)
	// 2.5 프레임 = 4000 샘플 → 2프레임 + 800 잔여.
	frames := c.push(make([]float32, ChunkSamples*2+800))
	if len(frames) != 2 {
		t.Fatalf("2.5 프레임: got %d want 2", len(frames))
	}
	for i, f := range frames {
		if len(f) != ChunkSamples {
			t.Fatalf("프레임[%d] 길이: got %d want %d", i, len(f), ChunkSamples)
		}
	}
	// 800 잔여 + 800 = 1600 → 1프레임.
	frames = c.push(make([]float32, 800))
	if len(frames) != 1 {
		t.Fatalf("잔여 이월: got %d want 1", len(frames))
	}
}

// 반환된 프레임이 내부 버퍼와 공유되지 않는(복사본) 것을 검증한다.
func TestChunker_FramesAreCopies(t *testing.T) {
	c := newChunker(4)
	in := []float32{1, 2, 3, 4, 5, 6, 7, 8}
	frames := c.push(in)
	if len(frames) != 2 {
		t.Fatalf("got %d frames want 2", len(frames))
	}
	// 첫 프레임을 변조해도 두 번째 프레임/후속 push에 영향 없어야 한다.
	frames[0][0] = 999
	if frames[1][0] != 5 {
		t.Errorf("프레임 간 aliasing 발견: frames[1][0]=%v want 5", frames[1][0])
	}
}

// ChunkSamples 상수가 계약값(1600)인지 확인한다.
func TestChunkSamplesConstant(t *testing.T) {
	if ChunkSamples != 1600 {
		t.Fatalf("ChunkSamples: got %d want 1600", ChunkSamples)
	}
	if SampleRate != 16000 {
		t.Fatalf("SampleRate: got %d want 16000", SampleRate)
	}
}
