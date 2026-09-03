//go:build !windows

package hudpos

// hideFromTaskbar: Windows 외에는 no-op. macOS는 번들 Info.plist의 LSUIElement +
// internal/activation(accessory 정책)이 이미 Dock 아이콘을 없앤다.
func hideFromTaskbar(title string) error { return nil }
