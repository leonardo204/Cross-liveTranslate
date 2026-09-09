//go:build darwin

package reveal

import (
	"errors"
	"os/exec"
)

// openFolder 는 Finder로 폴더를 연다. `open <dir>`는 서명된 번들 안에서도 그대로 동작한다
// (권한 설정 창처럼 커스텀 URL 스킴을 여는 경우와 달리 일반 경로라 제약이 없다).
func openFolder(dir string) error {
	if dir == "" {
		return errors.New("reveal: 경로가 비어 있습니다")
	}
	return exec.Command("/usr/bin/open", dir).Start()
}
