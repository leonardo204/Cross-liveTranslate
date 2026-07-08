//go:build darwin

package permission

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AVFoundation -framework AppKit
#include "permission_darwin.h"
*/
import "C"

// MicrophoneStatus returns the current microphone-permission state via
// AVCaptureDevice (원본 PermissionHelper.microphoneStatus() 이식).
func MicrophoneStatus() MicStatus {
	switch int(C.lt_permission_mic_status()) {
	case 0:
		return MicNotDetermined
	case 1:
		return MicAuthorized
	case 2:
		return MicDenied
	case 3:
		return MicRestricted
	default:
		return MicUnknown
	}
}

// RequestMicrophone explicitly triggers the TCC microphone dialog (fire-and-forget).
// malgo(miniaudio)가 TCC를 스스로 요청하지 않기 때문에, 이 명시 호출이 첫 실행 시
// 권한 다이얼로그를 확실히 띄운다. 이미 결정된 상태면 시스템이 무시하므로 언제나 안전.
func RequestMicrophone() {
	C.lt_permission_request_mic()
}

// OpenPrivacyPane opens the macOS Privacy pane for the given category via
// NSWorkspace (원본 deep link 이식). pane: "microphone" | "screencapture" | 그 외(루트).
func OpenPrivacyPane(pane string) {
	var code C.int
	switch pane {
	case PaneMicrophone:
		code = 0
	case PaneScreenCapture:
		code = 1
	default:
		code = 2
	}
	C.lt_permission_open_pane(code)
}
