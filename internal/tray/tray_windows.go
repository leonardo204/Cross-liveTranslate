//go:build windows

package tray

// Windows 트레이는 P3b에서 최소 stub이다. energye/systray는 자체 이벤트 런루프를
// 요구해 Wails의 메시지 펌프와 충돌 위험이 있어, Windows 실측 시점에 별도로 배선한다.
// 지금은 core 통합(controller↔overlay IPC)을 깨지 않도록 no-op으로 둔다.
//
// TODO(win): NOTIFYICONDATA(shell_NotifyIcon) 또는 systray를 Wails 메시지 루프와
// 통합해 Start/Stop/Show HUD/Quit 메뉴를 노출.

// Init records the handlers; no visible tray yet on Windows.
func Init(h Handlers) error {
	handlers = h
	return nil
}

// SetStatus is a no-op stub on Windows for now.
func SetStatus(string) {}

// SetRunning is a no-op stub on Windows for now.
func SetRunning(bool) {}

// SetHUDVisible is a no-op stub on Windows for now.
func SetHUDVisible(bool) {}
