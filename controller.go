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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cross-livetranslate/internal/app"
	"cross-livetranslate/internal/audio"
	"cross-livetranslate/internal/config"
	"cross-livetranslate/internal/cost"
	"cross-livetranslate/internal/gemini"
	"cross-livetranslate/internal/ipc"
	"cross-livetranslate/internal/permission"
	"cross-livetranslate/internal/pipeline"
	"cross-livetranslate/internal/recording"
	"cross-livetranslate/internal/subtitle"
	"cross-livetranslate/internal/tray"
	"cross-livetranslate/internal/vad"

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
	// testSubtitleOn tracks the '테스트 자막 표시'(고정 미리보기) 토글 상태. 설정 파일에
	// 저장하지 않는 일시 상태이며, 번역 정지 상태에서만 ON 될 수 있다(실자막 보존).
	testSubtitleOn bool

	// settings is the full persisted user-settings model (Wave 1). 락(mu) 보호.
	// 변경 바인딩 메서드가 이 값을 갱신하고 즉시 settings.json에 저장한다.
	settings config.Settings

	// 번역 음성 재생(A3). player/ducker는 start()에서 1회 생성되어 수명 내내 안정 포인터다
	// (Enqueue/Flush는 runLoop, Start/Stop/게인·덕킹 정책은 바인딩 goroutine이 호출).
	player *audio.Player
	ducker audio.Ducker

	// 비용 추정(A Wave3). estimator/recorder는 start()에서 1회 생성되는 안정 포인터다.
	// estimator: 세션/누적 USD. Add/Session/Cumulative는 내부 mutex로 보호되어 어느
	// goroutine에서 호출해도 안전하다(입력 계량은 runLoop tick, 출력 토큰은 applyEvent).
	estimator *cost.Estimator
	// recorder: 확정 자막 파일 기록. WriteLine은 runLoop(OnConfirmedLine), Start/Stop은
	// 바인딩 goroutine — recorder가 자체 mutex로 동기화한다.
	recorder *recording.Recorder
	// sentSamples는 VAD 게이트를 통과해 실제 송신된 16kHz mono 샘플 누적(입력 비용 근거).
	// countingSource(오디오 dispatch goroutine)가 더하고 runLoop tick이 델타를 소비한다.
	sentSamples atomic.Int64
	// lastSentSamples/lastCumSave는 runLoop 단독 접근(비용 델타·누적 저장 스로틀).
	lastSentSamples int64
	lastCumSave     time.Time

	// overlay 자식 프로세스.
	child      *exec.Cmd
	childStdin io.WriteCloser

	// settings 자식 프로세스(U1). settingsStdin: controller → settings control(show/hide/quit).
	// settings 자식의 stdout은 spawnSettings에서 별도 goroutine이 읽어 control("changed")를
	// 받으면 reloadSettings로 파일 변경을 반영한다.
	settingsChild *exec.Cmd
	settingsStdin io.WriteCloser
	// settingsWMu serializes writes to settingsStdin (controller → settings control
	// 채널의 단일 writer 규율). ShowSettings/running 통지가 서로 다른 goroutine에서
	// 호출될 수 있어 라인 인터리브를 막는다.
	settingsWMu sync.Mutex

	// hudVisible tracks whether the control-HUD window is currently shown (트레이
	// "제어 HUD 표시" 체크 표식 + 토글용). 바인딩/트레이 goroutine이 mu 아래 갱신한다.
	hudVisible bool

	// 자동 주기 업데이트 확인(원본 Sparkle SUEnableAutomaticChecks + SUScheduledCheckInterval).
	// autoUpdateLoop goroutine이 Settings.Update.AutoCheck면 앱 시작 후 1회 + 24h 주기로
	// checkUpdateWithCtx를 호출한다. 발견 시 아래 두 필드(mu 보호)를 채우고 emitHUD로 HUD에
	// 업데이트 배지를 표시한다. 설치는 App.DownloadAndInstallUpdate를 사용자가 트리거한다.
	updateAvailable bool   // 새 버전 사용 가능(HUD 배지 표시 트리거).
	updateVersion   string // 사용 가능한 새 버전 문자열(예 "1.2.3").
	// autoUpdateReload는 설정 변경(reloadSettings)이 자동확인을 새로 켰을 때 loop를 깨워
	// 즉시 체크하게 한다(원본: 토글 on 시 스케줄 재개). 버퍼 1 — non-blocking 신호.
	autoUpdateReload chan struct{}

	// 제어 HUD 상태(hud:update)용 실시간 입력 신호. countingSource(오디오 dispatch
	// goroutine)가 매 청크마다 갱신하고, runLoop tick과 emitHUD가 읽는다(atomic — 락 불필요).
	//   level        — 최근 청크 RMS(0~1), math.Float64bits로 인코딩.
	//   lastChunkNano — 최근 청크 수신 시각(UnixNano). 무음/정지 감지에 사용.
	level         atomic.Uint64
	lastChunkNano atomic.Int64

	// styleCh carries subtitle-style/position snapshots into runLoop so that all
	// stdin writes (subtitle + style) happen from the single runLoop goroutine
	// (stdin 단일 writer 불변식 유지 → 레이스 없음). 버퍼로 non-blocking push.
	styleCh chan ipc.StyleMsg

	// testCh carries '테스트 자막 표시' 토글 요청(true=on/false=off)을 runLoop로 전달한다.
	// 미리보기 자막은 자막엔진(runLoop 단독 소유)을 통해 표시되므로, overlay stdin 단일
	// writer 불변식을 유지하기 위해 반드시 runLoop에서만 엔진을 만진다.
	testCh chan bool

	closeOnce sync.Once
}

