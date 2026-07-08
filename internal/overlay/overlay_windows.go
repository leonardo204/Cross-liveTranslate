//go:build windows

package overlay

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// NOTE: Windows overlay attributes are code-complete but runtime-UNVERIFIED —
// they need validation on real Windows hardware (WebView2 transparency +
// WS_EX_TRANSPARENT click-through interaction, per specs/011 §risks). The Go
// here compiles under GOOS=windows; behaviour is to be confirmed in P3a-win.

// gwlExStyle (GWL_EXSTYLE) is -20; kept as a runtime var so the negative
// index can be converted to uintptr for the syscall without a constant
// overflow at compile time.
var gwlExStyle int32 = -20

const (
	wsExLayered     = 0x00080000
	wsExTransparent = 0x00000020
	wsExToolWindow  = 0x00000080

	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpNoActivate = 0x0010
	swpShowWindow = 0x0040
)

// hwndTopmost == (HWND)-1
var hwndTopmost = ^uintptr(0)

type rect struct {
	left, top, right, bottom int32
}

var (
	user32                   = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW          = user32.NewProc("FindWindowW")
	procGetWindowLongPtrW    = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW    = user32.NewProc("SetWindowLongPtrW")
	procSetWindowPos         = user32.NewProc("SetWindowPos")
	procEnumDisplayMonitors  = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW      = user32.NewProc("GetMonitorInfoW")
	procSetLayeredWindowAttr = user32.NewProc("SetLayeredWindowAttributes")
)

type monitorInfo struct {
	cbSize    uint32
	rcMonitor rect
	rcWork    rect
	dwFlags   uint32
}

// enumMonitors returns each display's full (rcMonitor) rectangle in
// enumeration order.
func enumMonitors() []rect {
	var rects []rect
	cb := syscall.NewCallback(func(hMonitor, hdc, lprc, lparam uintptr) uintptr {
		var mi monitorInfo
		mi.cbSize = uint32(unsafe.Sizeof(mi))
		r1, _, _ := procGetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&mi)))
		if r1 != 0 {
			rects = append(rects, mi.rcMonitor)
		}
		return 1 // continue enumeration
	})
	procEnumDisplayMonitors.Call(0, 0, cb, 0)
	return rects
}

// Apply locates the overlay HWND by its registered window class
// (WindowClassName) and stamps click-through + always-on-top + the target
// monitor's full rectangle.
func Apply(title string, monitorIndex int) error {
	className, err := syscall.UTF16PtrFromString(WindowClassName)
	if err != nil {
		return fmt.Errorf("overlay: class name: %w", err)
	}

	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(className)), 0)
	if hwnd == 0 {
		return fmt.Errorf("overlay: no window with class %q", WindowClassName)
	}

	// Merge overlay extended styles: transparent (click-through 입력 통과),
	// tool-window (no taskbar button / alt-tab entry).
	//
	// 투명도는 Wails의 WindowIsTranslucent(DWM 컴포지션) + WebviewIsTransparent에 맡긴다.
	// 과거엔 여기서 WS_EX_LAYERED + SetLayeredWindowAttributes(LWA_ALPHA,255)로 레이어드
	// 창을 만들었는데, LWA_ALPHA 255는 창 전체를 **불투명**으로 강제해 오버레이가 화면을
	// 까맣게 덮었다(Windows 실측 버그). 레이어드/불투명 강제를 제거해 DWM 투명이 살아나게 한다.
	exStyle, _, _ := procGetWindowLongPtrW.Call(hwnd, uintptr(gwlExStyle))
	exStyle |= wsExTransparent | wsExToolWindow
	procSetWindowLongPtrW.Call(hwnd, uintptr(gwlExStyle), exStyle)

	// Position: cover the requested monitor if we can enumerate it.
	rects := enumMonitors()
	if monitorIndex >= 0 && monitorIndex < len(rects) {
		r := rects[monitorIndex]
		procSetWindowPos.Call(
			hwnd, hwndTopmost,
			uintptr(r.left), uintptr(r.top),
			uintptr(r.right-r.left), uintptr(r.bottom-r.top),
			uintptr(swpNoActivate|swpShowWindow),
		)
	} else {
		procSetWindowPos.Call(
			hwnd, hwndTopmost, 0, 0, 0, 0,
			uintptr(swpNoMove|swpNoSize|swpNoActivate|swpShowWindow),
		)
	}

	return nil
}
