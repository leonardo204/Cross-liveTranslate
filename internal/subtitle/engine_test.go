package subtitle

import (
	"fmt"
	"reflect"
	"strings"
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
	e.TurnBoundarySilence = 0 // 이 테스트의 의도는 2s 무음 fallback 경로 — 1.0s 경계 트리거는 끈다.
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

// -----------------------------------------------------------------------------
// 화자 전환 근사(2색 교대) — 턴 경계 확정만 패리티를 토글한다.
// -----------------------------------------------------------------------------

// --- turnComplete 확정 → 다음 줄 패리티 토글 ---

func TestSpeakerToggleOnTurnComplete(t *testing.T) {
	e := New()
	e.IngestTranslatedDelta("first turn")
	e.TurnComplete()
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker after turnComplete = %d, want 1", e.curSpeaker)
	}
	e.IngestTranslatedDelta("second turn")
	e.TurnComplete()
	if e.curSpeaker != 0 {
		t.Fatalf("curSpeaker after 2nd turnComplete = %d, want 0", e.curSpeaker)
	}

	want := []Line{{Text: "first turn", Speaker: 0}, {Text: "second turn", Speaker: 1}}
	if got := e.DisplayLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DisplayLines = %v, want %v", got, want)
	}
}

// --- charBreak(글자수 초과) 확정 → 패리티 유지(같은 발화가 길어진 것) ---

func TestSpeakerNoToggleOnCharBreak(t *testing.T) {
	e := New()
	e.MaxLines = 1
	e.CharsPerLine = 5        // effectiveMaxChars = max(5*1, MaxCharsBeforeBreak)
	e.MaxCharsBeforeBreak = 0 // 줄 기반 임계(5)만 사용.

	e.IngestTranslatedDelta("abcde") // 5자 → charBreak 확정
	e.IngestTranslatedDelta("fghij") // 5자 → charBreak 확정
	if e.curSpeaker != 0 {
		t.Fatalf("curSpeaker after charBreaks = %d, want 0 (charBreak는 화자 전환이 아님)", e.curSpeaker)
	}

	want := []Line{{Text: "abcde", Speaker: 0}, {Text: "fghij", Speaker: 0}}
	if got := e.DisplayLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DisplayLines = %v, want %v", got, want)
	}

	// 같은 발화가 turnComplete로 끝나면 그때 비로소 다음 화자로 넘어간다.
	e.IngestTranslatedDelta("klm")
	e.TurnComplete()
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker after turnComplete = %d, want 1", e.curSpeaker)
	}
}

// --- 무음 2초 fallback 확정 → 토글, 직후 turnComplete 도착 → 이중 토글 없음 ---

func TestSpeakerToggleOnSilenceFallbackNoDoubleToggle(t *testing.T) {
	e := New()
	e.TurnBoundarySilence = 0 // 2s 무음 fallback 경로 자체를 검증한다(1.0s 갭 경계는 별도 테스트).
	t0 := time.Unix(1000, 0)
	e.IngestTranslatedDelta("silence line")
	e.Heartbeat(t0)                      // 활동 관측 → 기준 t0
	e.Heartbeat(t0.Add(3 * time.Second)) // 무음 2초 초과 → 자동 확정 + 토글
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker after silence fallback = %d, want 1", e.curSpeaker)
	}

	// 뒤늦게 turnComplete 도착: 진행 줄이 비어 push가 없으므로 토글도 없어야 한다.
	e.TurnComplete()
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker after late turnComplete = %d, want 1 (이중 토글 금지)", e.curSpeaker)
	}

	e.IngestTranslatedDelta("next line")
	want := []Line{{Text: "silence line", Speaker: 0}, {Text: "next line", Speaker: 1}}
	if got := e.DisplayLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DisplayLines = %v, want %v", got, want)
	}
}

// --- DisplayLines: 확정 롤업 + 진행 줄에 올바른 패리티 ---

func TestDisplayLinesSpeakerParity(t *testing.T) {
	e := New() // MaxLines=2 → DisplayLines keep = 4줄
	for _, s := range []string{"turn one", "turn two", "turn three"} {
		e.IngestTranslatedDelta(s)
		e.TurnComplete()
	}
	e.IngestTranslatedDelta("in progress") // 미확정 진행 줄.

	want := []Line{
		{Text: "turn one", Speaker: 0},
		{Text: "turn two", Speaker: 1},
		{Text: "turn three", Speaker: 0},
		{Text: "in progress", Speaker: 1},
	}
	if got := e.DisplayLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DisplayLines = %v, want %v", got, want)
	}
	// 텍스트 집합은 기존 DisplayTranslation과 동일해야 한다(녹화 등 기존 소비자 보호).
	if got, want := e.DisplayTranslation(), "turn one\nturn two\nturn three\nin progress"; got != want {
		t.Fatalf("DisplayTranslation = %q, want %q", got, want)
	}
}

