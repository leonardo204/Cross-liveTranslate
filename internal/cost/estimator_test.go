package cost

import (
	"math"
	"testing"

	"cross-livetranslate/internal/pipeline"
)

const eps = 1e-9

func approx(a, b float64) bool { return math.Abs(a-b) <= eps }

// 입력 비용: 1초 송신(16000 샘플) = 25 토큰 → 25/1e6 × $3.50.
func TestInputCost(t *testing.T) {
	e := New(0)
	e.AddSentSamples(16000) // 1초
	want := 25.0 / 1_000_000.0 * InputUSDPerMillionTokens
	if got := e.Session(); !approx(got, want) {
		t.Fatalf("session input: got %.12f want %.12f", got, want)
	}
	if got := e.Cumulative(); !approx(got, want) {
		t.Fatalf("cumulative input: got %.12f want %.12f", got, want)
	}
}

// 출력 비용: 1,000,000 토큰 = $21.00.
func TestOutputCost(t *testing.T) {
	e := New(0)
	e.AddOutputTokens(1_000_000)
	if got := e.Session(); !approx(got, OutputUSDPerMillionTokens) {
		t.Fatalf("session output: got %.12f want %.12f", got, OutputUSDPerMillionTokens)
	}
	if got := e.Cumulative(); !approx(got, OutputUSDPerMillionTokens) {
		t.Fatalf("cumulative output: got %.12f want %.12f", got, OutputUSDPerMillionTokens)
	}
}

// Add는 입력(샘플)과 출력(토큰)을 함께 누적한다.
func TestAddUsage(t *testing.T) {
	e := New(0)
	e.Add(pipeline.UsageInfo{SentSamples: 16000, OutputAudioTokens: 1_000_000})
	wantIn := 25.0 / 1_000_000.0 * InputUSDPerMillionTokens
	want := wantIn + OutputUSDPerMillionTokens
	if got := e.Session(); !approx(got, want) {
		t.Fatalf("session total: got %.12f want %.12f", got, want)
	}
}

// 시드된 누적 위에 세션 증분이 더해진다. ResetSession은 세션만 0으로 되돌리고 누적은 유지.
func TestSeedAndReset(t *testing.T) {
	e := New(1.25)
	if got := e.Cumulative(); !approx(got, 1.25) {
		t.Fatalf("seed: got %.12f want 1.25", got)
	}
	e.AddOutputTokens(1_000_000) // +$21
	if got := e.Cumulative(); !approx(got, 1.25+OutputUSDPerMillionTokens) {
		t.Fatalf("cumulative after add: got %.12f", got)
	}
	e.ResetSession()
	if got := e.Session(); !approx(got, 0) {
		t.Fatalf("session after reset: got %.12f want 0", got)
	}
	// 누적은 리셋 후에도 유지된다.
	if got := e.Cumulative(); !approx(got, 1.25+OutputUSDPerMillionTokens) {
		t.Fatalf("cumulative after reset: got %.12f", got)
	}
}

// 결정성: 같은 입력이면 항상 같은 결과.
func TestDeterministic(t *testing.T) {
	mk := func() float64 {
		e := New(0.5)
		for i := 0; i < 10; i++ {
			e.AddSentSamples(1600) // 100ms 청크
			e.AddOutputTokens(37)
		}
		return e.Session() + e.Cumulative()
	}
	if a, b := mk(), mk(); !approx(a, b) {
		t.Fatalf("non-deterministic: %.12f vs %.12f", a, b)
	}
}

// 0/음수 입력은 무시된다.
func TestNonPositiveIgnored(t *testing.T) {
	e := New(0)
	e.AddSentSamples(0)
	e.AddSentSamples(-5)
	e.AddOutputTokens(0)
	e.AddOutputTokens(-3)
	if got := e.Session(); got != 0 {
		t.Fatalf("non-positive should be ignored: got %.12f", got)
	}
}