// newController creates a controller with default language/model settings.
func newController() *Controller {
	return &Controller{
		model:            config.GeminiModel,
		target:           config.DefaultTargetLanguage,
		source:           config.DefaultSourceLanguage,
		sel:              audio.Selection{Mode: audio.SelectAuto},
		status:           "idle",
		events:           make(chan pipeline.Event, 256),
		styleCh:          make(chan ipc.StyleMsg, 8),
		testCh:           make(chan bool, 4),
		settings:         config.DefaultSettings(),
		hudVisible:       false, // 원본과 동일하게 HUD는 시작 시 숨김(StartHidden:true). 트레이/캡처로 표시.
		autoUpdateReload: make(chan struct{}, 1),
	}
}

// start boots the pipeline reconciler, spawns the overlay child, and launches
// the subtitle owner loop. Called from Wails OnStartup (ctx is the app context).
func (c *Controller) start(ctx context.Context, flags controllerFlags) {
	c.ctx = ctx

	// 마이크 권한 명시 요청(핵심 버그 수정): malgo(miniaudio)는 macOS TCC 마이크 권한을
	// 스스로 요청하지 않아, 권한 없이 캡처하면 무음만 흘러 Gemini가 "연결 중…"에서 멈춘다.
	// 원본은 AVAudioEngine이 첫 캡처 시 자동 요청하던 것을, 우리는 여기서 명시적으로
	// AVCaptureDevice requestAccess를 호출해 첫 실행 시 다이얼로그를 확실히 띄운다.
	// 이미 결정된(허용/거부) 상태면 시스템이 다이얼로그를 띄우지 않으므로 항상 호출해도 안전.
	// (ad-hoc 서명 개발 빌드는 재빌드 시 코드 해시가 바뀌어 TCC가 새 앱으로 인식 → 권한이
	//  초기화될 수 있다. 이는 개발 워크플로 한계이며 근본 해결은 안정적 서명(별도 작업).)
	permission.RequestMicrophone()

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

	// 비용/녹화(A Wave3): estimator는 영속된 누적 USD로 시드하고, recorder는 닫힌 상태로 생성.
	c.mu.Lock()
	seedCum := c.settings.Cost.CumulativeUSD
	c.mu.Unlock()
	c.estimator = cost.New(seedCum)
	c.recorder = recording.New()

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
		src, err := audio.SelectSource(s)
		if err != nil {
			return nil, err
		}
		// VAD(A Wave3): 설정이 켜져 있으면 에너지 게이트로 감싸 발화 청크만 통과시킨다
		// (무음 구간 미전송 → API 입력/출력 비용 절감). 꺼져 있으면 bypass(원본 그대로).
		c.mu.Lock()
		vadOn := c.settings.VAD.Enabled
		c.mu.Unlock()
		src = vad.WrapSource(src, vadOn)
		// 입력 비용 계량: 실제 송신되는(게이트 통과 후) 청크의 샘플 수를 누적한다.
		return c.countingSource(src), nil
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
	c.spawnSettings()
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

	// 자동 주기 업데이트 확인(원본 Sparkle 패리티): 앱 시작 후 짧은 지연 뒤 1회 +
	// 24시간 주기로 확인한다(Settings.Update.AutoCheck가 켜져 있을 때만).
	go c.autoUpdateLoop()

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

// spawnSettings launches the settings child process (same binary, `-role
// settings`) as a StartHidden window (U1). controller → settings stdin carries
// control(show/hide/quit); settings → controller stdout carries control("changed")
// which triggers reloadSettings(설정 파일 변경 반영). 실패는 치명적이지 않다(로그만).
func (c *Controller) spawnSettings() {
	exe, err := os.Executable()
	if err != nil {
		log.Println("[controller] os.Executable(settings):", err)
		return
	}
	cmd := exec.Command(exe, "-role", "settings")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Println("[controller] settings stdin pipe:", err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Println("[controller] settings stdout pipe:", err)
		_ = stdin.Close()
		return
	}
	// settings 진단 로그(Go log는 stderr로 나간다)는 controller 콘솔로 흘려보낸다.
	// stdout은 control 채널 전용이라 stderr만 상속시킨다.
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Println("[controller] settings start:", err)
		_ = stdin.Close()
		return
	}
	c.settingsChild = cmd
	c.settingsStdin = stdin
	log.Printf("[controller] settings child spawned pid=%d", cmd.Process.Pid)

	// settings 자식의 stdout에서 control 신호를 읽어 반영한다: "changed"(설정 파일 변경),
	// "test-subtitle-on/off"('테스트 자막 표시' 토글 → 오버레이 샘플 자막 표시/해제).
	go ipc.Dispatch(stdout, ipc.Handler{
		OnControl: func(m ipc.ControlMsg) {
			switch m.Cmd {
			case "changed":
				c.reloadSettings()
			case "test-subtitle-on":
				c.queueTestSubtitle(true)
			case "test-subtitle-off":
				c.queueTestSubtitle(false)
			}
		},
	})
	go func() {
		_ = cmd.Wait()
		log.Println("[controller] settings child exited")
	}()
}

