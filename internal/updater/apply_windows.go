//go:build windows

package updater

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// swHide is ShellExecute's SW_HIDE (헬퍼는 GUI 창을 만들지 않는다).
const swHide = 0

// errorCancelled 는 사용자가 UAC 프롬프트에서 "아니오"를 눌렀을 때 ShellExecute가 돌려주는
// ERROR_CANCELLED(1223)다. 이건 실패가 아니라 **사용자의 선택**이므로 문구를 달리한다.
const errorCancelled = syscall.Errno(1223)

// SwapAndRelaunch on Windows handles two asset shapes:
//
//  1. NSIS installer (".exe" with "installer" in name / ".msi").
//     Spawned with /S silent flag; the NSIS template re-launches the freshly
//     installed app.
//  2. Portable exe/zip (asset가 그대로 cross-livetranslate.exe인 경우). 실행 중인 .exe는
//     자기 자신을 덮어쓸 수 없으므로, 새로 받은 exe를 self-apply 모드로 띄워 교체시킨다
//     (applyupdate_windows.go).
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

// dirWritable reports whether the current process token can create files in dir.
//
// # 왜 권한 비트가 아니라 실제로 파일을 만들어 보는가
//
// Windows의 접근 권한은 ACL + 프로세스 토큰(승격 여부)의 조합이라, os.Stat의 모드 비트로는
// 절대 알 수 없다. 실제 관측된 버그가 이것이다: Program Files에 설치된 앱이 자기 자신을
// 교체하려다 60초 동안 "Access is denied"를 재시도하고 조용히 죽었는데, 그때 부모 앱은
// 이미 종료한 뒤여서 사용자에게는 **앱이 그냥 사라진 것**으로 보였다. 그래서 앱을 끄기
// 전에 여기서 먼저 확인한다.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".clt-write-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
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
// # 권한 분기
//
// 설치 위치가 사용자 쓰기 가능(포터블 — Downloads·바탕화면·%LOCALAPPDATA% 등)이면 그냥
// detached로 띄운다. Program Files처럼 쓰기 불가면 UAC 승격(runas)으로 띄워야 한다.
// 승격 실행은 DETACHED_PROCESS 같은 생성 플래그를 줄 수 없어 ShellExecute를 쓴다.
func swapPortable(newExe string) error {
	current, err := os.Executable()
	if err != nil {
		return fmt.Errorf("현재 실행 경로 확인 실패: %w", err)
	}
	if resolved, e := filepath.EvalSymlinks(current); e == nil {
		current = resolved
	}
	dir := filepath.Dir(current)
	needElevation := !dirWritable(dir)
	Logf("swapPortable current=%s newExe=%s pid=%d writable=%v",
		current, newExe, os.Getpid(), !needElevation)

	if needElevation {
		logBoth("update.elevate",
			"설치 폴더에 쓰기 권한 없음(%s) — UAC 승격으로 교체 헬퍼를 실행한다", dir)
		return elevatedApply(newExe, current)
	}

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

// elevatedApply launches the self-apply helper with a UAC elevation prompt.
//
// 반환 에러는 **사용자에게 그대로 보여도 되는 문장**이어야 한다. 이 함수가 에러를 돌려주면
// 호출자(update.go DownloadAndInstallUpdate)는 앱을 종료하지 않고 그 문장을 표면화한다 —
// 즉 사용자가 UAC를 거절해도 앱은 살아 있고, 무슨 일이 있었는지 알 수 있다.
func elevatedApply(newExe, target string) error {
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return fmt.Errorf("승격 실행 준비 실패: %w", err)
	}
	file, err := windows.UTF16PtrFromString(newExe)
	if err != nil {
		return fmt.Errorf("승격 실행 준비 실패: %w", err)
	}
	// 승격 경로는 프로세스가 아니라 셸이 인자를 파싱하므로 문자열로 넘긴다.
	// 대상 경로에 공백이 있다("C:\Program Files\...") → 반드시 따옴표로 감싼다.
	params, err := windows.UTF16PtrFromString(
		applyUpdateFlag + " " + elevatedFlag + ` --target "` + target + `"`)
	if err != nil {
		return fmt.Errorf("승격 실행 준비 실패: %w", err)
	}
	cwd, err := windows.UTF16PtrFromString(filepath.Dir(newExe))
	if err != nil {
		return fmt.Errorf("승격 실행 준비 실패: %w", err)
	}

	if err := windows.ShellExecute(0, verb, file, params, cwd, swHide); err != nil {
		if errors.Is(err, errorCancelled) {
			Logf("elevatedApply: 사용자가 UAC 승격을 거부했다")
			return fmt.Errorf(
				"업데이트를 설치하려면 관리자 권한이 필요합니다(설치 위치: %s). "+
					"권한 요청을 허용하거나, 앱을 사용자 폴더로 옮긴 뒤 다시 시도하세요",
				filepath.Dir(target))
		}
		Logf("elevatedApply ShellExecute FAILED: %v", err)
		return fmt.Errorf("관리자 권한으로 업데이트를 실행하지 못했습니다: %w", err)
	}
	Logf("elevatedApply: 승격된 apply-update helper 실행됨 target=%s", target)
	return nil
}

// InstallTargetDiagnostics 는 Windows에서 "지금 업데이트가 설치될 수 있는가"를 한 줄로
// 남긴다. macOS의 App Translocation 같은 개념은 없지만, **설치 폴더 쓰기 권한**이 정확히
// 같은 역할의 실패 지점이다(Program Files 설치본은 승격 없이는 자기 교체가 불가능하다).
// 사용자가 로그 하나만 보내와도 원인을 알 수 있도록 시작 시 기록한다.
func InstallTargetDiagnostics() string {
	exe, err := os.Executable()
	if err != nil {
		return "windows: 실행 경로 확인 불가"
	}
	dir := filepath.Dir(exe)
	if dirWritable(dir) {
		return "windows: 설치 폴더 쓰기 가능 — 자기 교체 업데이트 가능 (exe=" + exe + ")"
	}
	return "windows: 설치 폴더 쓰기 불가 — 업데이트 시 UAC 승격 필요 (exe=" + exe + ")"
}
