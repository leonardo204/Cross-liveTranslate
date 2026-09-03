//go:build windows

// tray_windows.go — Windows 시스템 트레이(Shell_NotifyIcon) 구현.
//
// # 왜 순수 Go(x/sys/windows syscall)인가
//
// 이전 구현은 no-op stub이었다. 주석에 남아 있던 이유는 "energye/systray 등 서드파티가
// 자체 이벤트 런루프를 요구해 Wails의 메시지 펌프와 충돌 위험"이었는데, 그 전제는
// **서드파티 라이브러리를 쓸 때만** 성립한다. Win32 메시지는 창을 만든 스레드의 큐로만
// 디스패치되므로, 트레이 전용 스레드에서 전용 숨김 창을 만들고 그 스레드에서만 메시지
// 루프를 돌리면 Wails 메인 루프와 완전히 격리된다(상호 간섭 0).
//
// 그래서 여기서는:
//
//	runtime.LockOSThread() 전용 goroutine
//	  └ RegisterClassExW + CreateWindowExW (보이지 않는 소유 창 — ShowWindow 호출 안 함)
//	      └ Shell_NotifyIconW(NIM_ADD)  콜백 메시지 = wmTrayCallback
//	          └ GetMessageW/DispatchMessageW 루프 (이 스레드 전용)
//
// cgo를 쓰지 않으므로 macOS→Windows 크로스빌드(scripts/build-win.sh)가 그대로 동작한다.
//
// # 메뉴 구성 (tray.go Handlers 문서 = 원본 liveTranslate MenuBarContent와 동일)
//
//	번역 시작 ↔ 번역 정지    (isRunning에 따라 라벨 동적)
//	────────
//	✓ 제어 HUD 표시          (표시 상태 체크 표식)
//	설정…
//	────────
//	종료
//
// 트레이 아이콘 좌클릭도 "제어 HUD 표시" 토글이다(가장 흔한 기대 동작).
//
// # 스레드 규율
//
//   - SetStatus/SetRunning/SetHUDVisible 은 아무 goroutine에서나 호출된다. 상태만 mutex로
//     갱신하고 PostMessageW(wmTrayRefresh)로 트레이 스레드를 깨워 실제 Shell_NotifyIcon
//     호출은 트레이 스레드에서만 하도록 강제한다.
//   - 메뉴 콜백(Handlers)은 goroutine으로 던진다. 메시지 루프를 붙잡으면 트레이가 굳는다
//     (darwin 구현이 AppKit 메인 스레드에서 겪은 문제와 동일한 성질).
package tray

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	moduser32   = windows.NewLazySystemDLL("user32.dll")
	modshell32  = windows.NewLazySystemDLL("shell32.dll")
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW      = moduser32.NewProc("RegisterClassExW")
	procCreateWindowExW       = moduser32.NewProc("CreateWindowExW")
	procDefWindowProcW        = moduser32.NewProc("DefWindowProcW")
	procDestroyWindow         = moduser32.NewProc("DestroyWindow")
	procGetMessageW           = moduser32.NewProc("GetMessageW")
	procTranslateMessage      = moduser32.NewProc("TranslateMessage")
	procDispatchMessageW      = moduser32.NewProc("DispatchMessageW")
	procPostMessageW          = moduser32.NewProc("PostMessageW")
	procPostQuitMessage       = moduser32.NewProc("PostQuitMessage")
	procCreatePopupMenu       = moduser32.NewProc("CreatePopupMenu")
	procAppendMenuW           = moduser32.NewProc("AppendMenuW")
	procTrackPopupMenu        = moduser32.NewProc("TrackPopupMenu")
	procDestroyMenu           = moduser32.NewProc("DestroyMenu")
	procGetCursorPos          = moduser32.NewProc("GetCursorPos")
	procSetForegroundWindow   = moduser32.NewProc("SetForegroundWindow")
	procLoadIconW             = moduser32.NewProc("LoadIconW")
	procRegisterWindowMessage = moduser32.NewProc("RegisterWindowMessageW")

	procShellNotifyIconW = modshell32.NewProc("Shell_NotifyIconW")
	procExtractIconW     = modshell32.NewProc("ExtractIconW")

	procGetModuleHandleW = modkernel32.NewProc("GetModuleHandleW")
)

