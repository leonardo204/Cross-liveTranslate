//go:build windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// SwapAndRelaunch on Windows handles two asset shapes:
//
//  1. NSIS installer (".exe" with "installer" in name / ".msi").
//     Spawned with /S silent flag; the NSIS template re-launches the freshly
//     installed app.
//  2. Portable zip (".zip" containing just cross-livetranslate.exe). The currently
//     running .exe cannot overwrite itself, so a PowerShell helper waits for
//     our PID to exit and then swaps the file + relaunches.
func SwapAndRelaunch(extractedDir string) error {
	Logf("SwapAndRelaunch dir=%s", extractedDir)
	info, err := os.Stat(extractedDir)
	if err != nil {
		Logf("stat FAILED: %v", err)
		return fmt.Errorf("자산 확인 실패: %w", err)
	}

	target := extractedDir
	if info.IsDir() {
		if p := findInstallerExe(extractedDir); p != "" {
			Logf("found installer exe: %s", p)
			target = p
		} else if p := findPortableExe(extractedDir); p != "" {
			Logf("found portable exe: %s", p)
			return swapPortable(p)
		} else {
			Logf("no exe found in %s", extractedDir)
			return fmt.Errorf("추출 결과에서 인스톨러/.exe를 찾지 못했습니다 (%s)", extractedDir)
		}
	}

	if isInstaller(target) {
		return spawnInstaller(target)
	}
	return swapPortable(target)
}

func isInstaller(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return strings.Contains(name, "installer") || strings.HasSuffix(name, ".msi")
}

func findInstallerExe(root string) string {
	var hit string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if isInstaller(p) && strings.HasSuffix(strings.ToLower(p), ".exe") {
			hit = p
		}
		return nil
	})
	return hit
}

func findPortableExe(root string) string {
	var hit string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		lower := strings.ToLower(p)
		if strings.HasSuffix(lower, ".exe") && !isInstaller(p) {
			hit = p
		}
		return nil
	})
	return hit
}

func spawnInstaller(installerPath string) error {
	// Break away from the Wails/WebView2 job so the silent installer survives
	// the app exiting; fall back if the job forbids breakaway.
	for _, flags := range []uint32{
		flagDetached | flagNewProcessGroup | flagBreakawayFromJob,
		flagDetached | flagNewProcessGroup,
	} {
		cmd := exec.Command(installerPath, "/S")
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
		if err := cmd.Start(); err == nil {
			return cmd.Process.Release()
		}
	}
	return fmt.Errorf("인스톨러 실행 실패")
}

// swapPortable performs the portable self-update by launching the freshly
// downloaded exe in "self-apply" mode (see applyupdate_windows.go).
//
// We deliberately do NOT use a PowerShell/batch helper: launching
// `powershell.exe -ExecutionPolicy Bypass -WindowStyle Hidden -File <temp>.ps1`
// is a classic malware signature and AV/EDR silently terminates it (observed:
// powershell got a PID but never executed a single line). Instead we re-run our
// own signature-verified binary, which is far less likely to be blocked.
//
// The new exe (running from temp) waits for this process to release the file
// lock, copies itself over the running exe, and relaunches it.
func swapPortable(newExe string) error {
	current, err := os.Executable()
	if err != nil {
		return fmt.Errorf("현재 실행 경로 확인 실패: %w", err)
	}
	if resolved, e := filepath.EvalSymlinks(current); e == nil {
		current = resolved
	}
	Logf("swapPortable current=%s newExe=%s pid=%d", current, newExe, os.Getpid())

	args := []string{applyUpdateFlag, "--target", current}
	var lastErr error
	for _, flags := range []uint32{
		flagDetached | flagNewProcessGroup | flagBreakawayFromJob,
		flagDetached | flagNewProcessGroup, // fallback: job forbids breakaway
	} {
		cmd := exec.Command(newExe, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
		if err := cmd.Start(); err == nil {
			Logf("apply-update helper launched flags=0x%X pid=%d", flags, cmd.Process.Pid)
			return cmd.Process.Release()
		} else {
			lastErr = err
			Logf("apply-update Start FAILED flags=0x%X: %v", flags, err)
		}
	}
	return fmt.Errorf("apply-update helper 실행 실패: %w", lastErr)
}
