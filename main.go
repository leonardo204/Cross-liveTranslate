// Cross-liveTranslate — cross-platform live translation app (Go + Wails v2).
//
// P0 scaffold: this entry point boots an empty Wails window and wires the
// self-update guard. Domain logic (audio capture, Gemini Live, subtitle
// overlay, system tray) is intentionally absent — see specs/000-…-plan.md §7
// for the phased roadmap. Those packages exist as stubs under internal/.
package main

import (
	"context"
	"embed"
	"log"
	"os"

	"cross-livetranslate/internal/updater"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
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

	app := NewApp()

	// NOTE: a plain opaque window for now. The subtitle overlay (F14) and
	// system tray (F17) are separate native shims landing in later phases —
	// see specs/000-cross-platform-porting-plan.md §3/§7.
	err := wails.Run(&options.App{
		Title:     "Cross-liveTranslate",
		Width:     1180,
		Height:    800,
		MinWidth:  900,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
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
		log.Fatalln("wails.Run:", err)
	}
}
