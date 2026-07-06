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
	"math"
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

	// settings is the full persisted user-settings model (Wave 1). 락(mu) 보호.
	// 변경 바인딩 메서드가 이 값을 갱신하고 즉시 settings.json에 저장한다.
	settings config.Settings

	// 번역 음성 재생(A3). player/ducker는 start()에서 1회 생성되어 수명 내내 안정 포인터다
	// (Enqueue/Flush는 runLoop, Start/Stop/게인·덕킹 정책은 바인딩 goroutine이 호출).
	player *audio.Player
	ducker audio.Ducker

	// overlay 자식 프로세스.
	child      *exec.Cmd
	childStdin io.WriteCloser

	// styleCh carries subtitle-style/position snapshots into runLoop so that all
	// stdin writes (subtitle + style) happen from the single runLoop goroutine
	// (stdin 단일 writer 불변식 유지 → 레이스 없음). 버퍼로 non-blocking push.
	styleCh chan ipc.StyleMsg

	closeOnce sync.Once
}

// newController creates a controller with default language/model settings.
func newController() *Controller {
	return &Controller{
		model:    config.GeminiModel,
		target:   config.DefaultTargetLanguage,
		source:   config.DefaultSourceLanguage,
		sel:      audio.Selection{Mode: audio.SelectAuto},
		status:   "idle",
		events:   make(chan pipeline.Event, 256),
		styleCh:  make(chan ipc.StyleMsg, 8),
		settings: config.DefaultSettings(),
	}
}

