package main

// controller.go — P3b: 제어 프로세스의 파이프라인 감독 + overlay 자식 프로세스 관리.
//
// controller 프로세스는 제어 HUD(Wails 창)를 띄우고, 이 파일의 supervisor가
// P2 번역 파이프라인(reconciler → 자막엔진)을 구동한다. 자막엔진의 표시 상태가
// 바뀔 때마다 overlay 자식 프로세스(같은 바이너리 `-role overlay`)의 stdin으로
// IPC(JSON 라인)를 push 해 실시간 번역 자막을 오버레이에 표시한다.
//
// 동시성 모델(원본 reconciler 불변식 준수):
//   - 자막엔진(subtitle.Engine)은 단일 owner goroutine(runLoop)에서만 접근한다.
//   - reconciler OnEvent는 버퍼 채널로 이벤트를 runLoop에 넘긴다(엔진 접근 직렬화).
//   - Wails 바인딩 메서드(Start/Stop/SetTarget/SetInput)는 desired 상태만 갱신하고
//     reconciler에 위임한다(엔진을 직접 만지지 않는다).
//   - overlay 자식 stdin 쓰기는 runLoop 단독이므로 추가 락이 불필요하다.

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"cross-livetranslate/internal/app"
	"cross-livetranslate/internal/audio"
	"cross-livetranslate/internal/config"
	"cross-livetranslate/internal/gemini"
	"cross-livetranslate/internal/ipc"
	"cross-livetranslate/internal/pipeline"
	"cross-livetranslate/internal/subtitle"
	"cross-livetranslate/internal/tray"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

var (
	errBadInput  = errors.New("controller: unknown -input value (auto|mic|loopback|device:<id>)")
	errBadDevice = errors.New("controller: device: requires a non-empty device id")
)

// controllerFlags carries the controller-role command-line options (검증/자동화용).
type controllerFlags struct {
	autostart bool
	target    string
	input     string
}

// controller supervises the translation pipeline and the overlay child process.
type Controller struct {
	ctx context.Context

	apiKey    string
	apiKeyErr error
	model     string

	r      *app.Reconciler
	events chan pipeline.Event

	// desired/config state (바인딩 메서드가 갱신, 락 보호).
	mu         sync.Mutex
	running    bool
	target     string
	source     string
	showSource bool
	sel        audio.Selection
	status     string

	// overlay 자식 프로세스.
	child      *exec.Cmd
	childStdin io.WriteCloser

	closeOnce sync.Once
}

// newController creates a controller with default language/model settings.
func newController() *Controller {
	return &Controller{
		model:  config.GeminiModel,
		target: config.DefaultTargetLanguage,
		source: config.DefaultSourceLanguage,
		sel:    audio.Selection{Mode: audio.SelectAuto},
		status: "idle",
		events: make(chan pipeline.Event, 256),
	}
}

// start boots the pipeline reconciler, spawns the overlay child, and launches
// the subtitle owner loop. Called from Wails OnStartup (ctx is the app context).
func (c *Controller) start(ctx context.Context, flags controllerFlags) {
	c.ctx = ctx

	// API 키 1회 로드. 실패해도 HUD는 뜨고, Start() 시 오류를 표면화한다.
	key, err := config.APIKey()
	c.mu.Lock()
	c.apiKey, c.apiKeyErr = key, err
	if flags.target != "" {
		c.target = flags.target
	}
	if sel, perr := parseInputSelection(flags.input); perr == nil && flags.input != "" {
		c.sel = sel
	}
	c.mu.Unlock()

	// 팩토리 주입: reconciler는 gemini/malgo에 직접 의존하지 않는다(headless와 동일).
	newProvider := func(cfg app.ProviderConfig) (pipeline.Provider, error) {
		return gemini.NewProvider(gemini.Config{
			APIKey:                    c.apiKey,
			Model:                     cfg.Model,
			TargetLanguage:            cfg.TargetLanguage,
			SourceLanguage:            cfg.SourceLanguage,
			RequestInputTranscription: cfg.ShowSource,
		}), nil
	}
	newSource := func(s audio.Selection) (audio.Source, error) {
		return audio.SelectSource(s)
	}
	onEvent := func(ev pipeline.Event) {
		select {
		case c.events <- ev:
		case <-ctx.Done():
		}
	}

	c.r = app.New(app.Options{NewProvider: newProvider, NewSource: newSource, OnEvent: onEvent})
	c.r.Start(ctx)

	c.spawnOverlay()
	go c.runLoop()

	c.initTray()

	if flags.autostart {
		if err := c.Start(); err != nil {
			log.Println("[controller] autostart failed:", err)
		}
	}
}

