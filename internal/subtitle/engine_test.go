package subtitle

import (
	"reflect"
	"testing"
	"time"
)

// --- collapseRepeats: 끝의 연속 반복 부분열 붕괴 ---

func TestCollapseRepeats(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"repeated phrase", "that makes the brain that makes the brain", "that makes the brain"},
		{"triple block", "a b c a b c a b c", "a b c"},
		{"too short (<6 tokens) unchanged", "a b a b", "a b a b"},
		{"no repetition", "one two three four five six", "one two three four five six"},
		{"short repeat (<3 tokens) protected", "one two three four one two", "one two three four one two"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := collapseRepeats(c.in); got != c.want {
				t.Fatalf("collapseRepeats(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// --- dedupGlobalSentences: 비연속 동일 문장 재등장 제거 ---

func TestDedupGlobalSentences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"nonconsecutive duplicate removed",
			"A sentence here. Another one. A sentence here.",
			"A sentence here. Another one.",
		},
		{
			"partial tail preserved even if dup",
			"Hello there. Hello there",
			"Hello there. Hello there",
		},
		{
			"short sentences not deduped",
			"Ne. Ne. Ne.",
			"Ne. Ne. Ne.",
		},
		{
			"no duplicates unchanged",
			"First sentence. Second sentence.",
			"First sentence. Second sentence.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dedupGlobalSentences(c.in); got != c.want {
				t.Fatalf("dedupGlobalSentences(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// --- delta 누적 경계: 겹침 머지 / 증분 / 전체 재전송 ---

func TestDeltaAccumulationBoundaries(t *testing.T) {
	t.Run("incremental append", func(t *testing.T) {
		e := New()
		e.IngestTranslatedDelta("Hello")
		e.IngestTranslatedDelta(" world")
		if got := e.currentTranslation; got != "Hello world" {
			t.Fatalf("current = %q, want %q", got, "Hello world")
		}
	})

	t.Run("overlap suffix/prefix merge", func(t *testing.T) {
		e := New()
		e.IngestTranslatedDelta("the cat")
		e.IngestTranslatedDelta("cat sat")
		if got := e.currentTranslation; got != "the cat sat" {
			t.Fatalf("current = %q, want %q", got, "the cat sat")
		}
	})

	t.Run("cumulative full resend dedups", func(t *testing.T) {
		e := New()
		e.IngestTranslatedDelta("hello")
		e.IngestTranslatedDelta("hello world")
		if got := e.currentTranslation; got != "hello world" {
			t.Fatalf("current = %q, want %q", got, "hello world")
		}
	})

	t.Run("empty and pure duplicate ignored", func(t *testing.T) {
		e := New()
		e.IngestTranslatedDelta("stable text")
		e.IngestTranslatedDelta("")            // 빈 조각 무시
		e.IngestTranslatedDelta("stable text") // 완전 중복 → newPart 없음
		if got := e.currentTranslation; got != "stable text" {
			t.Fatalf("current = %q, want %q", got, "stable text")
		}
	})
}

// --- charBreak: 길이 초과 시 현재 줄을 확정(roll-up push)하고 버퍼 비움 ---

func TestCharBreak(t *testing.T) {
	e := New()
	e.MaxLines = 1
	e.CharsPerLine = 10
	e.MaxCharsBeforeBreak = 0 // 줄 기반만 → 임계 10

	if got := e.effectiveMaxChars(); got != 10 {
		t.Fatalf("effectiveMaxChars = %d, want 10", got)
	}

	e.IngestTranslatedDelta("abcdefghijkl") // 12 runes >= 10 → 즉시 확정
	if e.currentTranslation != "" {
		t.Fatalf("current should be cleared after charBreak, got %q", e.currentTranslation)
	}
	want := []string{"abcdefghijkl"}
	if got := e.RollupLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("RollupLines = %v, want %v", got, want)
	}
	if !e.Visible() {
		t.Fatalf("engine should be visible after charBreak push")
	}
}

func TestCharBreakDefaultThreshold(t *testing.T) {
	e := New()
	// 기본: max(28*2, 50) = 56.
	if got := e.effectiveMaxChars(); got != 56 {
		t.Fatalf("default effectiveMaxChars = %d, want 56", got)
	}
}

// --- roll-up FIFO 클립: 내부 히스토리 유지 + 접근자 suffix(MaxLines) ---

func TestRollupClip(t *testing.T) {
	e := New()
	e.MaxLines = 2
	for _, s := range []string{"one", "two", "three", "four"} {
		e.IngestTranslatedSegment(s, true)
	}
	// 접근자는 마지막 MaxLines(2)줄만 노출.
	want := []string{"three", "four"}
	if got := e.RollupLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("RollupLines = %v, want %v", got, want)
	}
	// DisplayTranslation 은 keep=MaxLines+2=4 줄을 줄바꿈으로 잇는다.
	if got, want := e.DisplayTranslation(), "one\ntwo\nthree\nfour"; got != want {
		t.Fatalf("DisplayTranslation = %q, want %q", got, want)
	}
}

func TestRollupHistoryCap(t *testing.T) {
	e := New()
	e.MaxRollupHistory = 3
	e.MaxLines = 5
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		e.IngestTranslatedSegment(s, true)
	}
	// 내부 히스토리는 3개로 제한 → 접근자(suffix 5)도 3개만.
	want := []string{"c", "d", "e"}
	if got := e.RollupLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("RollupLines = %v, want %v", got, want)
	}
}

