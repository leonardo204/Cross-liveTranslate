//go:build windows

package permission

// Windows has no equivalent per-app microphone TCC gate that we query here (WASAPI
// capture는 시스템 마이크 개인정보 설정을 따르지만 사전 조회 API를 이 계층에서 쓰지 않는다).
// 마이크 상태는 항상 unknown, 요청/딥링크는 no-op으로 둔다(순수 크로스빌드 유지).

// MicrophoneStatus always reports unknown on Windows.
func MicrophoneStatus() MicStatus { return MicUnknown }

// RequestMicrophone is a no-op on Windows.
func RequestMicrophone() {}

// OpenPrivacyPane is a no-op on Windows (설정 딥링크는 settings.go의 openURL 폴백이 담당).
func OpenPrivacyPane(pane string) { _ = pane }
