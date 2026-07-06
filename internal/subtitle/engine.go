// Package subtitle — 자막 누적 + 영화 자막식 roll-up 표시 엔진.
//
// 원본 liveTranslate/Sources/Subtitle/SubtitleEngine.swift(+ specs/008)를 이식한
// 순수 로직 구현이다. cgo/네트워크/전역 상태 없이 문자열·줄 상태만 제공한다.
//
// # 동작 모델
//
//   - delta 수신(IngestTranslatedDelta / IngestSourceDelta) → 현재 줄에 append 하여
//     문장이 자라는 것을 즉시 표시한다(dedup으로 모델의 비연속 반복을 완화).
//   - 확정(charBreak / TurnComplete / 무음 fallback) → 현재 줄을 정리해 roll-up
//     FIFO(rollupLines)에 push 하고 버퍼를 비운다. 세그먼트 경로(IngestTranslatedSegment
//     final=true)도 동일한 roll-up 표시로 통일한다.
//   - 무음 정리 — 시간은 반드시 Heartbeat(now)로 주입한다. 원본의 두 타이머
//     (silenceTimeout=2s 자동 확정, silenceClearSeconds=8s 화면 정리)를
//     Heartbeat 안에서 결정적으로 판정한다. time.Now()/난수는 사용하지 않는다.
//
// 렌더링/오버레이 창(P3)은 범위 밖이다 — 엔진은 "무엇을, 언제까지 보일지"만 결정한다.
package subtitle

import (
	"slices"
	"strings"
	"time"
	"unicode"
)

// 기본 튜닝값(원본 AppConfig/SettingsStore/SubtitleEngine 에서 그대로 이식).
const (
	// DefaultMaxLines 는 화면에 유지할 roll-up 줄 수(원본 StyleDefault.maxLines).
	DefaultMaxLines = 2
	// DefaultCharsPerLine 은 줄당 글자 환산 계수(원본 AppConfig.charsPerSubtitleLine).
	DefaultCharsPerLine = 28
	// DefaultMaxCharsBeforeBreak 은 사용자 지정 charBreak 임계 하한(원본 AppConfig.defaultMaxCharsBeforeBreak).
	DefaultMaxCharsBeforeBreak = 50
	// DefaultMaxRollupHistory 는 roll-up 히스토리 버퍼 상한 문장 수(원본 maxRollupHistory).
	DefaultMaxRollupHistory = 12
	// DefaultSilenceTimeout 은 무음 자동 확정 임계(원본 silenceTimeout = 2.0s).
	DefaultSilenceTimeout = 2 * time.Second
	// DefaultSilenceClearTimeout 은 연속 무음 화면 정리 임계(원본 rollupSilenceClearSeconds = 8.0s).
	DefaultSilenceClearTimeout = 8 * time.Second
)

// Engine 은 결정적 자막 상태 머신이다. 시간은 Heartbeat(now)로만 주입되며 내부에서
// time.Now()를 호출하지 않는다. 동시 접근을 가정하지 않는다(호출자가 단일 goroutine에서
// 직렬화하거나 외부에서 잠금).
type Engine struct {
	// 설정(생성 후 조정 가능). New()가 원본 기본값으로 채운다.
	MaxLines            int           // 표시 유지 줄 수(RollupLines/DisplayTranslation 클립).
	CharsPerLine        int           // 줄당 글자 환산 계수.
	MaxCharsBeforeBreak int           // 사용자 지정 charBreak 하한(0이면 줄 기반만 사용).
	MaxRollupHistory    int           // roll-up 히스토리 버퍼 상한.
	SilenceTimeout      time.Duration // 무음 자동 확정 임계.
	SilenceClearTimeout time.Duration // 연속 무음 화면 정리 임계.

	// OnConfirmedLine 은 실제 roll-up push가 일어난 경우에만 호출된다(중복 무시 시 호출 안 함).
	// 녹화 등 부가 기능이 확정 자막을 소비할 수 있다. source는 비어 있을 수 있다.
	OnConfirmedLine func(source, translation string)

	// 텍스트 상태.
	currentTranslation   string
	currentSource        string
	confirmedTranslation string
	confirmedSource      string
	visible              bool

	// roll-up 상태.
	rollupLines []string
	segmentMode bool

	// generation 경계 리셋 대기 플래그.
	pendingGenerationReset bool

	// 결정적 무음 추적. 시간은 Heartbeat(now)로만 갱신된다.
	lastActivity time.Time // 마지막으로 활동이 관측된 heartbeat 시각.
	hasActivity  bool      // lastActivity가 유효한지.
	sawActivity  bool      // 마지막 heartbeat 이후 새 delta/segment가 있었는지.
}