// --- 숨은 스위치 off → 모든 줄 패리티 0(기존 단색 표시와 동일) ---

func TestDisplayLinesSpeakerAlternateOff(t *testing.T) {
	e := New()
	e.SpeakerAlternate = false
	for _, s := range []string{"turn one", "turn two", "turn three"} {
		e.IngestTranslatedDelta(s)
		e.TurnComplete()
	}
	e.IngestTranslatedDelta("in progress")

	for i, l := range e.DisplayLines() {
		if l.Speaker != 0 {
			t.Fatalf("line %d (%q) Speaker = %d, want 0 (스위치 off)", i, l.Text, l.Speaker)
		}
	}
	// 스위치를 다시 켜면 내부 패리티가 그대로 살아 있어야 한다(표시만 눌렀을 뿐).
	e.SpeakerAlternate = true
	if got := e.DisplayLines(); len(got) != 4 || got[1].Speaker != 1 || got[3].Speaker != 1 {
		t.Fatalf("re-enabled DisplayLines = %v, want 패리티 0/1/0/1", got)
	}
}

// --- Reset/미리보기는 기본 색(패리티 0)에서 다시 시작 ---

func TestSpeakerResetsOnResetAndPreview(t *testing.T) {
	e := New()
	e.IngestTranslatedDelta("turn one")
	e.TurnComplete()
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker = %d, want 1", e.curSpeaker)
	}
	e.Reset()
	if e.curSpeaker != 0 {
		t.Fatalf("curSpeaker after Reset = %d, want 0", e.curSpeaker)
	}

	e.IngestTranslatedDelta("turn one")
	e.TurnComplete()
	e.ShowPreview("샘플 자막", "")
	if e.curSpeaker != 0 {
		t.Fatalf("curSpeaker after ShowPreview = %d, want 0", e.curSpeaker)
	}
	want := []Line{{Text: "샘플 자막", Speaker: 0}}
	if got := e.DisplayLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("preview DisplayLines = %v, want %v", got, want)
	}
}

// --- charBreak가 먼저 확정한 뒤 delta 없이 TurnComplete → 토글이 삼켜지지 않는다 ---

func TestSpeakerToggleAfterCharBreakThenTurnComplete(t *testing.T) {
	e := New()
	e.MaxLines = 1
	e.CharsPerLine = 5        // effectiveMaxChars = 5
	e.MaxCharsBeforeBreak = 0 // 줄 기반 임계만 사용.

	e.IngestTranslatedDelta("abcde") // charBreak로 즉시 확정 → 진행 버퍼가 빈다.
	if e.currentTranslation != "" {
		t.Fatalf("charBreak 후 진행 버퍼 = %q, want \"\"", e.currentTranslation)
	}
	e.TurnComplete() // push할 내용은 없지만 턴은 끝났다 → 화자는 넘어가야 한다.
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker after charBreak+turnComplete = %d, want 1 (토글 삼킴 금지)", e.curSpeaker)
	}

	e.IngestTranslatedDelta("fghij") // 다음 화자의 줄(역시 charBreak로 확정).
	want := []Line{{Text: "abcde", Speaker: 0}, {Text: "fghij", Speaker: 1}}
	if got := e.DisplayLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DisplayLines = %v, want %v", got, want)
	}
	e.TurnComplete()
	if e.curSpeaker != 0 {
		t.Fatalf("curSpeaker after 2nd turn = %d, want 0", e.curSpeaker)
	}
}

// --- 내용 없이 turnComplete만 반복 → 토글 없음(빈 턴이 색을 소모하지 않는다) ---

func TestSpeakerNoToggleOnEmptyTurns(t *testing.T) {
	e := New()
	e.TurnComplete()
	e.TurnComplete()
	if e.curSpeaker != 0 {
		t.Fatalf("curSpeaker after empty turns = %d, want 0", e.curSpeaker)
	}
	e.IngestTranslatedDelta("first real line")
	e.TurnComplete()
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker after first real turn = %d, want 1", e.curSpeaker)
	}
}

// --- 재번역 중복(직전 줄과 동일 → push 생략)은 토글하지 않는다 ---

func TestSpeakerNoToggleOnDuplicateLine(t *testing.T) {
	e := New()
	e.TurnBoundarySilence = 0 // 확정 트리거를 2s 무음 하나로 고정해 중복 판정만 본다.
	t0 := time.Unix(1000, 0)
	e.IngestTranslatedDelta("hello world")
	e.Heartbeat(t0)
	e.Heartbeat(t0.Add(3 * time.Second)) // 무음 fallback 확정 → push + 토글.
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker after silence confirm = %d, want 1", e.curSpeaker)
	}

	// 같은 문장이 재번역으로 다시 들어온 뒤 turnComplete → push 생략 + 토글 없음.
	e.IngestTranslatedDelta("hello world")
	e.TurnComplete()
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker after duplicate turn = %d, want 1 (중복은 토글하지 않음)", e.curSpeaker)
	}
	want := []Line{{Text: "hello world", Speaker: 0}}
	if got := e.DisplayLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DisplayLines = %v, want %v", got, want)
	}
}

