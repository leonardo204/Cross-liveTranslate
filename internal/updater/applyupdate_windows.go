//go:build windows

package updater

import (
	"fmt"
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

const (
	applyUpdateFlag = "--apply-update"
	// elevatedFlag 는 이 헬퍼가 UAC 승격(runas)으로 실행됐음을 알린다. 교체 자체는 승격
	// 권한이 필요하지만, **교체 후 다시 띄우는 앱까지 관리자로 돌아서는 안 된다** —
	// 그래서 이 깃발이 있으면 재실행을 셸(explorer.exe)에 위임해 권한을 되돌린다.
	elevatedFlag = "--elevated"
)

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
// mode (`--apply-update [--elevated] --target <path>`). If so, it terminates the
// old app processes, swaps itself over <target>, relaunches the updated app, and
// exits. It os.Exit()s rather than returning on the apply path.
//
// This replaces the old PowerShell swap helper: re-running our own
// signature-verified binary is not blocked by AV/EDR the way a hidden
// `powershell -ExecutionPolicy Bypass -File <temp>.ps1` is.
//
// # 실패해도 앱이 사라지지 않는다
//
// 예전 구현은 교체에 실패하면 그냥 os.Exit(1) 했다. 그런데 부모 앱은 헬퍼를 띄운 직후
// 이미 스스로 종료한 상태라, 사용자 눈에는 **앱이 아무 메시지 없이 증발**했다(실제 관측된
// 버그 — Program Files 설치본에서 60초간 "Access is denied" 재시도 후 조용히 사망).
// 이제는 어떤 실패 경로에서도 원래 exe를 다시 띄운다.
func MaybeApplyUpdate(args []string) bool {
	var target string
	apply := false
	elevated := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case applyUpdateFlag:
			apply = true
		case elevatedFlag:
			elevated = true
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
	Logf("[apply] start self=%s target=%s elevated=%v", self, target, elevated)

	data, err := os.ReadFile(self)
	if err != nil {
		Logf("[apply] read self FAILED: %v", err)
		// 새 바이너리를 읽지도 못했다 — 대상은 아직 멀쩡하니 그대로 되살린다.
		relaunchApp(target, elevated)
		os.Exit(1)
	}

	// 잠금 해제를 기다리기만 하면, 구버전이 종료 시 프로세스를 남기는 버그가 있을 때(관측됨)
	// 대상 exe가 영원히 잠겨 업데이트가 실패한다. 그래서 들어온 업데이터(우리)가 대상 exe를
	// 실행 중인 옛 프로세스(controller/overlay/settings — 모두 같은 이미지 경로)를 **강제 종료**해
	// 잠금을 확실히 푼다. self는 임시 폴더의 다른 경로라 대상에 매칭되지 않아 안전하다.
	killed := killProcessesByImage(target)
	Logf("[apply] killed %d old process(es) holding the target", killed)

	if err := replaceExecutable(target, data); err != nil {
		logBoth("update.apply", "[apply] 교체 실패 — 구버전을 유지한다: %v", err)
		relaunchApp(target, elevated)
		os.Exit(1)
	}
	logBoth("update.apply", "[apply] 교체 완료 (%d bytes) target=%s", len(data), target)

	if err := relaunchApp(target, elevated); err != nil {
		Logf("[apply] relaunch gave up: %v", err)
		os.Exit(1)
	}
	os.Exit(0)
	return true
}

// replaceExecutable swaps the bytes of an executable that may still be mapped.
//
// # 왜 단순 os.WriteFile이 아닌가
//
// Windows는 실행 중이거나 방금 종료돼 이미지 섹션이 아직 남은 .exe를 쓰기용으로 **열지
// 못하게** 막고 ERROR_ACCESS_DENIED를 돌려준다. 반면 같은 볼륨 안에서의 rename은 허용된다.
// 그래서 옛 파일을 <exe>.old 로 밀어낸 뒤 새 파일을 그 자리에 쓴다.
//
// 실패하면 반드시 원래 이름으로 되돌린다 — 반쯤 교체된 상태로 남으면 앱이 아예 실행되지
// 않게 되고, 그건 업데이트 실패보다 훨씬 나쁘다.
func replaceExecutable(target string, data []byte) error {
	backup := target + ".old"
	_ = os.Remove(backup) // 이전 업데이트가 남긴 잔재.

	// 종료 직후 이미지 섹션이 풀릴 때까지 짧게 재시도한다(자식 종료와의 레이스).
	var lastErr error
	renamed := false
	for i := 0; i < 40; i++ {
		if lastErr = os.Rename(target, backup); lastErr == nil {
			renamed = true
			break
		}
		if i == 0 {
			killProcessesByImage(target) // 스폰 레이스 대비 한 번 더.
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !renamed {
		return fmt.Errorf("옛 실행 파일을 밀어내지 못했습니다(%s → %s): %w",
			target, backup, lastErr)
	}

	if err := os.WriteFile(target, data, 0o755); err != nil {
		// 롤백: 새 파일을 못 썼으니 옛 파일을 제자리로 되돌린다.
		if rbErr := os.Rename(backup, target); rbErr != nil {
			return fmt.Errorf("새 실행 파일 쓰기 실패(%v) + 롤백도 실패(%v) — "+
				"수동 복구 필요: %s 를 %s 로 되돌리세요", err, rbErr, backup, target)
		}
		return fmt.Errorf("새 실행 파일 쓰기 실패(롤백 완료): %w", err)
	}

	// <exe>.old 는 이미지 섹션이 아직 매핑돼 있으면 지금은 지워지지 않는다. 그럴 땐
	// 다음 실행의 CleanupStaleBackup이 치운다 — 그때는 옛 이미지가 확실히 내려가 있다.
	//
	// MOVEFILE_DELAY_UNTIL_REBOOT(재부팅 시 삭제 예약)는 쓰지 않는다: HKLM의
	// PendingFileRenameOperations에 쓰는 동작이라 **관리자 권한이 필요**하고, 일반 사용자로
	// 도는 포터블 실행에서는 조용히 실패해 13MB짜리 .old가 그대로 남는다(실측 확인).
	if err := os.Remove(backup); err != nil {
		Logf("[apply] %s 즉시 삭제 실패(다음 실행에서 정리): %v", backup, err)
	}
	return nil
}

// CleanupStaleBackup removes the <exe>.old left behind by a previous self-update.
//
// 교체 직후에는 옛 실행 파일의 이미지 섹션이 아직 매핑돼 있어 삭제가 막힐 수 있다. 이
// 함수는 **다음 실행 시점**에 불려 그 찌꺼기를 치운다(그때는 옛 프로세스가 완전히 사라져
// 있다). 실패는 무해 — 로그만 남기고 다음 실행에서 다시 시도한다.
func CleanupStaleBackup() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	backup := exe + ".old"
	if _, err := os.Stat(backup); err != nil {
		return // 정상 경로: 남은 게 없다.
	}
	if err := os.Remove(backup); err != nil {
		Logf("[cleanup] %s 삭제 실패(다음 실행에서 재시도): %v", backup, err)
		return
	}
	Logf("[cleanup] 이전 업데이트 잔여 파일 삭제: %s", backup)
}

// relaunchApp starts the app at target, detached from this helper process.
//
// elevated=true(우리가 UAC 승격으로 실행됨)이면 **셸(explorer.exe)을 경유해** 띄운다.
// 승격된 프로세스가 자식을 직접 만들면 그 자식도 관리자 권한을 물려받는데, 자막 앱이
// 관리자로 상주할 이유가 없고 드래그앤드롭 등 셸 연동도 깨진다. explorer.exe는 로그인
// 사용자의 일반 토큰으로 프로세스를 만들어 주므로 권한이 원래대로 돌아온다.
func relaunchApp(target string, elevated bool) error {
	if elevated {
		if err := relaunchViaShell(target); err == nil {
			Logf("[apply] relaunched via explorer (권한 강등 완료)")
			return nil
		} else {
			// 셸 경유가 막히면(그룹 정책 등) 관리자 권한으로라도 앱은 살려 둔다.
			//
			// 다만 이 경로로 뜬 앱은 승격된 토큰을 그대로 물려받는다. 자격 증명 저장소
			// (Windows Credential Manager)는 계정별로 분리돼 있어, UAC를 로그인 사용자가
			// 아닌 **다른 관리자 계정**으로 승인했다면 저장해 둔 Gemini API 키가 보이지
			// 않는다("업데이트했더니 키가 사라졌다"의 유일하게 실재하는 경로다).
			// 원인 추적이 가능하도록 트랜잭션 로그에도 함께 남긴다.
			logBoth("update.apply",
				"[apply] explorer 경유 재실행 실패 — 관리자 권한 그대로 실행한다(자격 증명 저장소가 "+
					"다른 계정이면 저장된 API 키가 보이지 않을 수 있다): %v", err)
		}
	}

	var lastErr error
	for _, flags := range []uint32{
		flagDetached | flagNewProcessGroup | flagBreakawayFromJob,
		flagDetached | flagNewProcessGroup,
	} {
		cmd := exec.Command(target)
		cmd.Dir = filepath.Dir(target)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		if e := cmd.Start(); e == nil {
			pid := cmd.Process.Pid
			_ = cmd.Process.Release()
			Logf("[apply] relaunched OK flags=0x%X pid=%d", flags, pid)
			return nil
		} else {
			lastErr = e
			Logf("[apply] relaunch FAILED flags=0x%X: %v", flags, e)
		}
	}
	return lastErr
}

// relaunchViaShell asks explorer.exe to start target with the logged-on user's
// (non-elevated) token.
func relaunchViaShell(target string) error {
	winDir := os.Getenv("WINDIR")
	if winDir == "" {
		winDir = `C:\Windows`
	}
	explorer := filepath.Join(winDir, "explorer.exe")
	cmd := exec.Command(explorer, target)
	cmd.Dir = filepath.Dir(target)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: flagDetached | flagNewProcessGroup,
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
