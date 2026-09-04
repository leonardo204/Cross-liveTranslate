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
	"fmt"
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
	"cross-livetranslate/internal/childproc"
	"cross-livetranslate/internal/config"
	"cross-livetranslate/internal/cost"
	"cross-livetranslate/internal/gemini"
	"cross-livetranslate/internal/hudpos"
	"cross-livetranslate/internal/ipc"
	"cross-livetranslate/internal/permission"
	"cross-livetranslate/internal/pipeline"
	"cross-livetranslate/internal/recording"
	"cross-livetranslate/internal/subtitle"
	"cross-livetranslate/internal/tray"
	"cross-livetranslate/internal/txlog"
	"cross-livetranslate/internal/updater"
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

	// app은 이 controller 프로세스에 바인드된 App(업데이트 설치 위임용). 설정 창의
	// '지금 설치'는 settings 프로세스에서 직접 App.DownloadAndInstallUpdate를 부르면
	// settings만 종료되고 본체(controller)는 살아남아 스왑/재실행이 꼬인다. 그래서 설치는
	// control 채널로 controller에 위임하고, 여기서 App을 통해 실행해 앱 전체를 종료·교체·재실행한다.
	app *App

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
	// boundaryPending 는 오디오 도메인 VAD 옵저버가 감지한 화자 경계 래치다.
	// 오디오 dispatch goroutine이 set하고 runLoop가 다음 번역 delta 직전에 소비(Swap)한다.
	// bool 1개면 충분하다 — 델타 사이에 경계가 두 번 잡혀도 화면상 한 번의 줄바꿈이면 된다.
	boundaryPending atomic.Bool
	// audioBoundaryMs 는 오디오 경계 임계(ms). 오디오 경로에서 락 없이 읽으려고 atomic이다.
	// 설정 로드/변경 시 controller가 갱신한다(0 이하 = 관찰 비활성).
	audioBoundaryMs atomic.Int64
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

	// trayOK 는 트레이 설치가 실제로 성공했는지다. 실패했는데도 HUD를 숨기면 다시 띄울
	// 수단이 사라져 앱이 조작 불능이 된다(제어 HUD는 frameless라 창 UI가 없다).
	// 그래서 닫기 가로채기(main.go OnBeforeClose)가 이 값을 보고 물러선다.
	trayOK atomic.Bool

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
		model:    config.GeminiModel,
		target:   config.DefaultTargetLanguage,
		source:   config.DefaultSourceLanguage,
		sel:      audio.Selection{Mode: audio.SelectAuto},
		status:   "idle",
		events:   make(chan pipeline.Event, 256),
		styleCh:  make(chan ipc.StyleMsg, 8),
		testCh:   make(chan bool, 4),
		settings: config.DefaultSettings(),
		// 실제 초기값은 runController가 hudStartsHidden()으로 정해 덮어쓴다(창의 StartHidden과
		// 반드시 일치해야 트레이 체크 표식/첫 토글이 헛돌지 않는다).
		hudVisible:       false,
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
	log.Printf("[controller] 마이크 권한 상태(시작 시)=%v — 미요청이면 다이얼로그 요청", permission.MicrophoneStatus())
	permission.RequestMicrophone()

	// 진단: 지금 업데이트가 어디에 설치될지(그리고 App Translocation 상태인지)를 시작 시
	// 남긴다. "자동 업데이트 후 임시 폴더에서 실행" 같은 사고를 로그 하나로 판별하기 위함.
	// 파일시스템 조회가 섞여 있으므로 시작 경로를 막지 않도록 goroutine으로 돌린다.
	go txlog.Logf("update.target", "%s", updater.InstallTargetDiagnostics())

	// 이전 자동 업데이트가 남긴 <exe>.old 를 치운다(Windows 전용 — 교체 직후에는 옛 이미지가
	// 아직 매핑돼 있어 지워지지 않는다). 다른 플랫폼에서는 no-op.
	go updater.CleanupStaleBackup()

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
	c.cacheAudioBoundary(settings) // 오디오 경로용 lock-free 캐시.
	// 설정에서 모델을 초기화한다(소프트코딩 오버라이드; 비면 상수 폴백 유지).
	if settings.Model.ID != "" {
		c.model = settings.Model.ID
	}
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

	// 녹화를 켜 둔 채 껐다면 그대로 켜진 상태로 시작한다(새 타임스탬프 파일).
	// 실제 파일은 첫 확정 자막에서 만들어지므로, 말이 없으면 빈 파일이 생기지 않는다.
	c.mu.Lock()
	recOn := c.settings.Recording.Enabled
	c.mu.Unlock()
	if recOn {
		if err := c.StartRecording("", false); err != nil {
			log.Println("[controller] 지난 녹화 상태 복원 실패:", err)
		}
	}

	// 제어 HUD를 드래그로 옮긴 자리를 주기적으로 기억한다(창에 타이틀바가 없어 드래그 종료
	// 이벤트를 받을 수 없다). 자리가 바뀐 경우에만 파일을 쓴다.
	go c.hudPositionWatcher(ctx)

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
		// 오디오 도메인 화자 경계 관찰(관찰 전용 — 오디오를 막거나 변형하지 않는다).
		// 게이팅이 켜져 있으면 게이트 인스턴스 하나로 게이팅+관찰을 겸한다(RMS 이중 계산 없음).
		// 훅은 오디오 goroutine에서 호출되므로 atomic set + 로그만 한다.
		boundary := c.audioBoundarySilence()
		src = vad.WrapSourceObserved(src, vadOn, boundary, vad.Hooks{
			OnSpeechStart: func(silence time.Duration, st vad.Levels) {
				txlog.Logf("vad.observe", "onset 발화 시작 — 직전 무음 %dms %s", silence.Milliseconds(), levelsText(st))
			},
			OnSpeechEnd: func(st vad.Levels) {
				txlog.Logf("vad.observe", "offset 발화 종료(여운 300ms 소진) %s", levelsText(st))
			},
			OnBoundary: func(silence time.Duration, st vad.Levels) {
				txlog.Logf("vad.observe", "화자 경계 후보 — 무음 %dms ≥ 임계 %dms %s",
					silence.Milliseconds(), c.audioBoundarySilence().Milliseconds(), levelsText(st))
				c.boundaryPending.Store(true) // runLoop가 다음 번역 delta 직전에 소비.
			},
		})
		// 설정 변경(engine.audioBoundarySilenceMs)을 재시작 없이 반영한다(atomic 읽기).
		vad.SetBoundarySilenceFunc(src, c.audioBoundarySilence)
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
	// 부모 수명에 묶는다(Windows Job Object): controller가 어떤 이유로 죽든(정상 종료/작업
	// 관리자 강제종료) OS가 이 자식을 함께 죽여 오버레이 프로세스가 고아로 남지 않게 한다.
	childproc.Supervise(cmd.Process.Pid)
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
	// 부모 수명에 묶는다(Windows Job Object) — settings 자식도 고아로 남지 않게.
	childproc.Supervise(cmd.Process.Pid)
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
			case "hud-reset":
				// 설정 창의 "위치 리셋" — 제어 HUD를 기본 위치(주 화면 우상단)로 되돌린다.
				// 창을 만지는 작업이라 UI 스레드를 막지 않도록 goroutine으로 넘긴다.
				go c.resetHUDPosition()
			case "install-update":
				// 설정 창의 '지금 설치' 위임 — controller(본체)에서 실행해야 앱 전체가
				// 종료·교체·재실행된다. goroutine으로 실행(control 리더 비차단).
				go c.installUpdate()
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
	c.cacheAudioBoundary(s) // 오디오 경로용 lock-free 캐시(설정 변경 즉시 반영).
	c.target = s.Language.Target
	c.source = s.Language.Source
	c.showSource = s.Language.ShowSource
	c.sel = selectionFromSettings(s.Input)
	running := c.running
	cfg := c.providerConfigLocked()
	sel := c.sel
	audioCfg := c.settings.Audio
	newAutoCheck := s.Update.AutoCheck
	hudVisible := s.HUD.Visible
	c.mu.Unlock()

	// 설정 창의 "제어 HUD 표시" 토글을 즉시 반영한다. setHUDVisible이 창 표시·트레이 체크
	// 표식·영속을 한 번에 맞추므로, 값이 그대로면 아무 일도 하지 않는다(중복 저장 없음).
	// 트레이가 없으면 숨기지 않는다 — HUD는 frameless라 다시 띄울 수단이 사라진다.
	if hudVisible || c.trayReady() {
		c.setHUDVisible(hudVisible)
	}

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
	// 화자 경계 트리거 튜닝값 주입(숨은 설정). 1차 트리거는 오디오 VAD 옵저버(→ 아래
	// boundaryPending 래치)이고, 델타 갭은 그 백업이다. 물음표 휴리스틱은 질문→답변 전환용.
	eng.TurnBoundarySilence = c.turnBoundarySilence()
	eng.QuestionBoundary = c.wantQuestionBoundary()
	txlog.Logf("engine.config", "audioBoundarySilence=%s turnBoundarySilence=%s silenceTimeout=%s silenceClear=%s questionBoundary=%v speakerAlternate=%v",
		c.audioBoundarySilence(), eng.TurnBoundarySilence, eng.SilenceTimeout, eng.SilenceClearTimeout,
		eng.QuestionBoundary, c.wantSpeakerAlternate())
	// 진단: 엔진의 판단 근거(확정 사유·화자 토글·경계 보류/해소·버퍼 상태)를 트랜잭션
	// 로그로 흘린다. 엔진은 순수 상태머신이라 파일 I/O를 하지 않고 이 훅으로만 밖에 알린다.
	// 메시지 앞머리가 곧 태그다("engine.confirm ..." → 태그 engine.confirm).
	eng.Logf = func(format string, args ...any) {
		tag, rest := splitLogTag(fmt.Sprintf(format, args...))
		txlog.Logf(tag, "%s", rest)
	}
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
		msg := buildSubtitleMsg(eng, c.wantSource(), c.wantSpeakerAlternate())
		sig := subtitleSignature(msg)
		if sig == lastSig {
			return
		}
		lastSig = sig
		// 진단: 오버레이로 보내는 가시 자막을 로그로 남긴다(오버레이에 안 뜰 때 controller가
		// 실제로 자막을 push 하는지 확인). 프레임 단위 고빈도라 CLT_VERBOSE=1일 때만.
		if logVerbose && msg.Visible && len(msg.Lines) > 0 {
			log.Printf("[controller] 오버레이 push: visible=%v lines=%d %q",
				msg.Visible, len(msg.Lines), truncRunes(strings.Join(msg.Lines, " / "), 60))
		}
		// 진단: 실제로 push되는 스냅샷만 기록한다(서명이 바뀐 경우 = 표시가 달라진 경우).
		// Join/truncate 비용을 피하려고 로깅이 살아 있을 때만 조립한다.
		if txlog.Enabled() {
			txlog.Logf("subtitle.push", "visible=%v lines=%d speakers=%v text=%q",
				msg.Visible, len(msg.Lines), msg.Speakers, truncRunes(strings.Join(msg.Lines, " | "), 200))
		}
		c.pushSubtitle(msg)
	}

	for {
		select {
		case <-c.ctx.Done():
			return
		case now := <-ticker.C:
			// 설정 변경(숨은 설정 engine.turnBoundarySilenceMs)을 재시작 없이 반영한다.
			// eng는 이 goroutine 단독 소유라 경합이 없다.
			eng.TurnBoundarySilence = c.turnBoundarySilence()
			eng.QuestionBoundary = c.wantQuestionBoundary()
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
		AltTextColor:  s.Subtitle.AltTextColor,
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
		Offset:        s.Position.Offset,
	}
}

