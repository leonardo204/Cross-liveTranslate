// Package soniox — Soniox 실시간 받아쓰기·번역 WebSocket 클라이언트(pipeline.Provider 구현).
//
// Gemini Live와 달리 Soniox는 **토큰 단위 텍스트**를 돌려준다. 토큰마다 확정 여부(is_final),
// 원문/번역 구분(translation_status), 원문 언어(language), 화자 번호(speaker)가 붙는다.
// 이 클라이언트는 그 토큰을 앱의 공용 이벤트로 바꾼다.
//
//	확정 원문 토큰   → SourceDelta      (Speaker, Lang)
//	확정 번역 토큰   → TranslatedDelta  (Speaker, Lang=그 화자의 최근 원문 언어)
//	미확정 원문 토큰 → SourceInterim    (현재 꼬리를 통째로 대체)
//	미확정 번역 토큰 → TranslatedInterim(같은 규칙. 2026-10 실측상 Soniox는 번역을 확정분만
//	                   보내 이 이벤트는 나오지 않지만, 보내기 시작하면 그대로 쓰인다)
//
// 확정 토큰은 한 번만 온다(다시 보내지 않는다). 미확정 토큰은 메시지마다 새로 온다.
// 번역 음성은 없다(텍스트만) — 번역 음성 재생은 Gemini 전용이다.
//
// 연결 규약(공식 문서): wss://stt-rt.soniox.com/transcribe-websocket 로 접속해 첫 메시지로
// JSON 설정을 보내고, 이후 오디오는 바이너리 프레임(pcm_s16le 16kHz mono)으로 보낸다.
// 오디오가 한동안 없으면(VAD 게이트가 무음을 거른 경우) {"type":"keepalive"}로 세션을 유지한다.
package soniox

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"cross-livetranslate/internal/audio"
	"cross-livetranslate/internal/pipeline"
	"cross-livetranslate/internal/txlog"
)

const (
	endpoint              = "wss://stt-rt.soniox.com/transcribe-websocket"
	initialReconnectDelay = 1 * time.Second
	maxReconnectDelay     = 30 * time.Second
	maxConnectAttempts    = 5
	// keepaliveAfter: 오디오를 이만큼 보내지 않았으면 keepalive 를 보낸다(서버 유휴 종료 방지).
	keepaliveAfter = 8 * time.Second
	eventBuffer    = 128
)

// TranslationTerm 은 정해 둔 번역 한 쌍이다.
type TranslationTerm struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// Config configures a Client.
type Config struct {
	APIKey string
	// Model 은 사용자가 고른 실시간 모델 id 다. 비면 자동(목록의 최신 모델). models.go 참고.
	Model            string
	TargetLanguage   string
	LanguageHints    []string
	Context          string
	Terms            []string
	TranslationTerms []TranslationTerm
}

// Client is a Soniox realtime client emitting pipeline.Events.
type Client struct {
	cfg Config

	events chan pipeline.Event
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	writeMu sync.Mutex
	mu      sync.Mutex
	conn    *websocket.Conn
	ready   bool
	started bool
	stopped bool

	lastSendNano atomic.Int64
	droppedSend  atomic.Uint64

	// model 은 Start 뒤 runLoop 가 한 번 정한 실제 연결 모델이다(재연결에도 그대로 쓴다).
	model string

	// 수신 goroutine 전용 변환 상태(tokens.go).
	conv *convState
}

// NewClient constructs an unstarted Client.
func NewClient(cfg Config) *Client {
	if strings.TrimSpace(cfg.TargetLanguage) == "" {
		cfg.TargetLanguage = "ko"
	}
	return &Client{cfg: cfg, conv: newConvState()}
}

// Start implements pipeline.Provider.
func (c *Client) Start(ctx context.Context) (<-chan pipeline.Event, error) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return nil, errors.New("soniox: 이미 시작됨")
	}
	c.started = true
	c.mu.Unlock()
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.events = make(chan pipeline.Event, eventBuffer)
	c.wg.Add(2)
	go c.runLoop()
	go c.keepaliveLoop()
	return c.events, nil
}

