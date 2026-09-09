//go:build !darwin && !windows

package reveal

import (
	"errors"
	"os/exec"
)

// openFolder: 그 외 플랫폼은 freedesktop 관례(xdg-open)를 따른다. 없으면 에러를 돌려주고
// 호출부가 경로만 화면에 남긴다.
func openFolder(dir string) error {
	if dir == "" {
		return errors.New("reveal: 경로가 비어 있습니다")
	}
	return exec.Command("xdg-open", dir).Start()
}
