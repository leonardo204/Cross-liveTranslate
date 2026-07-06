//go:build !darwin && !windows

package overlay

import "fmt"

// Apply is unsupported on platforms without a native overlay shim (only
// darwin and windows are implemented). Returns an error so callers can
// degrade gracefully.
func Apply(title string, monitorIndex int) error {
	return fmt.Errorf("overlay: native overlay window not supported on this platform")
}
