package vad

import (
	"context"
	"time"

	"cross-livetranslate/internal/audio"
)

// WrapSource returns an audio.Source that gates the wrapped source's chunks
// through an energy-based speech gate. enabled=false면 원본을 그대로 반환한다(bypass:
// 전부 통과, 게이트/할당 없음). 원본 VADGate의 소스 래핑 계약을 따르되 reconciler/gemini는
// 변경하지 않는다 — controller의 newSource 팩토리에서 Settings.VAD.Enabled면 감싼다.
func WrapSource(src audio.Source, enabled bool) audio.Source {
	return WrapSourceObserved(src, enabled, 0, Hooks{})
}

// WrapSourceObserved 는 게이팅과 관찰(화자 경계 감지)을 **게이트 인스턴스 하나로** 겸한다.
//
//	gating          — true면 발화 구간만 forward(기존 계약), false면 전 구간 통과.
//	boundarySilence — 화자 경계 판정 임계(0 이하면 경계 후보를 내지 않는다).
//	hooks           — 발화 전이/경계 콜백(오디오 goroutine에서 동기 호출 — 가볍게).
//
// 관찰은 오디오를 **절대 변형하지 않는다**: gating=false면 받은 청크 포인터를 그대로
// 넘긴다(복사/버퍼링/지연 없음). 게이팅도 관찰도 필요 없으면 원본을 그대로 반환해
// RMS 계산 비용조차 들지 않게 한다.
func WrapSourceObserved(src audio.Source, gating bool, boundarySilence time.Duration, hooks Hooks) audio.Source {
	if !gating && (boundarySilence <= 0 || !hooks.any()) {
		return src
	}
	return &gatedSource{
		src:             src,
		cfg:             Config{}.Normalize(),
		gating:          gating,
		boundarySilence: boundarySilence,
		hooks:           hooks,
	}
}

// gatedSource forwards chunks from the underlying source, optionally gating them,
// while observing speech transitions for speaker-boundary detection.
type gatedSource struct {
	src             audio.Source
	cfg             Config
	gating          bool          // false면 관찰 전용(전 구간 통과).
	boundarySilence time.Duration // 경계 임계(0 이하 = 경계 비활성).
	hooks           Hooks

	// BoundarySilenceFunc 가 설정돼 있으면 청크마다 이 값을 읽어 임계를 갱신한다
	// (설정 변경을 재시작 없이 반영). 오디오 경로에서 호출되므로 반드시 lock-free여야 한다.
	boundarySilenceFunc func() time.Duration
}

// SetBoundarySilenceFunc 는 경계 임계를 런타임에 읽어올 provider를 지정한다(선택).
// Start 전에 호출한다. provider는 오디오 dispatch goroutine에서 청크마다 호출되므로
// atomic 읽기 수준으로 가벼워야 한다.
func SetBoundarySilenceFunc(s audio.Source, f func() time.Duration) {
	if g, ok := s.(*gatedSource); ok {
		g.boundarySilenceFunc = f
	}
}

// Start wires the underlying source with a per-Start gate + observer. onChunk(사용자
// 콜백)은 게이팅이 켜져 있으면 발화 구간 청크만, 꺼져 있으면 원본 청크를 그대로 받는다.
// Source 계약상 onChunk는 단일 dispatch goroutine에서 호출되므로 게이트/옵저버 상태는
// 락 없이 직렬 접근된다.
func (g *gatedSource) Start(ctx context.Context, onChunk func(audio.Chunk)) error {
	gate := NewGate(g.cfg)
	obs := NewObserver(g.boundarySilence, g.hooks)
	return g.src.Start(ctx, func(c audio.Chunk) {
		// RMS는 청크당 **1회만** 계산해 게이팅(고정 임계·여운 750ms)과 관찰(적응 임계·
		// 여운 300ms)이 함께 쓴다. 두 판정은 파라미터가 다르므로 상태는 각자 관리한다.
		level := rms(c)
		fwd, _ := gate.processWithRMS(c, level)
		if g.boundarySilenceFunc != nil {
			obs.BoundarySilence = g.boundarySilenceFunc()
		}
		obs.Observe(len(c), level)

		if !g.gating {
			onChunk(c) // 관찰 전용 — 원본 청크를 그대로(복사/지연 없이) 통과.
			return
		}
		for _, out := range fwd {
			onChunk(out)
		}
	})
}

// Stop tears down the underlying source.
func (g *gatedSource) Stop() error { return g.src.Stop() }
