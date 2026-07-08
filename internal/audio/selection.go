package audio

import "errors"

// selection.go — 입력 소스 선택 모델 + 루프백 휴리스틱 (순수, cgo 없음).
//
// 이 파일은 malgo/cgo에 의존하지 않으므로 CGO 비활성 크로스빌드(GOOS=windows 등)에서도
// 컴파일된다. 실제 장치 열거/캡처(EnumerateDevices/SelectSource/loopback)는 cgo 파일에 있다.
// 원본 이식: liveTranslate AudioInputManager.swift(선택 규칙) + AudioDevice.swift(루프백 휴리스틱).

// SelectionMode is the requested input-source category.
type SelectionMode int

const (
	// SelectAuto picks the best source: BlackHole/loopback candidate if present,
	// else platform default (win=시스템 루프백, mac=마이크 폴백[P2b 탭 미구현], other=마이크).
	SelectAuto SelectionMode = iota
	// SelectMic forces the default microphone (malgo 기본 캡처 장치).
	SelectMic
	// SelectDevice forces a specific capture device by ID (DeviceInfo.ID).
	SelectDevice
	// SelectLoopback forces system-output loopback capture (win=WASAPI loopback).
	SelectLoopback
)

// String renders the SelectionMode for logging/CLI.
func (m SelectionMode) String() string {
	switch m {
	case SelectAuto:
		return "auto"
	case SelectMic:
		return "mic"
	case SelectDevice:
		return "device"
	case SelectLoopback:
		return "loopback"
	default:
		return "unknown"
	}
}

// Selection describes the desired capture source. DeviceID is only consulted
// when Mode == SelectDevice.
type Selection struct {
	Mode     SelectionMode
	DeviceID string
}

// DeviceInfo is a capture device discovered by EnumerateDevices.
// ID is a stable-per-run identifier (malgo device id hex) usable with
// SelectSource(Selection{Mode: SelectDevice, DeviceID: id}).
type DeviceInfo struct {
	ID                  string
	Name                string
	IsLoopbackCandidate bool
	// IsDefault marks the system default capture device.
	IsDefault bool
}

// ErrLoopbackUnsupported is returned by newLoopbackSource on platforms where a
// system-output loopback backend is not yet implemented (macOS 탭은 P2b, 그 외 미지원).
var ErrLoopbackUnsupported = errors.New("audio: loopback capture not supported on this platform")

// ErrNoDeviceID is returned when SelectDevice is requested without a DeviceID.
var ErrNoDeviceID = errors.New("audio: SelectDevice requires a non-empty DeviceID")

// ErrSystemTapPermission is returned when the macOS system-audio tap fails to
// start because the audio-capture permission is missing/denied. 상위(controller)가
// 이 에러를 인지해 HUD에 "시스템 오디오 권한 필요"를 명확히 표면화한다(무한 오류 대신).
var ErrSystemTapPermission = errors.New("audio: 시스템 오디오 캡처 권한이 필요합니다 — 설정에서 허용하세요")

// looksLikeLoopback estimates whether a device name denotes a virtual loopback
// input (BlackHole/Loopback/Soundflower, or an aggregate+virtual pairing).
// 원본 이식: AudioDevice.swift `isLikelyLoopback`.
func looksLikeLoopback(name string) bool {
	lowered := toLower(name)
	if contains(lowered, "blackhole") ||
		contains(lowered, "loopback") ||
		contains(lowered, "soundflower") {
		return true
	}
	return contains(lowered, "aggregate") && contains(lowered, "virtual")
}

// toLower is a dependency-free ASCII lowercaser (avoids pulling strings just for
// this pure file, and device names are effectively ASCII for the heuristic).
func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// contains reports whether sub occurs in s (substring search).
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
