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
	"time"

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

// runController boots the main control window. P3a keeps this at parity with
// the P0 placeholder (empty window + self-update binding); the HUD/settings
// UI and tray land in P3b.
func runController() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "Cross-liveTranslate",
		Width:     1180,
		Height:    800,
		MinWidth:  900,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: subFS("controller"),
		},
		BackgroundColour: &options.RGBA{R: 236, G: 236, B: 236, A: 1},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
		},
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		log.Fatalln("wails.Run(controller):", err)
	}
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

			// PoC subtitle driver — cycles sample captions for eyeball
			// verification (transparent background + click-through). Replaced
			// by the IPC-fed reconciler in P3b.
			go driveSampleSubtitles(ctx)
		},
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		log.Fatalln("wails.Run(overlay):", err)
	}
}

// subtitlePayload matches the frontend "subtitle:update" contract.
type subtitlePayload struct {
	Lines   []string `json:"lines"`
	Visible bool     `json:"visible"`
	Source  string   `json:"source,omitempty"`
}

// driveSampleSubtitles emits rotating sample captions every 2s until the
// context is cancelled (window closed).
func driveSampleSubtitles(ctx context.Context) {
	samples := []subtitlePayload{
		{Lines: []string{"안녕하세요 → Hello"}, Visible: true, Source: "안녕하세요"},
		{Lines: []string{"실시간 번역 오버레이 테스트"}, Visible: true},
		{Lines: []string{"Live translation overlay", "second line of subtitle"}, Visible: true},
		{Lines: []string{"클릭이 아래 앱으로 통과되는지 확인하세요"}, Visible: true, Source: "Check click-through"},
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Emit the first sample immediately.
	i := 0
	wruntime.EventsEmit(ctx, "subtitle:update", samples[i])

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			i = (i + 1) % len(samples)
			wruntime.EventsEmit(ctx, "subtitle:update", samples[i])
		}
	}
}