// spawnOverlay launches the overlay child process (same binary, `-role overlay`)
// and captures its stdin pipe for IPC pushes. 실패는 치명적이지 않다(로그만).
func (c *Controller) spawnOverlay() {
	exe, err := os.Executable()
	if err != nil {
		log.Println("[controller] os.Executable:", err)
		return
	}
	cmd := exec.Command(exe, "-role", "overlay")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Println("[controller] overlay stdin pipe:", err)
		return
	}
	// overlay 진단 로그를 controller 콘솔로 흘려보낸다(자막 데이터는 stdin 전용).
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Println("[controller] overlay start:", err)
		_ = stdin.Close()
		return
	}
	c.child = cmd
	c.childStdin = stdin
	log.Printf("[controller] overlay child spawned pid=%d", cmd.Process.Pid)

	// 자식이 죽으면 로그(감독). controller 종료 시 shutdown에서 Kill.
	go func() {
		_ = cmd.Wait()
		log.Println("[controller] overlay child exited")
	}()
}

// pushSubtitle marshals the current engine display state and writes it to the
// overlay child's stdin. runLoop 단독 호출(직렬).
func (c *Controller) pushSubtitle(msg ipc.SubtitleMsg) {
	if c.childStdin == nil {
		return
	}
	if err := ipc.WriteMsg(c.childStdin, msg); err != nil {
		log.Println("[controller] overlay push:", err)
	}
}

// runLoop is the single subtitle-engine owner goroutine. It applies pipeline
// events, ticks the heartbeat, and pushes subtitle snapshots to the overlay on
// any change (throttled to actual state transitions).
func (c *Controller) runLoop() {
	eng := subtitle.New()

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	var lastSig string
	maybePush := func() {
		msg := buildSubtitleMsg(eng, c.wantSource())
		sig := subtitleSignature(msg)
		if sig == lastSig {
			return
		}
		lastSig = sig
		c.pushSubtitle(msg)
	}

	for {
		select {
		case <-c.ctx.Done():
			return
		case now := <-ticker.C:
			eng.Heartbeat(now)
			maybePush()
		case ev := <-c.events:
			c.applyEvent(eng, ev)
			maybePush()
		}
	}
}

// applyEvent reflects a pipeline event into the subtitle engine (단일 goroutine).
// Surfaces lifecycle/state to the HUD status text and forwards nothing to stdout.
func (c *Controller) applyEvent(eng *subtitle.Engine, ev pipeline.Event) {
	switch ev.Kind {
	case pipeline.TranslatedDelta:
		eng.IngestTranslatedDelta(ev.Text)
	case pipeline.SourceDelta:
		eng.IngestSourceDelta(ev.Text)
	case pipeline.TurnComplete:
		eng.TurnComplete()
	case pipeline.GenerationComplete:
		eng.GenerationComplete()
	case pipeline.Interrupted:
		eng.Interrupted()
	case pipeline.State:
		c.setStatus("state: " + ev.State.String())
	case pipeline.PermanentFailure:
		c.setStatus("failed")
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	case pipeline.Usage, pipeline.OutputAudio:
		// P3b는 비용/음성 소비 없음 — 무시.
	}
}

// wantSource reports whether the source (원문) line should be shown in the overlay.
func (c *Controller) wantSource() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.showSource
}

// shutdown kills the overlay child and tears down the reconciler. Idempotent.
func (c *Controller) shutdown() {
	c.closeOnce.Do(func() {
		if c.child != nil && c.child.Process != nil {
			_ = c.child.Process.Kill()
		}
		if c.childStdin != nil {
			_ = c.childStdin.Close()
		}
		if c.r != nil {
			c.r.Close()
		}
	})
}

// initTray installs the system tray (menu bar) with Start/Stop/Show HUD/Quit.
// 트레이는 부차 목표: 실패해도 core 통합에 영향을 주지 않는다(로그만).
func (c *Controller) initTray() {
	err := tray.Init(tray.Handlers{
		OnStart: func() { _ = c.Start() },
		OnStop:  func() { _ = c.Stop() },
		OnShowHUD: func() {
			if c.ctx != nil {
				wruntime.WindowShow(c.ctx)
				wruntime.WindowUnminimise(c.ctx)
			}
		},
		OnQuit: func() {
			if c.ctx != nil {
				wruntime.Quit(c.ctx)
			}
		},
	})
	if err != nil {
		log.Println("[controller] tray init:", err)
	}
	tray.SetStatus(c.Status())
}

// -----------------------------------------------------------------------------
// Wails-bound methods (frontend: window.go.main.App.<Method>)
// -----------------------------------------------------------------------------

// Start begins translation with the current target/source/input selection.
func (c *Controller) Start() error {
	c.mu.Lock()
	if c.apiKeyErr != nil {
		err := c.apiKeyErr
		c.status = "no API key"
		c.mu.Unlock()
		return err
	}
	c.running = true
	c.status = "starting"
	d := app.Desired{
		Running:   true,
		Selection: c.sel,
		Provider: app.ProviderConfig{
			Model:          c.model,
			TargetLanguage: c.target,
			SourceLanguage: c.source,
			ShowSource:     c.showSource,
		},
	}
	c.mu.Unlock()

	c.r.SetDesired(d)
	c.emitStatus()
	return nil
}

