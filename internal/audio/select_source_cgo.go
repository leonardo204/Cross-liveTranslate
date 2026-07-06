//go:build cgo

// select_source_cgo.go — Selection → Source 결정 (cgo: 장치 열거/캡처 필요).
// 플랫폼 분기(auto 폴백 / loopback 백엔드)는 빌드태그 파일(loopback_*.go)로 분리한다.
package audio

// SelectSource resolves a Selection into a concrete capture Source.
//
//   - SelectMic:      기본 마이크(malgo 기본 캡처 장치).
//   - SelectDevice:   DeviceID로 지정한 캡처 장치(마이크/BlackHole 등 가상 입력).
//   - SelectLoopback: 시스템 출력 루프백(win=WASAPI loopback; mac은 P2b, 그 외 미지원).
//   - SelectAuto:     BlackHole/루프백 후보 감지 시 그 장치, 없으면 플랫폼 폴백
//     (win=시스템 루프백 / mac=마이크 폴백[P2b 탭 미구현] / 그 외=마이크).
//
// 원본 이식: AudioInputManager.swift `effectiveSelection`(auto 규칙).
func SelectSource(sel Selection) (Source, error) {
	switch sel.Mode {
	case SelectMic:
		return NewMalgoSource(), nil
	case SelectDevice:
		if sel.DeviceID == "" {
			return nil, ErrNoDeviceID
		}
		return NewMalgoSourceForDevice(sel.DeviceID), nil
	case SelectLoopback:
		return newLoopbackSource()
	case SelectAuto:
		return selectAuto()
	default:
		return NewMalgoSource(), nil
	}
}

// selectAuto implements the 자동 선택 규칙: 먼저 루프백 후보(BlackHole 등)를 찾아
// 그 장치를 캡처하고, 없으면 플랫폼별 폴백(platformAutoFallback)으로 수렴한다.
// 장치 열거가 실패해도 폴백으로 graceful degrade 한다.
func selectAuto() (Source, error) {
	if devs, err := EnumerateDevices(); err == nil {
		for i := range devs {
			if devs[i].IsLoopbackCandidate {
				return NewMalgoSourceForDevice(devs[i].ID), nil
			}
		}
	}
	return platformAutoFallback()
}
