// Package subtitle — 자막 누적 + 영화 자막식 roll-up 표시 엔진.
//
// 원본 liveTranslate/Sources/Subtitle/SubtitleEngine.swift(+ specs/008)를 이식한
// 순수 로직 구현이다. cgo/네트워크/전역 상태 없이 문자열·줄 상태만 제공한다.
//
// # 동작 모델
//
//   - delta 수신(IngestTranslatedDelta / IngestSourceDelta) → 현재 줄에 append 하여
//     문장이 자라는 것을 즉시 표시한다(dedup으로 모델의 비연속 반복을 완화).
//   - 확정(charBreak / 턴 경계 / 무음 fallback) → 현재 줄을 정리해 roll-up
//     FIFO(rollupLines)에 push 하고 버퍼를 비운다. 세그먼트 경로(IngestTranslatedSegment
//     final=true)도 동일한 roll-up 표시로 통일한다.
//   - 무음 정리 — 시간은 반드시 Heartbeat(now)로 주입한다. 원본의 두 타이머
//     (silenceTimeout=2s 자동 확정, silenceClearSeconds=8s 화면 정리)를
//     Heartbeat 안에서 결정적으로 판정한다. time.Now()/난수는 사용하지 않는다.
//
// # 화자 근사(2색 교대)
//
// Gemini Live에는 diarization이 없고, 실측상 gemini-3.5-live-translate는 turnComplete를
// 아예 보내지 않는다(연속 스트림). 그래서 아래 신호들을 "화자가 바뀌었을 것"의 근사로 쓴다.
// 실제 줄이 확정될 때 화자 패리티(curSpeaker 0/1)를 토글하며, 우선순위는 다음과 같다.
//
//   - TurnBoundaryHint()  — **1차**. controller가 오디오 도메인 VAD 옵저버(internal/vad)에서
//     받은 화자 경계 후보를 다음 번역 delta 직전에 넣어준다(reason=turnBoundary-audio).
//     진행 줄이 문장 중간이면 곧바로 확정하지 않고 보류(armed)했다가, 뒤따르는 delta가
//     문장을 끝내는 순간(또는 HintMaxDeltas 캡) 확정해 색이 문장 중간에서 바뀌지 않게 한다.
//   - 물음표 경계 — 물음표로 끝난 줄 뒤에 새 delta가 오면 질문→답변 전환으로 본다
//     (QuestionBoundary, reason=turnBoundary-question).
//   - 델타 갭(TurnBoundarySilence, 1.0s) — **백업**. 텍스트 도착 간격 기반이라 원문/번역
//     인터리빙과 서버 스트리밍 주기에 영향을 받는다(reason=turnBoundary-gap).
//   - TurnComplete() — 이 신호를 보내는 모델을 위해 유지한다(reason=turnBoundary-turnComplete).
//   - 무음 2초 fallback — 경계 트리거를 모두 끈 경우의 최후 보루(reason=turnBoundary-silence).
//
// 어느 경로든 "이 턴에 실제로 누적된 내용이 있을 때만"(contentSinceBoundary) 토글하므로
// 여러 트리거가 같은 경계에 겹쳐도 색은 한 번만 바뀐다. charBreak(글자수 초과) 확정은
// 같은 발화가 길어진 것이므로 토글하지 않는다.
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
	// DefaultTurnBoundarySilence 는 턴(발화) 경계로 간주하는 델타 갭 임계다.
	//
	// 근거(실측): gemini-3.5-live-translate는 turnComplete를 보내지 않는(연속 스트림) 모델이라
	// 턴 경계 신호가 없다. 실사용 로그의 수신 델타 간격 분포를 보면 연속 발화 중 스트리밍
	// 주기는 0.8~0.9s(140회)에 몰려 있고, 발화가 실제로 끊긴 지점은 1.0~1.7s(16회)에 나타나
	// 0.9s와 1.0s 사이에 뚜렷한 절벽이 있다. 그래서 1.0s를 경계 임계로 잡는다.
	// (원본 2.0s 무음 확정은 너무 늦어 확정 54건 중 52건이 charBreak로 잡혀 색 교대가
	// 사실상 동작하지 않았다.)
	DefaultTurnBoundarySilence = 1 * time.Second
	// HintMaxDeltas 는 오디오 경계 보류(문장 완결 대기)의 상한 델타 수. 이만큼 지나도
	// 종결부호가 오지 않으면 문장 중간이라도 확정한다(무한 보류 방지).
	HintMaxDeltas = 2
	// DefaultSilenceClearTimeout 은 연속 무음 화면 정리 임계(원본 rollupSilenceClearSeconds = 8.0s).
	DefaultSilenceClearTimeout = 8 * time.Second
)