// Win32 상수 (windows.h / shellapi.h / winuser.h).
const (
	wmDestroy       = 0x0002
	wmNull          = 0x0000
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmContextMenu   = 0x007B

	// WM_APP 이후는 애플리케이션 전용 메시지 영역이다.
	wmTrayCallback = 0x0400 + 1 // 트레이 아이콘 → 우리 창
	wmTrayRefresh  = 0x0400 + 2 // 다른 goroutine → 트레이 스레드: 툴팁 갱신
	wmTrayQuit     = 0x0400 + 3 // 다른 goroutine → 트레이 스레드: 아이콘 제거 후 종료
	wmTrayNotify   = 0x0400 + 4 // 다른 goroutine → 트레이 스레드: 풍선 알림 표시

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	// nifInfo(NIF_INFO) 는 NIM_MODIFY 시 풍선 알림(balloon)을 띄운다. Windows 10/11에서는
	// 시스템 토스트로 표시되고 몇 초 뒤 셸이 알아서 거둔다(수명은 셸이 정한다).
	nifInfo = 0x00000010
	// niifInfo(NIIF_INFO) — 풍선에 정보 아이콘을 붙인다.
	niifInfo = 0x00000001

	mfString    = 0x00000000
	mfSeparator = 0x00000800
	mfChecked   = 0x00000008

	tpmLeftAlign   = 0x0000
	tpmRightButton = 0x0002
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	idiApplication = 32512

	// 메뉴 커맨드 ID. TrackPopupMenu(TPM_RETURNCMD)가 이 값을 그대로 돌려준다.
	idToggleTranslate = 1001
	idToggleHUD       = 1002
	idSettings        = 1003
	idQuit            = 1004

	trayIconUID = 1
)

// point mirrors Win32 POINT.
type point struct {
	X int32
	Y int32
}

// msgStruct mirrors Win32 MSG (x64에서 48바이트).
type msgStruct struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

// wndClassEx mirrors Win32 WNDCLASSEXW (x64에서 80바이트).
type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

// notifyIconData mirrors Win32 NOTIFYICONDATAW (Vista+ 전체 크기 = x64에서 976바이트).
// cbSize는 unsafe.Sizeof로 채우므로 필드 정렬만 C와 맞으면 된다.
type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     windows.Handle
}

// winTray 는 트레이의 프로세스 전역 상태다(프로세스당 트레이 1개 — tray.go 계약).
// 상태는 아무 goroutine에서나 갱신되고, 네이티브 호출은 트레이 스레드만 한다.
var winTray struct {
	mu      sync.Mutex
	hwnd    uintptr
	hicon   windows.Handle
	title   string
	status  string
	running bool
	hud     bool
	started bool

	// 대기 중인 풍선 알림 텍스트. Notify가 채우고 트레이 스레드가 소비한다.
	balloonTitle string
	balloonText  string

	// done 은 트레이 스레드가 완전히 끝났을 때 닫힌다(트레이 스레드의 defer). Shutdown이
	// 아이콘 제거가 실제로 끝날 때까지 기다리는 데 쓴다 — PostMessage만 하고 바로 돌아오면
	// 호출 직후 프로세스가 죽어 알림 영역에 유령 아이콘이 남는다.
	done chan struct{}
}

// taskbarCreatedMsg 는 탐색기(Explorer)가 재시작될 때 브로드캐스트되는 등록 메시지다.
// 이걸 받으면 트레이 아이콘을 다시 추가해야 한다(안 하면 탐색기 재시작 후 아이콘 소실).
var taskbarCreatedMsg uint32

