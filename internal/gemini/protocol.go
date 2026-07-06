package gemini

// protocol.go — Gemini Live BidiGenerateContent 송신/수신 메시지 구조체.
// 원본 이식: liveTranslate GeminiLiveClient.swift (Codable 구조체 무변경 이식).
// 순수(cgo 없음) — GOOS=windows 크로스빌드가 통과해야 한다.

// ─── 송신 (Encodable) ──────────────────────────────────────────────────────

// SetupMessage is the first message sent on a new connection.
type SetupMessage struct {
	Setup Setup `json:"setup"`
}

// Setup mirrors the verified setup payload. 필드 순서는 무관(JSON object).
type Setup struct {
	Model            string           `json:"model"`
	GenerationConfig GenerationConfig `json:"generationConfig"`
	// InputAudioTranscription: nil이면 키 생략(공식 translate 예제와 동일). "원문 동시 표시" 시에만 {}.
	InputAudioTranscription *EmptyConfig `json:"inputAudioTranscription,omitempty"`
	// OutputAudioTranscription: 자막 본문 경로 — 항상 {}.
	OutputAudioTranscription EmptyConfig `json:"outputAudioTranscription"`
	// SessionResumption: handle 없으면 {} (새 세션에서 재개 활성화).
	SessionResumption SessionResumptionConfig `json:"sessionResumption"`
	// RealtimeInputConfig: 항상 명시 전송. P1은 서버 VAD ON(disabled=false).
	RealtimeInputConfig *RealtimeInputConfig `json:"realtimeInputConfig,omitempty"`
}

// GenerationConfig holds responseModalities + nested translationConfig.
//
// ⚠️ 핵심 규칙: translationConfig는 **반드시 generationConfig 내부에 nested**.
// top-level translationConfig는 서버가 close 1007로 거부한다(specs/002 A.2 검증).
type GenerationConfig struct {
	ResponseModalities []string           `json:"responseModalities"`
	TranslationConfig  *TranslationConfig `json:"translationConfig,omitempty"`
}

// TranslationConfig is nested inside GenerationConfig.
type TranslationConfig struct {
	// SourceLanguageCode: 원본은 source를 지정하지 않는다(서버 자동 감지). omitempty 이므로
	// -source 미지정(auto)이면 키가 생략되어 검증된 동작과 동일해진다.
	// 필드명은 targetLanguageCode 관례에 맞춘 것이며 프리뷰에서 **미검증**(명시 지정 시 실측 권장).
	SourceLanguageCode string `json:"sourceLanguageCode,omitempty"`
	TargetLanguageCode string `json:"targetLanguageCode"`
	// EchoTargetLanguage=true: 공식 translate 예제 기본값(입력이 이미 목표 언어면 그대로 따라 말함).
	EchoTargetLanguage bool `json:"echoTargetLanguage"`
}

// RealtimeInputConfig controls server VAD.
type RealtimeInputConfig struct {
	AutomaticActivityDetection AutomaticActivityDetection `json:"automaticActivityDetection"`
}

// AutomaticActivityDetection.Disabled=false → 서버 자동 VAD ON(P1 기본).
type AutomaticActivityDetection struct {
	Disabled bool `json:"disabled"`
}

// SessionResumptionConfig carries the 재개 핸들. Handle 빈 문자열이면 키 생략 → {}.
type SessionResumptionConfig struct {
	Handle string `json:"handle,omitempty"`
}

// EmptyConfig encodes as {} (inputAudioTranscription / outputAudioTranscription 본문).
type EmptyConfig struct{}

// RealtimeInputMessage carries an audio chunk (realtimeInput.audio).
type RealtimeInputMessage struct {
	RealtimeInput RealtimeInput `json:"realtimeInput"`
}

// RealtimeInput wraps an audio payload.
type RealtimeInput struct {
	Audio *RealtimeAudio `json:"audio,omitempty"`
}

// RealtimeAudio is base64 Int16 LE PCM + mimeType.
type RealtimeAudio struct {
	Data     string `json:"data"`     // base64 Int16 LE PCM
	MimeType string `json:"mimeType"` // "audio/pcm;rate=16000"
}

