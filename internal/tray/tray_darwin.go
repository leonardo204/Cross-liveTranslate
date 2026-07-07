//go:build darwin

package tray

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "tray_darwin.h"
*/
import "C"

import "unsafe"

// Init installs the macOS menu-bar (NSStatusBar) tray with the given handlers.
// Menu actions are dispatched on the main thread by AppKit and bridged to the
// stored Go handlers via the exported callbacks below.
func Init(h Handlers) error {
	handlers = h
	ctitle := C.CString("Cross-liveTranslate")
	defer C.free(unsafe.Pointer(ctitle))
	C.lt_tray_install(ctitle)
	return nil
}

// SetStatus updates the status-bar item's tooltip text.
func SetStatus(text string) {
	ctext := C.CString(text)
	defer C.free(unsafe.Pointer(ctext))
	C.lt_tray_set_status(ctext)
}

// SetRunning toggles the 번역 시작/정지 menu item label.
func SetRunning(running bool) {
	v := C.int(0)
	if running {
		v = 1
	}
	C.lt_tray_set_running(v)
}

// SetHUDVisible toggles the 제어 HUD 표시 menu item check mark.
func SetHUDVisible(visible bool) {
	v := C.int(0)
	if visible {
		v = 1
	}
	C.lt_tray_set_hud_visible(v)
}

//export lt_tray_go_toggle_translate
func lt_tray_go_toggle_translate() {
	if handlers.OnToggleTranslate != nil {
		handlers.OnToggleTranslate()
	}
}

//export lt_tray_go_toggle_hud
func lt_tray_go_toggle_hud() {
	if handlers.OnToggleHUD != nil {
		handlers.OnToggleHUD()
	}
}

//export lt_tray_go_settings
func lt_tray_go_settings() {
	if handlers.OnSettings != nil {
		handlers.OnSettings()
	}
}

//export lt_tray_go_quit
func lt_tray_go_quit() {
	if handlers.OnQuit != nil {
		handlers.OnQuit()
	}
}
