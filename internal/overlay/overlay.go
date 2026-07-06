// Package overlay — 네이티브 오버레이 창 shim(클릭통과·최상위·투명, 플랫폼별).
//
// Wails v2 exposes no native window handle, so each platform reaches the
// realized NSWindow / HWND directly via its own cgo/syscall path and stamps
// the overlay attributes that Wails options cannot express:
//
//   - screen-saver window level (above full-screen video)
//   - click-through (mouse events pass to the app underneath)
//   - clear/transparent surface (no window background)
//   - cover a chosen monitor's full frame
//
// The concrete Apply implementation is compiled per-GOOS:
//
//	overlay_darwin.go/.h/.m   darwin  (Cocoa, cgo)
//	overlay_windows.go        windows (WS_EX_LAYERED|TRANSPARENT, golang.org/x/sys)
//	overlay_other.go          everything else (stub, returns an error)
//
// Apply is intended to run from Wails' OnDomReady, once the webview window
// exists. It locates the target window by its Wails title and applies the
// overlay attributes for the monitor at monitorIndex (falling back to the
// primary screen when the index is out of range).
package overlay

// WindowTitle is the Wails window title the overlay process assigns to its
// single WebviewWindow. Apply uses it to locate the realized native window.
const WindowTitle = "LiveTranslate Overlay"

// WindowClassName is the Win32 window class the overlay process registers via
// Wails' Windows.WindowClassName option, so FindWindowW can locate the HWND.
const WindowClassName = "LiveTranslateOverlay"
