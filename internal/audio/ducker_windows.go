//go:build windows

// ducker_windows.go — Windows 원음 덕킹 stub.
//
// TODO(P-windows): ISimpleAudioVolume/IAudioEndpointVolume(WASAPI Core Audio)로 세션/
// 엔드포인트 볼륨을 낮춰 덕킹 구현. 실측 대기이므로 현재는 no-op stub이며, IsSupported()가
// false를 반환해 controller가 덕킹을 자동 비활성한다(재생/게인보상은 정상 동작).
package audio

// NewDucker returns a no-op Ducker (windows — 실측 대기).
func NewDucker() Ducker { return noopDucker{} }
