// Package vad — 순수 Go 에너지 기반 발화 게이트(voice-activity gate).
//
// 원본 이식: liveTranslate/Sources/Audio/VADGate.swift의 **게이트 계약**(발화 구간만
// forward, bypass, pre-roll/hangover로 첫·끝 음절 보존, 백프레셔). 단 추론 엔진은 원본이
// Silero(FluidAudio CoreML)였던 것과 달리 여기서는 **RMS 에너지 임계 + 히스테리시스**로
// 대체한다 — onnxruntime/외부 네이티브 라이브러리를 도입하지 않아 windows 크로스빌드가
// 순수(cgo 없음)로 유지된다.
//
// 청크는 16kHz mono Float32 100ms(1600 샘플, audio.ChunkSamples) 단위다.
// 파라미터는 원본 VadSegmentationConfig 개념을 100ms 프레임 수로 환산한다:
//   - minSpeech   0.20s → 2 프레임(발화 시작 확정에 필요한 연속 고에너지 프레임 수)
//   - hangover    0.75s → 8 프레임(발화 종료 후 여운 — 끝 음절 보존)
//   - pre-roll    0.20s → 2 프레임(발화 시작 직전 프레임 flush — 첫 음절 보존)
//
// bypass: WrapSource(src, enabled=false)면 게이트를 끼우지 않고 원본을 그대로 반환한다
// (전부 통과). 순수 상태머신이므로 로드 실패 개념이 없다.
package vad

import (
	"math"

	"cross-livetranslate/internal/audio"
)

// 게이트 기본 파라미터(원본 VADGate 개념 차용, 100ms 프레임 기준 환산).
const (
	// DefaultRMSThreshold is 발화로 간주할 최소 RMS(Float32 [-1,1] 기준). 무음/저잡음은 이 아래.
	DefaultRMSThreshold = 0.01
	// DefaultMinSpeechFrames is 발화 시작 확정에 필요한 연속 고에너지 프레임 수(≈0.20s).
	DefaultMinSpeechFrames = 2
	// DefaultHangoverFrames is 발화 종료 후 계속 통과시킬 여운 프레임 수(≈0.75s → ceil).
	DefaultHangoverFrames = 8
	// DefaultPreRollFrames is 발화 시작 직전 보관해 함께 흘릴 프레임 수(≈0.20s).
	DefaultPreRollFrames = 2
)

// Config parameterizes the energy gate. 0 값 필드는 기본 상수로 대체된다(Normalize).
type Config struct {
	RMSThreshold    float32
	MinSpeechFrames int
	HangoverFrames  int
	PreRollFrames   int
}

// Normalize fills zero fields with defaults and returns the effective config.
func (c Config) Normalize() Config {
	if c.RMSThreshold <= 0 {
		c.RMSThreshold = DefaultRMSThreshold
	}
	if c.MinSpeechFrames <= 0 {
		c.MinSpeechFrames = DefaultMinSpeechFrames
	}
	if c.HangoverFrames <= 0 {
		c.HangoverFrames = DefaultHangoverFrames
	}
	if c.PreRollFrames < 0 {
		c.PreRollFrames = DefaultPreRollFrames
	}
	return c
}

// Gate is a streaming energy-based speech gate. 단일 goroutine에서 Process를 순서대로
// 호출한다(Source dispatch goroutine과 동일) — 내부 상태는 락 없이 직렬 접근한다.
type Gate struct {
	cfg Config

	speaking  bool
	hangover  int         // 남은 여운 프레임 수(speaking일 때만 의미)
	speechRun int         // 연속 고에너지 프레임 수(onset 확정 카운터)
	preRoll   []audio.Chunk // 최근 저에너지 프레임(최대 PreRollFrames개, onset 시 flush)
	pending   []audio.Chunk // onset 확정 전 버퍼링한 고에너지 프레임(확정 시 flush)
}

// NewGate returns a gate with the given (normalized) config.
func NewGate(cfg Config) *Gate { return &Gate{cfg: cfg.Normalize()} }

// Speaking reports the current gate state(발화중 여부).
func (g *Gate) Speaking() bool { return g.speaking }

// Process feeds one chunk and returns the chunks to forward(발화 구간만) plus the
// current speaking state. 무음/소음은 빈 forward를 반환한다. onset 확정 시 pre-roll +
// 버퍼링된 onset 프레임을 함께 반환해 첫 음절을 보존한다.
func (g *Gate) Process(chunk audio.Chunk) (forward []audio.Chunk, speaking bool) {
	loud := rms(chunk) >= g.cfg.RMSThreshold

	if g.speaking {
		if loud {
			g.hangover = g.cfg.HangoverFrames // 발화 지속 — 여운 리셋.
		} else {
			g.hangover--
		}
		forward = append(forward, chunk) // 발화중(여운 포함)에는 모든 프레임 통과.
		if g.hangover <= 0 {
			// 발화 종료 — 다음 onset을 위해 상태 초기화. 이 프레임은 preRoll 시드로 보관.
			g.speaking = false
			g.speechRun = 0
			g.pending = nil
			g.preRoll = g.preRoll[:0]
			g.pushPreRoll(chunk)
		}
		return forward, g.speaking
	}

	// 미발화 상태.
	if loud {
		g.speechRun++
		g.pending = append(g.pending, chunk)
		if g.speechRun >= g.cfg.MinSpeechFrames {
			// onset 확정 — pre-roll(직전 무음) + 버퍼링한 onset 프레임을 함께 흘린다.
			g.speaking = true
			g.hangover = g.cfg.HangoverFrames
			forward = append(forward, g.preRoll...)
			forward = append(forward, g.pending...)
			g.preRoll = g.preRoll[:0]
			g.pending = nil
			g.speechRun = 0
		}
		return forward, g.speaking
	}

	// 무음: onset 미확정 → 버퍼 폐기하고 pre-roll 링에 보관.
	g.speechRun = 0
	g.pending = nil
	g.pushPreRoll(chunk)
	return nil, false
}

// pushPreRoll appends a chunk to the pre-roll ring, trimming to PreRollFrames.
func (g *Gate) pushPreRoll(chunk audio.Chunk) {
	if g.cfg.PreRollFrames <= 0 {
		return
	}
	// 방어적 복사: 상위(malgo 콜백)가 슬라이스를 재사용할 수 있으므로 보관본은 독립.
	cp := make(audio.Chunk, len(chunk))
	copy(cp, chunk)
	g.preRoll = append(g.preRoll, cp)
	if len(g.preRoll) > g.cfg.PreRollFrames {
		g.preRoll = g.preRoll[len(g.preRoll)-g.cfg.PreRollFrames:]
	}
}

// rms returns the root-mean-square level of the samples (0 for empty).
// audio.RMS와 동등하나, 순수 게이트 유닛 테스트가 audio 캡처 백엔드에 의존하지 않도록
// 게이트 내부에 자립 구현한다(값은 동일).
func rms(s audio.Chunk) float32 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		sum += float64(v) * float64(v)
	}
	return float32(math.Sqrt(sum / float64(len(s))))
}