// applyEvent reflects a pipeline event into the subtitle engine (단일 goroutine).
// Surfaces lifecycle/state to the HUD status text and forwards nothing to stdout.
func (c *Controller) applyEvent(eng *subtitle.Engine, ev pipeline.Event) {
	// 진단: 엔진에 반영되기 직전의 파이프라인 이벤트를 종류+텍스트로 남긴다. gemini.rx(수신)와
	// engine.*(판단) 사이의 연결고리라, 이벤트 채널에서 유실/지연이 있으면 여기서 드러난다.
	logPipelineEvent(ev)

	switch ev.Kind {
	case pipeline.TranslatedDelta:
		// 오디오 도메인에서 잡은 화자 경계를 **이 델타를 넣기 직전에** 적용한다. 번역 지연
		// 때문에 경계 직후 첫 델타가 도착할 즈음엔 이전 발화 번역이 대개 끝나 있어, 새 발화
		// 텍스트가 새 줄·새 색으로 시작한다. 래치는 소비 즉시 clear(Swap)한다.
		if c.boundaryPending.Swap(false) {
			eng.TurnBoundaryHint()
		}
		eng.IngestTranslatedDelta(ev.Text)
	case pipeline.SourceDelta:
		eng.IngestSourceDelta(ev.Text)
	case pipeline.TurnComplete:
		eng.TurnComplete()
		// 진단: 한 발화(turn)의 번역 결과를 로그로 남긴다. 자막이 화면에 안 뜰 때
		// Gemini가 번역을 반환하는지(=오디오가 실제로 전달되는지) 확인하는 근거.
		if t := strings.TrimSpace(eng.DisplayTranslation()); t != "" {
			log.Printf("[controller] 번역 자막(turn 완료): %q", truncRunes(t, 80))
		}
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
		// 상태 전이의 실제 에러 원문을 로그로 남긴다(그렇지 않으면 "연결 중…"에서 왜 멈췄는지
		// 알 수 없다). 에러가 있으면 함께 기록한다(예: "연결 끊김 — 재연결 중").
		if ev.Err != nil {
			log.Printf("[controller] gemini state=%s: %v", ev.State.String(), ev.Err)
		} else {
			log.Printf("[controller] gemini state=%s", ev.State.String())
		}
		c.setStatus("state: " + ev.State.String())
	case pipeline.PermanentFailure:
		// 영구 실패의 실제 원인을 반드시 로그로 남긴다(API 키/네트워크/모델 권한 등). 지금까지
		// 이 에러가 버려져 "연결 중…"의 원인을 추적할 수 없었다.
		log.Printf("[controller] gemini 영구 실패: %v", ev.Err)
		// 시스템 오디오 캡처 권한 실패는 HUD에 명확한 안내를 띄운다(무의미한 "오류" 대신).
		switch {
		case errors.Is(ev.Err, audio.ErrSystemTapPermission):
			c.setStatus("system-audio-permission")
		case errors.Is(ev.Err, audio.ErrLoopbackUnsupported):
			c.setStatus("loopback-unsupported")
		default:
			c.setStatus("failed")
		}
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

// wantSpeakerAlternate reports whether 화자 전환 근사 2색 교대를 적용할지(숨은 스위치).
// 설정 UI에는 없고 settings.json의 subtitle.speakerColorAlternate 로만 끌 수 있다.
func (c *Controller) wantSpeakerAlternate() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settings.Subtitle.SpeakerColorAlternate
}

// turnBoundarySilence returns 델타 갭 기반 턴 경계 임계(숨은 설정 engine.turnBoundarySilenceMs).
// 0 이하면 엔진이 이 트리거를 끄고 기존 2초 무음 확정으로 폴백한다.
func (c *Controller) turnBoundarySilence() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Duration(c.settings.Engine.TurnBoundarySilenceMs) * time.Millisecond
}

