//go:build !darwin && !windows

package updater

import "errors"

// SwapAndRelaunch is unsupported on Linux and other platforms.
func SwapAndRelaunch(_ string) error {
	return errors.New("자동 업데이트는 macOS / Windows에서만 지원됩니다")
}