// ShowSettings signals the settings child to show its window (트레이 "설정…" /
// HUD 설정 버튼). settings 자식이 없으면(스폰 실패) no-op이다. 창을 띄운 직후 현재 번역
// 실행 상태를 통지해 '테스트 자막 표시' 토글이 즉시 올바른 활성/비활성으로 표시되게 한다.
func (c *Controller) ShowSettings() {
	if c.settingsStdin == nil {
		return
	}
	c.sendSettingsControl("show")
	c.notifySettingsRunning(c.IsRunning())
}

// sendSettingsControl writes one control command to the settings child's stdin
// under settingsWMu (단일 writer 규율). settings 자식이 없으면 no-op이다.
func (c *Controller) sendSettingsControl(cmd string) {
	if c.settingsStdin == nil {
		return
	}
	c.settingsWMu.Lock()
	defer c.settingsWMu.Unlock()
	if err := ipc.WriteControl(c.settingsStdin, ipc.ControlMsg{Cmd: cmd}); err != nil {
		log.Println("[controller] settings control:", cmd, err)
	}
}

// notifySettingsRunning tells the settings child whether translation is running so
// its '테스트 자막 표시' 토글 활성/비활성 + 안내 caption을 갱신한다(원본 .disabled(isRunning)).
func (c *Controller) notifySettingsRunning(running bool) {
	if running {
		c.sendSettingsControl("running-on")
	} else {
		c.sendSettingsControl("running-off")
	}
}