// -----------------------------------------------------------------------------
// 진단 훅(Logf) — 확정 사유/턴 경계 도착 시점을 밖으로 흘린다(트랜잭션 로그 소비).
// -----------------------------------------------------------------------------

// collectLogs 는 엔진의 Logf 훅을 붙이고 기록된 메시지를 모으는 헬퍼다.
func collectLogs(e *Engine) *[]string {
	var got []string
	e.Logf = func(format string, args ...any) {
		got = append(got, fmt.Sprintf(format, args...))
	}
	return &got
}

// containsLine 은 부분 문자열을 포함한 첫 로그를 찾는다.
func containsLine(logs []string, sub string) (string, bool) {
	for _, l := range logs {
		if strings.Contains(l, sub) {
			return l, true
		}
	}
	return "", false
}

func TestLogfNilIsNoop(t *testing.T) {
	e := New() // Logf 미주입 — 어떤 경로에서도 패닉이 없어야 한다.
	e.IngestTranslatedDelta("내용")
	e.TurnComplete()
	e.Heartbeat(time.Unix(1000, 0))
	e.Heartbeat(time.Unix(1010, 0))
	e.Reset()
}

// 확정 사유가 세 갈래로 구분되어 기록된다: charBreak / turnBoundary-turnComplete /
// turnBoundary-silence. 확정이 어느 경로로 일어났는지는 이 사유 문자열로만 판별한다.
func TestLogfConfirmReasons(t *testing.T) {
	e := New()
	e.MaxLines = 1
	e.CharsPerLine = 5
	e.MaxCharsBeforeBreak = 0
	logs := collectLogs(e)

	e.IngestTranslatedDelta("abcde") // charBreak 확정
	if l, ok := containsLine(*logs, "reason=charBreak"); !ok {
		t.Fatalf("charBreak 확정 로그 없음: %v", *logs)
	} else if !strings.Contains(l, "toggled=false") {
		t.Fatalf("charBreak는 토글하지 않아야 한다: %q", l)
	}

	e.IngestTranslatedDelta("fghij")
	e.TurnComplete()
	if _, ok := containsLine(*logs, "reason=turnBoundary-turnComplete"); !ok {
		t.Fatalf("turnComplete 확정 로그 없음: %v", *logs)
	}

	// 무음 fallback 경로(1.0s 갭 경계를 꺼야 2s 경로가 드러난다).
	e2 := New()
	e2.TurnBoundarySilence = 0
	logs2 := collectLogs(e2)
	t0 := time.Unix(2000, 0)
	e2.IngestTranslatedDelta("무음으로 닫히는 줄")
	e2.Heartbeat(t0)
	e2.Heartbeat(t0.Add(3 * time.Second))
	if _, ok := containsLine(*logs2, "engine.silence 무음 3000ms 경과"); !ok {
		t.Fatalf("무음 fallback 로그 없음: %v", *logs2)
	}
	if _, ok := containsLine(*logs2, "reason=turnBoundary-silence"); !ok {
		t.Fatalf("무음 확정 사유 로그 없음: %v", *logs2)
	}
}

// turnComplete 도착 시점의 버퍼 상태가 기록된다 — 마커 미출현의 원인(무음 fallback이
// 선점해 버퍼가 비었는지 vs turnComplete 자체가 안 오는지)을 이 한 줄로 가른다.
func TestLogfTurnCompleteBufferState(t *testing.T) {
	e := New()
	logs := collectLogs(e)

	// 1) 내용이 있는 상태에서 도착 → currentEmpty=false + 토글.
	e.IngestTranslatedDelta("내용 있는 턴")
	e.TurnComplete()
	l, ok := containsLine(*logs, "engine.turn TurnComplete 도착")
	if !ok {
		t.Fatalf("TurnComplete 도착 로그 없음: %v", *logs)
	}
	if !strings.Contains(l, "currentEmpty=false") || !strings.Contains(l, `current="내용 있는 턴"`) {
		t.Fatalf("버퍼 상태 기록 불일치: %q", l)
	}

	// 2) 무음 fallback이 선점해 버퍼가 빈 뒤 도착 → currentEmpty=true + 토글 스킵 사유.
	*logs = nil
	t0 := time.Unix(3000, 0)
	e.IngestTranslatedDelta("무음이 먼저 닫는 턴")
	e.Heartbeat(t0)
	e.Heartbeat(t0.Add(3 * time.Second)) // 무음 확정(선점)
	e.TurnComplete()                     // 뒤늦은 turnComplete
	l, ok = containsLine(*logs, "engine.turn TurnComplete 도착")
	if !ok {
		t.Fatalf("TurnComplete 도착 로그 없음: %v", *logs)
	}
	if !strings.Contains(l, "currentEmpty=true") || !strings.Contains(l, "contentSinceBoundary=false") {
		t.Fatalf("선점 시 버퍼 상태 기록 불일치: %q", l)
	}
	if l, ok := containsLine(*logs, "skip=\"contentSinceBoundary=false"); !ok {
		t.Fatalf("토글 스킵 사유 로그 없음: %v", *logs)
	} else if !strings.Contains(l, "pushed=false") {
		t.Fatalf("선점된 turnComplete는 push가 없어야 한다: %q", l)
	}
}

