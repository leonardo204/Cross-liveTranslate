// Cross-liveTranslate — cross-platform live translation app (Go + Wails v2).
//
// Two-process architecture (specs/011-p3-overlay-ui-architecture.md): a single
// binary dispatches on `-role`:
//
//	controller (default) — control HUD + settings; owns tray, pipeline, and
//	                        spawns/supervises the overlay child (P3b).
//	overlay              — full-screen transparent, always-on-top, click-through
//	                        subtitle window (this file wires the P3a PoC).
//
// Each process embeds the same tree but serves its own frontend via fs.Sub,
// since Wails allows a single WebviewWindow per process.
package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"os"

	"cross-livetranslate/internal/ipc"
	"cross-livetranslate/internal/overlay"
	"cross-livetranslate/internal/updater"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend
var assets embed.FS

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
	fset.StringVar(&role, "role", "controller", "process role: controller | overlay")
	// Ignore parse errors from foreign flags; role keeps its default/value.
	_ = fset.Parse(os.Args[1:])

	switch role {
	case "overlay":
		runOverlay()
	default:
		runController()
	}
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

// runController boots the control HUD: a small always-on-top window that drives
// the P2 translation pipeline and supervises the overlay child process (P3b).
// The bound Controller exposes Start/Stop/SetTarget/SetInput/... to the HUD JS.
func runController() {
	flags := parseControllerFlags()

	app := NewApp()
	ctrl := newController()
	app.ctrl = ctrl

	err := wails.Run(&options.App{
		Title:       "Cross-liveTranslate",
		Width:       380,
		Height:      280,
		MinWidth:    340,
		MinHeight:   240,
		AlwaysOnTop: true,
		AssetServer: &assetserver.Options{
			Assets: subFS("controller"),
		},
		BackgroundColour: &options.RGBA{R: 24, G: 24, B: 28, A: 1},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
			ctrl.start(ctx, flags)
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
			WebviewIsTransparent: true,
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

			// IPC receiver: read subtitle snapshots from the controller (our
			// parent) over stdin and forward each to the frontend. Replaces the
			// P3a PoC timer with the real reconciler-fed subtitle stream.
			go ipc.ReadLoop(os.Stdin, func(m ipc.SubtitleMsg) {
				wruntime.EventsEmit(ctx, "subtitle:update", m)
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