// Send implements pipeline.Provider. 연결 전/재연결 중에는 청크를 버린다(Gemini와 같은 계약).
func (c *Client) Send(chunk audio.Chunk) error {
	c.mu.Lock()
	ready, conn := c.ready, c.conn
	c.mu.Unlock()
	if !ready || conn == nil {
		c.droppedSend.Add(1)
		return nil
	}
	pcm := audio.Float32ToInt16LE(chunk)
	c.writeMu.Lock()
	err := conn.WriteMessage(websocket.BinaryMessage, pcm)
	c.writeMu.Unlock()
	if err == nil {
		c.lastSendNano.Store(time.Now().UnixNano())
	}
	return err
}

// Stop implements pipeline.Provider. 멱등.
func (c *Client) Stop() error {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return nil
	}
	c.stopped = true
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		// 끝 신호(빈 프레임) — 서버가 남은 결과를 마무리한다. 실패는 무해.
		c.writeMu.Lock()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(""))
		c.writeMu.Unlock()
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.closeConn()
	c.wg.Wait()
	return nil
}

// DroppedSend exposes 연결 전 버린 청크 수(진단).
func (c *Client) DroppedSend() uint64 { return c.droppedSend.Load() }

type result int

const (
	resultDisconnected result = iota
	resultStopped
	resultPermanent
)

func (c *Client) runLoop() {
	defer c.wg.Done()
	defer close(c.events)
	// 연결할 모델을 한 번 정한다. 목록 조회가 늦어도 번역 시작이 오래 막히지 않게 짧게 기다린다.
	rctx, rcancel := context.WithTimeout(c.ctx, 5*time.Second)
	c.model, _ = c.resolveModel(rctx)
	rcancel()
	delay := initialReconnectDelay
	attempts := 0
	for {
		if c.ctx.Err() != nil {
			return
		}
		c.emitState(pipeline.StateConnecting, nil)
		r, gotData := c.connectOnce()
		switch r {
		case resultStopped:
			c.emitState(pipeline.StateDisconnected, nil)
			return
		case resultPermanent:
			return
		}
		if gotData {
			// 한동안 정상으로 돌았던 연결이 끊긴 것 — 실패 횟수를 새로 센다.
			attempts, delay = 0, initialReconnectDelay
		}
		attempts++
		if attempts > maxConnectAttempts {
			c.emit(pipeline.Event{Kind: pipeline.PermanentFailure,
				Err: fmt.Errorf("soniox: 연결 실패 — API 키/네트워크 확인 (%d회 연속 실패)", maxConnectAttempts)})
			return
		}
		txlog.Logf("soniox.conn", "재연결 대기 attempts=%d delay=%s", attempts, delay)
		c.emitState(pipeline.StateError, errors.New("연결 끊김 — 재연결 중"))
		select {
		case <-time.After(delay):
		case <-c.ctx.Done():
			return
		}
		delay = min(delay*2, maxReconnectDelay)
	}
}

// setupMessage builds the first JSON message.
func (c *Client) setupMessage() map[string]any {
	m := map[string]any{
		"api_key":                        c.cfg.APIKey,
		"model":                          c.model,
		"audio_format":                   "pcm_s16le",
		"sample_rate":                    audio.SampleRate,
		"num_channels":                   1,
		"enable_language_identification": true,
		"enable_speaker_diarization":     true,
		"enable_endpoint_detection":      true,
		"translation": map[string]any{
			"type":            "one_way",
			"target_language": c.cfg.TargetLanguage,
		},
	}
	if len(c.cfg.LanguageHints) > 0 {
		m["language_hints"] = c.cfg.LanguageHints
	}
	ctx := map[string]any{}
	if t := strings.TrimSpace(c.cfg.Context); t != "" {
		ctx["text"] = t
	}
	if len(c.cfg.Terms) > 0 {
		ctx["terms"] = c.cfg.Terms
	}
	if len(c.cfg.TranslationTerms) > 0 {
		ctx["translation_terms"] = c.cfg.TranslationTerms
	}
	if len(ctx) > 0 {
		m["context"] = ctx
	}
	return m
}

