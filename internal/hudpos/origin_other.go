//go:build (!darwin || !cgo) && !windows

package hudpos

// origin_other.go — 창 위치 저장/복원 미구현 플랫폼(linux 등, cgo 없는 darwin 포함).
// 호출자는 ErrUnsupported 를 받으면 위치 저장을 조용히 건너뛴다.

func windowOrigin(title string) (int, int, error) { return 0, 0, ErrUnsupported }

func setWindowOrigin(title string, x, y int) error { return ErrUnsupported }
