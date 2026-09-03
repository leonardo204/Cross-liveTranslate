// Cross-liveTranslate — cross-platform live translation app (Go + Wails v2).
//
// Three-role architecture (specs/013-ui-parity-rewrite.md — 원본 별도 창 구조 재현):
// a single binary dispatches on `-role`:
//
//	controller (default) — 제어 HUD(260×176 작은 플로팅, frameless·투명·always-on-top).
//	                        트레이·파이프라인 소유. overlay + settings 자식을 spawn·감독한다.
//	settings             — 설정 창(760×580 표준 타이틀바 "liveTranslate 설정", StartHidden).
//	                        controller가 control 신호로 show/hide한다. SettingsAPI 바인드.
//	overlay              — 전체화면 투명·always-on-top·클릭통과 자막 창.
//
// Each process embeds the same tree but serves its own frontend via fs.Sub,
// since Wails allows a single WebviewWindow per process.
//
// # macOS: Dock 아이콘 정책
//
// 세 프로세스가 한 번들을 공유하므로 그대로 두면 Dock 아이콘이 3개 뜬다. 원본
// liveTranslate와 동일하게 accessory(메뉴바) 앱으로 동작시킨다:
//
//   - 번들 Info.plist LSUIElement=true (build/darwin/Info.plist) → 세 프로세스 모두 Dock 0개.
//   - 각 role의 OnStartup에서 activation.Set(Accessory) — .app 밖에서 바이너리를 직접
//     실행하는 개발 경로(Info.plist 미적용)까지 덮는 안전장치.
//   - 설정 창을 띄울 때만 regular로 잠시 올리고(창을 앞으로), 숨기면 accessory로 복귀
//     (settings.go runSettingsControlLoop + internal/activation).
//
// Wails v2.12.0의 mac.Options.ActivationPolicy는 주석 처리(미지원)라 옵션으로 못 준다.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"

	"cross-livetranslate/internal/activation"
	"cross-livetranslate/internal/hudpos"
	"cross-livetranslate/internal/ipc"
	"cross-livetranslate/internal/overlay"
	"cross-livetranslate/internal/txlog"
	"cross-livetranslate/internal/updater"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend
var assets embed.FS

// logVerbose gates high-frequency 진단 로그(자막 push 등). 기본 off — 라이프사이클
// 로그(권한/연결/첫 청크/영구실패)는 항상 남기고, 프레임 단위 로그는 CLT_VERBOSE=1일 때만.
var logVerbose = os.Getenv("CLT_VERBOSE") == "1"

// trayCapable reports whether this platform has a real system tray that can bring a
// hidden control HUD back. darwin=NSStatusBar, windows=Shell_NotifyIcon(internal/tray).
// 트레이가 없는 플랫폼에서 HUD를 숨기면 되살릴 수단이 없으므로 아래 정책이 이 값에 걸린다.
var trayCapable = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// hudHideOnClose: 제어 HUD 닫기(X)를 Wails에게 맡겨 숨기게 할지(true) 종료할지(false).
//
// darwin은 기존 동작 그대로 Wails에 맡긴다. Windows는 false로 두고 OnBeforeClose에서 직접
// 가로챈다 — Wails의 HideWindowOnClose 경로는 창을 숨기기만 하고 OnBeforeClose를 호출하지
// 않아(v2.12 windows/frontend.go의 OnClose 바인딩) 트레이 체크 표식이 실제 표시 상태와
// 어긋나기 때문이다. 가로채기 경로는 숨김과 동시에 상태·체크 표식을 함께 갱신한다.
var hudHideOnClose = runtime.GOOS == "darwin"

// quitRequested 는 "정말 종료"(트레이 종료 메뉴 · 업데이트 설치)와 "창 닫기"를 구분한다.
// OnBeforeClose는 창 닫기만 가로채야 하므로, 진짜 종료 경로는 requestQuit로 이 깃발을 먼저
// 세운다. 그러지 않으면 트레이로 숨기기만 하다가 앱을 영영 종료할 수 없다.
var quitRequested atomic.Bool

