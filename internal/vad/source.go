package vad

import (
	"context"

	"cross-livetranslate/internal/audio"
)

// WrapSource returns an audio.Source that gates the wrapped source's chunks
// through an energy-based speech gate. enabled=false면 원본을 그대로 반환한다(bypass:
// 전부 통과, 게이트/할당 없음). 원본 VADGate의 소스 래핑 계약을 따르되 reconciler/gemini는
// 변경하지 않는다 — controller의 newSource 팩토리에서 Settings.VAD.Enabled면 감싼다.
func WrapSource(src audio.Source, enabled bool) audio.Source {
	if !enabled {
		return src
	}
	return &gatedSource{src: src, cfg: Config{}.Normalize()}
}

// gatedSource forwards only speech chunks from the underlying source.
type gatedSource struct {
	src audio.Source
	cfg Config
}

// Start wires the underlying source with a per-Start gate. onChunk(사용자 콜백)은
// 발화 구간 청크만 받는다. Source 계약상 onChunk는 단일 dispatch goroutine에서 호출되므로
// 게이트 상태는 락 없이 직렬 접근된다.
func (g *gatedSource) Start(ctx context.Context, onChunk func(audio.Chunk)) error {
	gate := NewGate(g.cfg)
	return g.src.Start(ctx, func(c audio.Chunk) {
		fwd, _ := gate.Process(c)
		for _, out := range fwd {
			onChunk(out)
		}
	})
}

// Stop tears down the underlying source.
func (g *gatedSource) Stop() error { return g.src.Stop() }
