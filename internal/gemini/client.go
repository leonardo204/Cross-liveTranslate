package gemini

// client.go — Gemini Live WebSocket 클라이언트.
// 원본 이식: liveTranslate GeminiLiveClient.swift.
//   - 연결 → setup 송신 → setupComplete 대기 → ready → 오디오 송신 허용
//   - 수신 루프 goroutine → pipeline.Event 채널
//   - 재연결: 지수 백오프 + sessionResumption 핸들 재사용 + 14분 선제 재연결 + goAway 핸드오버
//   - context 취소로 정리
//
// 순수 Go(gorilla/websocket) — cgo 없음. GOOS=windows 크로스빌드 통과.
//
// 보안: 엔드포인트 URL 쿼리에 API 키가 들어가므로 URL/에러 원문을 로그하지 않는다.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"cross-livetranslate/internal/audio"
	"cross-livetranslate/internal/pipeline"
)

const (
	endpoint              = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
	initialReconnectDelay = 1 * time.Second
	maxReconnectDelay     = 30 * time.Second
	maxConnectAttempts    = 5
	// proactiveInterval: 세션 한도(오디오 약 15분) 전에 미리 핸들 기반 재연결(원본 14분).
	proactiveInterval = 14 * time.Minute
	eventBuffer       = 64
)

// Config configures a Client.
type Config struct {
	APIKey                    string
	Model                     string
	TargetLanguage            string
	SourceLanguage            string // "" or "auto" → 서버 자동 감지
	RequestInputTranscription bool   // "원문 동시 표시" 시 true
	EmitOutputAudio           bool   // 번역 음성 재생(P4) 시 true. false면 24kHz PCM 이벤트를 방출하지 않아 채널 head-of-line 블로킹 방지.
}

// Client is a Gemini Live WebSocket client emitting pipeline.Events.
type Client struct {
	cfg Config

	events chan pipeline.Event

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	writeMu sync.Mutex // 송신 직렬화(gorilla conn은 동시 write 불가)

	mu                 sync.Mutex
	conn               *websocket.Conn
	ready              bool
	started            bool
	stopped            bool
	reconnectRequested bool // 선제/goAway 재연결(실패 아님)
	resumptionHandle   string
	proactiveTimer     *time.Timer

	// 아래는 runLoop goroutine 전용(락 불필요).
	reconnectDelay  time.Duration
	connectAttempts int

	droppedSend atomic.Uint64
}

// NewClient constructs an unstarted Client.
func NewClient(cfg Config) *Client {
	if cfg.Model == "" {
		cfg.Model = "models/gemini-3.5-live-translate-preview"
	}
	return &Client{cfg: cfg}
}

// Start begins the connection lifecycle and returns the event channel.
// 채널은 Stop() 또는 ctx 취소 시 닫힌다.
func (c *Client) Start(ctx context.Context) (<-chan pipeline.Event, error) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return nil, fmt.Errorf("gemini: 이미 시작됨")
	}
	c.started = true
	c.mu.Unlock()

	c.ctx, c.cancel = context.WithCancel(ctx)
	c.events = make(chan pipeline.Event, eventBuffer)
	c.reconnectDelay = initialReconnectDelay
	c.wg.Add(1)
	go c.runLoop()
	return c.events, nil
}

// Send injects an audio chunk. ready 상태에서만 송신하고, 그 외에는 드롭한다.
func (c *Client) Send(chunk audio.Chunk) error {
	c.mu.Lock()
	ready := c.ready
	conn := c.conn
	c.mu.Unlock()
	if !ready || conn == nil {
		c.droppedSend.Add(1)
		return nil // 연결 전/재연결 중 청크 유실 허용(원본 계약)
	}
	pcm := audio.Float32ToInt16LE(chunk)
	b64 := base64.StdEncoding.EncodeToString(pcm)
	return c.writeJSON(BuildAudioMessage(b64))
}

// Stop tears down the client and waits for the run loop to exit. Idempotent.
func (c *Client) Stop() error {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return nil
	}
	c.stopped = true
	c.mu.Unlock()

	if c.cancel != nil {
		c.cancel()
	}
	c.closeConn()
	c.wg.Wait()
	return nil
}

// DroppedSend returns the number of audio chunks dropped before ready.
func (c *Client) DroppedSend() uint64 { return c.droppedSend.Load() }

// ─── 내부: 연결 수명 ────────────────────────────────────────────────────────

type connectResult int

const (
	resultDisconnected connectResult = iota // 일시적 끊김 → 백오프 재연결
	resultReconnectNow                      // 선제/goAway → 즉시 재연결(핸들 유지)
	resultStopped                           // ctx 취소/Stop
	resultPermanent                         // 영구 실패(재연결 중단)
)