// Line 은 표시 대상 자막 한 줄과 그 줄의 화자 패리티다.
// Speaker 는 0 또는 1이며, 렌더러가 색(기본색/보조색)을 고르는 데만 쓴다.
// 실제 화자 식별이 아니라 턴 경계 기반 근사임에 주의한다.
type Line struct {
	Text    string
	Speaker int // 0=기본 색, 1=보조 색.
	// Source 는 이 줄에 대응하는 원문이다. 원문 동시 표시가 켜졌을 때만 채워지며,
	// 렌더러가 "번역문 → 원문" 순서로 바로 아래에 덧붙여 그린다. 확정된 줄은 확정
	// 시점의 원문 스냅샷을 그대로 들고 있어, 줄이 화면에 남아 있는 동안 원문도 함께 남는다.
	Source string
}

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

	// TurnBoundarySilence 는 "델타 갭이 이만큼 벌어지면 발화(턴)가 끊긴 것"으로 보는 임계다
	// (기본 1.0s). 오디오 VAD 옵저버(TurnBoundaryHint)가 1차 트리거이고 이쪽은 **백업**이다 —
	// 텍스트 도착 간격 기반이라 원문/번역 인터리빙과 서버 스트리밍 주기에 영향을 받는다.
	// 0 이하면 이 트리거를 끄고 기존 SilenceTimeout(2s) 동작으로 폴백한다.
	TurnBoundarySilence time.Duration

	// QuestionBoundary 는 "물음표로 끝난 줄 뒤의 새 델타 = 질문→답변 전환" 휴리스틱 스위치다
	// (기본 true, 설정 UI 미노출). 인터뷰/대화에서 오디오 무음만으로는 놓치는 교대를 잡는다.
	QuestionBoundary bool

	// SpeakerAlternate 는 화자 근사 2색 교대의 숨은 스위치다(기본 true, 설정 UI 미노출).
	// false면 DisplayLines 가 모든 줄의 Speaker 를 0으로 내려 기존 단색 표시와 동일해진다.
	// (내부 패리티 추적은 계속 돌아가므로 런타임 토글 시 상태가 어긋나지 않는다.)
	SpeakerAlternate bool

	// OnConfirmedLine 은 실제 roll-up push가 일어난 경우에만 호출된다(중복 무시 시 호출 안 함).
	// 녹화 등 부가 기능이 확정 자막을 소비할 수 있다. source는 비어 있을 수 있다.
	OnConfirmedLine func(source, translation string)

	// Logf 는 엔진의 판단 근거(확정 사유·화자 토글·턴 경계 도착 시점)를 밖으로 흘리는
	// 진단 훅이다. nil이면 no-op — 엔진은 순수 상태머신이라 파일/전역 로거를 직접 쓰지 않고,
	// controller가 트랜잭션 로거(internal/txlog)를 주입한다. 호출 빈도는 확정/턴 단위로 낮다.
	Logf func(format string, args ...any)

	// 텍스트 상태.
	currentTranslation   string
	currentSource        string
	confirmedTranslation string
	confirmedSource      string
	visible              bool

	// roll-up 상태.
	rollupLines []Line
	segmentMode bool

	// 화자 근사 상태. 턴 경계 확정(오디오 경계/물음표/갭/turnComplete/무음)에서만 0↔1
	// 토글된다(charBreak 제외). 진행 중(미확정) 줄도 이 값을 따른다.
	curSpeaker int
	// hintArmed/hintDeltas — 오디오 경계가 도착했지만 진행 줄이 문장 중간이라 확정을 보류한
	// 상태와, 보류 이후 지나간 델타 수(HintMaxDeltas에 도달하면 문장 미완이어도 확정).
	hintArmed  bool
	hintDeltas int

	// contentSinceBoundary 는 "마지막 턴 경계 이후 이 턴에 속한 번역 내용이 실제로
	// 누적됐는지"다. 토글 판정을 '이번 호출의 push 여부'가 아니라 이 플래그로 하는 이유:
	//   - charBreak로 이미 확정돼 버퍼가 빈 상태에서 경계 신호가 와도 턴은 끝난 것이므로
	//     토글해야 한다(플래그는 charBreak에서 지워지지 않는다).
	//   - 한 경계를 여러 트리거(오디오/갭/turnComplete)가 겹쳐 잡아도, 먼저 확정한 쪽이
	//     플래그를 내리므로 뒤따르는 신호는 토글하지 않는다(이중 토글 방지).
	contentSinceBoundary bool

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
		TurnBoundarySilence: DefaultTurnBoundarySilence,
		SpeakerAlternate:    true,
		QuestionBoundary:    true,
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
		recent := lineTexts(suffixCopy(e.rollupLines, keep))
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
	return lineTexts(suffixCopy(e.rollupLines, e.MaxLines))
}

