//go:build !darwin && !windows

package permission

// Non-darwin/non-windows platforms have no native microphone-permission shim,
// so status is always unknown and request/deep-link are no-ops (순수 크로스빌드 유지).

// MicrophoneStatus always reports unknown on unsupported platforms.
func MicrophoneStatus() MicStatus { return MicUnknown }

// RequestMicrophone is a no-op on unsupported platforms.
func RequestMicrophone() {}

// OpenPrivacyPane is a no-op on unsupported platforms.
func OpenPrivacyPane(pane string) { _ = pane }
