//go:build darwin && cgo

// loopback_darwin.go — macOS 시스템 캡처 stub.
//
// macOS의 무설치 시스템 오디오 캡처(Core Audio Process Tap)는 P2b에서 구현한다
// (원본 SystemTapAudioSource.swift 이식 — cgo/ObjC 브리지). 지금은 빌드/빌드태그
// 정합만 확보하고, loopback 요청 시 명시적 오류를 반환한다.
package audio

// newLoopbackSource: macOS 시스템 캡처(Process Tap)는 P2b 예정 → 미지원 오류.
func newLoopbackSource() (Source, error) {
	return nil, ErrLoopbackUnsupported
}

// platformAutoFallback: auto에서 루프백 후보(BlackHole 등)가 없고 mac 탭이 아직
// 미구현이므로 기본 마이크로 폴백한다(원본 auto 규칙: 14.4 미만/탭 미가용 시 마이크).
// 상위(호출부/headless)가 로그로 폴백을 알린다.
func platformAutoFallback() (Source, error) {
	return NewMalgoSource(), nil
}
