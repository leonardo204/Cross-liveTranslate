// Package hudpos positions the control-HUD window natively on the primary
// screen's top-right. Wails' Screen 정보에는 모니터 원점(X/Y)이 없어 멀티모니터에서
// 전역 좌표 계산이 불가능하고, WindowSetPosition이 창이 놓인 모니터 기준이라 보조
// 모니터에 생성되면 화면 밖으로 나간다. NSScreen으로 주 모니터 visibleFrame(메뉴바/독
// 제외) 우상단에 직접 배치해 이를 해결한다(원본 HUDController.defaultOrigin 대응).
package hudpos

// PositionPrimaryTopRight moves the window with the given title to the primary
// screen's top-right (below the menu bar). darwin 전용 — 그 외 플랫폼은 no-op.
// 실패는 무해(로그만): 배치 실패해도 창은 어딘가에 떠 있다.
func PositionPrimaryTopRight(title string) error {
	return positionPrimaryTopRight(title)
}
