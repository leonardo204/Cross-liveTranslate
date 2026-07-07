//go:build windows

package display

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// smCMonitors is GetSystemMetrics(SM_CMONITORS): number of display monitors on
// the desktop (excludes pseudo-monitors). Enumeration order aligns with
// overlay_windows.go's EnumDisplayMonitors, keeping display Index ==
// overlay monitorIndex == Position.MonitorIndex.
const smCMonitors = 80

var (
	dispUser32            = windows.NewLazySystemDLL("user32.dll")
	procGetSystemMetricsW = dispUser32.NewProc("GetSystemMetrics")
)

// ListScreens returns a best-effort monitor list on Windows.
//
// STUB (실측 대기): real per-monitor names via EnumDisplayDevices /
// GetMonitorInfo are not wired yet, so we emit generic "Display N" labels for
// the detected monitor count. Index 0 is treated as primary. This keeps the
// picker populated (and index-aligned with the overlay) until the native name
// enumeration lands. Pure syscall — no cgo, so GOOS=windows cross-builds stay
// clean.
func ListScreens() ([]ScreenInfo, error) {
	n, _, _ := procGetSystemMetricsW.Call(uintptr(smCMonitors))
	count := int(n)
	if count <= 0 {
		return []ScreenInfo{}, nil
	}
	screens := make([]ScreenInfo, 0, count)
	for i := 0; i < count; i++ {
		screens = append(screens, ScreenInfo{
			Index:   i,
			Name:    fmt.Sprintf("Display %d", i+1),
			Primary: i == 0,
		})
	}
	return screens, nil
}