func TestLogfClearAndReset(t *testing.T) {
	e := New()
	logs := collectLogs(e)
	t0 := time.Unix(4000, 0)
	e.IngestTranslatedDelta("정리될 줄")
	e.Heartbeat(t0)
	e.Heartbeat(t0.Add(9 * time.Second)) // SilenceClearTimeout 초과 → 화면 정리
	if _, ok := containsLine(*logs, "engine.clear 무음 9000ms 경과 → 화면 정리"); !ok {
		t.Fatalf("화면 정리 로그 없음: %v", *logs)
	}
	*logs = nil
	e.Reset()
	if _, ok := containsLine(*logs, "engine.reset 엔진 리셋"); !ok {
		t.Fatalf("리셋 로그 없음: %v", *logs)
	}
}

// -----------------------------------------------------------------------------
// 턴 경계 트리거(TurnBoundarySilence, 기본 1.0s) — turnComplete를 안 보내는 모델용.
// 실측 근거: 연속 발화 스트리밍 주기 0.8~0.9s vs 발화 경계 1.0~1.7s.
// -----------------------------------------------------------------------------

// (a) 1.0s 갭 → 경계 확정 + 화자 토글.
func TestTurnBoundaryGapConfirms(t *testing.T) {
	e := New()
	logs := collectLogs(e)
	t0 := time.Unix(1000, 0)

	e.IngestTranslatedDelta("첫 발화")
	e.Heartbeat(t0)                              // 활동 관측 → 기준 t0
	e.Heartbeat(t0.Add(1000 * time.Millisecond)) // 정확히 임계 → 경계 확정

	if e.currentTranslation != "" {
		t.Fatalf("경계 확정 후 진행 버퍼 = %q, want \"\"", e.currentTranslation)
	}
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker = %d, want 1 (경계 확정은 토글해야 한다)", e.curSpeaker)
	}
	want := []Line{{Text: "첫 발화", Speaker: 0}}
	if got := e.DisplayLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DisplayLines = %v, want %v (확정 줄 + 패리티 0)", got, want)
	}
	if _, ok := containsLine(*logs, "engine.gap 무음 1000ms ≥ 경계 임계 1000ms → 턴 경계 확정"); !ok {
		t.Fatalf("경계 확정 로그 없음: %v", *logs)
	}
	if _, ok := containsLine(*logs, "reason=turnBoundary-gap"); !ok {
		t.Fatalf("경계 확정 사유 로그 없음: %v", *logs)
	}

	// 다음 발화는 반대 패리티(보조 색)로 이어진다.
	e.IngestTranslatedDelta("둘째 발화")
	got := e.DisplayLines()
	if len(got) != 2 || got[1].Speaker != 1 {
		t.Fatalf("다음 발화 패리티 = %v, want 1", got)
	}
}

// (b) 0.9s 갭(경계 미만) → 연속 발화 스트리밍으로 보고 확정하지 않는다.
func TestTurnBoundaryGapBelowThresholdKeepsBuffer(t *testing.T) {
	e := New()
	logs := collectLogs(e)
	t0 := time.Unix(1000, 0)

	e.IngestTranslatedDelta("이어지는 발화")
	e.Heartbeat(t0)
	e.Heartbeat(t0.Add(900 * time.Millisecond)) // 0.9s — 스트리밍 주기 대역

	if e.currentTranslation != "이어지는 발화" {
		t.Fatalf("진행 버퍼 = %q, want 유지", e.currentTranslation)
	}
	if e.curSpeaker != 0 {
		t.Fatalf("curSpeaker = %d, want 0 (경계 미만은 토글 금지)", e.curSpeaker)
	}
	if len(e.rollupLines) != 0 {
		t.Fatalf("확정 줄 = %v, want 없음", e.rollupLines)
	}
	if l, ok := containsLine(*logs, "engine.gap"); ok {
		t.Fatalf("경계 미만인데 확정 로그가 남았다: %q", l)
	}
}

