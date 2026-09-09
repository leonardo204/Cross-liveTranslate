//go:build windows

package reveal

import (
	"errors"
	"os/exec"
)

// openFolder 는 탐색기로 폴더를 연다.
//
// explorer.exe 는 성공해도 종료 코드 1을 돌려주는 것으로 잘 알려져 있어 Run()으로 기다리면
// 멀쩡히 열린 창을 실패로 보고한다. 그래서 Start()로 띄우고 결과를 기다리지 않는다.
func openFolder(dir string) error {
	if dir == "" {
		return errors.New("reveal: 경로가 비어 있습니다")
	}
	return exec.Command("explorer.exe", dir).Start()
}
