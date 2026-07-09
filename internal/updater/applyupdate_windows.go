//go:build windows

package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows process-creation flags.
const (
	flagDetached         = 0x00000008 // DETACHED_PROCESS — no inherited console
	flagNewProcessGroup  = 0x00000200 // CREATE_NEW_PROCESS_GROUP
	flagBreakawayFromJob = 0x01000000 // CREATE_BREAKAWAY_FROM_JOB
)

const applyUpdateFlag = "--apply-update"

// killProcessesByImage terminates every running process whose full executable
// image path equals imagePath (case-insensitive), except the current process.
// Used by the self-apply updater to release the file lock on the old app exe
// (controller/overlay/settings all run the same image). Returns the count killed.
// Best-effort: individual failures are ignored.
func killProcessesByImage(imagePath string) int {
	want := strings.ToLower(filepath.Clean(imagePath))
	selfPID := uint32(os.Getpid())

	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		Logf("[apply] snapshot FAILED: %v", err)
		return 0
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return 0
	}
	killed := 0
	for {
		pid := pe.ProcessID
		if pid != 0 && pid != selfPID {
			if p := processImagePath(pid); p != "" &&
				strings.ToLower(filepath.Clean(p)) == want {
				if h, e := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid); e == nil {
					if windows.TerminateProcess(h, 1) == nil {
						killed++
					}
					windows.CloseHandle(h)
				}
			}
		}
		if err := windows.Process32Next(snap, &pe); err != nil {
			break
		}
	}
	return killed
}

// processImagePath returns the full executable path for a PID, or "" on failure.
func processImagePath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

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

	// 잠금 해제를 기다리기만 하면, 구버전이 종료 시 프로세스를 남기는 버그가 있을 때(관측됨)
	// 대상 exe가 영원히 잠겨 업데이트가 실패한다. 그래서 들어온 업데이터(우리)가 대상 exe를
	// 실행 중인 옛 프로세스(controller/overlay/settings — 모두 같은 이미지 경로)를 **강제 종료**해
	// 잠금을 확실히 푼다. self는 임시 폴더의 다른 경로라 대상에 매칭되지 않아 안전하다.
	killed := killProcessesByImage(target)
	Logf("[apply] killed %d old process(es) holding the target", killed)

	// Retry overwriting the target until the old process releases its file
	// lock (it exits a moment after launching us). ~60s budget.
	var werr error
	for i := 0; i < 120; i++ {
		if werr = os.WriteFile(target, data, 0o755); werr == nil {
			break
		}
		if i == 0 {
			// 첫 실패 후 남아있을 수 있는 프로세스를 한 번 더 정리(스폰 레이스 대비).
			killProcessesByImage(target)
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
