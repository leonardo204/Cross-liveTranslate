//go:build darwin

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// access(2) 모드 비트(darwin: W_OK=2, X_OK=1).
const (
	unixWOK = 0x2
	unixXOK = 0x1
)

// SwapAndRelaunch replaces the installed .app bundle with the bundle found under
// `extractedDir` using a detached shell helper. macOS는 실행 중인 실행파일을 열어두므로
// 같은 프로세스에서 자기 자신을 덮어쓸 수 없다 — 헬퍼가 부모 종료를 기다렸다가 `ditto`로
// 교체하고 새 번들을 `open` 한다.
//
// 설치 위치는 헬퍼가 아니라 여기서 결정한다(planInstall). App Translocation 상태(읽기전용
// 랜덤 마운트)에서 실행 중이면 원본 경로를 SPI로 되찾고, 그것도 안 되면 /Applications로
// 이설한다. **다운로드 임시 폴더는 어떤 경우에도 최종 실행 대상이 되지 않는다** — 예전에
// 교체 실패 시 임시 폴더 앱을 직접 실행하던 폴백이 "업데이트 후 앱이 임시 폴더에서 실행"
// 버그의 원인이었다.
func SwapAndRelaunch(extractedDir string) error {
	newAppPath, err := FindAppBundle(extractedDir)
	if err != nil {
		return err
	}
	current, err := currentAppBundle()
	if err != nil {
		return err
	}

	info := queryTranslocation(current)
	logBoth("update.target", "설치 위치 판정: current=%s translocated=%v spi=%v original=%q",
		current, info.Translocated, info.SPIAvailable, info.Original)

	plan, err := planInstall(installEnv{
		CurrentApp:      current,
		Translocated:    info.Translocated,
		OriginalPath:    resolveSymlinks(info.Original),
		NewAppName:      filepath.Base(newAppPath),
		ApplicationsDir: "/Applications",
		TempDir:         resolveSymlinks(os.TempDir()),
		// 실제 설치 직전이므로 정확도가 최우선 — 임시 파일 생성으로 쓰기 가능 여부를 확인한다
		// (권한 비트 계산이 놓치는 읽기전용 마운트/SIP까지 잡힌다).
		WritableDir: dirWritable,
		Exists:      pathExists,
	})
	if err != nil {
		logBoth("update.target", "설치 위치 결정 실패 — 업데이트 중단(기존 앱 유지): %v", err)
		return err
	}
	logBoth("update.target", "설치 대상 결정: mode=%s target=%s (%s)", plan.Mode, plan.Target, plan.Reason)

	helper, err := writeHelper(helperParams{
		ParentPID: os.Getpid(),
		Target:    plan.Target,
		Source:    newAppPath,
		OldApp:    current,
	})
	if err != nil {
		return err
	}

	cmd := exec.Command("/bin/sh", helper)
	cmd.SysProcAttr = detachAttr()
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("교체 helper 실행 실패: %w", err)
	}
	// Release the helper process so it survives our exit.
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("helper detach 실패: %w", err)
	}
	return nil
}

// InstallTargetDiagnostics returns a human-readable summary of where an update
// would be installed right now. controller가 트랜잭션 로그에 남겨, 실제 업데이트 전에도
// translocation 상태를 진단할 수 있게 한다.
//
// **부작용이 없어야 한다** — 앱 시작마다 호출되므로 /Applications나 사용자 폴더에 테스트
// 파일을 만들면 TCC 다이얼로그를 유발하거나 잡파일을 남길 수 있다. 그래서 쓰기 가능 여부를
// access(2)로만 확인한다(파일 생성 없음). 실제 설치 경로(SwapAndRelaunch)는 정확도를 위해
// 임시 파일 생성 프로브를 그대로 쓴다.
func InstallTargetDiagnostics() string {
	current, err := currentAppBundle()
	if err != nil {
		return fmt.Sprintf("번들 밖 실행(%v)", err)
	}
	info := queryTranslocation(current)
	plan, perr := planInstall(installEnv{
		CurrentApp:      current,
		Translocated:    info.Translocated,
		OriginalPath:    resolveSymlinks(info.Original),
		NewAppName:      filepath.Base(current),
		ApplicationsDir: "/Applications",
		TempDir:         resolveSymlinks(os.TempDir()),
		WritableDir:     dirWritableProbe,
		Exists:          pathExists,
	})
	if perr != nil {
		return fmt.Sprintf("current=%s translocated=%v original=%q → 설치 불가: %v",
			current, info.Translocated, info.Original, perr)
	}
	return fmt.Sprintf("current=%s translocated=%v spi=%v original=%q → mode=%s target=%s",
		current, info.Translocated, info.SPIAvailable, info.Original, plan.Mode, plan.Target)
}

