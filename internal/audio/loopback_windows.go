//go:build windows && cgo

// loopback_windows.go — Windows 시스템 출력 캡처(WASAPI loopback).
//
// malgo.Loopback 디바이스 타입은 miniaudio의 WASAPI loopback 백엔드를 사용해
// 시스템 기본 출력의 재생 스트림을 캡처한다. MalgoSource가 deviceType=Loopback로
// 초기화되면 나머지 파이프라인(16kHz/mono/1600 청크, 논블로킹 dispatch, 멱등 Stop)은
// 마이크 캡처와 동일 계약으로 동작한다.
//
// 실행 검증은 Windows 환경이 필요하다(win 미보유 시 코드/빌드태그 정합만 확인). 원본
// 근거: liveTranslate SystemTapAudioSource.swift(mac 탭)과 대응되는 win 경로.
package audio

import "github.com/gen2brain/malgo"

// newLoopbackSource returns a Source capturing the system output via WASAPI loopback.
func newLoopbackSource() (Source, error) {
	return &MalgoSource{deviceType: malgo.Loopback}, nil
}

// platformAutoFallback: auto 선택에서 루프백 후보 장치가 없을 때, Windows는
// 시스템 출력 루프백으로 수렴한다(원본 auto 규칙의 win 분기).
func platformAutoFallback() (Source, error) {
	return newLoopbackSource()
}
