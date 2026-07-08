//go:build !darwin || !cgo

package audio

// systemtap_other.go — non-darwin(또는 cgo 비활성) 플랫폼용 시스템 탭 스텁.
// Core Audio Process Tap은 macOS 14.4+ 전용이므로 그 외에서는 미지원으로 보고한다.
// (windows는 loopback_windows.go의 WASAPI loopback 경로를 사용한다.)

// SystemTapAvailable: 비-darwin은 항상 false(탭 미지원).
func SystemTapAvailable() bool { return false }

// SystemTapStatus: 비-darwin은 시스템 탭이 없으므로 미지원("restricted")으로 보고한다.
func SystemTapStatus() string { return "restricted" }
