//go:build !darwin

package updater

import "errors"

// extractDMG is not supported on non-Darwin platforms.
// The manifest should only contain .dmg URLs for darwin entries,
// so this path should never be reached in normal operation.
func extractDMG(_ []byte, _ string) error {
	return errors.New("DMG 형식은 macOS에서만 지원됩니다")
}