// requestQuit terminates the app for real, bypassing the close-to-tray intercept.
func requestQuit(ctx context.Context) {
	quitRequested.Store(true)
	if ctx != nil {
		wruntime.Quit(ctx)
	}
}

// hudStartsHidden decides the initial control-HUD visibility.
//
//	darwin  → 항상 숨김(원본 HUDController.isVisible=false 그대로)
//	그 외   → 항상 표시(시작 시 제어 HUD가 바로 보인다)
func hudStartsHidden() bool {
	// darwin — 항상 숨김(원본 HUDController.isVisible=false 그대로. 메뉴바로 띄운다).
	if runtime.GOOS == "darwin" {
		return true
	}
	// windows/기타 — 항상 표시. 마지막 상태를 복원하지 않는 이유: 작업표시줄 아이콘이 없는
	// 트레이 상주 앱이라, 숨긴 채로 시작하면 실행했는데 화면에 아무것도 안 나타난다(사용자가
	// 앱이 안 켜졌다고 오인한다). 숨김은 사용자가 직접 닫았을 때만 일어나야 한다.
	return false
}

// init forces Go's pure-Go DNS resolver instead of the macOS cgo resolver
// (getaddrinfo). 근본 버그 수정: 번역 시작 시 malgo(CoreAudio) 오디오 초기화와 gemini
// 웹소켓용 cgo DNS 조회가 동시에 cgo로 실행되면 macOS에서 SIGSEGV("signal arrived during
// cgo execution")로 controller가 급종료됐다(간헐적 "번역 안 됨"의 실제 원인). DNS를 순수 Go
// 리졸버로 돌리면 이 cgo 경합이 사라진다. 빌드 태그 netgo와 함께 이중 안전장치.
func init() {
	net.DefaultResolver.PreferGo = true
}

func main() {
	// Windows self-update: if this process was relaunched in apply mode
	// (`--apply-update --target ...`), perform the in-place swap + relaunch
	// and exit before starting the GUI. No-op on macOS/Linux.
	if updater.MaybeApplyUpdate(os.Args[1:]) {
		return
	}

	// `-role` selects the process personality. Parsed leniently so unknown
	// flags handled elsewhere (e.g. the updater's) don't abort startup.
	role := "controller"
	fset := flag.NewFlagSet("cross-livetranslate", flag.ContinueOnError)
	fset.StringVar(&role, "role", "controller", "process role: controller | settings | overlay")
	// Foreign flags(-autostart/-target/-input 등, parseControllerFlags가 처리)로 인한
	// "flag provided but not defined" 노이즈를 stderr에 찍지 않는다. role만 취하면 된다.
	fset.SetOutput(io.Discard)
	// Ignore parse errors from foreign flags; role keeps its default/value.
	_ = fset.Parse(os.Args[1:])

	// 진단 로그를 파일로도 남긴다. `open`으로 실행하면 stdout/stderr가 사라져 "연결 중…"에서
	// 멈추는 등의 원인(무음/API키/네트워크/권한)을 추적할 수 없다. 역할별 로그 파일에 append 하고,
	// 터미널 실행 시에도 보이도록 stderr에 티(tee)한다. 실패해도 무해(stderr 로깅만 유지).
	setupFileLogging(role)

	// 트랜잭션 로그(~/.liveTranslate/transactions.log): 오디오→gemini→pipeline→엔진→오버레이
	// 인과 사슬을 한 파일에서 시간순으로 따라가기 위한 진단 로그다. role 로그와 목적·경로가
	// 다르므로 둘 다 남긴다. 실패해도 무해(no-op으로 강등).
	txlog.Init(role, appVersion)
	defer txlog.Close()

	switch role {
	case "overlay":
		runOverlay()
	case "settings":
		runSettings()
	default:
		runController()
	}
}

