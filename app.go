// App is the Wails-bound struct. Its exported methods are callable from
// the frontend via window.go.main.App.<MethodName>(...).
//
// P0 scaffold: only lifecycle + self-update methods are bound here. Domain
// methods (audio, translation, subtitle, settings) arrive in later phases —
// see specs/000-cross-platform-porting-plan.md.
package main

import (
	"context"
)

// App is the Wails-bound struct.
type App struct {
	ctx context.Context
	// ctrl is set only in the controller role (nil in the overlay process);
	// it owns the pipeline + overlay child and backs the HUD's bound methods.
	ctrl *Controller
}

// NewApp creates a new App.
func NewApp() *App {
	return &App{}
}

// startup captures the Wails runtime context. Called from main.OnStartup.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}