// --- 세그먼트 final 교체: interim 갱신 후 final push ---

func TestSegmentInterimThenFinal(t *testing.T) {
	e := New()
	e.IngestTranslatedSegment("partial hypo", false)
	if got := e.DisplayTranslation(); got != "partial hypo" {
		t.Fatalf("interim DisplayTranslation = %q, want %q", got, "partial hypo")
	}
	if !e.Visible() {
		t.Fatal("should be visible during interim")
	}
	e.IngestTranslatedSegment("final sentence", true)
	if e.currentTranslation != "" {
		t.Fatalf("current should be cleared after final, got %q", e.currentTranslation)
	}
	if got := e.DisplayTranslation(); got != "final sentence" {
		t.Fatalf("final DisplayTranslation = %q, want %q", got, "final sentence")
	}
}

func TestSegmentDuplicateFinalIgnored(t *testing.T) {
	e := New()
	e.IngestTranslatedSegment("same line", true)
	e.IngestTranslatedSegment("same line", true) // 직전과 동일 → push 무시
	want := []string{"same line"}
	if got := e.RollupLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("RollupLines = %v, want %v", got, want)
	}
}

func TestSourceSegmentInterim(t *testing.T) {
	e := New()
	e.IngestSourceSegment("원문 진행 중", false)
	e.IngestTranslatedSegment("translated", false)
	if got := e.DisplaySource(); got != "원문 진행 중" {
		t.Fatalf("DisplaySource = %q, want %q", got, "원문 진행 중")
	}
}

// --- OnConfirmedLine 콜백: 실 push 시에만 호출 ---

