// Package hudpos positions the control-HUD window natively on the primary
// screen's top-right. Wails' Screen 정보에는 모니터 원점(X/Y)이 없어 멀티모니터에서
// 전역 좌표 계산이 불가능하고, WindowSetPosition이 창이 놓인 모니터 기준이라 보조
// 모니터에 생성되면 화면 밖으로 나간다. NSScreen으로 주 모니터 visibleFrame(메뉴바/독
// 제외) 우상단에 직접 배치해 이를 해결한다(원본 HUDController.defaultOrigin 대응).
package hudpos

import "errors"

// ErrOffScreen 은 복원하려던 자리가 지금 붙어 있는 어떤 화면에도 걸치지 않는다는 뜻이다
// (모니터를 뺐거나 해상도가 바뀐 경우). 호출자는 기본 위치로 되돌린다.
var ErrOffScreen = errors.New("hudpos: 저장된 위치가 화면 밖입니다")

// ErrUnsupported 는 이 플랫폼에 창 위치 조회/지정 구현이 없다는 뜻이다.
var ErrUnsupported = errors.New("hudpos: 이 플랫폼은 창 위치 저장을 지원하지 않습니다")

// PositionPrimaryTopRight moves the window with the given title to the primary
// screen's top-right (below the menu bar). darwin 전용 — 그 외 플랫폼은 no-op.
// 실패는 무해(로그만): 배치 실패해도 창은 어딘가에 떠 있다.
func PositionPrimaryTopRight(title string) error {
	return positionPrimaryTopRight(title)
}

// HideFromTaskbar removes the window's taskbar button / Alt+Tab entry
// (Windows 전용 — 트레이 상주 앱이므로 작업표시줄에 자리를 차지하지 않는다).
// 그 외 플랫폼은 no-op. 실패는 무해(로그만).
func HideFromTaskbar(title string) error {
	return hideFromTaskbar(title)
}

// WindowOrigin returns the titled window's position in global screen coordinates.
// 좌표 원점은 플랫폼 규칙을 그대로 쓴다(darwin 좌하단 / windows 좌상단) — 저장과 복원이
// 같은 플랫폼에서만 일어나므로 변환하지 않는다. 미지원 플랫폼은 ErrUnsupported.
func WindowOrigin(title string) (int, int, error) {
	return windowOrigin(title)
}

// SetWindowOrigin moves the titled window to a previously saved position.
// 그 자리가 지금 화면 밖이면 ErrOffScreen 을 돌려주고 창은 건드리지 않는다.
func SetWindowOrigin(title string, x, y int) error {
	return setWindowOrigin(title, x, y)
}
