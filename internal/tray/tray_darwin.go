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

// SetStatus updates the status-bar tooltip text.
func SetStatus(text string) {
	ctext := C.CString(text)
	defer C.free(unsafe.Pointer(ctext))
	C.lt_tray_set_status(ctext)
}

//export lt_tray_go_start
func lt_tray_go_start() {
	if handlers.OnStart != nil {
		handlers.OnStart()
	}
}

//export lt_tray_go_stop
func lt_tray_go_stop() {
	if handlers.OnStop != nil {
		handlers.OnStop()
	}
}

//export lt_tray_go_show
func lt_tray_go_show() {
	if handlers.OnShowHUD != nil {
		handlers.OnShowHUD()
	}
}

//export lt_tray_go_quit
func lt_tray_go_quit() {
	if handlers.OnQuit != nil {
		handlers.OnQuit()
	}
}