// logDir returns ~/Library/Logs/Cross-liveTranslate (created if missing).
func logDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Library", "Logs", "Cross-liveTranslate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// setupFileLogging redirects the standard logger to <role>.log (append) tee'd to
// stderr, and stamps date/time+shortfile prefixes. `open`-launched runs (no
// terminal) are then diagnosable by reading the file. 실패는 무해(stderr 유지).
func setupFileLogging(role string) {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	log.SetPrefix("[" + role + "] ")
	dir, err := logDir()
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, role+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	// stderr(fd 2)를 로그 파일로 리다이렉트해 패닉/cgo 치명 오류(fd 2 직접 출력)까지 파일에
	// 남긴다. `open` 실행은 stderr가 분리돼 크래시 원인이 유실되므로 이 단계가 진단의 핵심이다.
	// stderr 자체가 파일을 가리키므로 log 출력은 파일로 단일 기록한다(이중 기록 방지).
	redirectStderr(f)
	log.SetOutput(f)
	log.Printf("──── %s 프로세스 로깅 시작 (pid=%d) ────", role, os.Getpid())
}

// subFS returns the frontend subtree for a given process role as a root FS
// (so its index.html sits at the AssetServer root).
func subFS(dir string) fs.FS {
	sub, err := fs.Sub(assets, "frontend/"+dir)
	if err != nil {
		log.Fatalln("frontend sub-FS:", dir, err)
	}
	return sub
}

// hudWidth/hudHeight mirror 원본 제어 HUD 크기(FloatingPanel/HUDController). 비용행
// 표시 시 176이므로 U1은 176 고정으로 두어 잘림을 막는다(원본은 동적 150↔176).
const (
	hudWidth  = 260
	hudHeight = 176
)