// BuildSetup constructs the setup message. source=="" or "auto" → source 미지정.
func BuildSetup(model, target, source string, requestInputTranscription bool, resumptionHandle string) SetupMessage {
	tc := &TranslationConfig{TargetLanguageCode: target, EchoTargetLanguage: true}
	if source != "" && source != "auto" {
		tc.SourceLanguageCode = source
	}
	s := Setup{
		Model: model,
		GenerationConfig: GenerationConfig{
			ResponseModalities: []string{"AUDIO"},
			TranslationConfig:  tc,
		},
		OutputAudioTranscription: EmptyConfig{},
		SessionResumption:        SessionResumptionConfig{Handle: resumptionHandle},
		RealtimeInputConfig: &RealtimeInputConfig{
			AutomaticActivityDetection: AutomaticActivityDetection{Disabled: false},
		},
	}
	if requestInputTranscription {
		s.InputAudioTranscription = &EmptyConfig{}
	}
	return SetupMessage{Setup: s}
}

// BuildAudioMessage wraps base64 PCM into a realtimeInput audio message.
func BuildAudioMessage(pcmBase64 string) RealtimeInputMessage {
	return RealtimeInputMessage{
		RealtimeInput: RealtimeInput{
			Audio: &RealtimeAudio{Data: pcmBase64, MimeType: "audio/pcm;rate=16000"},
		},
	}
}

// ─── 수신 (Decodable) ──────────────────────────────────────────────────────

// ServerMessage is a BidiGenerateContentServerMessage — 하나 이상의 필드가 채워진다.
type ServerMessage struct {
	SetupComplete           *SetupComplete           `json:"setupComplete,omitempty"`
	ServerContent           *ServerContent           `json:"serverContent,omitempty"`
	UsageMetadata           *UsageMetadata           `json:"usageMetadata,omitempty"`
	GoAway                  *GoAway                  `json:"goAway,omitempty"`
	SessionResumptionUpdate *SessionResumptionUpdate `json:"sessionResumptionUpdate,omitempty"`
}

// SetupComplete signals setup 수락 → ready.
type SetupComplete struct{}

// ServerContent carries transcription deltas / model turn / turn boundaries.
type ServerContent struct {
	InputTranscription  *Transcription `json:"inputTranscription,omitempty"`
	OutputTranscription *Transcription `json:"outputTranscription,omitempty"`
	ModelTurn           *ModelTurn     `json:"modelTurn,omitempty"`
	TurnComplete        bool           `json:"turnComplete,omitempty"`
	GenerationComplete  bool           `json:"generationComplete,omitempty"`
	Interrupted         bool           `json:"interrupted,omitempty"`
}

// Transcription is a text delta (+ optional detected language).
type Transcription struct {
	Text         string `json:"text,omitempty"`
	LanguageCode string `json:"languageCode,omitempty"`
}

// ModelTurn carries output-audio parts (inlineData).
type ModelTurn struct {
	Parts []Part `json:"parts,omitempty"`
}

// Part holds inline binary data (output audio).
type Part struct {
	InlineData *InlineData `json:"inlineData,omitempty"`
}

// InlineData is base64 payload + mimeType (e.g. audio/pcm;rate=24000).
type InlineData struct {
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// UsageMetadata reports token usage.
type UsageMetadata struct {
	TotalTokenCount       int                  `json:"totalTokenCount,omitempty"`
	PromptTokenCount      int                  `json:"promptTokenCount,omitempty"`
	ResponseTokenCount    int                  `json:"responseTokenCount,omitempty"`
	ResponseTokensDetails []ModalityTokenCount `json:"responseTokensDetails,omitempty"`
}

// ModalityTokenCount is a per-modality token breakdown.
type ModalityTokenCount struct {
	Modality   string `json:"modality,omitempty"`
	TokenCount int    `json:"tokenCount,omitempty"`
}

// OutputAudioTokens returns 출력 오디오 토큰 수(AUDIO modality 우선, 없으면 responseTokenCount 폴백).
func (u UsageMetadata) OutputAudioTokens() int {
	var audio int
	for _, d := range u.ResponseTokensDetails {
		if d.Modality == "AUDIO" || d.Modality == "audio" {
			audio += d.TokenCount
		}
	}
	if audio > 0 {
		return audio
	}
	return u.ResponseTokenCount
}

// GoAway is 연결 종료 예고 → 핸들로 선제 재연결.
type GoAway struct {
	TimeLeft string `json:"timeLeft,omitempty"`
}

// SessionResumptionUpdate carries a 재개 핸들 갱신.
type SessionResumptionUpdate struct {
	NewHandle string `json:"newHandle,omitempty"`
	Resumable bool   `json:"resumable,omitempty"`
}
