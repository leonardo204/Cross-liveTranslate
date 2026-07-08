//go:build darwin

package main

import (
	"os"
	"syscall"
)

// redirectStderr points the process's stderr (fd 2) at f so that output written
// directly to fd 2 — Go panic tracebacks, cgo(malgo/CoreAudio) abort messages,
// runtime fatal errors — is captured in the log file even when the app is
// launched via `open` (which detaches the terminal, discarding stderr).
// log.SetOutput의 MultiWriter만으로는 log.* 호출만 잡히고, 패닉/치명 오류는 놓친다.
func redirectStderr(f *os.File) {
	_ = syscall.Dup2(int(f.Fd()), int(os.Stderr.Fd()))
}
