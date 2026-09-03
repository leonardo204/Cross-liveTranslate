//go:build windows

package hudpos

// taskbar_windows.go — 제어 HUD를 작업표시줄/Alt+Tab에서 제외한다(Windows 전용).
//
// # 왜 필요한가
//
// 이 앱은 트레이 상주 앱이다(원본 liveTranslate는 macOS accessory/LSUIElement 앱이라
// Dock에 뜨지 않는다). Windows에는 accessory 정책이 없어 그대로 두면 제어 HUD가
// 작업표시줄 버튼을 하나 차지한다 — 트레이 아이콘과 중복이고, 원본 동작과도 다르다.
//
// # 방법
//
// WS_EX_TOOLWINDOW 확장 스타일을 켜면 셸이 그 창을 작업표시줄/Alt+Tab 목록에서 뺀다.
// **주의**: 이 스타일은 창이 보이는 동안 바꾸면 셸이 이미 만든 작업표시줄 버튼을
// 갱신하지 않는다(버튼이 그대로 남는다). 그래서 숨김 → 스타일 변경 → 다시 표시 순서로
// 적용한다. WS_EX_APPWINDOW(작업표시줄 강제 표시)가 켜져 있으면 함께 끈다.

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// gwlExStyle (GWL_EXSTYLE) 는 -20. 음수 인덱스를 상수 오버플로 없이 uintptr로 넘기기
// 위해 런타임 변수로 둔다.
var gwlExStyle int32 = -20

const (
	wsExToolWindow = 0x00000080
	wsExAppWindow  = 0x00040000

	swHide           = 0
	swShowNoActivate = 4
)

var (
	moduser32             = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW       = moduser32.NewProc("FindWindowW")
	procGetWindowLongPtrW = moduser32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW = moduser32.NewProc("SetWindowLongPtrW")
	procShowWindow        = moduser32.NewProc("ShowWindow")
	procIsWindowVisible   = moduser32.NewProc("IsWindowVisible")
)

// hideFromTaskbar finds the window by title and removes its taskbar button.
// 창을 찾지 못하면 에러를 돌려준다(호출자는 로그만 남기고 계속 — 부차 기능).
func hideFromTaskbar(title string) error {
	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return fmt.Errorf("hudpos: 창 제목 변환: %w", err)
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(titlePtr)))
	if hwnd == 0 {
		return fmt.Errorf("hudpos: 제목 %q 창을 찾지 못했습니다", title)
	}

	ex, _, _ := procGetWindowLongPtrW.Call(hwnd, uintptr(gwlExStyle))
	if ex&wsExToolWindow != 0 && ex&wsExAppWindow == 0 {
		return nil // 이미 적용됨 — 깜빡임을 만들지 않는다.
	}

	visible, _, _ := procIsWindowVisible.Call(hwnd)
	if visible != 0 {
		procShowWindow.Call(hwnd, swHide)
	}
	procSetWindowLongPtrW.Call(hwnd, uintptr(gwlExStyle),
		(ex|wsExToolWindow)&^uintptr(wsExAppWindow))
	if visible != 0 {
		// SW_SHOWNOACTIVATE: 포커스를 뺏지 않고 원래대로 되돌린다.
		procShowWindow.Call(hwnd, swShowNoActivate)
	}
	return nil
}