// (c) TurnBoundarySilence=0 → 트리거 비활성, 기존 2s 무음 확정으로 폴백.
func TestTurnBoundaryGapDisabledFallsBackToSilence(t *testing.T) {
	e := New()
	e.TurnBoundarySilence = 0
	logs := collectLogs(e)
	t0 := time.Unix(1000, 0)

	e.IngestTranslatedDelta("폴백 확인 줄")
	e.Heartbeat(t0)
	e.Heartbeat(t0.Add(1500 * time.Millisecond)) // 1.5s — 경계 트리거가 살아 있었다면 확정될 시점
	if e.currentTranslation == "" {
		t.Fatal("트리거를 껐는데 1.5s에서 확정됐다")
	}
	e.Heartbeat(t0.Add(2500 * time.Millisecond)) // 2s 초과 → 기존 무음 확정
	if e.currentTranslation != "" {
		t.Fatalf("2s 무음 확정이 동작하지 않았다: %q", e.currentTranslation)
	}
	if _, ok := containsLine(*logs, "reason=turnBoundary-silence"); !ok {
		t.Fatalf("무음 fallback 사유 로그 없음: %v", *logs)
	}
	if _, ok := containsLine(*logs, "engine.gap"); ok {
		t.Fatalf("비활성인데 경계 로그가 남았다: %v", *logs)
	}
}

// (d) 경계 확정 직후 2s 시점 heartbeat → 추가 확정/토글 없음(no-op).
func TestTurnBoundaryGapThenSilenceIsNoop(t *testing.T) {
	e := New()
	t0 := time.Unix(1000, 0)
	e.IngestTranslatedDelta("한 번만 확정될 줄")
	e.Heartbeat(t0)
	e.Heartbeat(t0.Add(1000 * time.Millisecond)) // 경계 확정(토글 1회)
	speakerAfterGap := e.curSpeaker
	rollupAfterGap := len(e.rollupLines)

	e.Heartbeat(t0.Add(2000 * time.Millisecond)) // 2s 무음 시점 — 버퍼가 비어 no-op
	e.Heartbeat(t0.Add(3000 * time.Millisecond))
	if e.curSpeaker != speakerAfterGap {
		t.Fatalf("curSpeaker = %d, want %d (추가 토글 금지)", e.curSpeaker, speakerAfterGap)
	}
	if len(e.rollupLines) != rollupAfterGap {
		t.Fatalf("확정 줄 수 = %d, want %d (중복 확정 금지)", len(e.rollupLines), rollupAfterGap)
	}
	want := []Line{{Text: "한 번만 확정될 줄", Speaker: 0}}
	if got := e.DisplayLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DisplayLines = %v, want %v", got, want)
	}
}

// (e) charBreak로 이미 확정된 뒤 1.0s 갭 → 토글은 정확히 1회(contentSinceBoundary 규칙).
func TestTurnBoundaryGapAfterCharBreakTogglesOnce(t *testing.T) {
	e := New()
	e.MaxLines = 1
	e.CharsPerLine = 5
	e.MaxCharsBeforeBreak = 0
	t0 := time.Unix(1000, 0)

	e.IngestTranslatedDelta("abcde") // charBreak 확정(토글 없음, 플래그 유지)
	if e.curSpeaker != 0 {
		t.Fatalf("charBreak 직후 curSpeaker = %d, want 0", e.curSpeaker)
	}
	e.Heartbeat(t0)
	e.Heartbeat(t0.Add(1000 * time.Millisecond)) // 버퍼는 비었지만 갭 경계 — 확정 경로는 건너뛴다
	if e.curSpeaker != 0 {
		t.Fatalf("버퍼가 빈 상태의 갭에서 curSpeaker = %d, want 0 (확정할 내용이 없다)", e.curSpeaker)
	}

	// 같은 발화가 이어지다 갭이 벌어지면 그때 한 번만 토글된다.
	e.IngestTranslatedDelta("fg")
	e.Heartbeat(t0.Add(2000 * time.Millisecond))
	e.Heartbeat(t0.Add(3000 * time.Millisecond)) // 갭 경계 → 확정 + 토글 1회
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker = %d, want 1 (정확히 1회 토글)", e.curSpeaker)
	}
	e.Heartbeat(t0.Add(4000 * time.Millisecond)) // 추가 갭 — 확정할 내용 없음 → 토글 없음
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker = %d, want 1 (빈 버퍼 갭은 토글 금지)", e.curSpeaker)
	}
	want := []Line{{Text: "abcde", Speaker: 0}, {Text: "fg", Speaker: 0}}
	if got := e.DisplayLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DisplayLines = %v, want %v", got, want)
	}
}

// -----------------------------------------------------------------------------
// 오디오 도메인 화자 경계(TurnBoundaryHint) — VAD 옵저버가 실무음으로 잡은 경계.
// -----------------------------------------------------------------------------

