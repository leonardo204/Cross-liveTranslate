//go:build !darwin && !windows

package display

// ListScreens is a no-op stub on platforms without a native screen-enumeration
// shim (only darwin and windows are implemented). Returns an empty list so the
// settings picker degrades to the "자동 (주 화면)" option only.
func ListScreens() ([]ScreenInfo, error) {
	return []ScreenInfo{}, nil
}