// reloadSettings re-reads settings.json (settings 자식이 변경 후 신호를 보냄) and
// applies the language/input/audio/style values, hot-swapping the running pipeline
// and refreshing the overlay style + HUD. API 키도 재조회한다(설정 창에서 변경 가능).
func (c *Controller) reloadSettings() {
	s, err := config.Load()
	if err != nil {
		log.Println("[controller] settings reload:", err)
		return
	}
	s = normalizeSettings(s)

	c.mu.Lock()
	prevAutoCheck := c.settings.Update.AutoCheck
	c.settings = s
	c.target = s.Language.Target
	c.source = s.Language.Source
	c.showSource = s.Language.ShowSource
	c.sel = selectionFromSettings(s.Input)
	running := c.running
	cfg := c.providerConfigLocked()
	sel := c.sel
	audioCfg := c.settings.Audio
	newAutoCheck := s.Update.AutoCheck
	c.mu.Unlock()

	// 자동확인이 새로 켜졌으면(off→on) loop를 깨워 곧바로 한 번 확인한다(원본: 토글 on 시
	// 스케줄 재개). 꺼졌을 때는 loop가 다음 wake에서 설정을 읽고 스스로 skip하므로 신호 불필요.
	if newAutoCheck && !prevAutoCheck {
		c.signalAutoUpdateReload()
	}

	if running && c.r != nil {
		c.r.SetProviderConfig(cfg)
		c.r.SetSelection(sel)
	}
	c.applyAudioPolicy(audioCfg, running)
	c.queueStyle()

	// API 키가 설정 창에서 변경됐을 수 있으므로 재조회한다(값은 노출하지 않음).
	key, kerr := config.APIKey()
	c.mu.Lock()
	c.apiKey, c.apiKeyErr = key, kerr
	c.mu.Unlock()

	c.emitHUD()
	c.emitStatus()
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
	// 자막 확정 줄을 녹화기로 흘린다(A Wave3). recorder가 닫혀 있으면 WriteLine은 무시된다.
	// OnConfirmedLine은 이 goroutine(runLoop)에서 호출되고 recorder는 자체 mutex를 가진다.
	eng.OnConfirmedLine = func(source, translation string) {
		if c.recorder != nil {
			c.recorder.WriteLine(time.Now(), source, translation)
		}
	}

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
			c.accountInputCost(now)
			c.emitHUD() // 제어 HUD 실시간 상태(레벨/발화/상태) 주기 갱신.
			statsTick++
			if statsTick%20 == 0 { // 250ms × 20 ≈ 5s
				logPlayerStats()
			}
		case ev := <-c.events:
			c.applyEvent(eng, ev)
			maybePush()
		case sm := <-c.styleCh:
			c.pushStyle(sm)
		case on := <-c.testCh:
			if c.applyTestSubtitle(eng, on) {
				maybePush()
			}
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

// 테스트 자막(고정 미리보기) 샘플 문구 — 원본 liveTranslate AppState.setTestSubtitle 이식.
// 번역문은 항상, 원문은 '원문 동시 표시'가 켜졌을 때만 표시된다.
const (
	testSubtitleTranslation = "안녕하세요 — 자막 미리보기입니다"
	testSubtitleSource      = "Hello — subtitle preview"
)

// queueTestSubtitle hands a '테스트 자막 표시' 토글 요청을 runLoop로 넘긴다(non-blocking).
// 실제 엔진 조작/오버레이 push는 runLoop 단독(applyTestSubtitle)에서 일어난다.
func (c *Controller) queueTestSubtitle(on bool) {
	select {
	case c.testCh <- on:
	default: // 채널이 가득 차면(드묾) 최신 토글이 곧 다시 전달되므로 드롭 허용.
	}
}

// applyTestSubtitle applies a test-subtitle(고정 미리보기) toggle to the engine.
// runLoop 단독 호출(엔진 단일 owner). 반환값 true면 호출자가 오버레이로 push 해야 한다.
//
//   - on=true: 번역 중이 아니면 샘플 자막을 고정 표시(원문 동시 표시 시 원문도). 번역 중이면
//     실자막을 덮어쓰지 않도록 무시한다(원본 setTestSubtitle의 !isRunning 가드 이식).
//   - on=false: 미리보기를 숨긴다(hidePreview → reset). 미리보기가 켜져 있던 경우에만
//     엔진을 리셋하므로(테스트 자막이 꺼진 상태에서의 off는 no-op), 진행 중 실자막을
//     건드리지 않는다. 번역 시작(Start) 시 남아 있던 미리보기를 안전하게 청소하는 경로이기도
//     하다(그 시점엔 아직 실자막이 없다).
func (c *Controller) applyTestSubtitle(eng *subtitle.Engine, on bool) bool {
	if on {
		c.mu.Lock()
		running := c.running
		showSrc := c.showSource
		if running {
			// 번역 중 — 실자막 우선(미리보기 무시). 상태는 off로 유지한다.
			c.testSubtitleOn = false
			c.mu.Unlock()
			return false
		}
		c.testSubtitleOn = true
		c.mu.Unlock()
		src := ""
		if showSrc {
			src = testSubtitleSource
		}
		eng.ShowPreview(testSubtitleTranslation, src)
		return true
	}

	// off — 미리보기가 켜져 있던 경우에만 리셋(그 외엔 no-op → 실자막 보존).
	c.mu.Lock()
	wasOn := c.testSubtitleOn
	c.testSubtitleOn = false
	c.mu.Unlock()
	if !wasOn {
		return false
	}
	eng.HidePreview()
	return true
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
		// 비용 추정(A Wave3): 서버 usageMetadata의 출력 오디오 토큰을 누적한다(출력 비용).
		// 입력 비용은 송신 계량(accountInputCost)이 담당한다.
		if c.estimator != nil && ev.Usage != nil {
			c.estimator.AddOutputTokens(ev.Usage.OutputAudioTokens)
			c.emitCost()
		}
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
//     · DuckEnabled + 덕킹 지원 → ducker.Duck(DuckVolume).
//     · 기본 출력 공유(OutputDeviceID=="")면 게인보상 = min(1/DuckVolume,4.0) × SoftVolume
//     (원음과 함께 작아진 번역 음량을 되살림, tanh 리미터는 player.Enqueue에서 적용).
//     · 별도 출력장치면 게인 = SoftVolume(번역이 덕킹 영향 없음, 덕킹은 정책대로 원음에만).
//     · DuckEnabled off / 미지원 장치 → ducker.Restore(), 게인 = SoftVolume.
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
		// 자막 녹화 종료 + 누적 비용 영속화(A Wave3) — 종료 시 파일 핸들/누적을 안전히 마감한다.
		if c.recorder != nil {
			_ = c.recorder.Stop()
		}
		c.persistCumulative()
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
		// settings 자식도 함께 종료한다(전체 종료 — overlay/settings까지 kill).
		if c.settingsChild != nil && c.settingsChild.Process != nil {
			_ = c.settingsChild.Process.Kill()
		}
		if c.settingsStdin != nil {
			_ = c.settingsStdin.Close()
		}
		if c.r != nil {
			c.r.Close()
		}
	})
}