func TestOnConfirmedLineCallback(t *testing.T) {
	e := New()
	var got [][2]string
	e.OnConfirmedLine = func(source, translation string) {
		got = append(got, [2]string{source, translation})
	}
	e.IngestSourceSegment("src", false)
	e.IngestTranslatedSegment("line one", true)
	e.IngestTranslatedSegment("line one", true) // 중복 → 콜백 없음
	e.IngestTranslatedSegment("line two", true)

	want := [][2]string{{"src", "line one"}, {"src", "line two"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("callbacks = %v, want %v", got, want)
	}
}

// --- heartbeat 무음 정리: 타임아웃 경과 시 자동 확정 + 화면 clear ---

func TestHeartbeatSilenceConfirm(t *testing.T) {
	e := New()
	t0 := time.Unix(1000, 0)
	e.IngestTranslatedDelta("pending line")
	e.Heartbeat(t0) // 활동 관측 → 무음 기준 t0

	// SilenceTimeout(2s) 경과, SilenceClearTimeout(8s) 미만 → 자동 확정만.
	e.Heartbeat(t0.Add(3 * time.Second))
	if e.currentTranslation != "" {
		t.Fatalf("current should be confirmed at silence timeout, got %q", e.currentTranslation)
	}
	if got, want := e.RollupLines(), []string{"pending line"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RollupLines = %v, want %v", got, want)
	}
	if !e.Visible() {
		t.Fatal("should still be visible after silence confirm (< clear timeout)")
	}
}

func TestHeartbeatSilenceClear(t *testing.T) {
	e := New()
	t0 := time.Unix(1000, 0)
	e.IngestTranslatedDelta("pending line")
	e.Heartbeat(t0)

	// SilenceClearTimeout(8s) 경과 → 확정 후 화면 전체 정리.
	e.Heartbeat(t0.Add(9 * time.Second))
	if e.Visible() {
		t.Fatal("should be hidden after silence clear timeout")
	}
	if got := e.RollupLines(); len(got) != 0 {
		t.Fatalf("RollupLines should be empty after clear, got %v", got)
	}
	if got := e.DisplayTranslation(); got != "" {
		t.Fatalf("DisplayTranslation should be empty after clear, got %q", got)
	}
}

func TestHeartbeatActivityResetsSilence(t *testing.T) {
	e := New()
	t0 := time.Unix(1000, 0)
	e.IngestTranslatedDelta("line")
	e.Heartbeat(t0)

	// 계속 활동이 있으면 무음 기준이 갱신되어 clear가 발동하지 않는다.
	e.IngestTranslatedDelta(" more")
	e.Heartbeat(t0.Add(5 * time.Second)) // 활동 관측 → 기준 갱신
	e.Heartbeat(t0.Add(6 * time.Second)) // 기준(5s)에서 1s만 경과
	if !e.Visible() {
		t.Fatal("should remain visible while activity continues")
	}
	if got := e.DisplayTranslation(); got == "" {
		t.Fatal("display should not be cleared while activity continues")
	}
}

func TestHeartbeatBeforeAnyActivity(t *testing.T) {
	e := New()
	// 활동 전 heartbeat는 무해(패닉/변화 없음).
	e.Heartbeat(time.Unix(1000, 0))
	if e.Visible() {
		t.Fatal("should not be visible without any input")
	}
}

// --- TurnComplete / GenerationComplete / Interrupted / Reset ---

func TestTurnCompletePushesAndResetsNextDelta(t *testing.T) {
	e := New()
	e.IngestTranslatedDelta("first turn")
	e.TurnComplete()
	if got, want := e.RollupLines(), []string{"first turn"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RollupLines = %v, want %v", got, want)
	}
	// turn 종료 후 첫 delta는 새 turn → 이전 잔재 위에 누적되지 않음.
	e.IngestTranslatedDelta("second turn")
	if e.currentTranslation != "second turn" {
		t.Fatalf("current = %q, want %q", e.currentTranslation, "second turn")
	}
}

func TestGenerationCompleteReplacesOnNextDelta(t *testing.T) {
	e := New()
	e.IngestTranslatedDelta("draft one two")
	e.GenerationComplete()
	// 다음 delta에서 직전 generation을 비우고 새로 시작.
	e.IngestTranslatedDelta("revised")
	if got := e.currentTranslation; got != "revised" {
		t.Fatalf("current = %q, want %q", got, "revised")
	}
}

func TestInterruptedPreservesRollup(t *testing.T) {
	e := New()
	e.IngestTranslatedSegment("confirmed line", true)
	e.IngestTranslatedDelta("in progress")
	e.Interrupted()
	if e.currentTranslation != "" {
		t.Fatalf("current should be cleared by interrupt, got %q", e.currentTranslation)
	}
	if got, want := e.RollupLines(), []string{"confirmed line"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rollup should be preserved, got %v want %v", got, want)
	}
	if !e.Visible() {
		t.Fatal("visibility should be preserved after interrupt")
	}
}

func TestReset(t *testing.T) {
	e := New()
	e.IngestTranslatedSegment("something", true)
	e.IngestSourceSegment("원문", false)
	e.Reset()
	if e.Visible() || e.DisplayTranslation() != "" || e.DisplaySource() != "" || len(e.RollupLines()) != 0 {
		t.Fatalf("state not fully cleared: visible=%v dt=%q ds=%q roll=%v",
			e.Visible(), e.DisplayTranslation(), e.DisplaySource(), e.RollupLines())
	}
	if e.segmentMode {
		t.Fatal("segmentMode should be false after reset")
	}
}

// --- 테스트 자막(고정 미리보기) ---

func TestShowPreviewFixed(t *testing.T) {
	e := New()
	e.ShowPreview("안녕하세요 — 자막 미리보기입니다", "Hello — subtitle preview")

	if !e.Visible() {
		t.Fatal("preview should be visible")
	}
	if got, want := e.DisplayTranslation(), "안녕하세요 — 자막 미리보기입니다"; got != want {
		t.Fatalf("DisplayTranslation = %q, want %q", got, want)
	}
	if got, want := e.DisplaySource(), "Hello — subtitle preview"; got != want {
		t.Fatalf("DisplaySource = %q, want %q", got, want)
	}

	// 고정 표시: 무음 정리 타임아웃을 한참 넘겨도 사라지지 않는다(타이머/활동 추적 비활성).
	t0 := time.Unix(1000, 0)
	e.Heartbeat(t0)
	e.Heartbeat(t0.Add(60 * time.Second))
	if !e.Visible() {
		t.Fatal("fixed preview must survive silence clear timeout")
	}
	if e.DisplayTranslation() == "" {
		t.Fatal("fixed preview text must persist across heartbeats")
	}
}

func TestPreviewWithoutSource(t *testing.T) {
	e := New()
	e.ShowPreview("안녕하세요 — 자막 미리보기입니다", "")
	if e.DisplaySource() != "" {
		t.Fatalf("source should be empty when preview source is empty, got %q", e.DisplaySource())
	}
	if !e.Visible() || e.DisplayTranslation() == "" {
		t.Fatal("translation-only preview should still be visible")
	}
}

func TestHidePreviewClears(t *testing.T) {
	e := New()
	e.ShowPreview("안녕하세요 — 자막 미리보기입니다", "Hello — subtitle preview")
	e.HidePreview()
	if e.Visible() || e.DisplayTranslation() != "" || e.DisplaySource() != "" {
		t.Fatalf("HidePreview must fully clear: visible=%v dt=%q ds=%q",
			e.Visible(), e.DisplayTranslation(), e.DisplaySource())
	}
}

// --- 결정성: 같은 입력 → 같은 출력 ---

func TestDeterminism(t *testing.T) {
	run := func() (string, []string, bool) {
		e := New()
		t0 := time.Unix(500, 0)
		e.IngestSourceDelta("source alpha")
		e.IngestTranslatedDelta("alpha")
		e.IngestTranslatedDelta("alpha beta")
		e.Heartbeat(t0)
		e.IngestTranslatedSegment("gamma delta", true)
		e.Heartbeat(t0.Add(time.Second))
		e.IngestTranslatedSegment("epsilon", true)
		e.Heartbeat(t0.Add(3 * time.Second))
		return e.DisplayTranslation(), e.RollupLines(), e.Visible()
	}
	dt1, rl1, v1 := run()
	dt2, rl2, v2 := run()
	if dt1 != dt2 || v1 != v2 || !reflect.DeepEqual(rl1, rl2) {
		t.Fatalf("nondeterministic output:\n  run1: dt=%q roll=%v vis=%v\n  run2: dt=%q roll=%v vis=%v",
			dt1, rl1, v1, dt2, rl2, v2)
	}
}

// --- 비세그먼트 폴백 표시(confirmed) ---

func TestNonSegmentConfirmedFallback(t *testing.T) {
	e := New()
	// segmentMode를 켜지 않은 경로: source delta만 오면 confirmed 폴백 확인.
	e.IngestSourceDelta("원문만")
	// 아직 번역이 없으므로 confirmed로 폴백되진 않지만 current가 표시된다.
	if got := e.DisplaySource(); got != "원문만" {
		t.Fatalf("DisplaySource = %q, want %q", got, "원문만")
	}
	if e.segmentMode {
		t.Fatal("source-only delta should not enable segmentMode")
	}
}
