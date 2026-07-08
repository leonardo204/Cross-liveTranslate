//go:build darwin && cgo

// loopback_darwin.go — macOS 시스템 오디오(루프백) 캡처.
//
// 1순위: macOS 14.4+ Core Audio Process Tap(무설치 시스템 오디오 직접 캡처 —
// 원본 SystemTapAudioSource.swift 이식, systemtap_darwin.{h,m,go}). BlackHole 등
// 가상 장치 설치·화면 녹화 권한 불필요(오디오 캡처 TCC 권한만 첫 tap 시).
// 2순위(14.4 미만): 설치된 가상 루프백 장치(BlackHole/Loopback/Soundflower 등)를
// 자동 선택. 원본도 시스템 탭 미가용 시 가상 장치 경로로 폴백한다.
package audio

// newLoopbackSource 는 Source 를 "생성"만 하고 아직 Start 하지 않는다. 탭 가용성은
// macOS 버전만으로 값싸게 판단하고(실제 tap 생성/권한은 Start 에서 표면화), 우선순위대로:
//  1. macOS 14.4+ → SystemTapSource(무설치 직접 캡처).
//  2. 그 미만 → 설치된 가상 루프백 장치(BlackHole 등) 자동 선택.
//  3. 둘 다 없으면 ErrLoopbackUnsupported.
func newLoopbackSource() (Source, error) {
	// 1) Core Audio Process Tap (macOS 14.4+). 실제 실패는 Start 에서 표면화되며
	//    reconciler 가 PermanentFailure 로 처리한다.
	if SystemTapAvailable() {
		return NewSystemTapSource(), nil
	}
	// 2) 폴백: 설치된 가상 루프백 장치(BlackHole 등).
	if devs, err := EnumerateDevices(); err == nil {
		for i := range devs {
			if devs[i].IsLoopbackCandidate {
				return NewMalgoSourceForDevice(devs[i].ID), nil
			}
		}
	}
	// 3) 미지원 — 상위에서 사용자에게 표면화되도록 명시적 오류.
	return nil, ErrLoopbackUnsupported
}

// platformAutoFallback: auto에서 루프백 후보(BlackHole 등)가 없고 mac 탭이 아직
// 미구현이므로 기본 마이크로 폴백한다(원본 auto 규칙: 14.4 미만/탭 미가용 시 마이크).
// 상위(호출부/headless)가 로그로 폴백을 알린다.
func platformAutoFallback() (Source, error) {
	return NewMalgoSource(), nil
}