// initTray installs the system tray (menu bar) mirroring the 원본 메뉴 구성:
// 번역 시작/정지 · 제어 HUD 표시(체크) · 설정… · 종료. 콜백을 controller로 브릿지한다.
// 트레이는 부차 목표: 실패해도 core 통합에 영향을 주지 않는다(로그만).
func (c *Controller) initTray() {
	err := tray.Init(tray.Handlers{
		OnToggleTranslate: func() { _ = c.ToggleCapture() },
		OnToggleHUD:       func() { c.toggleHUD() },
		OnSettings:        func() { c.ShowSettings() },
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
	tray.SetRunning(c.IsRunning())
	c.mu.Lock()
	vis := c.hudVisible
	c.mu.Unlock()
	tray.SetHUDVisible(vis)
}

// toggleHUD shows/hides the control-HUD window (트레이 "제어 HUD 표시").
// 표시 상태를 트레이 체크 표식에 반영한다.
func (c *Controller) toggleHUD() {
	if c.ctx == nil {
		return
	}
	c.mu.Lock()
	vis := !c.hudVisible
	c.hudVisible = vis
	c.mu.Unlock()
	if vis {
		wruntime.WindowShow(c.ctx)
		wruntime.WindowUnminimise(c.ctx)
	} else {
		wruntime.WindowHide(c.ctx)
	}
	tray.SetHUDVisible(vis)
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
	sel := c.sel
	c.running = true
	c.status = "starting"
	d := app.Desired{
		Running:   true,
		Selection: sel,
		Provider:  c.providerConfigLocked(),
	}
	audioCfg := c.settings.Audio
	c.mu.Unlock()

	// 마이크 권한 확인(캡처 시작 시): macOS에서 miniaudio 입력 캡처는 마이크 TCC 권한에
	// 종속된다(권한 없으면 무음 → Gemini가 "연결 중…"에서 무한 대기). 루프백 전용 선택이
	// 아니면(Auto/Mic/Device 모두 마이크 경로) 상태를 확인해:
	//   - 미요청: 다이얼로그를 띄운다(첫 실행 안전망 — start()에서 이미 요청했더라도 idempotent).
	//   - 거부/제한: 무한 "연결 중" 대신 HUD에 명확히 "마이크 권한 필요"를 표면화한다.
	// (windows/기타 OS는 MicrophoneStatus가 unknown이라 이 분기가 no-op.)
	if sel.Mode != audio.SelectLoopback {
		switch permission.MicrophoneStatus() {
		case permission.MicNotDetermined:
			permission.RequestMicrophone()
		case permission.MicDenied, permission.MicRestricted:
			log.Println("[controller] 마이크 권한 필요 — 시스템 설정 > 개인정보 보호 및 보안 > 마이크에서 허용하세요")
			c.mu.Lock()
			c.status = "mic-permission"
			c.mu.Unlock()
		}
	}

	c.r.SetDesired(d)
	// 테스트 자막(고정 미리보기)이 켜져 있으면 끈다 — 실제 자막이 우선(원본 AppState.start 이식).
	// running이 이미 true라 새 미리보기 요청은 무시되고, 남아 있던 미리보기 엔진 상태만 청소된다.
	c.queueTestSubtitle(false)
	// 설정 창의 '테스트 자막 표시' 토글을 비활성으로 갱신하도록 실행 상태를 통지한다.
	c.notifySettingsRunning(true)
	// 비용(A Wave3): 새 세션이므로 세션 비용을 0에서 시작한다(누적은 유지). HUD를 즉시 갱신.
	if c.estimator != nil {
		c.estimator.ResetSession()
		c.emitCost()
	}
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
	// 설정 창의 '테스트 자막 표시' 토글을 다시 활성화하도록 정지 상태를 통지한다.
	c.notifySettingsRunning(false)
	// 재생 정지 + 원음 볼륨 복원(running=false).
	c.applyAudioPolicy(audioCfg, false)
	// 비용(A Wave3): 세션 종료 시 누적 비용을 영속화한다.
	c.persistCumulative()
	c.emitStatus()
	return nil
}

// ToggleCapture flips translation on/off (제어 HUD 시작·정지 버튼 / 트레이 번역 토글).
// 원본 AppState.toggleCapture 등가.
func (c *Controller) ToggleCapture() error {
	if c.IsRunning() {
		return c.Stop()
	}
	return c.Start()
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
	prevAutoCheck := c.settings.Update.AutoCheck
	c.settings = s
	c.target = s.Language.Target
	c.source = s.Language.Source
	c.showSource = s.Language.ShowSource
	c.sel = selectionFromSettings(s.Input)
	running := c.running
	cfg := c.providerConfigLocked()
	sel := c.sel
	audioCfg := c.settings.Audio
	newAutoCheck := s.Update.AutoCheck
	c.mu.Unlock()

	if newAutoCheck && !prevAutoCheck {
		c.signalAutoUpdateReload()
	}

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
// cost + recording (A Wave3)
// -----------------------------------------------------------------------------

// countingSource wraps a source so every forwarded chunk's sample count is added
// to c.sentSamples (입력 비용 근거). 게이트 통과 후 실제 송신될 청크만 계량된다.
type countingSource struct {
	src  audio.Source
	ctrl *Controller
}

func (c *Controller) countingSource(src audio.Source) audio.Source {
	return &countingSource{src: src, ctrl: c}
}

func (s *countingSource) Start(ctx context.Context, onChunk func(audio.Chunk)) error {
	return s.src.Start(ctx, func(chunk audio.Chunk) {
		s.ctrl.sentSamples.Add(int64(len(chunk)))
		// 제어 HUD 레벨 미터(원본 audio.level): 청크 RMS + 수신 시각을 atomic으로 기록한다.
		s.ctrl.level.Store(math.Float64bits(float64(audio.RMS(chunk))))
		s.ctrl.lastChunkNano.Store(time.Now().UnixNano())
		onChunk(chunk)
	})
}

func (s *countingSource) Stop() error { return s.src.Stop() }

// accountInputCost folds newly-sent audio samples into the estimator and refreshes
// the HUD. runLoop 단독 호출(tick) — sentSamples 델타를 소비하고 누적 비용을 주기적으로 영속화한다.
func (c *Controller) accountInputCost(now time.Time) {
	if c.estimator == nil {
		return
	}
	cur := c.sentSamples.Load()
	if delta := cur - c.lastSentSamples; delta > 0 {
		c.lastSentSamples = cur
		c.estimator.AddSentSamples(int(delta))
		c.emitCost()
	}
	// 누적 비용을 ~10초마다 settings.json에 영속화(프로세스 급종료 대비). 변경 없으면 no-op.
	if now.Sub(c.lastCumSave) >= 10*time.Second {
		c.lastCumSave = now
		c.persistCumulative()
	}
}

// emitCost pushes the current session/cumulative USD to the HUD (HUDEnabled일 때만).
// 어느 goroutine에서든 호출 가능(estimator/EventsEmit 모두 스레드-세이프).
func (c *Controller) emitCost() {
	if c.ctx == nil || c.estimator == nil {
		return
	}
	c.mu.Lock()
	hud := c.settings.Cost.HUDEnabled
	c.mu.Unlock()
	if !hud {
		return
	}
	wruntime.EventsEmit(c.ctx, "cost:update", map[string]float64{
		"session":    c.estimator.Session(),
		"cumulative": c.estimator.Cumulative(),
	})
}

// persistCumulative writes the estimator's cumulative USD into settings.json (best-effort).
func (c *Controller) persistCumulative() {
	if c.estimator == nil {
		return
	}
	c.mu.Lock()
	c.settings.Cost.CumulativeUSD = c.estimator.Cumulative()
	snap := c.settings
	c.mu.Unlock()
	c.saveSettings(snap)
}

// StartRecording opens a subtitle recording file in Settings.Recording.Directory.
// filename이 비어 있으면 타임스탬프 기본 이름(subtitles-YYYYMMDD-HHMMSS.txt)을 쓴다.
// append=true면 이어붙이기, false면 새로쓰기. 확정 자막이 들어올 때마다 한 줄씩 기록된다.
func (c *Controller) StartRecording(filename string, appendMode bool) error {
	if c.recorder == nil {
		return errors.New("controller: recorder not initialized")
	}
	c.mu.Lock()
	dir := c.settings.Recording.Directory
	c.mu.Unlock()
	if strings.TrimSpace(dir) == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, "Documents")
		}
	}
	name := strings.TrimSpace(filename)
	if name == "" {
		name = "subtitles-" + time.Now().Format("20060102-150405") + ".txt"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if err := c.recorder.Start(path, appendMode); err != nil {
		return err
	}
	log.Printf("[controller] 자막 녹화 시작: %s (append=%v)", path, appendMode)
	c.emitRecording()
	return nil
}

// StopRecording closes the current recording file (멱등).
func (c *Controller) StopRecording() error {
	if c.recorder == nil {
		return nil
	}
	err := c.recorder.Stop()
	log.Println("[controller] 자막 녹화 종료")
	c.emitRecording()
	return err
}

// ToggleRecording flips subtitle recording on/off (제어 HUD 녹화 토글 버튼).
// 시작 시 기본 파일(타임스탬프, 새로쓰기)로 연다. 원본 AppState.toggleRecording 등가.
func (c *Controller) ToggleRecording() error {
	if c.IsRecording() {
		return c.StopRecording()
	}
	return c.StartRecording("", false)
}

// IsRecording reports whether subtitle recording is active.
func (c *Controller) IsRecording() bool {
	return c.recorder != nil && c.recorder.IsRecording()
}

// emitRecording pushes the current recording state to the HUD (best-effort).
func (c *Controller) emitRecording() {
	if c.ctx == nil {
		return
	}
	wruntime.EventsEmit(c.ctx, "recording:update", c.IsRecording())
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

// emitStatus pushes the current status to the HUD frontend (best-effort) and
// syncs the tray tooltip + 번역 토글 라벨.
func (c *Controller) emitStatus() {
	if c.ctx == nil {
		return
	}
	st := c.Status()
	wruntime.EventsEmit(c.ctx, "status:update", st)
	tray.SetStatus(st)
	tray.SetRunning(c.IsRunning())
	c.emitHUD()
}

// -----------------------------------------------------------------------------
// 제어 HUD 상태 이벤트 (hud:update) — 원본 MonitorHUD가 표시하는 필드 전부.
// -----------------------------------------------------------------------------

// hudPayload mirrors 원본 MonitorHUD의 표시 상태(캡처/레벨/VAD/소스/키/비용/녹화).
// U2 제어 HUD 프론트가 이 payload를 그대로 그린다(계약).
type hudPayload struct {
	Capturing         bool    `json:"capturing"`
	StatusText        string  `json:"statusText"` // "캡처중" | "정지"
	Level             float64 `json:"level"`      // 0~1 입력 RMS
	VADEnabled        bool    `json:"vadEnabled"`
	Speaking          bool    `json:"speaking"`
	ActiveSourceLabel string  `json:"activeSourceLabel"`
	APIKeyLoaded      bool    `json:"apiKeyLoaded"`
	GeminiStatus      string  `json:"geminiStatus"`
	CostHUDEnabled    bool    `json:"costHUDEnabled"`
	SentUSD           float64 `json:"sentUSD"`
	RecvUSD           float64 `json:"recvUSD"`
	TotalUSD          float64 `json:"totalUSD"`
	Recording         bool    `json:"recording"`
	IsRunning         bool    `json:"isRunning"`
	// 자동 업데이트 확인 결과(원본 Sparkle 자동확인 → 업데이트 알림). 자동 체크가 새 버전을
	// 발견하면 HUD에 클릭 가능한 "업데이트 vX.Y.Z" 배지를 띄운다(클릭 → 설치).
	UpdateAvailable bool   `json:"updateAvailable"`
	UpdateVersion   string `json:"updateVersion"`
}

// buildHUD snapshots the current control-HUD display state. 어느 goroutine에서든
// 호출 가능(estimator/atomic/락 모두 스레드-세이프).
func (c *Controller) buildHUD() hudPayload {
	c.mu.Lock()
	running := c.running
	vadOn := c.settings.VAD.Enabled
	costHUD := c.settings.Cost.HUDEnabled
	sel := c.sel
	status := c.status
	keyLoaded := c.apiKeyErr == nil
	updateAvail := c.updateAvailable
	updateVer := c.updateVersion
	c.mu.Unlock()

	// 최근 청크 유무로 무음/정지 시 레벨을 0으로 감쇠한다(원본 clampedLevel: 캡처 중 아니면 0).
	now := time.Now().UnixNano()
	last := c.lastChunkNano.Load()
	recent := last != 0 && now-last < int64(400*time.Millisecond)
	level := 0.0
	if recent && running {
		level = math.Float64frombits(c.level.Load())
	}
	if level < 0 {
		level = 0
	} else if level > 1 {
		level = 1
	}

	capturing := running
	speaking := running && vadOn && recent
	statusText := "정지"
	if capturing {
		statusText = "캡처중"
	}

	var sent, recv, total float64
	if c.estimator != nil {
		sent = c.estimator.SessionInput()
		recv = c.estimator.SessionOutput()
		total = c.estimator.Session()
	}

	return hudPayload{
		Capturing:         capturing,
		StatusText:        statusText,
		Level:             level,
		VADEnabled:        vadOn,
		Speaking:          speaking,
		ActiveSourceLabel: activeSourceLabel(sel),
		APIKeyLoaded:      keyLoaded,
		GeminiStatus:      geminiStatusText(status, keyLoaded),
		CostHUDEnabled:    costHUD,
		SentUSD:           sent,
		RecvUSD:           recv,
		TotalUSD:          total,
		Recording:         c.IsRecording(),
		IsRunning:         running,
		UpdateAvailable:   updateAvail,
		UpdateVersion:     updateVer,
	}
}

// emitHUD pushes the current control-HUD state to the HUD frontend (best-effort).
func (c *Controller) emitHUD() {
	if c.ctx == nil {
		return
	}
	wruntime.EventsEmit(c.ctx, "hud:update", c.buildHUD())
}

// activeSourceLabel maps the current selection to 원본 activeSourceLabel 문구.
func activeSourceLabel(sel audio.Selection) string {
	switch sel.Mode {
	case audio.SelectMic:
		return "마이크"
	case audio.SelectLoopback:
		return "시스템 소리(루프백)"
	case audio.SelectDevice:
		if sel.DeviceID != "" {
			return "장치: " + sel.DeviceID
		}
		return "장치(미지정)"
	default:
		return "자동 선택"
	}
}

// geminiStatusText maps the controller's internal status string to 원본 AppState의
// geminiStatus 문구(제어 HUD 하단 "번역: <상태>"에 쓰인다).
func geminiStatusText(status string, keyLoaded bool) string {
	if !keyLoaded {
		return "API 키 없음 — 설정에서 Gemini API 키를 입력하세요"
	}
	switch {
	case status == "mic-permission":
		return "마이크 권한 필요 — 설정에서 허용"
	case strings.Contains(status, "ready"):
		return "번역 중"
	case strings.Contains(status, "connecting"), status == "starting":
		return "연결 중…"
	case strings.Contains(status, "disconnected"), status == "stopped", status == "idle":
		return "연결 안 됨"
	case status == "failed", strings.Contains(status, "error"):
		return "오류"
	default:
		return status
	}
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
