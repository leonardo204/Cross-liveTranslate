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

// 근본 버그 수정(SIGSEGV): 아래 export 콜백은 AppKit 메뉴 target-action에서 **메인 스레드**로
// 동기 호출된다. 핸들러(번역 시작 등)는 malgo(CoreAudio) 오디오 초기화·리컨실러 채널 작업 등
// 무거운 작업을 하는데, 이를 메인 스레드에서 동기로 돌리면 AppKit 런루프를 점유/재진입해
// "signal arrived during cgo execution" SIGSEGV로 앱이 급종료된다(트레이로 시작 시 크래시,
// autostart는 goroutine이라 정상이던 차이의 원인). 따라서 각 핸들러를 goroutine에서 실행해
// 메뉴 액션이 AppKit에 즉시 반환되도록 한다(Wails 바인딩 호출도 동일하게 off-main-thread다).
func runHandler(fn func()) {
	if fn != nil {
		go fn()
	}
}

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
func lt_tray_go_toggle_translate() { runHandler(handlers.OnToggleTranslate) }

//export lt_tray_go_toggle_hud
func lt_tray_go_toggle_hud() { runHandler(handlers.OnToggleHUD) }

//export lt_tray_go_settings
func lt_tray_go_settings() { runHandler(handlers.OnSettings) }

//export lt_tray_go_quit
func lt_tray_go_quit() { runHandler(handlers.OnQuit) }