// runHandler 는 콜백을 goroutine으로 던진다. 메뉴 액션이 메시지 루프를 붙잡으면 트레이가
// 굳으므로(핸들러가 오디오 초기화 등 무거운 작업을 한다) 반드시 비동기로 실행한다.
func runHandler(fn func()) {
	if fn != nil {
		go fn()
	}
}

// Init installs the Windows notification-area (system tray) icon with the given
// handlers. 전용 OS 스레드에서 숨김 창 + 메시지 루프를 돌린다. 아이콘 설치가 끝나면
// 반환하므로, 반환 시점에는 트레이가 이미 보인다. 두 번째 호출은 no-op이다.
func Init(h Handlers) error {
	winTray.mu.Lock()
	if winTray.started {
		winTray.mu.Unlock()
		return nil
	}
	// handlers 는 트레이 스레드가 읽는다 — 스레드를 띄우기 전에 락 안에서 채운다.
	handlers = h
	winTray.started = true
	winTray.title = "Cross-liveTranslate"
	winTray.status = "idle"
	done := make(chan struct{})
	winTray.done = done
	winTray.mu.Unlock()

	ready := make(chan error, 1)
	go trayThread(ready, done)
	err := <-ready
	if err != nil {
		// 설치에 실패했으면 started 를 원복한다. 그대로 두면 다음 Init 호출이 위 가드에 걸려
		// **성공(nil)** 을 돌려주고, 호출자는 트레이가 있다고 믿고 제어 HUD를 숨긴다.
		// HUD는 frameless라 창 UI가 없어서 그 순간 앱을 다시 부를 방법이 사라진다.
		winTray.mu.Lock()
		winTray.started = false
		winTray.mu.Unlock()
	}
	return err
}

// Shutdown removes the tray icon and stops the tray thread. Idempotent / safe to
// call when Init failed(창 핸들이 없으면 no-op).
func Shutdown() {
	winTray.mu.Lock()
	hwnd := winTray.hwnd
	done := winTray.done
	winTray.mu.Unlock()
	if hwnd == 0 {
		return
	}
	procPostMessageW.Call(hwnd, wmTrayQuit, 0, 0)
	if done == nil {
		return
	}
	// 아이콘을 실제로 지우는 Shell_NotifyIcon(NIM_DELETE)은 트레이 스레드가 처리한다.
	// 여기서 기다리지 않으면 호출 직후 프로세스가 종료돼 그 호출이 실행되지 못하고,
	// 알림 영역에 죽은 프로세스의 아이콘이 남는다(마우스를 올려야 사라진다).
	// 타임아웃은 안전장치다 — 종료 경로가 트레이 스레드 때문에 막히지 않게 한다.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// SetStatus updates the tray tooltip text ("Cross-liveTranslate — <status>").
func SetStatus(text string) {
	winTray.mu.Lock()
	winTray.status = text
	winTray.mu.Unlock()
	refreshTray()
}

// SetRunning toggles the 번역 시작/정지 menu item label.
// 메뉴는 우클릭 시점에 새로 만들어지므로 상태만 기록하면 된다.
func SetRunning(running bool) {
	winTray.mu.Lock()
	winTray.running = running
	winTray.mu.Unlock()
}

// SetHUDVisible toggles the 제어 HUD 표시 menu item check mark.
func SetHUDVisible(visible bool) {
	winTray.mu.Lock()
	winTray.hud = visible
	winTray.mu.Unlock()
}

// Notify shows a balloon notification (Windows 10/11에서는 시스템 토스트) on the tray
// icon. 몇 초 뒤 셸이 알아서 거두어간다 — 우리가 지울 필요가 없다. 트레이가 아직
// 없으면 조용히 버린다(알림은 부차 기능 — 실패해도 흐름을 막지 않는다).
func Notify(title, text string) {
	winTray.mu.Lock()
	winTray.balloonTitle = title
	winTray.balloonText = text
	hwnd := winTray.hwnd
	winTray.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, wmTrayNotify, 0, 0)
	}
}