// start boots the pipeline reconciler, spawns the overlay child, and launches
// the subtitle owner loop. Called from Wails OnStartup (ctx is the app context).
func (c *Controller) start(ctx context.Context, flags controllerFlags) {
	c.ctx = ctx

	// 설정을 먼저 로드해 적용한다(Wave 1). 실패해도 기본값으로 HUD는 뜬다.
	settings, serr := config.Load()
	if serr != nil {
		log.Println("[controller] settings load:", serr)
	}

	// API 키 1회 로드. 실패해도 HUD는 뜨고, Start() 시 오류를 표면화한다.
	key, err := config.APIKey()
	c.mu.Lock()
	c.apiKey, c.apiKeyErr = key, err
	c.settings = settings
	// 설정에서 언어/입력/원문을 초기화한다.
	if settings.Language.Target != "" {
		c.target = settings.Language.Target
	}
	if settings.Language.Source != "" {
		c.source = settings.Language.Source
	}
	c.showSource = settings.Language.ShowSource
	c.sel = selectionFromSettings(settings.Input)
	// 플래그가 있으면 설정을 오버라이드한다(자동화/검증용). 오버라이드 값은 저장하지 않는다.
	if flags.target != "" {
		c.target = flags.target
	}
	if sel, perr := parseInputSelection(flags.input); perr == nil && flags.input != "" {
		c.sel = sel
	}
	c.mu.Unlock()

	// 번역 음성 재생(A3): player/ducker를 1회 생성한다(디바이스는 Start 시점에 연다).
	c.player = audio.NewPlayer()
	c.ducker = audio.NewDucker()

	// 팩토리 주입: reconciler는 gemini/malgo에 직접 의존하지 않는다(headless와 동일).
	newProvider := func(cfg app.ProviderConfig) (pipeline.Provider, error) {
		return gemini.NewProvider(gemini.Config{
			APIKey:                    c.apiKey,
			Model:                     cfg.Model,
			TargetLanguage:            cfg.TargetLanguage,
			SourceLanguage:            cfg.SourceLanguage,
			RequestInputTranscription: cfg.ShowSource,
			// 재생이 켜질 때만 서버가 24kHz PCM을 생성/전송한다(EmitOutputAudio).
			EmitOutputAudio: cfg.EmitOutputAudio,
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

	// 초기 스타일/위치를 오버레이에 전달한다. 오버레이 프론트가 "style:update"를 구독하기
	// 전에 첫 emit이 유실될 수 있으므로(Wails 이벤트는 미구독 시 버퍼링 안 됨), DOM 배선
	// 레이스를 덮도록 잠깐 간격으로 몇 차례 재전송한다. 스타일 적용은 idempotent다.
	go func() {
		for i := 0; i < 3; i++ {
			c.queueStyle()
			select {
			case <-ctx.Done():
				return
			case <-time.After(600 * time.Millisecond):
			}
		}
	}()

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
	// 재생 진단(A3): 약 5초마다 player Stats를 로깅해 OutputAudio가 실제로 링버퍼로
	// 흐르는지(EnqueuedBytes 증가), 백프레셔/ dedup 드롭이 있는지 검증 가능하게 한다.
	var statsTick int
	logPlayerStats := func() {
		if c.player == nil {
			return
		}
		st := c.player.Stats()
		if st.EnqueuedBytes == 0 && st.DroppedChunks == 0 && st.DupSkipped == 0 {
			return // 재생 미사용 — 조용히.
		}
		log.Printf("[controller] player stats: enqueued=%dB dropped=%d dupSkip=%d buffered=%dms",
			st.EnqueuedBytes, st.DroppedChunks, st.DupSkipped, st.BufferedMS)
	}
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
			statsTick++
			if statsTick%20 == 0 { // 250ms × 20 ≈ 5s
				logPlayerStats()
			}
		case ev := <-c.events:
			c.applyEvent(eng, ev)
			maybePush()
		case sm := <-c.styleCh:
			c.pushStyle(sm)
		}
	}
}

// pushStyle writes a style/position snapshot to the overlay child's stdin.
// runLoop 단독 호출(직렬) — pushSubtitle과 같은 goroutine이라 stdin 단일 writer 유지.
func (c *Controller) pushStyle(msg ipc.StyleMsg) {
	if c.childStdin == nil {
		return
	}
	if err := ipc.WriteStyle(c.childStdin, msg); err != nil {
		log.Println("[controller] overlay style push:", err)
	}
}

// queueStyle builds a StyleMsg from the current settings and hands it to runLoop
// via styleCh (non-blocking). 설정 변경/초기화 시 호출 — 실제 stdin write는 runLoop이 한다.
func (c *Controller) queueStyle() {
	c.mu.Lock()
	msg := styleMsgFromSettings(c.settings)
	c.mu.Unlock()
	select {
	case c.styleCh <- msg:
	default: // 채널이 가득 차면(오버레이 미준비 등) 최신값이 곧 다시 전송되므로 드롭 허용.
	}
}

// styleMsgFromSettings maps persisted subtitle-style + position settings into an
// IPC StyleMsg (원본 SubtitleStyle/Overlay 속성 그대로).
func styleMsgFromSettings(s config.Settings) ipc.StyleMsg {
	return ipc.StyleMsg{
		FontFamily:    s.Subtitle.FontFamily,
		FontSize:      s.Subtitle.FontSize,
		FontWeight:    s.Subtitle.FontWeight,
		TextColor:     s.Subtitle.TextColor,
		StrokeEnabled: s.Subtitle.StrokeEnabled,
		StrokeColor:   s.Subtitle.StrokeColor,
		StrokeWidth:   s.Subtitle.StrokeWidth,
		GlowEnabled:   s.Subtitle.GlowEnabled,
		GlowColor:     s.Subtitle.GlowColor,
		GlowRadius:    s.Subtitle.GlowRadius,
		BgEnabled:     s.Subtitle.BgEnabled,
		BgColor:       s.Subtitle.BgColor,
		BgOpacity:     s.Subtitle.BgOpacity,
		Align:         s.Subtitle.Align,
		MaxLines:      s.Subtitle.MaxLines,
		MonitorIndex:  s.Position.MonitorIndex,
		Vertical:      s.Position.Vertical,
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
		// 진행 중 번역 오디오도 즉시 폐기(서버 interrupted — 재생은 계속 가능 상태 유지).
		if c.player != nil {
			c.player.Flush()
		}
	case pipeline.OutputAudio:
		// 번역 음성 재생(A3): PlaybackEnabled일 때만 서버가 PCM을 보내므로(EmitOutputAudio),
		// 여기서는 링버퍼로 흘려보낸다. 재생 정지 상태면 player.Enqueue가 내부에서 드롭한다.
		if c.player != nil {
			c.player.Enqueue(ev.AudioPCM)
		}
	case pipeline.State:
		c.setStatus("state: " + ev.State.String())
	case pipeline.PermanentFailure:
		c.setStatus("failed")
		c.mu.Lock()
		c.running = false
		audioCfg := c.settings.Audio
		c.mu.Unlock()
		// 세션 종료 — 재생 정지 + 원음 볼륨 복원.
		c.applyAudioPolicy(audioCfg, false)
	case pipeline.Usage:
		// P3b는 비용 소비 없음 — 무시.
	}
}

// providerConfigLocked builds the current provider config from controller state.
// 호출자는 c.mu를 보유해야 한다. EmitOutputAudio는 번역 음성 재생 여부를 반영한다(A3).
func (c *Controller) providerConfigLocked() app.ProviderConfig {
	return app.ProviderConfig{
		Model:           c.model,
		TargetLanguage:  c.target,
		SourceLanguage:  c.source,
		ShowSource:      c.showSource,
		EmitOutputAudio: c.settings.Audio.PlaybackEnabled,
	}
}

// applyAudioPolicy applies the translated-audio playback + ducking policy (A3).
// 원본 이식: liveTranslate AppState.applyAudioOutputPolicy.
//
//   - playing = PlaybackEnabled && running(번역 실행 중).
//   - playing false → player.Stop() + ducker.Restore().
//   - playing true → 출력장치 반영 후 player.Start(멱등):
//       · DuckEnabled + 덕킹 지원 → ducker.Duck(DuckVolume).
//       · 기본 출력 공유(OutputDeviceID=="")면 게인보상 = min(1/DuckVolume,4.0) × SoftVolume
//         (원음과 함께 작아진 번역 음량을 되살림, tanh 리미터는 player.Enqueue에서 적용).
//       · 별도 출력장치면 게인 = SoftVolume(번역이 덕킹 영향 없음, 덕킹은 정책대로 원음에만).
//       · DuckEnabled off / 미지원 장치 → ducker.Restore(), 게인 = SoftVolume.
//
// player/ducker는 start()에서 생성되어 nil이 아니지만 방어적으로 검사한다. 여러 goroutine
// (Start/Stop/SaveSettings/PermanentFailure)에서 호출될 수 있으나 각 메서드는 짧고 멱등하며,
// player/ducker 내부가 자체 동기화한다.
func (c *Controller) applyAudioPolicy(a config.AudioSettings, running bool) {
	if c.player == nil || c.ducker == nil {
		return
	}
	if !(a.PlaybackEnabled && running) {
		_ = c.player.Stop()
		c.ducker.Restore()
		return
	}

	// 출력 장치 반영 후 재생 시작(멱등).
	c.player.SetOutputDevice(a.OutputDeviceID)
	if err := c.player.Start(); err != nil {
		log.Println("[controller] player start:", err)
	}

	sharesDefaultOutput := a.OutputDeviceID == "" // 미지정이면 시스템 기본 출력(=덕킹 대상)을 공유.
	gain := a.SoftVolume

	switch {
	case a.DuckEnabled && c.ducker.IsSupported():
		c.ducker.Duck(a.DuckVolume)
		if sharesDefaultOutput {
			comp := 4.0
			if a.DuckVolume > 0 {
				comp = math.Min(1.0/a.DuckVolume, 4.0)
			}
			gain = comp * a.SoftVolume
		}
	case a.DuckEnabled: // 지원되지 않는 출력 장치 → 덕킹 자동 비활성.
		log.Println("[controller] 원음 덕킹 미지원 출력 장치 — 덕킹 비활성(재생/게인은 정상)")
		c.ducker.Restore()
	default: // 덕킹 off.
		c.ducker.Restore()
	}

	c.player.SetGain(gain)
	log.Printf("[controller] audio policy: playing device=%q duck=%v gain=%.2f",
		a.OutputDeviceID, a.DuckEnabled, gain)
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
		// 번역 음성 재생 정지 + 원음 볼륨 복원(A3) — 프로세스 종료 시 시스템 볼륨을 남기지 않는다.
		if c.player != nil {
			_ = c.player.Stop()
		}
		if c.ducker != nil {
			c.ducker.Restore()
		}
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
		Provider:  c.providerConfigLocked(),
	}
	audioCfg := c.settings.Audio
	c.mu.Unlock()

	c.r.SetDesired(d)
	// 재생/덕킹 정책 적용(재생 켜짐 + 실행 중일 때 player.Start + 게인보상 + 덕킹).
	c.applyAudioPolicy(audioCfg, true)
	c.emitStatus()
	return nil
}

