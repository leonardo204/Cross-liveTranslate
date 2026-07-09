//go:build !windows

package overlay

// RunNativeWindows is a no-op on non-Windows platforms. The Windows native
// layered-window overlay (native_windows.go/.cpp/.h) replaces the WebView2
// overlay only on Windows; macOS keeps its WebView2 + Cocoa overlay. main.go
// guards the real call with runtime.GOOS == "windows", so this stub only exists
// to let the package link on other platforms.
func RunNativeWindows() {}