// refreshTray asks the tray thread to push the current tooltip to the shell.
func refreshTray() {
	winTray.mu.Lock()
	hwnd := winTray.hwnd
	winTray.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, wmTrayRefresh, 0, 0)
	}
}

// trayThread owns the tray window for the whole process lifetime.
// LockOSThread 필수: Win32 메시지는 **창을 만든 스레드**로만 디스패치되므로, goroutine이
// 다른 OS 스레드로 옮겨가면 메시지를 영영 못 받는다.
func trayThread(ready chan<- error, done chan<- struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// 어떤 경로로 끝나든(설치 실패·메시지 루프 종료) Shutdown의 대기를 반드시 풀어준다.
	defer close(done)

	hinst, _, _ := procGetModuleHandleW.Call(0)

	classNamePtr, err := windows.UTF16PtrFromString("CrossLiveTranslateTrayWnd")
	if err != nil {
		ready <- fmt.Errorf("트레이 클래스명 변환 실패: %w", err)
		return
	}
	titlePtr, err := windows.UTF16PtrFromString("Cross-liveTranslate Tray")
	if err != nil {
		ready <- fmt.Errorf("트레이 창 제목 변환 실패: %w", err)
		return
	}

	wc := wndClassEx{
		LpfnWndProc:   syscall.NewCallback(trayWndProc),
		HInstance:     windows.Handle(hinst),
		LpszClassName: classNamePtr,
	}
	wc.CbSize = uint32(unsafe.Sizeof(wc))

	atom, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		ready <- fmt.Errorf("트레이 창 클래스 등록 실패: %v", callErr)
		return
	}

	// 보이지 않는 소유 창. HWND_MESSAGE(메시지 전용 창)를 쓰지 않는 이유: 메시지 전용
	// 창은 SetForegroundWindow 대상이 될 수 없어, 팝업 메뉴가 "바깥 클릭으로 닫히지 않는"
	// 고전 버그를 유발한다(MSDN TrackPopupMenu 주의사항). 일반 창을 만들되 ShowWindow를
	// 절대 호출하지 않아 화면에 나타나지 않게 한다.
	hwnd, _, callErr := procCreateWindowExW.Call(
		0,                                 // dwExStyle
		atom,                              // lpClassName (등록 atom)
		uintptr(unsafe.Pointer(titlePtr)), // lpWindowName
		0,                                 // dwStyle — WS_VISIBLE 없음
		0, 0, 0, 0,                        // x, y, w, h
		0,     // hWndParent
		0,     // hMenu
		hinst, // hInstance
		0,     // lpParam
	)
	if hwnd == 0 {
		ready <- fmt.Errorf("트레이 창 생성 실패: %v", callErr)
		return
	}

	hicon := loadTrayIcon(windows.Handle(hinst))

	winTray.mu.Lock()
	winTray.hwnd = hwnd
	winTray.hicon = hicon
	winTray.mu.Unlock()

	// 탐색기 재시작 감지용 등록 메시지(값은 세션마다 다르므로 런타임에 조회).
	if m, _, _ := procRegisterWindowMessage.Call(
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("TaskbarCreated")))); m != 0 {
		taskbarCreatedMsg = uint32(m)
	}

	if err := addTrayIcon(hwnd); err != nil {
		procDestroyWindow.Call(hwnd)
		winTray.mu.Lock()
		winTray.hwnd = 0
		winTray.mu.Unlock()
		ready <- err
		return
	}
	ready <- nil

	var m msgStruct
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		// GetMessage: 0 = WM_QUIT, -1 = 오류. 둘 다 루프 종료.
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	winTray.mu.Lock()
	winTray.hwnd = 0
	winTray.mu.Unlock()
}