// Stop halts translation but keeps the process/overlay alive.
func (c *Controller) Stop() error {
	c.mu.Lock()
	c.running = false
	c.status = "stopped"
	audioCfg := c.settings.Audio
	c.mu.Unlock()
	if c.r != nil {
		c.r.SetRunning(false)
	}
	// 재생 정지 + 원음 볼륨 복원(running=false).
	c.applyAudioPolicy(audioCfg, false)
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
// 변경 사항을 settings.json에 즉시 저장한다(Wave 1).
func (c *Controller) SetTarget(lang string) {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return
	}
	c.mu.Lock()
	c.target = lang
	c.settings.Language.Target = lang
	snap := c.settings
	running := c.running
	cfg := c.providerConfigLocked()
	c.mu.Unlock()
	c.saveSettings(snap)
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
	c.settings.Input = settingsInputFromSelection(sel)
	snap := c.settings
	running := c.running
	c.mu.Unlock()
	c.saveSettings(snap)
	if running && c.r != nil {
		c.r.SetSelection(sel)
	}
	return nil
}

// SetShowSource toggles source-transcription (원문 동시 표시). Hot-swaps if running.
// 변경 사항을 settings.json에 즉시 저장한다(Wave 1).
func (c *Controller) SetShowSource(on bool) {
	c.mu.Lock()
	c.showSource = on
	c.settings.Language.ShowSource = on
	snap := c.settings
	running := c.running
	cfg := c.providerConfigLocked()
	c.mu.Unlock()
	c.saveSettings(snap)
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
// Settings + API-key bindings (Wave 1 / A1)
// -----------------------------------------------------------------------------

// GetSettings returns the current full user-settings model for the settings UI.
func (c *Controller) GetSettings() config.Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settings
}

