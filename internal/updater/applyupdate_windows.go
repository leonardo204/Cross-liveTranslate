//go:build windows

package updater

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Windows process-creation flags.
const (
	flagDetached         = 0x00000008 // DETACHED_PROCESS — no inherited console
	flagNewProcessGroup  = 0x00000200 // CREATE_NEW_PROCESS_GROUP
	flagBreakawayFromJob = 0x01000000 // CREATE_BREAKAWAY_FROM_JOB
)

const applyUpdateFlag = "--apply-update"

// MaybeApplyUpdate checks whether this process was relaunched in self-apply
// mode (`--apply-update --target <path>`). If so, it waits for the old exe to
// release its file lock, copies itself over <target>, relaunches the updated
// app, and exits. It os.Exit()s rather than returning on the apply path.
//
// This replaces the old PowerShell swap helper: re-running our own
// signature-verified binary is not blocked by AV/EDR the way a hidden
// `powershell -ExecutionPolicy Bypass -File <temp>.ps1` is.
func MaybeApplyUpdate(args []string) bool {
	var target string
	apply := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case applyUpdateFlag:
			apply = true
		case "--target":
			if i+1 < len(args) {
				target = args[i+1]
				i++
			}
		}
	}
	if !apply || target == "" {
		return false
	}

	self, err := os.Executable()
	if err != nil {
		Logf("[apply] os.Executable FAILED: %v", err)
		os.Exit(1)
	}
	Logf("[apply] start self=%s target=%s", self, target)

	data, err := os.ReadFile(self)
	if err != nil {
		Logf("[apply] read self FAILED: %v", err)
		os.Exit(1)
	}

	// Retry overwriting the target until the old process releases its file
	// lock (it exits a moment after launching us). ~60s budget.
	var werr error
	for i := 0; i < 120; i++ {
		if werr = os.WriteFile(target, data, 0o755); werr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if werr != nil {
		Logf("[apply] write target FAILED (locked?): %v", werr)
		os.Exit(1)
	}
	Logf("[apply] target replaced (%d bytes), relaunching", len(data))

	// Relaunch the updated app (visible) detached from this apply process.
	var lastErr error
	for _, flags := range []uint32{
		flagDetached | flagNewProcessGroup | flagBreakawayFromJob,
		flagDetached | flagNewProcessGroup,
	} {
		cmd := exec.Command(target)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		if e := cmd.Start(); e == nil {
			pid := cmd.Process.Pid
			cmd.Process.Release()
			Logf("[apply] relaunched OK flags=0x%X pid=%d", flags, pid)
			os.Exit(0)
		} else {
			lastErr = e
			Logf("[apply] relaunch FAILED flags=0x%X: %v", flags, e)
		}
	}
	Logf("[apply] relaunch gave up: %v", lastErr)
	os.Exit(1)
	return true
}
