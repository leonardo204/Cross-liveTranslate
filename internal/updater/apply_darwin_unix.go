//go:build darwin

package updater

import "syscall"

// detachAttr puts the helper into its own session so it survives the parent's
// quit and is not killed when the GUI app terminates.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
