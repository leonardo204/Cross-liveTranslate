//go:build !windows && !darwin && cgo

// loopback_other.go — 그 외 플랫폼(linux 등) 루프백 stub.
//
// 시스템 출력 캡처 백엔드는 windows(WASAPI)/mac(P2b 탭)만 대상으로 한다. 그 외
// 플랫폼에서는 loopback을 미지원으로 두고, auto 폴백은 기본 마이크를 쓴다.
package audio

// newLoopbackSource: 미지원 플랫폼 → 명시적 오류.
func newLoopbackSource() (Source, error) {
	return nil, ErrLoopbackUnsupported
}

// platformAutoFallback: 루프백 후보가 없으면 기본 마이크로 폴백.
func platformAutoFallback() (Source, error) {
	return NewMalgoSource(), nil
}
