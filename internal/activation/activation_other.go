//go:build !darwin

package activation

// Set 은 darwin 외 플랫폼에서 no-op이다. Dock/활성화 정책은 macOS 개념이며,
// Windows의 작업표시줄 노출은 창 스타일(WS_EX_TOOLWINDOW)과 표시 여부로 결정된다
// (오버레이 네이티브 창은 이미 WS_EX_TOOLWINDOW라 작업표시줄에 뜨지 않는다).
func Set(Policy) {}

// WatchHide 는 darwin 외 플랫폼에서 no-op이다.
func WatchHide() {}
