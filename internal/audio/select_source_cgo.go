//go:build cgo

// select_source_cgo.go — Selection → Source 결정 (cgo: 장치 열거/캡처 필요).
// 플랫폼 분기(auto 폴백 / loopback 백엔드)는 빌드태그 파일(loopback_*.go)로 분리한다.
package audio

// SelectSource resolves a Selection into a concrete capture Source.
//
//   - SelectMic:      기본 마이크(malgo 기본 캡처 장치).
//   - SelectDevice:   DeviceID로 지정한 캡처 장치(마이크/BlackHole 등 가상 입력).
//   - SelectLoopback: 시스템 출력 루프백(win=WASAPI loopback; mac은 P2b, 그 외 미지원).
//   - SelectAuto:     시스템 출력 직접 캡처를 먼저 시도하고, 불가하면 마이크로 내려간다.
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

// selectAuto implements the 자동 선택 규칙.
//
// 이 앱을 쓰는 이유는 대부분 "컴퓨터에서 나는 소리"를 번역하는 것이므로, 자동은 시스템
// 출력 직접 캡처(newLoopbackSource)를 **먼저** 시도한다. macOS 는 Core Audio Process Tap
// (14.4+), Windows 는 WASAPI 루프백이고, 둘 다 가상 오디오 장치 설치가 필요 없다.
// 그 경로가 없는 환경(구형 macOS + 가상 장치 미설치, 그 외 OS)에서만 기본 마이크로 내려간다.
//
// 예전에는 BlackHole 같은 가상 장치가 설치돼 있을 때만 시스템 소리를 잡고 그 외에는 곧장
// 마이크로 갔다. macOS 탭이 구현된 뒤에도 이 규칙이 남아 있어, 자동을 골라도 시스템 소리가
// 잡히지 않았다(newLoopbackSource 안에 가상 장치 폴백이 이미 들어 있다).
func selectAuto() (Source, error) {
	if src, err := newLoopbackSource(); err == nil {
		return src, nil
	}
	// 시스템 출력을 잡을 방법이 없다 — 기본 마이크로 내려간다(원본 auto 규칙의 마지막 단계).
	return NewMalgoSource(), nil
}
