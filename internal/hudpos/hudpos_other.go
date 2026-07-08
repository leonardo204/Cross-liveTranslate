//go:build !darwin || !cgo

package hudpos

// positionPrimaryTopRight: 비-darwin(또는 cgo 비활성)은 no-op. Wails의 기본 배치 또는
// 각 플랫폼 창 관리자에 맡긴다.
func positionPrimaryTopRight(title string) error { return nil }
