//go:build !windows

package updater

// MaybeApplyUpdate is a no-op on non-Windows platforms — macOS uses the DMG
// mount + ditto swap path in apply_darwin.go. Present so main.go can call it
// unconditionally at startup.
func MaybeApplyUpdate(args []string) bool { return false }