// runController boots the control HUD: a small frameless, transparent,
// always-on-top window(260×176, 원본 FloatingPanel 재현)이 P2 번역 파이프라인을 구동하고
// overlay + settings 자식 프로세스를 감독한다. 바인드된 Controller가 ToggleCapture/Start/
// Stop/ShowSettings/ToggleRecording 등 제어 HUD 계약을 노출한다.
func runController() {
	flags := parseControllerFlags()

	app := NewApp()
	ctrl := newController()
	app.ctrl = ctrl
	ctrl.app = app // 설정 창의 '지금 설치'를 controller 경유로 실행하기 위한 참조.

	// 초기 HUD 표시 상태를 먼저 정해 Wails 옵션(StartHidden)과 controller 상태(트레이 체크
	// 표식의 진실원)를 일치시킨다. 예전에는 Windows에서 창은 보이는데 hudVisible=false라
	// 첫 토글이 "숨기기"가 아니라 "보이기"로 헛돌았다.
	startHidden := hudStartsHidden()
	ctrl.hudVisible = !startHidden

	err := wails.Run(&options.App{
		Title:       "Cross-liveTranslate",
		Width:       hudWidth,
		Height:      hudHeight,
		Frameless:   true,
		AlwaysOnTop: true,
		// 원본 HUDController.isVisible=false — macOS는 시작 시 제어 HUD를 숨기고 트레이로
		// 띄운다. 그러나 Windows는 트레이가 아직 stub(no-op)이라 숨기면 창을 띄울 수단이 없어
		// 앱이 보이지 않게 실행된다. 따라서 트레이가 없는 플랫폼에서는 HUD를 처음부터 표시한다.
		StartHidden: startHidden,
		// macOS는 트레이로 HUD를 다시 띄울 수 있어 닫기=숨김(HideWindowOnClose:true)이 맞다.
		// Windows는 트레이가 stub이라 닫으면 되살릴 수단이 없다 → 닫기=종료로 두어야 앱과
		// 자식 프로세스가 정상 정리된다(닫아도 숨기만 하면 프로세스가 계속 남는 문제 해결).
		HideWindowOnClose: hudHideOnClose,
		BackgroundColour:  &options.RGBA{R: 0, G: 0, B: 0, A: 0},
		AssetServer: &assetserver.Options{
			Assets: subFS("controller"),
		},
		Mac: &mac.Options{
			WebviewIsTransparent: true,
			WindowIsTranslucent:  false,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: true,
		},
		OnStartup: func(ctx context.Context) {
			// 메뉴바(accessory) 앱 — Dock 아이콘을 띄우지 않는다. 번들 Info.plist의
			// LSUIElement=true가 1차 보증이고, 이 호출은 .app 밖에서 바이너리를 직접
			// 실행하는 개발 경로(Info.plist 미적용)까지 덮는 안전장치다. darwin 외에는 no-op.
			activation.Set(activation.Accessory)
			app.startup(ctx)
			ctrl.start(ctx, flags)
		},
		OnDomReady: func(ctx context.Context) {
			// 창이 realize된 뒤 배치해야 Wails의 기본(중앙) 초기 배치에 덮어써지지 않는다.
			positionHUDTopRight(ctx)
			// 트레이 상주 앱 — 제어 HUD는 작업표시줄/Alt+Tab에 자리를 차지하지 않는다
			// (원본 macOS accessory 앱 동작의 Windows 등가물). 그 외 플랫폼은 no-op.
			hideHUDFromTaskbar()
		},
		// 닫기(X) 가로채기 — Windows 전용 경로(hudHideOnClose=false인 트레이 플랫폼).
		// 트레이가 있으면 닫기는 종료가 아니라 "트레이로 숨김"이어야 한다. 진짜 종료
		// (트레이 종료 메뉴 · 업데이트 설치)는 requestQuit가 quitRequested를 세워 통과시킨다.
		OnBeforeClose: func(ctx context.Context) bool {
			// 트레이 설치가 실패했다면 가로채지 않는다 — HUD는 frameless라 창 UI가 없어서,
			// 숨겨 놓고 트레이도 없으면 앱을 다시 부를 방법이 사라진다.
			if trayCapable && !hudHideOnClose && ctrl.trayReady() && !quitRequested.Load() {
				ctrl.hideHUD()
				return true // 종료를 막는다.
			}
			return false
		},
		OnShutdown: func(ctx context.Context) {
			ctrl.shutdown()
		},
		Bind: []interface{}{
			app,
			ctrl,
		},
	})
	if err != nil {
		log.Fatalln("wails.Run(controller):", err)
	}
}

// positionHUDTopRight places the control HUD near the primary screen's top-right
// (원본 HUDController.defaultOrigin: 우상단 20pt 안쪽, 메뉴바 아래). 실패는 무해(로그만).
//
// 멀티모니터 견고성: Wails의 ScreenGetAll에는 모니터 원점(X/Y)이 없고 WindowSetPosition은
// 창이 놓인 모니터 기준이라, Wails가 HUD를 보조 모니터에 생성하면 화면 밖으로 나갔다
// (3모니터 환경에서 관측됨). NSScreen 기반 네이티브 배치(hudpos)로 주 모니터 visibleFrame
// 우상단에 확실히 놓는다.
func positionHUDTopRight(ctx context.Context) {
	if err := hudpos.PositionPrimaryTopRight("Cross-liveTranslate"); err != nil {
		log.Println("[controller] HUD 배치:", err)
	}
}

// hideHUDFromTaskbar removes the control-HUD taskbar button (Windows 전용).
// 실패는 무해(로그만) — 작업표시줄에 버튼 하나가 남을 뿐 기능에 지장이 없다.
func hideHUDFromTaskbar() {
	if err := hudpos.HideFromTaskbar("Cross-liveTranslate"); err != nil {
		log.Println("[controller] HUD 작업표시줄 제외:", err)
	}
}

