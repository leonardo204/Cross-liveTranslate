package gemini

// gemini_provider.go — Gemini Live Client를 pipeline.Provider로 어댑트한다.
// 원본 이식: liveTranslate GeminiLiveTranslationProvider(TranslationProvider 준수).

import (
	"context"

	"cross-livetranslate/internal/audio"
	"cross-livetranslate/internal/pipeline"
)

// Provider adapts a gemini.Client to the pipeline.Provider interface.
type Provider struct {
	client *Client
}

// 컴파일 타임에 pipeline.Provider 준수 확인.
var _ pipeline.Provider = (*Provider)(nil)

// NewProvider constructs a Gemini-backed pipeline.Provider from Config.
func NewProvider(cfg Config) *Provider {
	return &Provider{client: NewClient(cfg)}
}

// Start begins the event stream.
func (p *Provider) Start(ctx context.Context) (<-chan pipeline.Event, error) {
	return p.client.Start(ctx)
}

// Send injects an audio chunk (실시간 오디오 goroutine에서 호출 가능).
func (p *Provider) Send(chunk audio.Chunk) error {
	return p.client.Send(chunk)
}

// Stop tears down the provider.
func (p *Provider) Stop() error {
	return p.client.Stop()
}

// DroppedSend exposes 백프레셔 드롭 카운터(디버그).
func (p *Provider) DroppedSend() uint64 {
	return p.client.DroppedSend()
}