// DisplayLines 는 지금 화면에 뿌릴 줄들(확정 roll-up suffix + 진행 중 줄)을 화자 패리티와
// 함께 반환한다. DisplayTranslation()과 같은 표시 집합이지만, 소비자(오버레이 IPC)가 줄마다
// 색을 고를 수 있도록 분해된 형태다. 빈/공백 줄은 제외한다(오버레이가 빈 줄을 그리지 않도록).
// SpeakerAlternate=false면 모든 Speaker 가 0이다.
func (e *Engine) DisplayLines() []Line {
	var out []Line
	// add 는 한 덩어리(줄바꿈 포함 가능)를 표시 줄들로 쪼개 담는다. 원문은 그 덩어리의
	// 마지막 표시 줄에만 붙여 "번역문 → 원문" 순서가 유지되게 한다.
	add := func(text string, speaker int, source string) {
		parts := strings.Split(text, "\n")
		last := -1
		for i, ln := range parts {
			if strings.TrimSpace(ln) != "" {
				last = i
			}
		}
		for i, ln := range parts {
			if strings.TrimSpace(ln) == "" {
				continue
			}
			src := ""
			if i == last {
				src = source
			}
			out = append(out, Line{Text: ln, Speaker: e.speakerOf(speaker), Source: src})
		}
	}
	if e.segmentMode {
		keep := e.MaxLines + 2
		for _, l := range suffixCopy(e.rollupLines, keep) {
			add(l.Text, l.Speaker, l.Source)
		}
		if e.currentTranslation != "" {
			add(e.currentTranslation, e.curSpeaker, e.currentSource)
		}
		return out
	}
	// 비세그먼트 폴백(미리보기 포함): 누적 중이면 누적분, 아니면 마지막 확정분 한 덩어리.
	if e.currentTranslation == "" {
		add(e.confirmedTranslation, e.curSpeaker, e.confirmedSource)
	} else {
		add(e.currentTranslation, e.curSpeaker, e.currentSource)
	}
	return out
}

// speakerOf 는 스위치가 꺼져 있으면 패리티를 0으로 눌러 기존 단색 표시를 유지한다.
func (e *Engine) speakerOf(s int) int {
	if !e.SpeakerAlternate {
		return 0
	}
	return s & 1
}

// -----------------------------------------------------------------------------
// 입력 API
// -----------------------------------------------------------------------------