func (c *Client) connectOnce() (result, bool) {
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 15 * time.Second
	txlog.Logf("soniox.conn", "연결 시도 model=%s target=%q hints=%v terms=%d translationTerms=%d context=%d자",
		c.model, c.cfg.TargetLanguage, c.cfg.LanguageHints, len(c.cfg.Terms), len(c.cfg.TranslationTerms), len([]rune(c.cfg.Context)))
	hdr := http.Header{"Authorization": {"Bearer " + c.cfg.APIKey}}
	conn, resp, err := dialer.DialContext(c.ctx, endpoint, hdr)
	if err != nil {
		if c.ctx.Err() != nil {
			return resultStopped, false
		}
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		log.Printf("[soniox] 연결 실패 httpStatus=%d err=%v", status, err)
		txlog.Logf("soniox.conn", "연결 실패 httpStatus=%d err=%v", status, err)
		if status >= 400 && status < 500 {
			c.emit(pipeline.Event{Kind: pipeline.PermanentFailure,
				Err: fmt.Errorf("soniox: 연결 거부 (HTTP %d) — API 키 확인", status)})
			return resultPermanent, false
		}
		return resultDisconnected, false
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer c.clearConn(conn)

	c.writeMu.Lock()
	err = conn.WriteJSON(c.setupMessage())
	c.writeMu.Unlock()
	if err != nil {
		if c.ctx.Err() != nil {
			return resultStopped, false
		}
		return resultDisconnected, false
	}
	// Soniox는 setupComplete 같은 응답이 없다 — 설정을 보낸 즉시 오디오를 받는다.
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
	c.lastSendNano.Store(time.Now().UnixNano())
	c.emitState(pipeline.StateReady, nil)
	txlog.Logf("soniox.conn", "연결됨 — 오디오 송신 시작")

	gotData := false
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if c.ctx.Err() != nil {
				return resultStopped, gotData
			}
			txlog.Logf("soniox.conn", "수신 종료 err=%v — 재연결", err)
			return resultDisconnected, gotData
		}
		gotData = true
		if r, done := c.handleMessage(data); done {
			return r, gotData
		}
	}
}

func (c *Client) keepaliveLoop() {
	defer c.wg.Done()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		c.mu.Lock()
		ready, conn := c.ready, c.conn
		c.mu.Unlock()
		if !ready || conn == nil {
			continue
		}
		if time.Since(time.Unix(0, c.lastSendNano.Load())) < keepaliveAfter {
			continue
		}
		c.writeMu.Lock()
		err := conn.WriteJSON(map[string]string{"type": "keepalive"})
		c.writeMu.Unlock()
		if err == nil {
			c.lastSendNano.Store(time.Now().UnixNano())
		}
	}
}

func (c *Client) clearConn(conn *websocket.Conn) {
	c.mu.Lock()
	if c.conn == conn {
		c.conn = nil
		c.ready = false
	}
	c.mu.Unlock()
	_ = conn.Close()
}

func (c *Client) closeConn() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (c *Client) emit(ev pipeline.Event) {
	select {
	case c.events <- ev:
	case <-c.ctx.Done():
	}
}

func (c *Client) emitState(s pipeline.LifecycleState, err error) {
	c.emit(pipeline.Event{Kind: pipeline.State, State: s, Err: err})
}

// resolveModel picks the model for this session and logs why.
func (c *Client) resolveModel(ctx context.Context) (string, string) {
	m, note := ResolveModel(ctx, c.cfg.APIKey, c.cfg.Model)
	txlog.Logf("soniox.conn", "모델 결정 model=%s 설정=%q — %s", m, c.cfg.Model, note)
	return m, note
}