// Hint는 다른 턴 경계와 동일하게 확정 + 토글한다.
func TestTurnBoundaryHintConfirmsAndToggles(t *testing.T) {
	e := New()
	logs := collectLogs(e)

	e.IngestTranslatedDelta("첫 화자 발화입니다.") // 문장 완결 → Hint가 즉시 확정
	e.TurnBoundaryHint()

	if e.currentTranslation != "" {
		t.Fatalf("Hint 후 진행 버퍼 = %q, want \"\"", e.currentTranslation)
	}
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker = %d, want 1", e.curSpeaker)
	}
	if _, ok := containsLine(*logs, "engine.audio 오디오 화자 경계 힌트"); !ok {
		t.Fatalf("오디오 경계 로그 없음: %v", *logs)
	}
	if _, ok := containsLine(*logs, "reason=turnBoundary-audio"); !ok {
		t.Fatalf("오디오 경계 사유 로그 없음: %v", *logs)
	}

	e.IngestTranslatedDelta("둘째 화자 발화")
	want := []Line{{Text: "첫 화자 발화입니다.", Speaker: 0}, {Text: "둘째 화자 발화", Speaker: 1}}
	if got := e.DisplayLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DisplayLines = %v, want %v", got, want)
	}
}

// 갭 확정이 같은 경계를 먼저 잡았으면, 뒤이은 Hint는 토글하지 않는다(이중 토글 방지 불변식).
func TestTurnBoundaryHintAfterGapNoDoubleToggle(t *testing.T) {
	e := New()
	t0 := time.Unix(1000, 0)

	e.IngestTranslatedDelta("갭이 먼저 닫는 발화")
	e.Heartbeat(t0)
	e.Heartbeat(t0.Add(1200 * time.Millisecond)) // 갭 경계 확정 + 토글
	if e.curSpeaker != 1 {
		t.Fatalf("갭 확정 후 curSpeaker = %d, want 1", e.curSpeaker)
	}
	rollupAfterGap := len(e.rollupLines)

	e.TurnBoundaryHint() // 같은 실제 경계를 오디오 쪽에서도 감지 — 추가 토글 금지.
	if e.curSpeaker != 1 {
		t.Fatalf("Hint 후 curSpeaker = %d, want 1 (이중 토글 금지)", e.curSpeaker)
	}
	if len(e.rollupLines) != rollupAfterGap {
		t.Fatalf("확정 줄 수 = %d, want %d (중복 확정 금지)", len(e.rollupLines), rollupAfterGap)
	}
}

// 반대 순서(오디오 Hint가 먼저, 갭이 뒤늦게)도 토글은 1회다.
func TestTurnBoundaryGapAfterHintNoDoubleToggle(t *testing.T) {
	e := New()
	t0 := time.Unix(2000, 0)

	e.IngestTranslatedDelta("오디오가 먼저 닫는 발화입니다.") // 문장 완결 → 즉시 확정
	e.Heartbeat(t0)
	e.TurnBoundaryHint() // 오디오 경계 확정 + 토글
	if e.curSpeaker != 1 {
		t.Fatalf("Hint 후 curSpeaker = %d, want 1", e.curSpeaker)
	}
	rollupAfterHint := len(e.rollupLines)

	e.Heartbeat(t0.Add(1500 * time.Millisecond)) // 갭 임계 초과 — 버퍼가 비어 no-op
	if e.curSpeaker != 1 {
		t.Fatalf("갭 후 curSpeaker = %d, want 1 (이중 토글 금지)", e.curSpeaker)
	}
	if len(e.rollupLines) != rollupAfterHint {
		t.Fatalf("확정 줄 수 = %d, want %d", len(e.rollupLines), rollupAfterHint)
	}
}

// 내용이 없는 상태의 Hint는 확정/토글 없이 무해하게 지나간다(빈 턴 소모 금지).
func TestTurnBoundaryHintOnEmptyBufferIsNoop(t *testing.T) {
	e := New()
	e.TurnBoundaryHint()
	e.TurnBoundaryHint()
	if e.curSpeaker != 0 {
		t.Fatalf("curSpeaker = %d, want 0", e.curSpeaker)
	}
	if len(e.rollupLines) != 0 {
		t.Fatalf("확정 줄 = %v, want 없음", e.rollupLines)
	}
	// 실제 내용이 온 뒤의 Hint에서 비로소 토글된다.
	e.IngestTranslatedDelta("첫 실제 발화입니다.") // 문장 완결 → 즉시 확정
	e.TurnBoundaryHint()
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker = %d, want 1", e.curSpeaker)
	}
}

// -----------------------------------------------------------------------------
// 텍스트 휴리스틱 1: 물음표 경계(질문 → 답변 전환)
// -----------------------------------------------------------------------------