// SaveSettings validates and persists the full settings model, then applies the
// language/input/show-source values (hot-swaps if running). 자막 스타일/오디오 등
// 상세 적용은 후속 웨이브 — 이번 웨이브는 저장·조회 + 언어/입력/원문 적용이 동작한다.
func (c *Controller) SaveSettings(s config.Settings) error {
	s = normalizeSettings(s)

	c.mu.Lock()
	c.settings = s
	c.target = s.Language.Target
	c.source = s.Language.Source
	c.showSource = s.Language.ShowSource
	c.sel = selectionFromSettings(s.Input)
	running := c.running
	cfg := c.providerConfigLocked()
	sel := c.sel
	audioCfg := c.settings.Audio
	c.mu.Unlock()

	if err := s.Save(); err != nil {
		return err
	}
	if running && c.r != nil {
		c.r.SetProviderConfig(cfg)
		c.r.SetSelection(sel)
	}
	// 재생/덕킹 설정 변경 반영(재생 토글·출력장치·소프트볼륨·덕킹). running과 결합해 적용.
	c.applyAudioPolicy(audioCfg, running)
	// 자막 스타일/위치(모니터·수직·폰트·색 등) 변경을 오버레이에 즉시 반영한다.
	c.queueStyle()
	return nil
}

// SaveAPIKey stores (or clears, when empty) the Gemini API key in the Keychain,
// then refreshes the in-memory key so Start() works without env. 키는 노출하지 않는다.
func (c *Controller) SaveAPIKey(key string) error {
	if err := config.SaveAPIKey(key); err != nil {
		return err
	}
	newKey, err := config.APIKey()
	c.mu.Lock()
	c.apiKey, c.apiKeyErr = newKey, err
	c.mu.Unlock()
	return nil
}

