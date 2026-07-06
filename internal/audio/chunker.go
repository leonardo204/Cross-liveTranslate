package audio

// chunker accumulates Float32 samples and slices them into fixed-size frames.
//
// It is a pure helper (no audio backend / no cgo) so the ChunkSamples(1600)
// boundary logic can be unit-tested independently of malgo. The realtime malgo
// callback feeds arbitrary-sized buffers here and forwards the complete frames.
type chunker struct {
	size int
	buf  []float32
}

// newChunker returns a chunker emitting frames of exactly size samples.
func newChunker(size int) *chunker {
	return &chunker{size: size, buf: make([]float32, 0, size*2)}
}

// push appends samples and returns any complete frames (each len == size).
// Each returned frame is a fresh copy, safe to hand to another goroutine.
// The unconsumed tail (< size) is retained for the next push.
func (c *chunker) push(samples []float32) []Chunk {
	c.buf = append(c.buf, samples...)
	var out []Chunk
	for len(c.buf) >= c.size {
		frame := make(Chunk, c.size)
		copy(frame, c.buf[:c.size])
		out = append(out, frame)
		// 남은 꼬리를 버퍼 앞으로 당겨 backing array를 재사용한다.
		c.buf = append(c.buf[:0], c.buf[c.size:]...)
	}
	return out
}