// Stop halts translation but keeps the process/overlay alive.
func (c *Controller) Stop() error {
	c.mu.Lock()
	c.running = false
	c.status = "stopped"
	c.mu.Unlock()
	if c.r != nil {
		c.r.SetRunning(false)
	}
	c.emitStatus()
	return nil
}

// IsRunning reports whether translation is currently active.
func (c *Controller) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// SetTarget changes the target (번역 대상) language. Hot-swaps if running.
func (c *Controller) SetTarget(lang string) {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return
	}
	c.mu.Lock()
	c.target = lang
	running := c.running
	cfg := app.ProviderConfig{
		Model:          c.model,
		TargetLanguage: c.target,
		SourceLanguage: c.source,
		ShowSource:     c.showSource,
	}
	c.mu.Unlock()
	if running && c.r != nil {
		c.r.SetProviderConfig(cfg)
	}
}

// SetInput changes the capture source: auto|mic|loopback|device:<id>.
// If running, the reconciler restarts the source. Returns a parse error.
func (c *Controller) SetInput(mode string) error {
	sel, err := parseInputSelection(mode)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.sel = sel
	running := c.running
	c.mu.Unlock()
	if running && c.r != nil {
		c.r.SetSelection(sel)
	}
	return nil
}

// SetShowSource toggles source-transcription (원문 동시 표시). Hot-swaps if running.
func (c *Controller) SetShowSource(on bool) {
	c.mu.Lock()
	c.showSource = on
	running := c.running
	cfg := app.ProviderConfig{
		Model:          c.model,
		TargetLanguage: c.target,
		SourceLanguage: c.source,
		ShowSource:     on,
	}
	c.mu.Unlock()
	if running && c.r != nil {
		c.r.SetProviderConfig(cfg)
	}
}

// Status returns the current human-readable pipeline status for the HUD.
func (c *Controller) Status() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// ListDevices enumerates available capture devices for the input picker.
func (c *Controller) ListDevices() []audio.DeviceInfo {
	devs, err := audio.EnumerateDevices()
	if err != nil {
		return nil
	}
	return devs
}

// -----------------------------------------------------------------------------
// status helpers
// -----------------------------------------------------------------------------

func (c *Controller) setStatus(s string) {
	c.mu.Lock()
	c.status = s
	c.mu.Unlock()
	c.emitStatus()
}

// emitStatus pushes the current status to the HUD frontend (best-effort).
func (c *Controller) emitStatus() {
	if c.ctx == nil {
		return
	}
	wruntime.EventsEmit(c.ctx, "status:update", c.Status())
	tray.SetStatus(c.Status())
}

// -----------------------------------------------------------------------------
// subtitle snapshot construction
// -----------------------------------------------------------------------------

// buildSubtitleMsg renders the engine's current display state into an IPC message.
// Lines = 확정 roll-up 줄 + 진행 중 줄(엔진의 canonical DisplayTranslation 분해).
func buildSubtitleMsg(eng *subtitle.Engine, showSource bool) ipc.SubtitleMsg {
	var lines []string
	for _, ln := range strings.Split(eng.DisplayTranslation(), "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, ln)
		}
	}
	src := ""
	if showSource {
		src = eng.DisplaySource()
	}
	return ipc.SubtitleMsg{
		Lines:   lines,
		Source:  src,
		Visible: eng.Visible(),
	}
}

// subtitleSignature is a cheap change-detection key for throttling IPC pushes.
func subtitleSignature(m ipc.SubtitleMsg) string {
	var b strings.Builder
	if m.Visible {
		b.WriteByte('1')
	} else {
		b.WriteByte('0')
	}
	b.WriteByte('|')
	b.WriteString(m.Source)
	b.WriteByte('|')
	for _, l := range m.Lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

// parseInputSelection maps a CLI/HUD input string to an audio.Selection.
// Mirrors cmd/headless parseSelection (auto|mic|loopback|device:<id>).
func parseInputSelection(s string) (audio.Selection, error) {
	switch {
	case s == "" || s == "auto":
		return audio.Selection{Mode: audio.SelectAuto}, nil
	case s == "mic":
		return audio.Selection{Mode: audio.SelectMic}, nil
	case s == "loopback":
		return audio.Selection{Mode: audio.SelectLoopback}, nil
	case strings.HasPrefix(s, "device:"):
		id := strings.TrimPrefix(s, "device:")
		if id == "" {
			return audio.Selection{}, errBadDevice
		}
		return audio.Selection{Mode: audio.SelectDevice, DeviceID: id}, nil
	default:
		return audio.Selection{}, errBadInput
	}
}