// TestAPIKey verifies a key and returns "" on success, or a user-facing (키 비포함)
// error message on failure. 키 값은 결코 로그/반환값에 노출하지 않는다.
func (c *Controller) TestAPIKey(key string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := config.TestAPIKey(ctx, key); err != nil {
		return err.Error()
	}
	return ""
}

// HasAPIKey reports whether a usable Gemini API key exists (env 또는 키체인).
func (c *Controller) HasAPIKey() bool {
	return config.HasAPIKey()
}

// -----------------------------------------------------------------------------
// settings helpers
// -----------------------------------------------------------------------------

// saveSettings persists a settings snapshot best-effort (로그만, 호출자 흐름 비차단).
func (c *Controller) saveSettings(s config.Settings) {
	if err := s.Save(); err != nil {
		log.Println("[controller] settings save:", err)
	}
}

// selectionFromSettings maps persisted InputSettings to an audio.Selection.
// 알 수 없는 모드는 auto로 폴백한다.
func selectionFromSettings(in config.InputSettings) audio.Selection {
	switch in.Mode {
	case "mic":
		return audio.Selection{Mode: audio.SelectMic}
	case "loopback":
		return audio.Selection{Mode: audio.SelectLoopback}
	case "device":
		if in.DeviceID != "" {
			return audio.Selection{Mode: audio.SelectDevice, DeviceID: in.DeviceID}
		}
		return audio.Selection{Mode: audio.SelectAuto}
	default:
		return audio.Selection{Mode: audio.SelectAuto}
	}
}

// settingsInputFromSelection maps an audio.Selection back to persisted InputSettings.
func settingsInputFromSelection(sel audio.Selection) config.InputSettings {
	switch sel.Mode {
	case audio.SelectMic:
		return config.InputSettings{Mode: "mic"}
	case audio.SelectLoopback:
		return config.InputSettings{Mode: "loopback"}
	case audio.SelectDevice:
		return config.InputSettings{Mode: "device", DeviceID: sel.DeviceID}
	default:
		return config.InputSettings{Mode: "auto"}
	}
}

// normalizeSettings clamps/repairs incoming settings to safe ranges (UI 검증 보조).
// 빈 필수값은 기본값으로 되돌린다. 자막 색 등 형식 검증은 최소로 유지(후속 웨이브에서 강화).
func normalizeSettings(s config.Settings) config.Settings {
	def := config.DefaultSettings()
	if strings.TrimSpace(s.Language.Target) == "" {
		s.Language.Target = def.Language.Target
	}
	if strings.TrimSpace(s.Language.Source) == "" {
		s.Language.Source = def.Language.Source
	}
	if s.Input.Mode == "" {
		s.Input.Mode = def.Input.Mode
	}
	// 자막 폰트 크기(UI 16..72), 최대 줄수(1..4) 클램프.
	if s.Subtitle.FontSize < 16 {
		s.Subtitle.FontSize = 16
	} else if s.Subtitle.FontSize > 72 {
		s.Subtitle.FontSize = 72
	}
	if s.Subtitle.MaxLines < 1 {
		s.Subtitle.MaxLines = 1
	} else if s.Subtitle.MaxLines > 4 {
		s.Subtitle.MaxLines = 4
	}
	if s.Subtitle.GlowRadius < 0 {
		s.Subtitle.GlowRadius = 0
	} else if s.Subtitle.GlowRadius > 30 {
		s.Subtitle.GlowRadius = 30
	}
	s.Subtitle.BgOpacity = clamp01(s.Subtitle.BgOpacity)
	s.Audio.SoftVolume = clamp01(s.Audio.SoftVolume)
	s.Audio.DuckVolume = clamp01(s.Audio.DuckVolume)
	if s.Position.MonitorIndex < 0 {
		s.Position.MonitorIndex = 0
	}
	return s
}

// clamp01 constrains v to [0, 1].
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
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