// New 는 원본 기본 파라미터로 초기화한 엔진을 반환한다.
func New() *Engine {
	return &Engine{
		MaxLines:            DefaultMaxLines,
		CharsPerLine:        DefaultCharsPerLine,
		MaxCharsBeforeBreak: DefaultMaxCharsBeforeBreak,
		MaxRollupHistory:    DefaultMaxRollupHistory,
		SilenceTimeout:      DefaultSilenceTimeout,
		SilenceClearTimeout: DefaultSilenceClearTimeout,
	}
}

// -----------------------------------------------------------------------------
// 표시 상태 접근자 (headless 통합이 소비)
// -----------------------------------------------------------------------------

// DisplayTranslation 은 HUD에 보여줄 현재 번역문을 반환한다.
//   - roll-up 모드: 최근 확정 줄들(suffix)에 진행 중 줄을 맨 아래로 붙여 줄바꿈으로 잇는다.
//   - 비세그먼트 폴백: 누적 중이면 누적분, 아니면 마지막 확정분.
func (e *Engine) DisplayTranslation() string {
	if e.segmentMode {
		keep := e.MaxLines + 2
		recent := suffixCopy(e.rollupLines, keep)
		if e.currentTranslation == "" {
			return strings.Join(recent, "\n")
		}
		return strings.Join(append(recent, e.currentTranslation), "\n")
	}
	if e.currentTranslation == "" {
		return e.confirmedTranslation
	}
	return e.currentTranslation
}

// DisplaySource 는 HUD에 보여줄 현재 원문을 반환한다. 세그먼트 모드에선 진행 중 원문 1줄.
func (e *Engine) DisplaySource() string {
	if e.segmentMode {
		return e.currentSource
	}
	if e.currentSource == "" {
		return e.confirmedSource
	}
	return e.currentSource
}

// Visible 은 자막을 화면에 보여야 하는지 반환한다.
func (e *Engine) Visible() bool { return e.visible }

// RollupLines 는 확정된 roll-up 줄들을 최대 MaxLines개(suffix)까지 반환한다(복사본).
func (e *Engine) RollupLines() []string {
	return suffixCopy(e.rollupLines, e.MaxLines)
}

// -----------------------------------------------------------------------------
// 입력 API
// -----------------------------------------------------------------------------

// IngestTranslatedDelta 는 번역 delta 조각을 현재 줄에 이어붙여 누적하고 즉시 표시한다.
// 빈/중복 조각은 무시된다. 누적이 charBreak 임계를 넘으면 즉시 확정(roll-up push)한다.
func (e *Engine) IngestTranslatedDelta(text string) {
	e.applyPendingGenerationReset()
	if !e.appendIfMeaningful(text, &e.currentTranslation) {
		return
	}
	e.segmentMode = true
	e.visible = true
	e.markActivity()
	if runeLen(e.currentTranslation) >= e.effectiveMaxChars() {
		e.confirmTurn()
	}
}