// trayWndProc handles the tray callback + our own cross-thread control messages.
func trayWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch uint32(msg) {
	case wmTrayCallback:
		// NOTIFYICON_VERSION을 설정하지 않았으므로 고전 규약이다:
		// wParam = 아이콘 UID, lParam = 마우스 메시지.
		switch uint32(lparam) {
		case wmLButtonUp, wmLButtonDblClk:
			runHandler(handlers.OnToggleHUD)
		case wmRButtonUp, wmContextMenu:
			showTrayMenu(hwnd)
		}
		return 0

	case wmTrayRefresh:
		modifyTrayIcon(hwnd)
		return 0

	case wmTrayNotify:
		showBalloon(hwnd)
		return 0

	case wmTrayQuit:
		deleteTrayIcon(hwnd)
		procDestroyWindow.Call(hwnd)
		return 0

	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}

	// 탐색기 재시작 → 아이콘 재설치(아니면 트레이에서 영영 사라진다).
	if taskbarCreatedMsg != 0 && uint32(msg) == taskbarCreatedMsg {
		_ = addTrayIcon(hwnd)
		return 0
	}

	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

// showTrayMenu builds and tracks the context menu on the tray thread.
// TPM_RETURNCMD를 쓰므로 WM_COMMAND 배선 없이 선택된 ID가 곧바로 반환된다.
func showTrayMenu(hwnd uintptr) {
	winTray.mu.Lock()
	running, hud := winTray.running, winTray.hud
	winTray.mu.Unlock()

	hmenu, _, _ := procCreatePopupMenu.Call()
	if hmenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hmenu)

	translateLabel := "번역 시작"
	if running {
		translateLabel = "번역 정지"
	}
	appendMenuItem(hmenu, mfString, idToggleTranslate, translateLabel)
	appendMenuItem(hmenu, mfSeparator, 0, "")

	hudFlags := uintptr(mfString)
	if hud {
		hudFlags |= mfChecked
	}
	appendMenuItem(hmenu, hudFlags, idToggleHUD, "제어 HUD 표시")
	appendMenuItem(hmenu, mfString, idSettings, "설정…")
	appendMenuItem(hmenu, mfSeparator, 0, "")
	appendMenuItem(hmenu, mfString, idQuit, "종료")

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	// MSDN: 트레이 메뉴가 "바깥을 클릭해도 닫히지 않는" 문제를 막으려면 소유 창을
	// 포그라운드로 올린 뒤 TrackPopupMenu를 호출하고, 끝난 뒤 더미 메시지를 보내야 한다.
	procSetForegroundWindow.Call(hwnd)

	// 음수 좌표(보조 모니터가 주 모니터 왼쪽/위에 있는 배치)를 잃지 않도록 하위 32비트를
	// 그대로 넘긴다. uintptr(int32)로 부호확장하면 64비트 쓰레기 값이 된다.
	cmd, _, _ := procTrackPopupMenu.Call(
		hmenu,
		uintptr(tpmLeftAlign|tpmRightButton|tpmReturnCmd|tpmNoNotify),
		uintptr(uint32(pt.X)),
		uintptr(uint32(pt.Y)),
		0,
		hwnd,
		0,
	)
	procPostMessageW.Call(hwnd, wmNull, 0, 0)

	switch cmd {
	case idToggleTranslate:
		runHandler(handlers.OnToggleTranslate)
	case idToggleHUD:
		runHandler(handlers.OnToggleHUD)
	case idSettings:
		runHandler(handlers.OnSettings)
	case idQuit:
		runHandler(handlers.OnQuit)
	}
}

// appendMenuItem is a thin AppendMenuW wrapper (빈 텍스트는 구분선용).
func appendMenuItem(hmenu, flags uintptr, id uint32, text string) {
	if text == "" { // 구분선(MF_SEPARATOR) — 문자열이 없다.
		procAppendMenuW.Call(hmenu, flags, uintptr(id), 0)
		return
	}
	ptr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return // NUL이 섞인 라벨은 없다 — 여기 오면 그 항목만 빠진다.
	}
	// unsafe.Pointer → uintptr 변환은 **호출식 안에서** 해야 한다. 결과를 변수에 담아 두면
	// UTF-16 버퍼를 가리키는 유일한 참조가 uintptr뿐이라 호출 전에 GC가 회수할 수 있다.
	procAppendMenuW.Call(hmenu, flags, uintptr(id), uintptr(unsafe.Pointer(ptr)))
}

