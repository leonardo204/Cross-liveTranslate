//go:build windows

package hudpos

// origin_windows.go — 제어 HUD 창의 위치를 읽고 되돌린다(Windows 전용).
//
// Wails 의 WindowGetPosition/WindowSetPosition 은 창이 놓인 모니터를 기준으로 삼아
// 다중 모니터에서 좌표가 어긋난다. 여기서는 Win32 GetWindowRect/SetWindowPos 를 직접 써
// **가상 화면 전역 좌표**(좌상단 원점)를 다룬다 — 저장한 자리를 다른 모니터에서도 그대로
// 되살릴 수 있다.

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SetWindowPos 플래그 + GetSystemMetrics 인덱스(winuser.h).
const (
	swpNoSize     = 0x0001
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010

	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
)

var (
	procGetWindowRect    = moduser32.NewProc("GetWindowRect")
	procSetWindowPos     = moduser32.NewProc("SetWindowPos")
	procGetSystemMetrics = moduser32.NewProc("GetSystemMetrics")
)

// winRect mirrors Win32 RECT.
type winRect struct {
	Left, Top, Right, Bottom int32
}

// findWindowByTitle resolves a top-level window handle from its title.
func findWindowByTitle(title string) (uintptr, error) {
	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return 0, fmt.Errorf("hudpos: 창 제목 변환: %w", err)
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(titlePtr)))
	if hwnd == 0 {
		return 0, fmt.Errorf("hudpos: 제목 %q 창을 찾지 못했습니다", title)
	}
	return hwnd, nil
}

// windowRect reads the window's bounds in virtual-screen coordinates.
func windowRect(hwnd uintptr) (winRect, error) {
	var r winRect
	ok, _, callErr := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ok == 0 {
		return r, fmt.Errorf("hudpos: 창 영역 조회 실패: %v", callErr)
	}
	return r, nil
}

func windowOrigin(title string) (int, int, error) {
	hwnd, err := findWindowByTitle(title)
	if err != nil {
		return 0, 0, err
	}
	r, err := windowRect(hwnd)
	if err != nil {
		return 0, 0, err
	}
	return int(r.Left), int(r.Top), nil
}

// setWindowOrigin restores a saved position. 저장한 자리가 지금 붙어 있는 모니터 전체
// 영역(가상 화면)과 조금도 겹치지 않으면 창을 건드리지 않고 ErrOffScreen 을 돌려준다 —
// 모니터를 뺀 뒤 창이 보이지 않는 곳으로 사라지는 것을 막는다.
func setWindowOrigin(title string, x, y int) error {
	hwnd, err := findWindowByTitle(title)
	if err != nil {
		return err
	}
	r, err := windowRect(hwnd)
	if err != nil {
		return err
	}
	w := int(r.Right - r.Left)
	h := int(r.Bottom - r.Top)

	vx := systemMetric(smXVirtualScreen)
	vy := systemMetric(smYVirtualScreen)
	vw := systemMetric(smCXVirtualScreen)
	vh := systemMetric(smCYVirtualScreen)
	if vw > 0 && vh > 0 {
		if x+w <= vx || x >= vx+vw || y+h <= vy || y >= vy+vh {
			return ErrOffScreen
		}
	}

	ok, _, callErr := procSetWindowPos.Call(hwnd, 0,
		uintptr(int32(x)), uintptr(int32(y)), 0, 0,
		uintptr(swpNoSize|swpNoZOrder|swpNoActivate))
	if ok == 0 {
		return fmt.Errorf("hudpos: 창 위치 지정 실패: %v", callErr)
	}
	return nil
}

// systemMetric wraps GetSystemMetrics (부호 있는 값이라 int32로 되돌린다).
func systemMetric(index int) int {
	v, _, _ := procGetSystemMetrics.Call(uintptr(int32(index)))
	return int(int32(v))
}

// windows 빌드에서 x/sys/windows 를 쓰지 않는 경로가 생겨도 import 가 남도록 하는 참조.
var _ = windows.Handle(0)