// IngestSourceDelta 는 원문 delta 조각을 현재 원문 줄에 누적한다.
// 표시/확정 타이밍은 번역 delta·turnComplete·무음이 주도하므로 여기선 누적만 한다.
func (e *Engine) IngestSourceDelta(text string) {
	e.applyPendingGenerationReset()
	if e.appendIfMeaningful(text, &e.currentSource) {
		e.markActivity()
	}
}

// IngestTranslatedSegment 는 STT/MT 세그먼트 엔진용 번역 수신 경로다.
// final=true면 dedup/collapse 후 roll-up FIFO에 직접 push, false면 진행 줄만 교체한다.
func (e *Engine) IngestTranslatedSegment(text string, final bool) {
	e.ingestSegment(&text, nil, final)
}

// IngestSourceSegment 는 STT/MT 세그먼트 엔진용 원문 수신 경로다.
// 원문은 진행 중(interim) 줄로만 라이브 갱신한다(확정 없음).
func (e *Engine) IngestSourceSegment(text string, final bool) {
	e.ingestSegment(nil, &text, final)
}

// TurnComplete 은 턴(발화) 종료 신호다. 누적된 현재 줄을 확정(roll-up push)하고,
// 다음 delta에서 버퍼를 비우도록 generation 리셋을 예약한다.
func (e *Engine) TurnComplete() {
	e.confirmTurn()
	e.pendingGenerationReset = true
}

// GenerationComplete 은 재번역(generation) 경계 신호다. 현재 줄은 그대로 유지하고
// 다음 delta에서 버퍼를 비우도록 예약한다. 무음 자동 확정은 다음 heartbeat 기준이 갱신되어
// 경계 직후 잘못 확정되지 않는다(원본의 silenceTask 취소에 대응).
func (e *Engine) GenerationComplete() {
	e.pendingGenerationReset = true
	e.markActivity()
}

// Interrupted 는 서버 인터럽트 수신 시 진행 중(미확정) 버퍼만 정리한다.
// 이미 확정된 rollupLines/confirmed/visible 은 보존한다.
func (e *Engine) Interrupted() {
	e.currentTranslation = ""
	e.currentSource = ""
	e.pendingGenerationReset = false
}

// Heartbeat 은 스트림 heartbeat(STT/delta)마다 주입되는 결정적 시간 신호다.
// 마지막 활동 이후 경과 시간이 임계를 넘으면 무음 자동 확정 / 화면 정리를 수행한다.
//   - 마지막 heartbeat 이후 새 활동이 있었으면 무음 기준 시각을 now로 리셋한다.
//   - 활동이 없고 SilenceTimeout 경과 + 진행 버퍼가 남아 있으면 자동 확정(roll-up push).
//   - 활동이 없고 SilenceClearTimeout 경과면 화면 전체를 비운다.
func (e *Engine) Heartbeat(now time.Time) {
	if e.sawActivity {
		e.lastActivity = now
		e.hasActivity = true
		e.sawActivity = false
		return
	}
	if !e.hasActivity {
		return
	}
	elapsed := now.Sub(e.lastActivity)
	if elapsed >= e.SilenceTimeout && (e.currentTranslation != "" || e.currentSource != "") {
		e.confirmTurn()
	}
	if elapsed >= e.SilenceClearTimeout {
		e.clearScreen()
	}
}

// Reset 은 세션 정지/재시작 시 모든 누적 텍스트와 상태를 비우고 즉시 숨긴다.
func (e *Engine) Reset() {
	e.currentTranslation = ""
	e.currentSource = ""
	e.confirmedTranslation = ""
	e.confirmedSource = ""
	e.rollupLines = nil
	e.segmentMode = false
	e.pendingGenerationReset = false
	e.visible = false
	e.lastActivity = time.Time{}
	e.hasActivity = false
	e.sawActivity = false
}

// -----------------------------------------------------------------------------
// 내부: 세그먼트/확정/표시
// -----------------------------------------------------------------------------