// stderrLogger is a Wails logger that writes to os.Stderr instead of the default
// os.Stdout. settings 자식의 stdout은 controller가 읽는 control(NDJSON) 채널이므로,
// Wails 진단 로그가 그 스트림을 오염시키지 않도록 stderr로 분리한다.
type stderrLogger struct{}

func (stderrLogger) write(level, message string) {
	_, _ = fmt.Fprintln(os.Stderr, level+" | "+message)
}
func (l stderrLogger) Print(message string)   { l.write("PRT", message) }
func (l stderrLogger) Trace(message string)   { l.write("TRA", message) }
func (l stderrLogger) Debug(message string)   { l.write("DEB", message) }
func (l stderrLogger) Info(message string)    { l.write("INF", message) }
func (l stderrLogger) Warning(message string) { l.write("WAR", message) }
func (l stderrLogger) Error(message string)   { l.write("ERR", message) }
func (l stderrLogger) Fatal(message string)   { l.write("FAT", message) }

// runSettings boots the settings window(760×580 표준 타이틀바 "liveTranslate 설정",
// StartHidden). controller가 control("show")로 표시한다. SettingsAPI + App을 바인드하고
// (SettingsAPI가 설정 파일·API 키·디바이스·모델·버전 계약을 노출), stdin control 루프를 돈다.
func runSettings() {
	app := NewApp()
	api := newSettingsAPI(app)

	err := wails.Run(&options.App{
		Title:         "liveTranslate 설정",
		Width:         760,
		Height:        580,
		MinWidth:      760,
		MinHeight:     580,
		MaxWidth:      760,
		MaxHeight:     580,
		DisableResize: true,
		StartHidden:   true,
		// 이 프로세스의 stdout은 controller가 읽는 control 채널 전용(NDJSON)이다.
		// Wails 기본 로거는 os.Stdout에 쓰므로 control 라인과 뒤섞여 test-subtitle-on/off·changed
		// 등 제어 메시지가 손상/유실된다(버그: '테스트 자막 표시'가 오버레이에 전혀 반영 안 됨).
		// Wails 로그를 stderr로 돌려 stdout을 순수 control 채널로 유지한다.
		Logger:   stderrLogger{},
		LogLevel: logger.ERROR,
		// 원본 SettingsWindowController: isReleasedWhenClosed=false — 닫기(X) 시 창을
		// 파괴/종료하지 않고 숨기기만 해야 트레이/HUD에서 다시 열 수 있다.
		HideWindowOnClose: true,
		AssetServer: &assetserver.Options{
			Assets: subFS("settings"),
		},
		BackgroundColour: &options.RGBA{R: 236, G: 236, B: 238, A: 1},
		OnStartup: func(ctx context.Context) {
			// 시작 시점엔 창이 숨겨져 있으므로 accessory(Dock 아이콘 없음)로 둔다.
			// control("show")를 받을 때만 regular로 올린다(원본 SettingsWindowController).
			activation.Set(activation.Accessory)
			app.startup(ctx)
			api.setCtx(ctx)
			go runSettingsControlLoop(ctx)
		},
		Bind: []interface{}{
			app,
			api,
		},
	})
	if err != nil {
		log.Fatalln("wails.Run(settings):", err)
	}
}

// parseControllerFlags leniently reads controller-role flags from os.Args.
// Foreign flags (e.g. -role, updater flags) are ignored so startup never aborts.
func parseControllerFlags() controllerFlags {
	var f controllerFlags
	var role string
	fset := flag.NewFlagSet("controller", flag.ContinueOnError)
	fset.BoolVar(&f.autostart, "autostart", false, "start translation immediately on launch")
	fset.StringVar(&f.target, "target", "", "target language (BCP-47), e.g. en, ko, ja")
	fset.StringVar(&f.input, "input", "", "input source: auto|mic|loopback|device:<id>")
	fset.StringVar(&role, "role", "controller", "process role (ignored here)")
	_ = fset.Parse(os.Args[1:])
	return f
}