func (c *Client) runLoop() {
	defer c.wg.Done()
	defer close(c.events)

	for {
		if c.ctx.Err() != nil {
			return
		}
		c.emitState(pipeline.StateConnecting, nil)

		switch c.connectOnce() {
		case resultStopped:
			c.emitState(pipeline.StateDisconnected, nil)
			return
		case resultPermanent:
			return
		case resultReconnectNow:
			// 선제/goAway: 즉시 재연결(핸들 유지, 백오프/카운트 없음).
			continue
		case resultDisconnected:
			c.connectAttempts++
			// 핸들 기반 재개가 끊김 → 핸들이 만료/무효일 수 있으니 폐기 후 새 세션.
			c.mu.Lock()
			hadHandle := c.resumptionHandle != ""
			c.resumptionHandle = ""
			c.mu.Unlock()
			_ = hadHandle
			if c.connectAttempts > maxConnectAttempts {
				c.emit(pipeline.Event{
					Kind: pipeline.PermanentFailure,
					Err:  fmt.Errorf("gemini: 연결 실패 — API 키/네트워크 확인 (%d회 연속 실패)", maxConnectAttempts),
				})
				return
			}
			delay := c.reconnectDelay
			c.reconnectDelay = min(c.reconnectDelay*2, maxReconnectDelay)
			c.emitState(pipeline.StateError, fmt.Errorf("연결 끊김 — 재연결 중"))
			select {
			case <-time.After(delay):
			case <-c.ctx.Done():
				return
			}
		}
	}
}

// connectOnce dials, sends setup, and runs the receive loop until the connection
// ends. 반환값이 다음 재연결 정책을 결정한다.
func (c *Client) connectOnce() connectResult {
	c.mu.Lock()
	handle := c.resumptionHandle
	c.mu.Unlock()

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 15 * time.Second
	// 보안: endpointURL()에는 API 키가 쿼리로 들어가므로 URL을 로그하지 않는다(호스트만).
	log.Printf("[gemini] 연결 시도 model=%q target=%q source=%q resume=%v",
		c.cfg.Model, c.cfg.TargetLanguage, c.cfg.SourceLanguage, handle != "")
	conn, resp, err := dialer.DialContext(c.ctx, c.endpointURL(), nil)
	if err != nil {
		if c.ctx.Err() != nil {
			return resultStopped
		}
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		log.Printf("[gemini] 연결 실패 httpStatus=%d err=%v", status, err)
		// 4xx 핸드셰이크 거부 → 키/모델/요청 문제. 재연결 무의미.
		if resp != nil && resp.StatusCode >= 400 && resp.StatusCode < 500 {
			c.emit(pipeline.Event{
				Kind: pipeline.PermanentFailure,
				Err:  fmt.Errorf("gemini: 연결 거부 (HTTP %d) — API 키/모델 권한 확인", resp.StatusCode),
			})
			return resultPermanent
		}
		return resultDisconnected
	}
	c.setConn(conn)
	defer c.clearConn()
	defer conn.Close()
	log.Printf("[gemini] 웹소켓 연결됨 — setup 송신")

	// setup(첫 메시지) 전송.
	if err := c.writeJSON(BuildSetup(c.cfg.Model, c.cfg.TargetLanguage, c.cfg.SourceLanguage, c.cfg.RequestInputTranscription, handle)); err != nil {
		if c.ctx.Err() != nil {
			return resultStopped
		}
		log.Printf("[gemini] setup 송신 실패 err=%v", err)
		return resultDisconnected
	}
	log.Printf("[gemini] setup 송신 완료 — setupComplete 대기")

	// 수신 루프.
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if c.ctx.Err() != nil {
				return resultStopped
			}
			if c.takeReconnectRequested() {
				return resultReconnectNow
			}
			// close 코드가 정책 위반류면 영구 실패.
			if ce, ok := err.(*websocket.CloseError); ok {
				switch ce.Code {
				case websocket.ClosePolicyViolation, websocket.CloseUnsupportedData, websocket.CloseInvalidFramePayloadData:
					c.emit(pipeline.Event{
						Kind: pipeline.PermanentFailure,
						Err:  fmt.Errorf("gemini: 연결이 정책상 거부됨 (close %d) — API 키/모델 권한 확인", ce.Code),
					})
					return resultPermanent
				}
			}
			return resultDisconnected
		}
		if c.handleServerData(data) {
			return resultReconnectNow
		}
	}
}

