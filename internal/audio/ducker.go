package audio

// ducker.go — A3(Wave2): 원음 덕킹 인터페이스(순수, cgo 없음 → 전 플랫폼 컴파일).
//
// 원본 이식: liveTranslate/Sources/Audio/SystemAudioDucker.swift
//   (기본 출력 장치의 kAudioDevicePropertyVolumeScalar를 일시적으로 낮춰 원문 소리를 줄이고,
//    정지 시 원래 볼륨 복원. 마스터 볼륨 스칼라 미지원 장치는 조용히 skip).
//
// 실제 구현은 플랫폼별 파일에 있다:
//   - ducker_darwin.go       (darwin && cgo)  — CoreAudio 실구현.
//   - ducker_darwin_nocgo.go (darwin && !cgo) — no-op.
//   - ducker_windows.go      (windows)        — no-op stub(ISimpleAudioVolume 실측 대기).
//   - ducker_other.go        (그 외)          — no-op.
// NewDucker()는 플랫폼별 파일이 제공한다.

// Ducker temporarily lowers the system default output volume (원음 덕킹) and
// restores it. 미지원 장치에서는 no-op으로 조용히 무시한다.
type Ducker interface {
	// Duck saves the current volume once(첫 호출) and sets it to `to`(0..1).
	// 이미 덕킹 중이면 레벨만 갱신한다.
	Duck(to float64)
	// Restore restores the saved volume (없으면 no-op).
	Restore()
	// IsSupported reports whether the current default output supports master
	// volume control(=덕킹 가능). 미지원이면 controller가 덕킹을 자동 비활성한다.
	IsSupported() bool
}

// noopDucker is the fallback used on platforms/builds without a volume backend.
type noopDucker struct{}

func (noopDucker) Duck(float64)     {}
func (noopDucker) Restore()         {}
func (noopDucker) IsSupported() bool { return false }