// runOverlay boots the transparent, always-on-top, click-through subtitle
// window and drives a PoC subtitle loop for visual verification.
//
// Wails options give us frameless/always-on-top/transparent-webview/hidden;
// the click-through, screen-saver level, clear background, and monitor cover
// are stamped natively in OnDomReady via internal/overlay.Apply.
func runOverlay() {
	// Windows: WebView2 per-pixel transparency is broken on Windows 10 (the
	// overlay renders as an opaque/black rectangle). Bypass Wails entirely and
	// draw the subtitle with a native Win32 layered window (UpdateLayeredWindow
	// + GDI+ premultiplied ARGB — true per-pixel alpha). This never returns
	// until the overlay window is destroyed. macOS keeps the WebView2 path below.
	if runtime.GOOS == "windows" {
		overlay.RunNativeWindows()
		return
	}

	app := NewApp()

	err := wails.Run(&options.App{
		Title:            overlay.WindowTitle,
		Width:            1280,
		Height:           720,
		Frameless:        true,
		AlwaysOnTop:      true,
		StartHidden:      true,
		BackgroundColour: &options.RGBA{R: 0, G: 0, B: 0, A: 0},
		AssetServer: &assetserver.Options{
			Assets: subFS("overlay"),
		},
		Mac: &mac.Options{
			WebviewIsTransparent: true,
			WindowIsTranslucent:  false,
		},
		Windows: &windows.Options{
			// per-pixel 투명(자막만 보이고 나머지 완전 투명)의 정석 경로:
			// WebviewIsTransparent:true + 창 배경색 알파 0(BackgroundColour A:0). WebView2가
			// DirectComposition으로 픽셀 단위 알파를 합성한다.
			// WindowIsTranslucent는 DWM BlurBehind/Acrylic backdrop을 켜 화면이 뿌옇게(blur)
			// 되므로 반드시 끈다(Windows 실측: translucent=true → blur). Wails 문서 기준.
			WebviewIsTransparent: true,
			WindowIsTranslucent:  false,
			WindowClassName:      overlay.WindowClassName,
		},
		OnStartup: func(ctx context.Context) {
			// 자막 오버레이는 클릭통과 창이라 Dock에 뜰 이유가 없다(accessory).
			activation.Set(activation.Accessory)
			app.startup(ctx)
		},
		OnDomReady: func(ctx context.Context) {
			// Window is realized: stamp native overlay attributes, then show.
			if err := overlay.Apply(overlay.WindowTitle, 0); err != nil {
				log.Println("overlay.Apply:", err)
			}
			wruntime.WindowShow(ctx)

			// IPC receiver: read the controller's (our parent) NDJSON stream over
			// stdin and route each message to the frontend. Two message kinds
			// share the stream: subtitle snapshots (text) and style updates
			// (font/color/outline/glow/background/align/lines/monitor/vertical).
			// A single goroutine consumes the stream, so lastMonitor needs no lock.
			lastMonitor := 0 // seeded from the initial Apply(index 0) above.
			go ipc.Dispatch(os.Stdin, ipc.Handler{
				OnSubtitle: func(m ipc.SubtitleMsg) {
					wruntime.EventsEmit(ctx, "subtitle:update", m)
				},
				OnStyle: func(m ipc.StyleMsg) {
					wruntime.EventsEmit(ctx, "style:update", m)
					// Re-cover the target monitor when the chosen index changes.
					// overlay.Apply hops to the main thread internally (safe here).
					if m.MonitorIndex != lastMonitor {
						lastMonitor = m.MonitorIndex
						if err := overlay.Apply(overlay.WindowTitle, m.MonitorIndex); err != nil {
							log.Println("overlay.Apply(monitor):", err)
						}
					}
				},
			})
		},
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		log.Fatalln("wails.Run(overlay):", err)
	}
}