// IngestTranslatedDelta 는 번역 delta 조각을 현재 줄에 이어붙여 누적하고 즉시 표시한다.
// 빈/중복 조각은 무시된다. 누적이 charBreak 임계를 넘으면 즉시 확정(roll-up push)한다.
func (e *Engine) IngestTranslatedDelta(text string) {
	e.applyPendingGenerationReset()

	// 질문 경계(텍스트 휴리스틱): 지금까지의 줄이 물음표로 끝났고 이 델타가 새 내용을
	// 가져오면, 질문→답변 전환으로 보고 **델타를 붙이기 전에** 줄을 확정한다.
	// 델타가 실제로 새 내용을 더하는 경우에만 확정해야 중복 델타로 헛확정이 나지 않는다.
	if e.QuestionBoundary && e.currentTranslation != "" &&
		endsWithQuestion(e.currentTranslation) {
		if fresh, ok := newContentOf(e.currentTranslation, text); ok {
			e.logf("engine.question 물음표 뒤 새 델타 → 질문/답변 경계 current=%q delta=%q",
				preview(e.currentTranslation), preview(fresh))
			e.confirmTurn(reasonQuestion)
			// 새 줄은 이 델타만으로 시작한다(앞 문장과 이어붙이지 않는다).
			e.currentTranslation = fresh
			e.segmentMode = true
			e.visible = true
			e.markActivity()
			e.contentSinceBoundary = true
			e.resolveHintOnDelta()
			if runeLen(e.currentTranslation) >= e.effectiveMaxChars() {
				e.confirmTurn(reasonCharBreak)
			}
			return
		}
	}

	if !e.appendIfMeaningful(text, &e.currentTranslation) {
		return
	}
	e.segmentMode = true
	e.visible = true
	e.markActivity()
	// 이 턴에 속한 번역 내용이 실제로 들어왔다 — 다음 턴 경계에서 화자를 넘길 근거.
	e.contentSinceBoundary = true
	// 보류 중인 오디오 경계가 있으면 문장 완결/캡 조건을 여기서 판정한다.
	if e.resolveHintOnDelta() {
		return // 경계로 확정됐다 — charBreak 판정은 다음 델타부터.
	}
	if runeLen(e.currentTranslation) >= e.effectiveMaxChars() {
		// charBreak — 같은 발화가 길어져 줄이 넘어간 것이므로 화자 패리티는 유지한다.
		e.confirmTurn(reasonCharBreak)
	}
}

// IngestSourceDelta 는 원문 delta 조각을 현재 원문 줄에 누적한다.
// 표시/확정 타이밍은 번역 delta·턴 경계·무음이 주도하므로 여기선 누적만 한다.
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
// 턴 경계이므로 이 턴에 누적된 내용이 있었다면 화자 패리티를 토글한다(화자 전환 근사).
// 무음 fallback으로 이미 확정된 직후라면 플래그가 내려가 있어 이중 토글되지 않는다.
func (e *Engine) TurnComplete() {
	// 진단: turnComplete 도착 시점의 진행 버퍼 상태를 남긴다. 버퍼가 비어 있으면
	// 무음 fallback/charBreak가 이미 이 턴을 확정했다는 뜻이다(확정 사유는 engine.confirm
	// 로그의 reason으로 판별한다).
	e.logf("engine.turn TurnComplete 도착 currentEmpty=%v current=%q contentSinceBoundary=%v speaker=%d",
		e.currentTranslation == "", preview(e.currentTranslation), e.contentSinceBoundary, e.curSpeaker)

	e.confirmTurn(reasonTurnComplete)
	e.pendingGenerationReset = true
}

// TurnBoundaryHint 는 오디오 도메인(VAD 옵저버)에서 감지한 화자 경계를 반영한다.
// 캡처 오디오의 실무음이 임계(기본 700ms) 이상 지속된 뒤 새 발화가 시작되면 controller가
// 그 다음 번역 delta를 넣기 **직전에** 호출한다. 번역 지연 특성상 그 시점엔 이전 발화의
// 번역이 대체로 끝나 있어, 새 발화 텍스트가 새 줄·새 색으로 시작한다.
//
// 확정/토글 규칙은 다른 턴 경계와 완전히 동일하다. 특히 갭 트리거가 같은 실제 경계를 먼저
// 잡아 contentSinceBoundary를 내렸다면 여기서는 토글하지 않는다(이중 토글 방지 불변식).
func (e *Engine) TurnBoundaryHint() {
	e.logf("engine.audio 오디오 화자 경계 힌트 currentEmpty=%v current=%q contentSinceBoundary=%v speaker=%d",
		e.currentTranslation == "", preview(e.currentTranslation), e.contentSinceBoundary, e.curSpeaker)

	// 문장 중간에서 색이 바뀌면 어색하다. 진행 줄이 문장 종결부호로 끝나지 않았으면 즉시
	// 확정하지 않고 **보류(armed)** 했다가, 뒤따르는 델타가 문장을 끝내는 순간 확정한다.
	// 무한 보류를 막기 위해 델타 HintMaxDeltas개까지만 기다린다.
	if e.currentTranslation != "" && !endsWithSentence(e.currentTranslation) {
		if e.hintArmed {
			e.logf("engine.hint 경계 보류 유지(이미 보류 중) deltas=%d", e.hintDeltas)
			return
		}
		e.hintArmed = true
		e.hintDeltas = 0
		e.logf("engine.hint 경계 보류(문장 미완) current=%q", preview(e.currentTranslation))
		return
	}

	e.confirmTurn(reasonAudio)
}

