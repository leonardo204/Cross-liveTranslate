// Package cost — 비용 추정(세션/누적 USD).
//
// 원본 이식: liveTranslate/Sources/Cost/CostEstimator.swift + Config/AppConfig.swift.
// 통화는 USD(환율 환산 없음). **세션 비용**(현재 번역 세션)과 **누적 비용**(영속)을 분리해
// 추적한다. 제어 HUD는 세션 비용을, 설정/영속은 누적 비용을 사용한다.
//
// 비용 계산식(원본 §9.1 동일):
//   - 입력(실시간·정확): 송신 오디오 누적 시간(초) × 25 tok/s → tokens/1M × $3.50.
//     송신 청크의 16kHz mono 샘플 수로 초를 누적한다(seconds = samples / 16000).
//   - 출력: 서버 usageMetadata 출력 오디오 토큰 누적 → tokens/1M × $21.00.
//
// 결정적(Date/난수 없음). 순수 패키지 — cgo 없음 → windows 크로스빌드 가능.
// 동시성: 내부 mutex로 보호한다(Add는 controller runLoop, Session/Cumulative getter는
// 바인딩 goroutine에서 호출될 수 있다).
package cost

import (
	"sync"

	"cross-livetranslate/internal/pipeline"
)

// 단가/토큰율 상수. 값은 원본 AppConfig.swift에서 이식(결정적).
//
//	costInputUSDPerMillionTokens  = 3.50
//	costOutputUSDPerMillionTokens = 21.00
//	costAudioTokensPerSecond      = 25.0
//	audioSampleRate               = 16000
const (
	// InputUSDPerMillionTokens is the 입력(오디오) 토큰 백만 개당 USD.
	InputUSDPerMillionTokens = 3.50
	// OutputUSDPerMillionTokens is the 출력(오디오) 토큰 백만 개당 USD.
	OutputUSDPerMillionTokens = 21.00
	// AudioTokensPerSecond is 입력 오디오 1초당 토큰 수(비용 추정 근거).
	AudioTokensPerSecond = 25.0
	// sampleRate is the 16kHz mono 송신 샘플레이트(입력 시간 환산용).
	sampleRate = 16000.0
)

// Estimator accumulates session and cumulative translation cost in USD.
// 원본 CostEstimator 등가. 세션은 원시값(초/토큰)에서 파생 계산하고, 누적은 증분을 더한다.
type Estimator struct {
	mu sync.Mutex

	// 세션 원시값(원본과 동일하게 초/토큰을 보존해 결정적으로 USD를 파생).
	sessionSentSeconds  float64
	sessionOutputTokens int

	// 누적 총 비용(USD). 시작 시 Settings.Cost.CumulativeUSD로 시드된다.
	cumulativeUSD float64
}

// New returns an estimator seeded with the persisted cumulative USD.
// 세션 원시값은 0에서 시작한다(ResetSession 불필요 — 첫 세션은 0).
func New(seedCumulativeUSD float64) *Estimator {
	return &Estimator{cumulativeUSD: seedCumulativeUSD}
}

// sessionInputUSD returns the 세션 입력 비용(USD). 호출자는 mu를 보유한다.
func (e *Estimator) sessionInputUSD() float64 {
	tokens := e.sessionSentSeconds * AudioTokensPerSecond
	return tokens / 1_000_000.0 * InputUSDPerMillionTokens
}

// sessionOutputUSD returns the 세션 출력 비용(USD). 호출자는 mu를 보유한다.
func (e *Estimator) sessionOutputUSD() float64 {
	return float64(e.sessionOutputTokens) / 1_000_000.0 * OutputUSDPerMillionTokens
}

// AddSentSamples accumulates input cost from sent 16kHz-mono sample count.
// 원본 addSentAudio 등가: 세션 입력 증분만큼 누적에도 더한다(양수일 때만).
func (e *Estimator) AddSentSamples(samples int) {
	if samples <= 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	before := e.sessionInputUSD()
	e.sessionSentSeconds += float64(samples) / sampleRate
	if delta := e.sessionInputUSD() - before; delta > 0 {
		e.cumulativeUSD += delta
	}
}

// AddOutputTokens accumulates output cost from usageMetadata output-audio tokens.
// 원본 addOutputTokens 등가: 세션 출력 증분만큼 누적에도 더한다(양수일 때만).
func (e *Estimator) AddOutputTokens(tokens int) {
	if tokens <= 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	before := e.sessionOutputUSD()
	e.sessionOutputTokens += tokens
	if delta := e.sessionOutputUSD() - before; delta > 0 {
		e.cumulativeUSD += delta
	}
}

// Add folds a Usage event into both input(SentSamples) and output(OutputAudioTokens)
// cost. 서버 Usage 이벤트는 출력 토큰만 채우고, 입력 샘플은 controller가 송신 계량으로 채운다.
func (e *Estimator) Add(u pipeline.UsageInfo) {
	e.AddSentSamples(u.SentSamples)
	e.AddOutputTokens(u.OutputAudioTokens)
}

// Session returns the current session cost (입력 + 출력) in USD.
func (e *Estimator) Session() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sessionInputUSD() + e.sessionOutputUSD()
}

// SessionInput returns the current 세션 입력(전송) 비용 in USD (제어 HUD 비용 행).
func (e *Estimator) SessionInput() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sessionInputUSD()
}

// SessionOutput returns the current 세션 출력(수신) 비용 in USD (제어 HUD 비용 행).
func (e *Estimator) SessionOutput() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sessionOutputUSD()
}

// Cumulative returns the persisted-and-growing cumulative cost in USD.
func (e *Estimator) Cumulative() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cumulativeUSD
}

// ResetSession zeroes the session cost (번역 시작 시). 누적은 유지한다.
func (e *Estimator) ResetSession() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sessionSentSeconds = 0
	e.sessionOutputTokens = 0
}
