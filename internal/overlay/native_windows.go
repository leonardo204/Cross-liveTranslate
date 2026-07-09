//go:build windows

package overlay

/*
#cgo CXXFLAGS: -std=c++11 -DUNICODE -D_UNICODE -Wall
// -static: mingw 런타임(libgcc/libstdc++/libwinpthread)을 전부 정적 링크한다. 이게 없으면
// 포터블 exe가 libwinpthread-1.dll을 못 찾아 실행되지 않는다(관측된 버그). 시스템 DLL
// (gdiplus/gdi32/user32/ole32, UCRT)은 import lib라 영향 없이 동적 임포트로 남는다.
#cgo LDFLAGS: -static -lgdiplus -lgdi32 -luser32 -lole32 -lstdc++ -lgcc -lpthread
#include <stdlib.h>
#include "native_windows.h"
*/
import "C"

import (
	"log"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"cross-livetranslate/internal/ipc"
)

// RunNativeWindows runs the Windows native subtitle overlay: a Win32 layered
// window drawn with GDI+ (per-pixel alpha via UpdateLayeredWindow), instead of a
// WebView2 window whose per-pixel transparency is broken on Windows 10.
//
// It must own an OS thread for the lifetime of the window + message loop (Win32
// requires the message pump to run on the thread that created the window), so it
// locks the calling goroutine to its OS thread. The controller streams NDJSON
// subtitle/style messages over this process's stdin; a background goroutine
// parses them and updates the native render state (repaint is posted to the UI
// thread). This function blocks until the window is destroyed (WM_QUIT).
func RunNativeWindows() {
	runtime.LockOSThread()

	if rc := C.lt_native_init(); rc != 0 {
		log.Printf("overlay: native window init failed (rc=%d)", int(rc))
		return
	}

	// stdin IPC reader — runs off the UI thread; only mutates C-side state and
	// posts repaint requests (no cross-thread GDI).
	go readNativeIPC()

	// Win32 message loop on the locked OS thread; blocks until WM_QUIT.
	C.lt_native_run_loop()
}

// readNativeIPC consumes the controller's NDJSON stream from stdin and forwards
// each subtitle/style message into the native (C++) render state.
func readNativeIPC() {
	ipc.Dispatch(os.Stdin, ipc.Handler{
		OnSubtitle: func(m ipc.SubtitleMsg) {
			// Join lines with '\n'; the C++ side splits and applies maxLines.
			cLines := C.CString(strings.Join(m.Lines, "\n"))
			cSource := C.CString(m.Source)
			C.lt_native_update_subtitle(cLines, cSource, cBool(m.Visible))
			C.free(unsafe.Pointer(cLines))
			C.free(unsafe.Pointer(cSource))
		},
		OnStyle: func(m ipc.StyleMsg) {
			cFontFamily := C.CString(m.FontFamily)
			cFontWeight := C.CString(m.FontWeight)
			cTextColor := C.CString(m.TextColor)
			cStrokeColor := C.CString(m.StrokeColor)
			cGlowColor := C.CString(m.GlowColor)
			cBgColor := C.CString(m.BgColor)
			cAlign := C.CString(m.Align)
			cVertical := C.CString(m.Vertical)
			C.lt_native_update_style(
				cFontFamily, C.double(m.FontSize), cFontWeight,
				cTextColor,
				cBool(m.StrokeEnabled), cStrokeColor, C.double(m.StrokeWidth),
				cBool(m.GlowEnabled), cGlowColor, C.double(m.GlowRadius),
				cBool(m.BgEnabled), cBgColor, C.double(m.BgOpacity),
				cAlign, C.int(m.MaxLines),
				C.int(m.MonitorIndex), cVertical, C.double(m.Offset),
			)
			C.free(unsafe.Pointer(cFontFamily))
			C.free(unsafe.Pointer(cFontWeight))
			C.free(unsafe.Pointer(cTextColor))
			C.free(unsafe.Pointer(cStrokeColor))
			C.free(unsafe.Pointer(cGlowColor))
			C.free(unsafe.Pointer(cBgColor))
			C.free(unsafe.Pointer(cAlign))
			C.free(unsafe.Pointer(cVertical))
		},
	})
}

// cBool maps a Go bool to the C int (0/1) the native layer expects.
func cBool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}