// resolveHintOnDelta 는 보류 중인 오디오 경계를 델타 도착 시점에 판정한다.
// 버퍼가 문장으로 끝났으면 즉시 확정하고, 그렇지 않으면 델타 카운트를 올려 캡(HintMaxDeltas)에
// 도달했을 때 확정한다. 확정했으면 true.
func (e *Engine) resolveHintOnDelta() bool {
	if !e.hintArmed {
		return false
	}
	if endsWithSentence(e.currentTranslation) {
		e.logf("engine.hint 보류 해소(문장 완결) deltas=%d current=%q", e.hintDeltas, preview(e.currentTranslation))
		e.disarmHint()
		e.confirmTurn(reasonAudio)
		return true
	}
	e.hintDeltas++
	if e.hintDeltas >= HintMaxDeltas {
		e.logf("engine.hint 보류 해소(델타 캡 %d 도달) current=%q", HintMaxDeltas, preview(e.currentTranslation))
		e.disarmHint()
		e.confirmTurn(reasonAudio)
		return true
	}
	return false
}

// disarmHint 는 보류 상태를 해제한다.
func (e *Engine) disarmHint() {
	e.hintArmed = false
	e.hintDeltas = 0
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
// 마지막 활동 이후 경과 시간이 임계를 넘으면 턴 경계 확정 / 무음 자동 확정 / 화면 정리를 한다.
//   - 마지막 heartbeat 이후 새 활동이 있었으면 무음 기준 시각을 now로 리셋한다.
//   - 활동이 없고 TurnBoundarySilence(1.0s) 경과 + 진행 버퍼가 남아 있으면 턴 경계로 확정한다
//     (화자 패리티 토글 포함). 오디오 경계(TurnBoundaryHint)를 보완하는 백업 트리거다.
//   - 활동이 없고 SilenceTimeout(2s) 경과 + 진행 버퍼가 남아 있으면 자동 확정(roll-up push).
//     경계 확정이 먼저 버퍼를 비우므로 보통은 no-op이고, 경계 트리거를 끈 경우의 폴백이다.
//   - 활동이 없고 SilenceClearTimeout(8s) 경과면 화면 전체를 비운다.
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
	if e.TurnBoundarySilence > 0 && elapsed >= e.TurnBoundarySilence &&
		(e.currentTranslation != "" || e.currentSource != "") {
		// 델타 갭이 경계 임계를 넘었다 → 발화가 끊긴 지점으로 보고 턴 경계 확정.
		e.logf("engine.gap 무음 %dms ≥ 경계 임계 %dms → 턴 경계 확정 current=%q",
			elapsed.Milliseconds(), e.TurnBoundarySilence.Milliseconds(), preview(e.currentTranslation))
		e.confirmTurn(reasonGap)
	}
	if elapsed >= e.SilenceTimeout && (e.currentTranslation != "" || e.currentSource != "") {
		// 무음 2초 fallback — 어떤 경계 트리거도 잡지 못한 턴으로 보고 확정/토글한다.
		e.logf("engine.silence 무음 %dms 경과 → 자동 확정(경계 트리거 미발동) current=%q",
			elapsed.Milliseconds(), preview(e.currentTranslation))
		e.confirmTurn(reasonSilence)
	}
	if elapsed >= e.SilenceClearTimeout {
		e.logf("engine.clear 무음 %dms 경과 → 화면 정리 rollup=%d", elapsed.Milliseconds(), len(e.rollupLines))
		e.clearScreen()
	}
}

