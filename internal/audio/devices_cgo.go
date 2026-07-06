//go:build cgo

// devices_cgo.go — malgo 기반 캡처 장치 열거. cgo 필요 → `//go:build cgo`로 격리.
package audio

import (
	"fmt"

	"github.com/gen2brain/malgo"
)

// EnumerateDevices lists the system capture devices (마이크 + BlackHole 등 가상 입력).
// 각 장치의 이름 휴리스틱으로 루프백 후보를 표시한다(원본 AudioDevice.swift.isLikelyLoopback).
//
// 반환된 DeviceInfo.ID는 SelectSource(Selection{Mode: SelectDevice, DeviceID: id})와
// NewMalgoSourceForDevice(id)에 그대로 넘겨 특정 장치를 캡처할 수 있다.
func EnumerateDevices() ([]DeviceInfo, error) {
	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return nil, fmt.Errorf("audio: init malgo context: %w", err)
	}
	defer func() {
		_ = mctx.Uninit()
		mctx.Free()
	}()

	raw, err := mctx.Devices(malgo.Capture)
	if err != nil {
		return nil, fmt.Errorf("audio: enumerate capture devices: %w", err)
	}

	out := make([]DeviceInfo, 0, len(raw))
	for i := range raw {
		name := raw[i].Name()
		out = append(out, DeviceInfo{
			ID:                  raw[i].ID.String(),
			Name:                name,
			IsLoopbackCandidate: looksLikeLoopback(name),
			IsDefault:           raw[i].IsDefault != 0,
		})
	}
	return out, nil
}