// newNotifyIconData builds a NOTIFYICONDATAW seeded with the current tooltip.
func newNotifyIconData(hwnd uintptr) notifyIconData {
	winTray.mu.Lock()
	title, status, hicon := winTray.title, winTray.status, winTray.hicon
	winTray.mu.Unlock()

	tip := title
	if status != "" {
		tip = title + " — " + status
	}

	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = trayIconUID
	nid.UFlags = nifMessage | nifIcon | nifTip
	nid.UCallbackMessage = wmTrayCallback
	nid.HIcon = hicon
	copyTip(&nid.SzTip, tip)
	return nid
}

// copyUTF16 fills a fixed-size UTF-16 buffer, truncating with a NUL terminator.
// 잘릴 때 상위 서로게이트만 남지 않도록 짝이 깨지면 한 칸 더 물러난다(깨진 문자 방지).
func copyUTF16(dst []uint16, s string) {
	u := windows.StringToUTF16(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
		if n := len(u) - 1; n > 0 && u[n-1] >= 0xD800 && u[n-1] <= 0xDBFF {
			u[n-1] = 0 // 상위 서로게이트 홀로 남음 — 함께 잘라낸다.
		}
	}
	copy(dst, u)
}

// copyTip writes a UTF-16 tooltip into the fixed-size NOTIFYICONDATA buffer.
func copyTip(dst *[128]uint16, s string) { copyUTF16(dst[:], s) }

func addTrayIcon(hwnd uintptr) error {
	nid := newNotifyIconData(hwnd)
	r, _, callErr := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	if r == 0 {
		return fmt.Errorf("트레이 아이콘 등록 실패: %v", callErr)
	}
	return nil
}

func modifyTrayIcon(hwnd uintptr) {
	nid := newNotifyIconData(hwnd)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

// showBalloon pushes the pending balloon text to the shell (NIM_MODIFY + NIF_INFO).
// 툴팁/아이콘 플래그는 넣지 않는다 — 풍선만 띄우고 기존 아이콘 상태는 건드리지 않는다.
func showBalloon(hwnd uintptr) {
	winTray.mu.Lock()
	title, text := winTray.balloonTitle, winTray.balloonText
	winTray.mu.Unlock()
	if text == "" {
		return
	}

	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = trayIconUID
	nid.UFlags = nifInfo
	nid.DwInfoFlags = niifInfo
	copyUTF16(nid.SzInfoTitle[:], title)
	copyUTF16(nid.SzInfo[:], text)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

func deleteTrayIcon(hwnd uintptr) {
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = trayIconUID
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

// loadTrayIcon takes the app's own icon from the running .exe (Wails가
// build/windows/icon.ico를 실행 파일 리소스로 넣는다). 실패하면 시스템 기본 아이콘으로
// 폴백한다 — 아이콘이 없다고 트레이 자체를 포기하지는 않는다.
func loadTrayIcon(hinst windows.Handle) windows.Handle {
	if exe, err := os.Executable(); err == nil {
		if p, err := windows.UTF16PtrFromString(exe); err == nil {
			// ExtractIconW: 0 = 오류, 1 = 아이콘 없음. 둘 다 폴백 대상.
			if h, _, _ := procExtractIconW.Call(
				uintptr(hinst), uintptr(unsafe.Pointer(p)), 0); h > 1 {
				return windows.Handle(h)
			}
		}
	}
	h, _, _ := procLoadIconW.Call(0, idiApplication)
	return windows.Handle(h)
}
