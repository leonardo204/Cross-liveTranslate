//go:build !darwin && !windows

package updater

import "errors"

// SwapAndRelaunch is unsupported on Linux and other platforms.
func SwapAndRelaunch(_ string) error {
	return errors.New("자동 업데이트는 macOS / Windows에서만 지원됩니다")
}

// InstallTargetDiagnostics 는 이 플랫폼에서 해당 사항이 없다(자동 업데이트 미지원).
func InstallTargetDiagnostics() string {
	return "이 플랫폼은 자동 업데이트를 지원하지 않습니다"
}