func TestQuestionBoundaryConfirmsAndToggles(t *testing.T) {
	e := New()
	logs := collectLogs(e)

	e.IngestTranslatedDelta("어떻게 생각하세요?")
	if len(e.rollupLines) != 0 {
		t.Fatalf("질문만으로 확정되면 안 된다: %v", e.rollupLines)
	}
	e.IngestTranslatedDelta("저는 좋다고 봅니다") // 답변 델타 → 이음새에서 경계

	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker = %d, want 1 (질문→답변 토글)", e.curSpeaker)
	}
	if _, ok := containsLine(*logs, "reason=turnBoundary-question"); !ok {
		t.Fatalf("질문 경계 사유 로그 없음: %v", *logs)
	}
	want := []Line{
		{Text: "어떻게 생각하세요?", Speaker: 0},
		{Text: "저는 좋다고 봅니다", Speaker: 1},
	}
	if got := e.DisplayLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DisplayLines = %v, want %v", got, want)
	}
}

// 닫는 따옴표가 붙은 물음표도 인식하고, 마침표로 끝난 줄은 경계가 아니다.
func TestQuestionBoundaryPunctuationForms(t *testing.T) {
	cases := []struct {
		name    string
		first   string
		wantSep bool
	}{
		{"물음표", "정말요?", true},
		{"전각 물음표", "정말요？", true},
		{"인용 끝 물음표", `"정말요?"`, true},
		{"마침표", "그렇습니다.", false},
		{"미완결", "그리고 또", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := New()
			e.IngestTranslatedDelta(c.first)
			e.IngestTranslatedDelta(" 다음 발화 내용")
			gotSep := len(e.rollupLines) == 1
			if gotSep != c.wantSep {
				t.Fatalf("분리 여부 = %v, want %v (rollup=%v cur=%q)",
					gotSep, c.wantSep, e.rollupLines, e.currentTranslation)
			}
			if c.wantSep && e.curSpeaker != 1 {
				t.Fatalf("경계인데 토글되지 않았다: speaker=%d", e.curSpeaker)
			}
		})
	}
}

func TestQuestionBoundaryDisabled(t *testing.T) {
	e := New()
	e.QuestionBoundary = false
	e.IngestTranslatedDelta("어떻게 생각하세요?")
	e.IngestTranslatedDelta(" 저는 좋다고 봅니다")

	if len(e.rollupLines) != 0 {
		t.Fatalf("비활성인데 확정됐다: %v", e.rollupLines)
	}
	if e.curSpeaker != 0 {
		t.Fatalf("비활성인데 토글됐다: %d", e.curSpeaker)
	}
	if e.currentTranslation != "어떻게 생각하세요? 저는 좋다고 봅니다" {
		t.Fatalf("버퍼 = %q, want 이어붙임", e.currentTranslation)
	}
}

// 중복/빈 델타는 질문 경계를 유발하지 않는다(헛확정 방지).
func TestQuestionBoundaryIgnoresEmptyOrDuplicateDelta(t *testing.T) {
	e := New()
	e.IngestTranslatedDelta("어떻게 생각하세요?")
	e.IngestTranslatedDelta("")           // 빈 델타
	e.IngestTranslatedDelta("어떻게 생각하세요?") // 완전 중복(새 내용 없음)

	if len(e.rollupLines) != 0 {
		t.Fatalf("중복/빈 델타로 확정됐다: %v", e.rollupLines)
	}
	if e.curSpeaker != 0 {
		t.Fatalf("중복/빈 델타로 토글됐다: %d", e.curSpeaker)
	}
}

// -----------------------------------------------------------------------------
// 텍스트 휴리스틱 2: 경계 보류(문장부호 스냅)
// -----------------------------------------------------------------------------

// 문장 중간에 경계가 오면 보류했다가, 문장이 끝나는 델타에서 확정한다.
func TestHintDeferredUntilSentenceEnd(t *testing.T) {
	e := New()
	logs := collectLogs(e)

	e.IngestTranslatedDelta("이 문장은 아직")
	e.TurnBoundaryHint() // 문장 미완 → 보류
	if len(e.rollupLines) != 0 || e.curSpeaker != 0 {
		t.Fatalf("보류여야 하는데 확정됐다: rollup=%v speaker=%d", e.rollupLines, e.curSpeaker)
	}
	if _, ok := containsLine(*logs, "engine.hint 경계 보류(문장 미완)"); !ok {
		t.Fatalf("보류 로그 없음: %v", *logs)
	}

	e.IngestTranslatedDelta(" 끝나지 않았습니다.") // 문장 완결 → 해소
	if len(e.rollupLines) != 1 {
		t.Fatalf("문장 완결에도 확정되지 않았다: %v", e.rollupLines)
	}
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker = %d, want 1", e.curSpeaker)
	}
	if got := e.rollupLines[0].Text; got != "이 문장은 아직 끝나지 않았습니다." {
		t.Fatalf("확정 줄 = %q", got)
	}
	if _, ok := containsLine(*logs, "engine.hint 보류 해소(문장 완결)"); !ok {
		t.Fatalf("해소 로그 없음: %v", *logs)
	}
}