func (e *Engine) ingestSegment(translation, source *string, final bool) {
	e.segmentMode = true
	e.pendingGenerationReset = false

	if source != nil {
		e.currentSource = *source
	}

	if translation != nil {
		if final {
			collapsed := strings.TrimSpace(dedupGlobalSentences(collapseRepeats(*translation)))
			if collapsed != "" && (len(e.rollupLines) == 0 || e.rollupLines[len(e.rollupLines)-1] != collapsed) {
				e.pushRollup(collapsed, e.currentSource)
			}
			e.currentTranslation = "" // final-only: 진행 중 번역 줄 없음.
		} else {
			e.currentTranslation = *translation
		}
	}

	e.visible = true
	e.markActivity()
}

// confirmTurn 은 누적된 현재(delta) 줄을 정리해 roll-up FIFO에 push 한다(charBreak/turnComplete/
// 무음 fallback 공유). 직전 줄과 공백 무시 동일하면 push 하지 않는다(경계 중복 방지).
func (e *Engine) confirmTurn() {
	sourceSnapshot := e.currentSource
	collapsed := strings.TrimSpace(dedupGlobalSentences(collapseRepeats(e.currentTranslation)))
	e.currentTranslation = ""
	e.currentSource = ""
	if collapsed == "" {
		return
	}
	if n := len(e.rollupLines); n > 0 && removeWhitespace(e.rollupLines[n-1]) == removeWhitespace(collapsed) {
		// 직전 줄과 동일한 경계/재번역 중복 → push 생략.
	} else {
		e.pushRollup(collapsed, sourceSnapshot)
	}
	e.segmentMode = true
	e.visible = true
}

func (e *Engine) pushRollup(line, source string) {
	e.rollupLines = append(e.rollupLines, line)
	if len(e.rollupLines) > e.MaxRollupHistory {
		e.rollupLines = e.rollupLines[len(e.rollupLines)-e.MaxRollupHistory:]
	}
	if e.OnConfirmedLine != nil {
		e.OnConfirmedLine(source, line)
	}
}

func (e *Engine) clearScreen() {
	e.rollupLines = nil
	e.currentTranslation = ""
	e.currentSource = ""
	e.visible = false
	e.hasActivity = false
}

// applyPendingGenerationReset 은 generation 리셋이 대기 중이면 직전 generation의 누적을
// confirmed로 옮기고 current를 비운다(다음 generation이 빈 버퍼에서 새로 자라도록).
func (e *Engine) applyPendingGenerationReset() {
	if !e.pendingGenerationReset {
		return
	}
	if e.currentTranslation != "" {
		e.confirmedTranslation = e.currentTranslation
	}
	if e.currentSource != "" {
		e.confirmedSource = e.currentSource
	}
	e.currentTranslation = ""
	e.currentSource = ""
	e.pendingGenerationReset = false
}

func (e *Engine) markActivity() { e.sawActivity = true }

// effectiveMaxChars 는 charBreak 임계를 계산한다: max(줄 기반, 사용자 지정).
// 줄수 증가가 항상 누적량 증가로 이어지도록 둘 중 큰 값을 쓴다(원본 maxCharsBeforeBreak).
func (e *Engine) effectiveMaxChars() int {
	lines := e.MaxLines
	if lines < 1 {
		lines = 1
	}
	byLines := e.CharsPerLine * lines
	if e.MaxCharsBeforeBreak > byLines {
		return e.MaxCharsBeforeBreak
	}
	return byLines
}

// -----------------------------------------------------------------------------
// 내부: 누적 + dedup
// -----------------------------------------------------------------------------