// handleServerData parses one server message and emits events.
// 반환 true면 즉시 재연결(goAway)을 요청한다.
func (c *Client) handleServerData(data []byte) (reconnectNow bool) {
	var msg ServerMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		// 미지 메시지는 무시(프리뷰 — 미지 필드 허용).
		return false
	}

	if msg.SetupComplete != nil {
		c.onSetupComplete()
		return false
	}

	if sc := msg.ServerContent; sc != nil {
		if sc.OutputTranscription != nil && sc.OutputTranscription.Text != "" {
			c.emit(pipeline.Event{Kind: pipeline.TranslatedDelta, Text: sc.OutputTranscription.Text})
		}
		if sc.InputTranscription != nil && sc.InputTranscription.Text != "" {
			c.emit(pipeline.Event{Kind: pipeline.SourceDelta, Text: sc.InputTranscription.Text})
		}
		// 일부 모델은 번역문을 modelTurn.parts[].text로 보낸다(오디오 대신 텍스트 경로).
		if sc.ModelTurn != nil {
			for _, part := range sc.ModelTurn.Parts {
				if part.Text != "" {
					c.emit(pipeline.Event{Kind: pipeline.TranslatedDelta, Text: part.Text})
				}
			}
		}
		if sc.ModelTurn != nil && c.cfg.EmitOutputAudio {
			// EmitOutputAudio가 false면 재생 소비자가 없으므로 대용량 PCM 이벤트를 만들지 않는다
			// (64슬롯 이벤트 채널에서 자막/상태 이벤트를 밀어내는 head-of-line 블로킹 방지).
			for _, part := range sc.ModelTurn.Parts {
				if part.InlineData == nil || part.InlineData.Data == "" {
					continue
				}
				decoded, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
				if err != nil || len(decoded) == 0 {
					continue
				}
				c.emit(pipeline.Event{Kind: pipeline.OutputAudio, AudioPCM: decoded})
			}
		}
		if sc.Interrupted {
			c.emit(pipeline.Event{Kind: pipeline.Interrupted})
		}
		// generationComplete를 turnComplete보다 먼저(같은 메시지에 둘 다 실릴 수 있음).
		if sc.GenerationComplete {
			c.emit(pipeline.Event{Kind: pipeline.GenerationComplete})
		}
		if sc.TurnComplete {
			c.emit(pipeline.Event{Kind: pipeline.TurnComplete})
		}
	}

	if u := msg.UsageMetadata; u != nil {
		c.emit(pipeline.Event{Kind: pipeline.Usage, Usage: &pipeline.UsageInfo{
			OutputAudioTokens: u.OutputAudioTokens(),
			TotalTokens:       u.TotalTokenCount,
		}})
	}

	if msg.SessionResumptionUpdate != nil {
		if msg.SessionResumptionUpdate.Resumable && msg.SessionResumptionUpdate.NewHandle != "" {
			c.mu.Lock()
			c.resumptionHandle = msg.SessionResumptionUpdate.NewHandle
			c.mu.Unlock()
		}
	}

	if msg.GoAway != nil {
		// 저장된 핸들로 선제 핸드오버(무중단 재연결).
		// return true가 곧바로 resultReconnectNow를 유발하므로 reconnectRequested 플래그는
		// 건드리지 않는다. 플래그를 세우면 소비되지 않은 채 남아, 이후 진짜 일시적 끊김을
		// handover로 오분류(백오프/카운트 스킵)해 재연결 storm 방어가 무너진다.
		// 플래그는 triggerProactiveReconnect(소켓 close로 read를 깨우는 경로) 전용.
		return true
	}

	return false
}

// onSetupComplete: ready 진입 + 백오프 리셋 + 선제 재연결 타이머 재무장.
func (c *Client) onSetupComplete() {
	c.reconnectDelay = initialReconnectDelay
	c.connectAttempts = 0

	c.mu.Lock()
	c.ready = true
	if c.proactiveTimer != nil {
		c.proactiveTimer.Stop()
	}
	c.proactiveTimer = time.AfterFunc(proactiveInterval, c.triggerProactiveReconnect)
	c.mu.Unlock()

	log.Printf("[gemini] setupComplete 수신 — READY (오디오 송신 시작)")
	c.emitState(pipeline.StateReady, nil)
}

// triggerProactiveReconnect: 14분 경과 → 재연결 요청 후 소켓을 닫아 수신 루프를 깨운다.
func (c *Client) triggerProactiveReconnect() {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.reconnectRequested = true
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close() // ReadMessage가 에러 → takeReconnectRequested → resultReconnectNow
	}
}

// ─── 내부: 소켓/상태 헬퍼 ──────────────────────────────────────────────────

func (c *Client) setConn(conn *websocket.Conn) {
	c.mu.Lock()
	c.conn = conn
	c.ready = false
	c.mu.Unlock()
}

func (c *Client) clearConn() {
	c.mu.Lock()
	if c.proactiveTimer != nil {
		c.proactiveTimer.Stop()
		c.proactiveTimer = nil
	}
	c.conn = nil
	c.ready = false
	c.mu.Unlock()
}

func (c *Client) closeConn() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (c *Client) takeReconnectRequested() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.reconnectRequested
	c.reconnectRequested = false
	return r
}

func (c *Client) writeJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("gemini: not connected")
	}
	return conn.WriteJSON(v)
}

func (c *Client) endpointURL() string {
	// 키를 쿼리에 붙인다. 이 문자열은 절대 로그하지 않는다.
	return endpoint + "?key=" + c.cfg.APIKey
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