// resolveSymlinks 는 비교 정확도를 위해 경로의 심볼릭 링크를 푼다(/tmp → /private/tmp 등).
// 해석에 실패하면(없는 경로) 원본을 그대로 돌려준다 — 판정은 문자열 정규화로 이어진다.
func resolveSymlinks(p string) string {
	if p == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// pathExists reports whether path exists (파일/디렉토리 무관).
func pathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Lstat(path)
	return err == nil
}

// dirWritableProbe 는 **비파괴** 쓰기 가능 프로브다: access(2)로 쓰기+탐색 권한만 본다.
// 진단 경로(앱 시작 시마다 호출)에서 쓰며, 파일을 만들지 않아 TCC 다이얼로그나 잡파일이
// 생기지 않는다. 읽기전용 마운트는 권한 비트로는 드러나지 않을 수 있어, 실제 설치 시점에는
// dirWritable(생성 테스트)로 다시 확인한다.
func dirWritableProbe(dir string) bool {
	if dir == "" {
		return false
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return false
	}
	return syscall.Access(dir, unixWOK|unixXOK) == nil
}

// dirWritable reports whether we can create/replace entries in dir. 번들 교체는 부모
// 디렉토리에서 mv/ditto 로 이뤄지므로 디렉토리 쓰기 권한이 기준이다. 실제로 임시 파일을
// 만들어 확인한다(권한 비트 계산보다 정확 — 읽기전용 마운트/SIP까지 잡힌다).
func dirWritable(dir string) bool {
	if dir == "" {
		return false
	}
	f, err := os.CreateTemp(dir, ".lt-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// currentAppBundle walks upward from os.Executable() until it finds a
// `*.app` ancestor. Falls back to an error so callers (e.g. running raw
// from `go run`) get a clear message instead of overwriting nothing.
func currentAppBundle() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	for dir != "/" && dir != "." {
		if strings.HasSuffix(dir, ".app") {
			return dir, nil
		}
		dir = filepath.Dir(dir)
	}
	return "", fmt.Errorf(".app 번들 안에서 실행 중이 아닙니다 (exe=%s) — 업데이트를 적용할 수 없습니다", exe)
}

// helperParams are the values baked into the detached swap script.
type helperParams struct {
	ParentPID int
	Target    string // 새 번들을 설치할 최종 경로(임시 폴더 아님).
	Source    string // 추출된 새 번들(임시 폴더) — 복사 원본으로만 쓰고 실행하지 않는다.
	OldApp    string // 현재 실행 중인 번들(translocated일 수 있다) — 자식 정리/롤백 대상.
}

// shellQuote wraps s in single quotes for POSIX sh, escaping embedded quotes.
//
// **왜 Go의 %q를 쓰면 안 되는가**: %q는 Go 문자열 리터럴 인용이지 셸 인용이 아니다.
// 큰따옴표 안에서 셸은 `$VAR`·`$(cmd)`·백틱·`\`를 계속 해석하므로, 경로에 그런 문자가
// 있으면 헬퍼 실행 시점에 **임의 명령이 실행되고** 엉뚱한 경로가 설치/삭제 대상이 된다
// (rm -rf "$TARGET" 롤백 분기까지 오염). 작은따옴표는 셸이 어떤 확장도 하지 않으므로
// 바이트 그대로 전달된다 — 유일한 예외인 작은따옴표 자신만 '\” 로 끊어 이어붙인다.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func writeHelper(p helperParams) (string, error) {
	dir, err := os.MkdirTemp("", "cross-livetranslate-swap-*")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "swap.sh")
	script := fmt.Sprintf(`#!/bin/sh
# 스왑 helper: 부모(앱) 종료 대기 -> .app 교체 -> quarantine 제거 -> 새 버전 실행 -> 정리.
# 모든 단계를 로그에 남겨 실패를 진단할 수 있게 한다(detached라 stderr가 버려지므로
# 파일 로깅이 유일한 단서).
#
# 불변식 1: **SOURCE(임시 폴더)를 실행 대상으로 삼지 않는다.** 교체가 불가능하면 기존 앱을
#           되살리고 끝낸다 — 임시 폴더는 청소되므로 그곳을 실행하면 다음 실행이 깨진다.
# 불변식 2: 모든 경로는 작은따옴표로 인용되어 셸 확장이 일어나지 않는다(shellQuote).
PARENT=%d
TARGET=%s
SOURCE=%s
OLDAPP=%s
LOG=%s
TXLOG=%s
SELF_DIR=%s
STAGE_DIR=$(dirname "$SOURCE")
BACKUP="${TARGET}.bak.$$"

# 두 로그에 함께 남긴다: 업데이터 진단 로그(LOG)와 앱 트랜잭션 로그(TXLOG, 사용자가 바로
# 첨부하는 파일). TXLOG는 "타임스탬프 [role] [tag] 내용" 한 줄 포맷을 따른다.
log() {
  printf '%%s  [swap] %%s\n' "$(date +%%Y-%%m-%%dT%%H:%%M:%%S%%z)" "$*" >> "$LOG" 2>/dev/null
  [ -n "$TXLOG" ] && printf '%%s [swap] [update.swap] %%s\n' "$(date '+%%Y-%%m-%%d %%H:%%M:%%S.000')" "$*" >> "$TXLOG" 2>/dev/null
  return 0
}

log "start PARENT=$PARENT TARGET=$TARGET OLDAPP=$OLDAPP BACKUP=$BACKUP STAGE=$STAGE_DIR"

# 부모(컨트롤러) 종료 대기 (최대 ~30s). 타임아웃이면 그 사실을 남긴다 — 아래 교체 실패의
# 원인이 "부모가 아직 번들을 잡고 있어서"인지 구분하는 근거가 된다.
PARENT_GONE=0
for i in $(seq 1 60); do
  if ! kill -0 "$PARENT" 2>/dev/null; then PARENT_GONE=1; break; fi
  sleep 0.5
done
if [ "$PARENT_GONE" = "1" ]; then
  log "parent exited"
else
  log "WARN: parent 종료 대기 타임아웃(30s) — pid=$PARENT 아직 실행 중. 교체를 계속 시도합니다"
fi

# 남은 자식 프로세스(overlay/settings)를 정리해 번들 잠금을 푼다.
# pkill -f 는 인자를 정규식으로 해석해 경로의 메타문자에 오동작하므로, ps 출력과
# **바이트 접두사 비교**로 대상을 고른다(글로빙/정규식 없음).
kill_bundle_procs() {
  prefix="$1/Contents/MacOS/"
  len=$(printf %%s "$prefix" | wc -c | tr -d ' ')
  ps -Ao pid= -o command= 2>/dev/null | while read -r pid cmd; do
    head=$(printf %%s "$cmd" | cut -b "1-$len")
    if [ "$head" = "$prefix" ]; then
      kill "$pid" 2>/dev/null && log "child killed pid=$pid ($1)"
    fi
  done
}
kill_bundle_procs "$OLDAPP"
if [ "$TARGET" != "$OLDAPP" ]; then
  kill_bundle_procs "$TARGET"
fi
sleep 0.5

# 기존 번들이 있으면 백업 후 교체, 없으면(이설 최초 설치) 바로 복사.
# translocation 마운트가 아직 해제되지 않아 mv가 EBUSY로 실패할 수 있으므로 백오프 재시도.
if [ -d "$TARGET" ]; then
  MOVED=0
  for attempt in 1 2 3 4 5 6 7 8; do
    if mv "$TARGET" "$BACKUP" 2>>"$LOG"; then MOVED=1; break; fi
    log "WARN: 기존 번들 이동 실패(시도 $attempt/8) — 마운트 해제/잠금 해제 대기 후 재시도"
    sleep 1
  done
  if [ "$MOVED" != "1" ]; then
    log "ERROR: 기존 번들 이동 실패 — 업데이트 중단, 기존 앱을 그대로 둡니다: $TARGET"
    open -n "$TARGET" >/dev/null 2>&1 && log "기존 앱 재실행" || log "기존 앱 재실행 실패(수동 실행 필요)"
    exit 1
  fi
fi

if ditto "$SOURCE" "$TARGET" 2>>"$LOG"; then
  [ -d "$BACKUP" ] && rm -rf "$BACKUP"
  if [ "$TARGET" != "$OLDAPP" ]; then
    log "ditto 설치 완료 — 번들을 $TARGET 로 이설했습니다(기존 실행 위치: $OLDAPP)"
  else
    log "ditto 교체 완료: $TARGET"
  fi
else
  log "ERROR: ditto 실패 — 롤백 시도"
  rm -rf "$TARGET"
  if [ -d "$BACKUP" ]; then
    if mv "$BACKUP" "$TARGET" 2>>"$LOG"; then
      log "백업 복원 완료: $TARGET"
      open -n "$TARGET" >/dev/null 2>&1 && log "롤백 후 기존 버전 재실행" || log "기존 버전 재실행 실패"
    else
      log "ERROR: 백업 복원 실패 — 기존 앱은 $BACKUP 에 그대로 있습니다. 이 폴더를 $TARGET 로 옮기면 복구됩니다"
    fi
  else
    # 새로 만들려던 위치라 복원할 백업이 없다 — 원래 실행 중이던 번들을 되살린다.
    open -n "$OLDAPP" >/dev/null 2>&1 && log "기존 실행 번들 재실행: $OLDAPP" || log "기존 번들 재실행 실패"
  fi
  exit 1
fi

# quarantine 제거: 이게 남아 있으면 다음 실행에서 다시 App Translocation(읽기전용 랜덤
# 경로)으로 뜨고 그 다음 업데이트가 또 실패한다. ditto는 확장속성을 그대로 복사하므로
# 설치 **후** 대상에서 지운다.
if xattr -dr com.apple.quarantine "$TARGET" 2>>"$LOG"; then
  log "quarantine 제거 완료: $TARGET"
else
  log "WARN: quarantine 제거 실패(무시 가능): $TARGET"
fi

# 새 버전 실행(-n: 기존 인스턴스 무시하고 새로).
if open -n "$TARGET"; then
  log "새 버전 실행 완료: $TARGET"
else
  log "ERROR: open 실패 — 수동 실행 필요: $TARGET"
fi

# 정리: 다운로드/추출 스테이징 폴더(수십 MB)와 헬퍼 자신의 임시 폴더를 지운다.
# 헬퍼 스크립트는 아직 실행 중이므로 자기 폴더 삭제는 분리된 백그라운드에서 지연 실행한다.
case "$STAGE_DIR" in
  /*) rm -rf "$STAGE_DIR" && log "스테이징 폴더 정리: $STAGE_DIR" ;;
esac
( sleep 5; rm -rf "$SELF_DIR" ) >/dev/null 2>&1 &
`, p.ParentPID, shellQuote(p.Target), shellQuote(p.Source), shellQuote(p.OldApp),
		shellQuote(LogPath()), shellQuote(txlogPath()), shellQuote(dir))
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return "", err
	}
	return path, nil
}