// 종결부호가 계속 안 오면 델타 캡(HintMaxDeltas)에서 확정한다(무한 보류 방지).
func TestHintDeltaCapResolves(t *testing.T) {
	e := New()
	logs := collectLogs(e)

	e.IngestTranslatedDelta("종결부호가")
	e.TurnBoundaryHint()
	e.IngestTranslatedDelta(" 계속") // 1번째 델타 — 아직 보류
	if len(e.rollupLines) != 0 {
		t.Fatalf("캡 이전에 확정됐다: %v", e.rollupLines)
	}
	e.IngestTranslatedDelta(" 오지 않는다") // 2번째 델타 — 캡 도달 → 확정
	if len(e.rollupLines) != 1 {
		t.Fatalf("캡에서 확정되지 않았다: rollup=%v cur=%q", e.rollupLines, e.currentTranslation)
	}
	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker = %d, want 1", e.curSpeaker)
	}
	if _, ok := containsLine(*logs, "engine.hint 보류 해소(델타 캡 2 도달)"); !ok {
		t.Fatalf("캡 해소 로그 없음: %v", *logs)
	}
}

// 보류 중 charBreak가 먼저 확정하면 보류는 흡수되고 추가 토글이 없다.
func TestHintAbsorbedByCharBreak(t *testing.T) {
	e := New()
	e.MaxLines = 1
	e.CharsPerLine = 8
	e.MaxCharsBeforeBreak = 0
	logs := collectLogs(e)

	e.IngestTranslatedDelta("문장중간")
	e.TurnBoundaryHint()             // 보류
	e.IngestTranslatedDelta("에서길어짐") // 9글자 → charBreak 확정(보류 흡수)

	if len(e.rollupLines) != 1 {
		t.Fatalf("charBreak 확정이 없다: %v", e.rollupLines)
	}
	if e.curSpeaker != 0 {
		t.Fatalf("charBreak인데 토글됐다: %d", e.curSpeaker)
	}
	if _, ok := containsLine(*logs, "engine.hint 보류 해소(다른 확정 흡수)"); !ok {
		t.Fatalf("흡수 로그 없음: %v", *logs)
	}
	// 흡수 후 다음 경계는 정상적으로 한 번만 토글한다.
	e.IngestTranslatedDelta("다음발화.")
	e.TurnBoundaryHint()
	if e.curSpeaker != 1 {
		t.Fatalf("흡수 후 다음 경계 토글 실패: %d", e.curSpeaker)
	}
}

// 보류 중 갭 확정이 먼저 발동해도 이중 토글이 없다.
func TestHintAbsorbedByGapNoDoubleToggle(t *testing.T) {
	e := New()
	logs := collectLogs(e)
	t0 := time.Unix(1000, 0)

	e.IngestTranslatedDelta("문장 중간에서 끊긴")
	e.TurnBoundaryHint() // 보류
	e.Heartbeat(t0)
	e.Heartbeat(t0.Add(1200 * time.Millisecond)) // 갭 확정(보류 흡수 + 토글 1회)

	if e.curSpeaker != 1 {
		t.Fatalf("curSpeaker = %d, want 1 (정확히 1회 토글)", e.curSpeaker)
	}
	if len(e.rollupLines) != 1 {
		t.Fatalf("확정 줄 = %v, want 1", e.rollupLines)
	}
	if _, ok := containsLine(*logs, "engine.hint 보류 해소(다른 확정 흡수)"); !ok {
		t.Fatalf("흡수 로그 없음: %v", *logs)
	}
	// 보류가 남아 있었다면 다음 델타에서 또 확정됐을 것이다 — 그렇지 않아야 한다.
	e.IngestTranslatedDelta("다음 화자 발화.")
	if len(e.rollupLines) != 1 {
		t.Fatalf("흡수 후에도 보류가 살아 있었다: %v", e.rollupLines)
	}
	if e.curSpeaker != 1 {
		t.Fatalf("추가 토글이 발생했다: %d", e.curSpeaker)
	}
}

// Reset은 보류 상태도 지운다(세션 재시작 시 유령 경계 방지).
func TestHintClearedOnReset(t *testing.T) {
	e := New()
	e.IngestTranslatedDelta("보류 유발 문장")
	e.TurnBoundaryHint()
	e.Reset()
	if e.hintArmed {
		t.Fatal("Reset 후에도 보류가 남았다")
	}
	e.IngestTranslatedDelta("새 세션 발화.")
	if len(e.rollupLines) != 0 {
		t.Fatalf("리셋 후 유령 확정: %v", e.rollupLines)
	}
}
