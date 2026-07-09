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

	"cross-livetranslate/internal/hudpos"
	"cross-livetranslate/internal/ipc"
	"cross-livetranslate/internal/overlay"
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

// hudStartHidden: 제어 HUD를 시작 시 숨길지. darwin은 트레이(NSStatusBar)로 HUD를
// 띄우므로 숨긴다(원본 동작). Windows/기타는 트레이가 stub이라 숨기면 창을 띄울 수단이
// 없으므로 처음부터 표시한다. (Windows 트레이 구현 시 false로 되돌린다.)
var hudStartHidden = runtime.GOOS == "darwin"

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

	err := wails.Run(&options.App{
		Title:            "Cross-liveTranslate",
		Width:            hudWidth,
		Height:           hudHeight,
		Frameless:   true,
		AlwaysOnTop: true,
		// 원본 HUDController.isVisible=false — macOS는 시작 시 제어 HUD를 숨기고 트레이로
		// 띄운다. 그러나 Windows는 트레이가 아직 stub(no-op)이라 숨기면 창을 띄울 수단이 없어
		// 앱이 보이지 않게 실행된다. 따라서 트레이가 없는 플랫폼에서는 HUD를 처음부터 표시한다.
		StartHidden:      hudStartHidden,
		HideWindowOnClose: true,
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
			app.startup(ctx)
			ctrl.start(ctx, flags)
		},
		OnDomReady: func(ctx context.Context) {
			// 창이 realize된 뒤 배치해야 Wails의 기본(중앙) 초기 배치에 덮어써지지 않는다.
			positionHUDTopRight(ctx)
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