// ShowPreview 는 '테스트 자막 표시' 토글 ON 시(번역 정지 상태) 샘플 자막을 페이드/타이머
// 없이 고정 표시한다(원본 SubtitleEngine.showPreview 이식). current/confirmed를 동일 텍스트로
// 채워 어느 표시 경로에서도 빈 버퍼로 자막이 사라지지 않게 하고, 무음 추적을 비활성화
// (hasActivity=false, lastActivity=zero)해 Heartbeat가 자동 확정/화면 정리를 하지 않게 한다.
// source가 빈 문자열이면 원문 줄은 표시되지 않는다(원문 동시 표시가 꺼진 경우).
func (e *Engine) ShowPreview(translation, source string) {
	e.currentTranslation = translation
	e.currentSource = source
	e.confirmedTranslation = translation
	e.confirmedSource = source
	e.rollupLines = nil
	e.segmentMode = false
	e.pendingGenerationReset = false
	e.visible = true
	e.curSpeaker = 0 // 미리보기는 항상 기본 색.
	e.contentSinceBoundary = false
	e.disarmHint()
	e.lastActivity = time.Time{}
	e.hasActivity = false
	e.sawActivity = false
}

// HidePreview 는 '테스트 자막 표시' 토글 OFF 시 미리보기를 비우고 즉시 숨긴다
// (원본 SubtitleEngine.hidePreview → reset 경로 이식).
func (e *Engine) HidePreview() { e.Reset() }