// appendIfMeaningful 은 delta를 버퍼에 누적한다. 증분/누적/반복이 섞여 와도 중복 없이 합친다.
//  1. 겹침 억제 머지: delta 접두사가 buffer 접미사와 겹치면 새 부분만 붙인다.
//  2. collapseRepeats(즉시 반복) → dedupGlobalSentences(전역 중복 문장)로 정리.
//
// 실질적으로 버퍼가 바뀌어 표시 갱신이 필요하면 true를 반환한다.
func (e *Engine) appendIfMeaningful(delta string, buffer *string) bool {
	if delta == "" {
		return false
	}
	if *buffer == "" {
		collapsed := dedupGlobalSentences(collapseRepeats(delta))
		if collapsed == "" {
			return false
		}
		*buffer = collapsed
		return true
	}
	bChars := []rune(*buffer)
	dChars := []rune(delta)
	k := min(len(bChars), len(dChars))
	for k > 0 {
		if slices.Equal(bChars[len(bChars)-k:], dChars[:k]) {
			break
		}
		k--
	}
	newPart := string(dChars[k:])
	if newPart == "" {
		return false
	}
	merged := *buffer + newPart
	collapsed := dedupGlobalSentences(collapseRepeats(merged))
	if collapsed == *buffer {
		return false
	}
	*buffer = collapsed
	return true
}

// collapseRepeats 는 공백 토큰열에서 끝에 연속으로 반복된 동일 부분열을 1회로 합친다.
// 최소 3토큰 반복부터, 전체 토큰이 6개 이상일 때만 붕괴한다(원본 collapseRepeats).
func collapseRepeats(text string) string {
	tokens := strings.Fields(text)
	if len(tokens) < 6 {
		return text
	}
	for guard := 0; guard < 200; guard++ {
		changed := false
		n := len(tokens)
		for m := n / 2; m >= 3; m-- {
			if slices.Equal(tokens[n-m:n], tokens[n-2*m:n-m]) {
				tokens = tokens[:n-m]
				changed = true
				break
			}
		}
		if !changed {
			break
		}
	}
	return strings.Join(tokens, " ")
}

// dedupGlobalSentences 는 누적 버퍼에서 이미 등장한 동일 문장의 재등장을 제거한다(첫 등장만 유지).
// 종결부호(. ! ? 。 ！ ？)로 문장을 나누고 공백 무시 정규화로 비교한다.
// 진행 중인 마지막 조각(종결부호로 안 끝남)은 성장 중이므로 제거하지 않는다.
// norm 길이 4 미만의 짧은 문장은 우연 중복 방지를 위해 dedup 대상에서 제외한다.
func dedupGlobalSentences(text string) string {
	terminators := map[rune]bool{'.': true, '!': true, '?': true, '。': true, '！': true, '？': true}
	var sentences []string
	var cur strings.Builder
	for _, ch := range text {
		cur.WriteRune(ch)
		if terminators[ch] {
			sentences = append(sentences, cur.String())
			cur.Reset()
		}
	}
	hasPartialTail := cur.Len() > 0
	if hasPartialTail {
		sentences = append(sentences, cur.String())
	}
	seen := map[string]bool{}
	var result []string
	for i, s := range sentences {
		isPartialLast := hasPartialTail && i == len(sentences)-1
		key := removeWhitespace(s)
		keyLen := runeLen(key)
		if !isPartialLast && keyLen >= 4 && seen[key] {
			continue // 이미 표시한 완성 문장의 재등장 → 제거.
		}
		if !isPartialLast && keyLen >= 4 {
			seen[key] = true
		}
		result = append(result, s)
	}
	return strings.Join(result, "")
}

// -----------------------------------------------------------------------------
// 내부: 유틸
// -----------------------------------------------------------------------------

func removeWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

func runeLen(s string) int { return len([]rune(s)) }

// suffixCopy 는 s의 마지막 n개 원소를 새 슬라이스로 복사해 반환한다(원본 미변형).
func suffixCopy(s []string, n int) []string {
	if n < 0 {
		n = 0
	}
	if len(s) < n {
		n = len(s)
	}
	out := make([]string, n)
	copy(out, s[len(s)-n:])
	return out
}
