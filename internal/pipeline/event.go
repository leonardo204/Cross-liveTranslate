package pipeline

import (
	"context"

	"cross-livetranslate/internal/audio"
)

// 이 파일은 백엔드 독립 이벤트 모델 + Provider 추상화를 정의한다.
// 원본 이식: liveTranslate PipelineEvent.swift / TranslationProvider.swift.
// 순수(cgo 없음) — GOOS=windows 크로스빌드가 통과해야 한다.

// Kind identifies which kind of Event this is.
type Kind int

const (
	// SourceDelta is an incremental piece of the source (원문) transcription.
	SourceDelta Kind = iota
	// TranslatedDelta is an incremental piece of the translated 자막.
	TranslatedDelta
	// TurnComplete signals serverContent.turnComplete.
	TurnComplete
	// GenerationComplete signals serverContent.generationComplete (재번역 경계).
	GenerationComplete
	// Interrupted signals serverContent.interrupted.
	Interrupted
	// OutputAudio carries a translated output-audio chunk (24kHz Int16 LE PCM).
	OutputAudio
	// Usage carries usageMetadata token counts.
	Usage
	// State carries a lifecycle transition (see Event.State / Event.Err).
	State
	// PermanentFailure signals reconnection was abandoned (세션 수명 종료).
	PermanentFailure
)

// String renders the Kind for logging.
func (k Kind) String() string {
	switch k {
	case SourceDelta:
		return "SourceDelta"
	case TranslatedDelta:
		return "TranslatedDelta"
	case TurnComplete:
		return "TurnComplete"
	case GenerationComplete:
		return "GenerationComplete"
	case Interrupted:
		return "Interrupted"
	case OutputAudio:
		return "OutputAudio"
	case Usage:
		return "Usage"
	case State:
		return "State"
	case PermanentFailure:
		return "PermanentFailure"
	default:
		return "Unknown"
	}
}

// LifecycleState is the connection/pipeline lifecycle state carried by a State Event.
type LifecycleState int

const (
	// StateDisconnected — not connected.
	StateDisconnected LifecycleState = iota
	// StateConnecting — connecting or reconnecting.
	StateConnecting
	// StateReady — setup complete, audio may be sent.
	StateReady
	// StateError — transient error (see Event.Err).
	StateError
)

// String renders the LifecycleState for logging.
func (s LifecycleState) String() string {
	switch s {
	case StateDisconnected:
		return "disconnected"
	case StateConnecting:
		return "connecting"
	case StateReady:
		return "ready"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

// UsageInfo holds token/sample counts for cost estimation (Usage events).
type UsageInfo struct {
	// OutputAudioTokens is usageMetadata의 출력 오디오 토큰 수(AUDIO modality 우선).
	OutputAudioTokens int
	// TotalTokens is usageMetadata.totalTokenCount.
	TotalTokens int
	// SentSamples is the number of 16kHz mono samples sent (입력 비용 추정용).
	SentSamples int
}

// Event is the single result event type every backend/pipeline produces.
// 원본 PipelineEvent 등가. 필드는 Kind에 따라 선택적으로 채워진다.
type Event struct {
	Kind     Kind
	Text     string          // SourceDelta / TranslatedDelta
	Final    bool            // 세그먼트 확정 여부(delta 경로에서는 미사용)
	AudioPCM []byte          // OutputAudio (24kHz Int16 LE PCM)
	Usage    *UsageInfo      // Usage
	State    LifecycleState  // State
	Err      error           // State(error) / PermanentFailure
}

// Provider receives audio and emits a stream of Events.
// 원본 TranslationProvider 등가. 구현은 Gemini Live client 어댑터(gemini_provider.go).
type Provider interface {
	// Start begins the event stream (연결/배선). 반환된 채널은 Stop 또는 ctx 취소 시 닫힌다.
	Start(ctx context.Context) (<-chan Event, error)
	// Send injects an audio chunk. 실시간 오디오 goroutine에서 호출 가능(ready 전에는 드롭).
	Send(chunk audio.Chunk) error
	// Stop tears down the provider (완전 종료까지 대기).
	Stop() error
}