// audioBoundarySilence returns 오디오 도메인 화자 경계 임계(숨은 설정
// engine.audioBoundarySilenceMs). **오디오 dispatch goroutine에서 청크마다 호출되므로
// 락을 쓰지 않는다** — 값은 atomic에 캐시되고 설정 로드/변경 시 갱신된다.
func (c *Controller) audioBoundarySilence() time.Duration {
	return time.Duration(c.audioBoundaryMs.Load()) * time.Millisecond
}

// wantQuestionBoundary reports 물음표 휴리스틱(질문→답변 전환) 사용 여부
// (숨은 설정 engine.questionBoundary).
func (c *Controller) wantQuestionBoundary() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settings.Engine.QuestionBoundary
}

// cacheAudioBoundary mirrors 설정의 오디오 경계 임계를 lock-free 캐시에 반영한다.
// 설정을 c.settings에 넣는 모든 경로(초기화/reload/save)에서 호출한다.
func (c *Controller) cacheAudioBoundary(s config.Settings) {
	c.audioBoundaryMs.Store(int64(s.Engine.AudioBoundarySilenceMs))
}

// shutdown kills the overlay child and tears down the reconciler. Idempotent.
func (c *Controller) shutdown() {
	c.closeOnce.Do(func() {
		// 종료 직전 제어 HUD가 놓인 자리를 기억한다(감시 주기 사이에 껐어도 남는다).
		c.captureHUDPosition()
		// 트레이 아이콘을 명시적으로 제거한다. 안 하면 Windows 알림 영역에 프로세스가 죽은
		// 뒤에도 유령 아이콘이 남는다(마우스를 올려야 사라진다).
		tray.Shutdown()
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

// installUpdate runs the self-update pipeline from the controller(본체) process so
// that quitting terminates the whole app tree(controller + overlay + settings),
// letting the swap helper replace the bundle and relaunch. 설정 창의 '지금 설치'가
// control("install-update")로 이 경로를 호출한다. controller의 pending이 비어 있을 수
// 있으니 먼저 CheckUpdate로 스테이징한 뒤 설치한다. goroutine에서 호출(control 리더 비차단).
func (c *Controller) installUpdate() {
	if c.app == nil {
		log.Println("[controller] install-update: app 미바인드")
		return
	}
	if _, err := c.app.CheckUpdate(); err != nil {
		log.Println("[controller] install-update CheckUpdate:", err)
		return
	}
	if err := c.app.DownloadAndInstallUpdate(); err != nil {
		log.Println("[controller] install-update 실패:", err)
	}
}

// initTray installs the system tray (menu bar) mirroring the 원본 메뉴 구성:
// 번역 시작/정지 · 제어 HUD 표시(체크) · 설정… · 종료. 콜백을 controller로 브릿지한다.
// 트레이는 부차 목표: 실패해도 core 통합에 영향을 주지 않는다(로그만).
func (c *Controller) initTray() {
	err := tray.Init(tray.Handlers{
		OnToggleTranslate: func() { _ = c.ToggleCapture() },
		OnToggleHUD:       func() { c.toggleHUD() },
		OnSettings:        func() { c.ShowSettings() },
		// 트레이 "종료"는 창 닫기 가로채기(OnBeforeClose)를 통과해야 하는 **진짜 종료**다.
		OnQuit: func() { requestQuit(c.ctx) },
	})
	if err != nil {
		log.Println("[controller] tray init:", err)
	} else {
		c.trayOK.Store(true)
	}
	tray.SetStatus(c.Status())
	tray.SetRunning(c.IsRunning())
	c.mu.Lock()
	vis := c.hudVisible
	c.mu.Unlock()
	tray.SetHUDVisible(vis)
}

// toggleHUD shows/hides the control-HUD window (트레이 "제어 HUD 표시" · 트레이 아이콘 좌클릭).
func (c *Controller) toggleHUD() {
	c.mu.Lock()
	vis := !c.hudVisible
	c.mu.Unlock()
	c.setHUDVisible(vis)
}

// hideHUD hides the control HUD into the tray. 창의 닫기(X) 버튼 가로채기(main.go
// OnBeforeClose)가 호출한다 — 닫기는 종료가 아니라 "트레이로 숨김"이어야 하고, 그때도
// 트레이 체크 표식과 영속 상태가 실제와 어긋나면 안 된다.
func (c *Controller) hideHUD() {
	if !c.setHUDVisible(false) {
		return // 이미 숨겨져 있었다 — 풍선을 중복해서 띄우지 않는다.
	}
	// Windows는 작업표시줄 아이콘이 없어(hudpos.HideFromTaskbar) 창을 닫으면 앱이 그냥
	// 사라진 것처럼 보인다. 어디로 갔는지 트레이 풍선으로 한 번 알려준다(몇 초 뒤 자동 소멸).
	// darwin/기타는 no-op.
	tray.Notify("Cross-liveTranslate", "트레이에서 계속 실행 중입니다. 트레이 아이콘을 클릭하면 제어 HUD가 다시 나타납니다.")
}

// trayReady reports whether the tray is installed and can bring the HUD back.
func (c *Controller) trayReady() bool { return c.trayOK.Load() }

// resetHUDPosition moves the control HUD back to its default spot (주 화면 우상단).
// 설정 창의 "위치 리셋" 버튼이 control("hud-reset")으로 호출한다. 숨겨져 있으면 먼저
// 띄운다 — 보이지 않는 창을 옮기면 사용자는 아무 일도 일어나지 않은 것으로 본다.
// 저장해 둔 자리도 함께 지운다(안 지우면 감시 goroutine이 곧 다시 덮어쓴다).
func (c *Controller) resetHUDPosition() {
	if c.ctx == nil {
		return
	}
	c.mu.Lock()
	c.settings.HUD.HasPosition = false
	c.settings.HUD.X, c.settings.HUD.Y = 0, 0
	snap := c.settings
	c.mu.Unlock()
	c.saveSettings(snap)

	c.setHUDVisible(true)
	positionHUDTopRight(c.ctx)
}

// restoreHUDPosition puts the control HUD back where the user last left it.
// 창이 화면에 올라온 직후(OnDomReady) 한 번 호출한다. 저장된 자리가 없거나, 그 자리가
// 지금 붙어 있는 화면 밖이면(모니터를 뺐을 때) 기본 위치(주 화면 우상단)로 간다.
func (c *Controller) restoreHUDPosition() {
	if c.ctx == nil {
		return
	}
	c.mu.Lock()
	has := c.settings.HUD.HasPosition
	x, y := c.settings.HUD.X, c.settings.HUD.Y
	c.mu.Unlock()

	if !has {
		positionHUDTopRight(c.ctx)
		return
	}
	err := hudpos.SetWindowOrigin(hudWindowTitle, x, y)
	if err == nil {
		return
	}
	if errors.Is(err, hudpos.ErrOffScreen) {
		log.Printf("[controller] 저장된 HUD 위치(%d,%d)가 화면 밖 — 기본 위치로 되돌린다", x, y)
		c.mu.Lock()
		c.settings.HUD.HasPosition = false
		snap := c.settings
		c.mu.Unlock()
		c.saveSettings(snap)
	} else if !errors.Is(err, hudpos.ErrUnsupported) {
		log.Println("[controller] HUD 위치 복원:", err)
	}
	positionHUDTopRight(c.ctx)
}

// captureHUDPosition remembers where the control HUD is right now so the next
// launch puts it back there. 창이 숨겨져 있으면 읽지 않는다(숨긴 창의 좌표는 의미가 없다).
//
// **c.mu 를 쥔 채 hudpos 를 부르지 않는다.** darwin 구현이 메인 스레드로 hop 하는데,
// 그 메인 스레드가 닫기(X) 처리 중 c.mu 를 기다리고 있으면 서로 물린다.
func (c *Controller) captureHUDPosition() {
	c.mu.Lock()
	vis := c.hudVisible
	c.mu.Unlock()
	if !vis {
		return
	}

	x, y, err := hudpos.WindowOrigin(hudWindowTitle)
	if err != nil {
		return // 미지원 플랫폼이거나 창을 못 찾았다 — 위치 기억은 부가 기능이라 조용히 넘어간다.
	}

	c.mu.Lock()
	if c.settings.HUD.HasPosition && c.settings.HUD.X == x && c.settings.HUD.Y == y {
		c.mu.Unlock()
		return // 그대로다 — 파일을 다시 쓰지 않는다.
	}
	c.settings.HUD.X, c.settings.HUD.Y = x, y
	c.settings.HUD.HasPosition = true
	snap := c.settings
	c.mu.Unlock()

	c.saveSettings(snap)
	c.sendSettingsControl("reload") // 설정 창이 열려 있으면 최신 값으로 다시 읽게 한다.
}

// hudPositionWatcher periodically records the control HUD's spot so a drag is not
// lost when the app quits. 창에는 타이틀바가 없어(frameless) 드래그 종료 이벤트를 받을 수
// 없으므로 주기적으로 확인한다. 자리가 바뀌었을 때만 파일을 쓴다(드래그는 드물다).
func (c *Controller) hudPositionWatcher(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.captureHUDPosition()
		}
	}
}

// setHUDVisible is the single place that changes control-HUD visibility.
// 창 표시/숨김 · controller 상태 · 트레이 체크 표식 · settings.json 영속을 한 번에 맞춘다
// (세 곳이 따로 갱신되면 반드시 어긋난다 — 실제로 그 버그가 있었다).
func (c *Controller) setHUDVisible(vis bool) bool {
	if c.ctx == nil {
		return false
	}
	c.mu.Lock()
	if c.hudVisible == vis {
		c.mu.Unlock()
		return false
	}
	c.hudVisible = vis
	c.settings.HUD.Visible = vis
	snap := c.settings
	c.mu.Unlock()

	if vis {
		wruntime.WindowShow(c.ctx)
		wruntime.WindowUnminimise(c.ctx)
	} else {
		// 숨기고 나면 좌표를 읽을 수 없으므로 지금 기억해 둔다(감시 주기 사이에 숨겨도
		// 마지막 자리가 남는다). hudVisible 은 이미 false라 captureHUDPosition 을 쓸 수 없다.
		if x, y, err := hudpos.WindowOrigin(hudWindowTitle); err == nil {
			c.mu.Lock()
			c.settings.HUD.X, c.settings.HUD.Y = x, y
			c.settings.HUD.HasPosition = true
			snap = c.settings
			c.mu.Unlock()
		}
		wruntime.WindowHide(c.ctx)
	}
	tray.SetHUDVisible(vis)
	// 다음 실행이 마지막 표시 상태로 시작하도록 영속한다(main.go hudStartsHidden이 읽는다).
	// 이 경로는 창 닫기(X) 가로채기를 통해 **메인 메시지 스레드**에서도 불리므로, 디스크
	// 쓰기로 메시지 펌프를 붙잡지 않도록 비동기로 넘긴다(Save는 temp+rename 원자적 쓰기).
	go c.saveSettings(snap)
	return true
}

// applyHUDCapturePolicy applies 캡처 시작/정지에 따른 제어 HUD 자동 표시·숨김 정책
// (원본 HUDController.applyCapturePolicy 이식). 두 설정 모두 기본 off라, 켠 사용자에게만
// 동작한다. 숨김은 트레이가 있을 때만 한다 — 트레이가 없으면 다시 띄울 수단이 사라진다.
func (c *Controller) applyHUDCapturePolicy(running bool) {
	c.mu.Lock()
	autoShow := c.settings.HUD.AutoShowOnCapture
	hideOnStop := c.settings.HUD.HideOnStop
	c.mu.Unlock()

	if running {
		if autoShow {
			c.setHUDVisible(true)
		}
		return
	}
	if hideOnStop && c.trayReady() {
		c.setHUDVisible(false)
	}
}

// HideHUD hides the control HUD into the tray. 제어 HUD의 '닫기(X)' 버튼이 호출하는
// 바인딩이다(프론트는 Windows에서만 이 버튼을 노출한다 — macOS는 트레이/메뉴바로 닫는다).
//
// 트레이가 없으면 숨기지 않는다: HUD는 frameless라 창 UI가 없어서, 숨겨 놓고 트레이도
// 없으면 앱을 다시 부를 방법이 사라진다(안전장치 — OnBeforeClose와 동일한 판단).
func (c *Controller) HideHUD() {
	if !c.trayReady() {
		log.Println("[controller] HideHUD: 트레이가 없어 숨기지 않는다")
		return
	}
	c.hideHUD()
}

// HUDCloseAvailable reports whether the control HUD may be closed into the tray.
// 제어 HUD 프론트가 닫기(X) 버튼을 그리기 전에 물어본다 — 트레이 설치가 실패했으면 HideHUD가
// 거부하므로, 버튼만 보이고 눌러도 아무 일이 없는 상태를 만들지 않기 위해서다.
func (c *Controller) HUDCloseAvailable() bool { return c.trayReady() }

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
		st := permission.MicrophoneStatus()
		log.Printf("[controller] Start() 마이크 권한 상태=%v (입력모드=%v)", st, sel.Mode)
		switch st {
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
	// 제어 HUD 자동 표시 정책(설정 > 제어 HUD > "캡처 시작 시 자동 표시").
	c.applyHUDCapturePolicy(true)
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
	// 제어 HUD 자동 숨김 정책(설정 > 제어 HUD > "캡처 정지 시 숨김").
	c.applyHUDCapturePolicy(false)
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
	c.cacheAudioBoundary(s) // 오디오 경로용 lock-free 캐시(설정 변경 즉시 반영).
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
	// 화자 교대 보조 색이 비어 있으면(구버전 settings.json) 기본값으로 채운다.
	if strings.TrimSpace(s.Subtitle.AltTextColor) == "" {
		s.Subtitle.AltTextColor = def.Subtitle.AltTextColor
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
	// 세부 세로위치(영역 내 offset)를 [0,1]로 클램프한다(원본 subtitleVerticalOffset 규칙).
	s.Position.Offset = clamp01(s.Position.Offset)
	return s
}

// truncRunes shortens s to at most n runes (…를 붙임), UTF-8 경계를 깨지 않는다(로그 프리뷰용).
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
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
	log.Printf("[controller] 오디오 소스 Start — 첫 청크 대기(무음이면 마이크 권한/장치 확인)")
	var firstLogged bool
	return s.src.Start(ctx, func(chunk audio.Chunk) {
		s.ctrl.sentSamples.Add(int64(len(chunk)))
		// 제어 HUD 레벨 미터(원본 audio.level): 청크 RMS + 수신 시각을 atomic으로 기록한다.
		rms := float64(audio.RMS(chunk))
		s.ctrl.level.Store(math.Float64bits(rms))
		s.ctrl.lastChunkNano.Store(time.Now().UnixNano())
		if !firstLogged {
			firstLogged = true
			log.Printf("[controller] 첫 오디오 청크 수신 samples=%d rms=%.4f — 캡처 정상", len(chunk), rms)
		}
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
	log.Printf("[controller] 자막 녹화 예약: %s (append=%v) — 첫 확정 자막에서 파일을 만든다", path, appendMode)
	c.setRecordingEnabled(true)
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
	if oerr := c.recorder.OpenError(); oerr != nil {
		log.Println("[controller] 자막 녹화 파일을 만들지 못했습니다:", oerr)
	}
	c.setRecordingEnabled(false)
	c.emitRecording()
	return err
}

// setRecordingEnabled persists the 녹화 토글 상태 so the next launch starts the same
// way. 값이 그대로면 파일을 다시 쓰지 않는다.
func (c *Controller) setRecordingEnabled(on bool) {
	c.mu.Lock()
	if c.settings.Recording.Enabled == on {
		c.mu.Unlock()
		return
	}
	c.settings.Recording.Enabled = on
	snap := c.settings
	c.mu.Unlock()
	c.saveSettings(snap)
	c.sendSettingsControl("reload") // 설정 창이 열려 있으면 최신 값으로 다시 읽게 한다.
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
	case status == "system-audio-permission":
		return "시스템 오디오 권한 필요 — 설정에서 허용"
	case status == "loopback-unsupported":
		return "시스템 오디오 캡처 미지원 — 마이크 입력을 쓰거나 macOS 14.4+ 필요"
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
// Lines = 확정 roll-up 줄 + 진행 중 줄, Speakers = 줄별 화자 패리티(0/1, 턴 경계 근사).
// alternate=false면 엔진이 패리티를 전부 0으로 내려 기존 단색 표시와 동일해진다.
// eng는 runLoop 단독 소유라 여기서 스위치를 반영해도 경합이 없다.
func buildSubtitleMsg(eng *subtitle.Engine, showSource, alternate bool) ipc.SubtitleMsg {
	eng.SpeakerAlternate = alternate
	dl := eng.DisplayLines()
	var lines []string
	var speakers []int
	var sources []string
	hasSource := false
	for _, l := range dl {
		lines = append(lines, l.Text)
		speakers = append(speakers, l.Speaker)
		s := ""
		if showSource {
			s = l.Source
		}
		if s != "" {
			hasSource = true
		}
		sources = append(sources, s)
	}
	if !hasSource {
		sources = nil // 원문이 하나도 없으면 배열 자체를 보내지 않는다(메시지 간소화).
	}
	// 구버전 오버레이 하위호환: 진행 중 원문 1줄을 기존 필드에도 그대로 채운다.
	src := ""
	if showSource {
		src = eng.DisplaySource()
	}
	return ipc.SubtitleMsg{
		Lines:    lines,
		Speakers: speakers,
		Sources:  sources,
		Source:   src,
		Visible:  eng.Visible(),
	}
}

// -----------------------------------------------------------------------------
// 트랜잭션 로그 헬퍼 (~/.liveTranslate/transactions.log — internal/txlog)
// -----------------------------------------------------------------------------

// logPipelineEvent records one pipeline event as it is applied to the engine.
// 오디오 이벤트는 바이트 수만 남긴다(고빈도·대용량이라 내용은 무의미).
func logPipelineEvent(ev pipeline.Event) {
	if !txlog.Enabled() {
		return
	}
	switch ev.Kind {
	case pipeline.OutputAudio:
		txlog.Logf("pipe.event", "OutputAudio pcmBytes=%d", len(ev.AudioPCM))
	case pipeline.State:
		txlog.Logf("pipe.event", "State state=%s err=%v", ev.State.String(), ev.Err)
	case pipeline.PermanentFailure:
		txlog.Logf("pipe.event", "PermanentFailure err=%v", ev.Err)
	case pipeline.Usage:
		if ev.Usage != nil {
			txlog.Logf("pipe.event", "Usage outputAudioTokens=%d totalTokens=%d",
				ev.Usage.OutputAudioTokens, ev.Usage.TotalTokens)
		}
	default:
		txlog.Logf("pipe.event", "%s text=%q", ev.Kind.String(), ev.Text)
	}
}

// levelsText 는 VAD 옵저버의 적응 판정 상태를 로그 한 조각으로 만든다. 매 프레임이 아니라
// 전이/경계 시점에만 붙여 로그 폭주를 막는다(적응 임계 자체는 프레임마다 변한다).
func levelsText(st vad.Levels) string {
	mode := "고정(워밍업)"
	if st.Adaptive {
		mode = "적응"
	}
	return fmt.Sprintf("[rms=%.4f floor=%.4f on=%.4f off=%.4f %s]",
		st.RMS, st.Floor, st.OnLevel, st.OffLevel, mode)
}

// splitLogTag splits an engine diagnostic message into (tag, rest). 엔진은 txlog를
// import 하지 않으므로(순수 상태머신) 메시지 앞머리에 태그를 실어 보낸다:
// "engine.confirm reason=..." → ("engine.confirm", "reason=..."). 앞머리가 없으면
// 통째로 "engine" 태그로 넘긴다.
func splitLogTag(msg string) (tag, rest string) {
	i := strings.IndexByte(msg, ' ')
	if i <= 0 || !strings.HasPrefix(msg, "engine.") {
		return "engine", msg
	}
	return msg[:i], msg[i+1:]
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
	for i, l := range m.Lines {
		// 화자 패리티도 서명에 포함한다 — 텍스트가 그대로여도 색이 바뀌면 push 해야 한다.
		sp := 0
		if i < len(m.Speakers) {
			sp = m.Speakers[i]
		}
		b.WriteByte(byte('0' + sp&1))
		b.WriteByte(':')
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