// Reset 은 세션 정지/재시작 시 모든 누적 텍스트와 상태를 비우고 즉시 숨긴다.
func (e *Engine) Reset() {
	e.logf("engine.reset 엔진 리셋 rollup=%d current=%q speaker=%d",
		len(e.rollupLines), preview(e.currentTranslation), e.curSpeaker)
	e.currentTranslation = ""
	e.currentSource = ""
	e.confirmedTranslation = ""
	e.confirmedSource = ""
	e.rollupLines = nil
	e.segmentMode = false
	e.pendingGenerationReset = false
	e.visible = false
	e.curSpeaker = 0 // 새 세션은 기본 색부터.
	e.contentSinceBoundary = false
	e.disarmHint()
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
			// 세그먼트(STT/MT) 경로는 Gemini Live 턴 경계와 별개라 화자 패리티를 토글하지
			// 않는다(현 파이프라인 미사용 경로 — 근사 규칙을 turnComplete/무음에 한정).
			collapsed := strings.TrimSpace(dedupGlobalSentences(collapseRepeats(*translation)))
			if collapsed != "" && (len(e.rollupLines) == 0 || e.rollupLines[len(e.rollupLines)-1].Text != collapsed) {
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

// confirmReason 은 확정(confirmTurn)이 일어난 사유다. 화자 패리티 토글 여부(turnBoundary)와
// 진단 로그의 사유 문자열을 함께 결정한다.
type confirmReason int

const (
	reasonCharBreak    confirmReason = iota // 글자수 초과 — 같은 발화가 길어져 줄이 넘어감.
	reasonTurnComplete                      // 서버 turnComplete — 보내는 모델에서의 경계 신호.
	reasonAudio                             // 오디오 무음 관찰(VAD 옵저버) — 1차 경계 트리거.
	reasonQuestion                          // 물음표 뒤 새 델타 — 질문→답변 전환 휴리스틱.
	reasonGap                               // 델타 갭 ≥ TurnBoundarySilence(1.0s) — 백업 트리거.
	reasonSilence                           // 무음 2초 fallback — 경계 트리거를 끈 경우의 폴백.
)

// isTurnBoundary 는 이 사유가 턴(발화) 경계인지 보고한다(charBreak만 경계가 아니다).
func (r confirmReason) isTurnBoundary() bool { return r != reasonCharBreak }

func (r confirmReason) String() string {
	switch r {
	case reasonTurnComplete:
		return "turnBoundary-turnComplete"
	case reasonAudio:
		return "turnBoundary-audio"
	case reasonQuestion:
		return "turnBoundary-question"
	case reasonGap:
		return "turnBoundary-gap"
	case reasonSilence:
		return "turnBoundary-silence"
	default:
		return "charBreak"
	}
}

// confirmTurn 은 누적된 현재(delta) 줄을 정리해 roll-up FIFO에 push 한다(charBreak/오디오 경계/
// 물음표/갭/turnComplete/무음 fallback 공유). 직전 줄과 공백 무시 동일하면 push 하지 않는다
// (경계 중복 방지).
//
// 화자 패리티는 턴 경계 사유(charBreak 외 전부)이고 contentSinceBoundary가
// 서 있을 때만 토글하고, 판정 후 플래그를 내린다. 이 조합으로 세 경우가 모두 맞는다:
//   - 정상 턴: delta 누적 → TurnComplete → push + 토글.
//   - charBreak가 먼저 확정해 버퍼가 빈 뒤 TurnComplete: push는 없지만 플래그가 살아 있어 토글.
//   - 무음 fallback이 턴을 닫은 직후 늦은 TurnComplete: 플래그가 이미 내려가 토글 없음(이중 토글 방지).
//
// 단, 직전 줄과 동일해 push를 생략한 경우(재번역 중복)는 그 내용이 이미 앞선 확정에서
// 화면·패리티에 반영된 것이므로 토글 없이 플래그만 내린다.
func (e *Engine) confirmTurn(reason confirmReason) {
	sourceSnapshot := e.currentSource
	collapsed := strings.TrimSpace(dedupGlobalSentences(collapseRepeats(e.currentTranslation)))
	e.currentTranslation = ""
	e.currentSource = ""

	duplicate := false
	pushed := false
	if collapsed != "" {
		if n := len(e.rollupLines); n > 0 && removeWhitespace(e.rollupLines[n-1].Text) == removeWhitespace(collapsed) {
			duplicate = true // 직전 줄과 동일한 경계/재번역 중복 → push 생략.
		} else {
			e.pushRollup(collapsed, sourceSnapshot)
			pushed = true
		}
		e.segmentMode = true
		e.visible = true
	}

	// 보류 중이던 오디오 경계는 이 확정에 흡수된다 — 이 확정이 어차피 줄을 닫고 토글 규칙을
	// 적용하므로, 뒤늦게 별도로 한 번 더 확정/토글하지 않는다(이중 토글 방지 불변식 유지).
	if e.hintArmed {
		e.logf("engine.hint 보류 해소(다른 확정 흡수) reason=%s deltas=%d", reason, e.hintDeltas)
		e.disarmHint()
	}

	before := e.curSpeaker
	toggled := false
	skip := ""
	switch {
	case !reason.isTurnBoundary():
		skip = "charBreak(턴 경계 아님)"
	case !e.contentSinceBoundary:
		skip = "contentSinceBoundary=false(이 턴에 누적된 내용 없음 — 이중 토글 방지)"
	case duplicate:
		skip = "직전 줄과 중복(이미 반영됨)"
		e.contentSinceBoundary = false
	default:
		e.curSpeaker ^= 1
		toggled = true
		e.contentSinceBoundary = false
	}

	// 진단: 확정 1건의 전체 판단 근거를 한 줄로 남긴다(사유·push 여부·패리티 전후·스킵 사유).
	if e.Logf != nil {
		e.logf("engine.confirm reason=%s pushed=%v duplicate=%v line=%q speaker=%d→%d toggled=%v skip=%q rollup=%d",
			reason, pushed, duplicate, preview(collapsed), before, e.curSpeaker, toggled, skip, len(e.rollupLines))
	}
}

func (e *Engine) pushRollup(line, source string) {
	// 확정 시점의 원문을 줄에 함께 보관한다. 확정하면서 currentSource 를 비우기 때문에,
	// 이 스냅샷이 없으면 화면의 원문 줄만 먼저 사라진다(번역문은 roll-up 으로 남는다).
	e.rollupLines = append(e.rollupLines, Line{Text: line, Speaker: e.curSpeaker, Source: source})
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
	// 화면이 완전히 비워졌으므로 다음 발화는 기본 색(패리티 0)에서 다시 시작한다.
	e.curSpeaker = 0
	e.contentSinceBoundary = false
	e.disarmHint()
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
	merged, ok := mergeDelta(*buffer, delta)
	if !ok {
		return false
	}
	*buffer = merged
	return true
}

// mergeDelta 는 buffer에 delta를 합친 결과와 "실질 변화가 있었는지"를 반환한다(순수 함수).
// 질문 경계 판정처럼 **버퍼를 바꾸기 전에** 델타가 새 내용을 가져오는지 알아야 하는 곳에서도
// 같은 규칙을 재사용하려고 분리했다.
func mergeDelta(buffer, delta string) (string, bool) {
	if delta == "" {
		return buffer, false
	}
	if buffer == "" {
		collapsed := dedupGlobalSentences(collapseRepeats(delta))
		if collapsed == "" {
			return buffer, false
		}
		return collapsed, true
	}
	bChars := []rune(buffer)
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
		return buffer, false
	}
	merged := buffer + newPart
	collapsed := dedupGlobalSentences(collapseRepeats(merged))
	if collapsed == buffer {
		return buffer, false
	}
	return collapsed, true
}

// newContentOf 는 delta가 buffer에 **실제로 더하는 새 내용**만 뽑아낸다.
// 질문 경계에서 새 줄을 시작할 때 쓴다: 중복 델타(더할 내용 없음)면 false를 반환해 헛확정을
// 막고, 겹침이 있으면 겹친 부분(예: 앞 문장의 물음표)을 뺀 나머지만 새 줄로 넘긴다.
func newContentOf(buffer, delta string) (string, bool) {
	merged, changed := mergeDelta(buffer, delta)
	if !changed {
		return "", false // 빈/중복 델타 — 경계로 볼 근거가 없다.
	}
	part := delta
	if strings.HasPrefix(merged, buffer) {
		part = merged[len(buffer):]
	}
	part = strings.TrimLeft(part, " \t")
	fresh, ok := mergeDelta("", part)
	if !ok {
		return "", false
	}
	return fresh, true
}

// -----------------------------------------------------------------------------
// 내부: 문장 끝 판정(텍스트 휴리스틱)
// -----------------------------------------------------------------------------

// sentenceEnders/questionEnders 는 문장 종결로 볼 문자다(전각 포함).
var (
	sentenceEnders = map[rune]bool{'.': true, '!': true, '?': true, '。': true, '！': true, '？': true}
	questionEnders = map[rune]bool{'?': true, '？': true}
)

// closingRunes 는 종결부호 뒤에 붙을 수 있는 닫는 따옴표/괄호다("맞나요?" 같은 인용 끝).
var closingRunes = map[rune]bool{
	'"': true, '\'': true, '”': true, '’': true, '」': true, '』': true,
	')': true, '）': true, ']': true, '】': true, '»': true,
}

// lastMeaningfulRune 은 뒤쪽 공백과 닫는 따옴표/괄호를 걷어낸 마지막 문자를 반환한다.
func lastMeaningfulRune(s string) (rune, bool) {
	r := []rune(strings.TrimSpace(s))
	for len(r) > 0 {
		last := r[len(r)-1]
		if unicode.IsSpace(last) || closingRunes[last] {
			r = r[:len(r)-1]
			continue
		}
		return last, true
	}
	return 0, false
}

// endsWithSentence 는 문장이 종결부호로 끝났는지 보고한다(닫는 따옴표 동반 허용).
func endsWithSentence(s string) bool {
	last, ok := lastMeaningfulRune(s)
	return ok && sentenceEnders[last]
}

// endsWithQuestion 은 문장이 물음표로 끝났는지 보고한다(닫는 따옴표 동반 허용).
func endsWithQuestion(s string) bool {
	last, ok := lastMeaningfulRune(s)
	return ok && questionEnders[last]
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

// logf 는 Logf 훅이 주입돼 있을 때만 진단 메시지를 흘린다(nil이면 no-op).
func (e *Engine) logf(format string, args ...any) {
	if e.Logf == nil {
		return
	}
	e.Logf(format, args...)
}

// preview 는 로그용으로 텍스트를 앞 40룬으로 자른다(레코드 비대화 방지).
func preview(s string) string {
	const max = 40
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

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
func suffixCopy[T any](s []T, n int) []T {
	if n < 0 {
		n = 0
	}
	if len(s) < n {
		n = len(s)
	}
	out := make([]T, n)
	copy(out, s[len(s)-n:])
	return out
}

// lineTexts 는 Line 슬라이스에서 텍스트만 뽑아낸다(화자 패리티를 모르는 기존 소비자용).
func lineTexts(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}
