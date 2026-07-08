// Package permission exposes OS media-permission status/request/deep-link
// helpers, ported from 원본 liveTranslate/Sources/App/PermissionHelper.swift.
//
// 배경(왜 이 패키지가 필요한가):
//   원본 macOS 앱은 AVAudioEngine으로 마이크를 캡처하며, 첫 캡처 시 AVFoundation이
//   TCC 마이크 권한 다이얼로그를 자동으로 띄운다. 반면 이 크로스플랫폼 이식은 malgo
//   (miniaudio)로 캡처하는데, miniaudio는 macOS TCC 마이크 권한을 명시적으로 요청하지
//   않는다. 그 결과 권한 없이 캡처가 시작되고 → 무음만 흘러 → Gemini가 오디오를 받지
//   못해 "연결 중…"에서 영원히 멈춘다(번역 안 됨). 이를 근본 해결하려면 우리가 직접
//   AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio 를 호출해 다이얼로그를
//   띄워야 한다. 이 패키지가 그 명시적 요청과 상태 조회/딥링크를 제공한다.
//
// 이 파일(permission.go)은 순수 Go(타입/상수만) — cgo 없음, 빌드태그 없음 → windows
// 크로스빌드에 그대로 들어간다. 실제 조회/요청/딥링크는 플랫폼 파일이 구현한다:
//   - permission_darwin.go (+ .h/.m) : AVFoundation/AppKit cgo 실구현.
//   - permission_windows.go          : stub(마이크는 항상 unknown, 나머지 no-op).
//   - permission_other.go            : stub(그 외 OS).
package permission

// MicStatus is the microphone-permission state (원본 PermissionHelper.Status의
// 마이크 관련 케이스 이식). 프론트가 라벨을 매핑한다(미요청/허용됨/거부됨/제한됨/확인불가).
type MicStatus string

const (
	// MicNotDetermined: 아직 묻지 않음(최초 실행). 요청하면 다이얼로그가 뜬다.
	MicNotDetermined MicStatus = "notDetermined"
	// MicAuthorized: 허용됨 — 캡처 가능.
	MicAuthorized MicStatus = "authorized"
	// MicDenied: 거부됨 — 시스템 설정에서 직접 켜야 함(재요청해도 다이얼로그 안 뜸).
	MicDenied MicStatus = "denied"
	// MicRestricted: 시스템 정책으로 제한됨.
	MicRestricted MicStatus = "restricted"
	// MicUnknown: 조회 불가(비-darwin 플랫폼 등).
	MicUnknown MicStatus = "unknown"
)

// Privacy-pane identifiers accepted by OpenPrivacyPane (원본 deep link 대상).
const (
	// PaneMicrophone → x-apple.systempreferences ...?Privacy_Microphone.
	PaneMicrophone = "microphone"
	// PaneScreenCapture → ...?Privacy_ScreenCapture (원본 시스템 오디오 캡처 안내 대상).
	PaneScreenCapture = "screencapture"
	// PanePrivacy → 개인정보 보호 및 보안 루트(폴백).
	PanePrivacy = "privacy"
)

// NeedsAction reports whether the user must take manual action (설정에서 켜기)
// for the given microphone status — 원본 Status.needsAction (denied || restricted).
func (s MicStatus) NeedsAction() bool {
	return s == MicDenied || s == MicRestricted
}
