//go:build darwin

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SwapAndRelaunch replaces the currently running .app bundle with the bundle
// found under `extractedDir` using a detached shell helper. Because macOS
// holds the executable open while it runs, we cannot overwrite it from
// within the same process — the helper waits for the parent PID to exit,
// performs a `ditto` swap, then launches the fresh bundle via `open`.
func SwapAndRelaunch(extractedDir string) error {
	newAppPath, err := FindAppBundle(extractedDir)
	if err != nil {
		return err
	}
	current, err := currentAppBundle()
	if err != nil {
		return err
	}
	helper, err := writeHelper(current, newAppPath, os.Getpid())
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

func writeHelper(target, source string, parentPID int) (string, error) {
	dir, err := os.MkdirTemp("", "cross-livetranslate-swap-*")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "swap.sh")
	logPath := LogPath()
	script := fmt.Sprintf(`#!/bin/sh
# 스왑 helper: 부모(앱) 종료 대기 → .app 교체 → 새 버전 실행. 모든 단계를 로그에 남겨
# 실패를 진단할 수 있게 한다(detached라 stderr가 버려지므로 파일 로깅이 유일한 단서).
PARENT=%d
TARGET=%q
SOURCE=%q
LOG=%q
log() { printf '%%s  [swap] %%s\n' "$(date +%%Y-%%m-%%dT%%H:%%M:%%S%%z)" "$*" >> "$LOG" 2>/dev/null; }

log "start PARENT=$PARENT TARGET=$TARGET SOURCE=$SOURCE"

# translocation 감지: 다운로드/DMG에서 바로 실행한 앱은 읽기전용 랜덤 경로로 변환되어
# 교체가 불가능하다. 이 경우 명확히 로그하고 새 버전만 실행(사용자가 /Applications로 이동 필요).
case "$TARGET" in
  */AppTranslocation/*)
    log "ERROR: 앱이 translocation 상태입니다(다운로드 폴더/DMG에서 실행). /Applications로 옮긴 뒤 업데이트하세요. 새 버전만 실행합니다."
    open -n "$SOURCE" && log "opened SOURCE(new) 직접 실행" || log "open SOURCE 실패"
    exit 0
  ;;
esac

# 부모(컨트롤러) 종료 대기 (최대 ~30s).
for i in $(seq 1 60); do
  if ! kill -0 "$PARENT" 2>/dev/null; then break; fi
  sleep 0.5
done
log "parent exited (or timeout)"

# 남은 자식 프로세스(overlay/settings 등 TARGET에서 실행 중)를 정리해 번들 잠금을 푼다.
pkill -f "$TARGET/Contents/MacOS/" 2>/dev/null && log "lingering child procs killed" || true
sleep 0.5

# ditto 교체(권한/속성 포함 복제).
BACKUP="${TARGET}.bak.$$"
if ! mv "$TARGET" "$BACKUP" 2>>"$LOG"; then
  log "ERROR: mv 실패(권한/경로) — 교체 중단. 새 버전만 실행."
  open -n "$SOURCE" && log "opened SOURCE(new)" || log "open SOURCE 실패"
  exit 1
fi
if ditto "$SOURCE" "$TARGET" 2>>"$LOG"; then
  rm -rf "$BACKUP"
  log "ditto 교체 완료"
else
  log "ERROR: ditto 실패 — 롤백"
  rm -rf "$TARGET"
  mv "$BACKUP" "$TARGET"
  open -n "$TARGET" && log "롤백 후 기존 버전 재실행" || log "open 실패"
  exit 1
fi

# 새 버전 실행(-n: 기존 인스턴스 무시하고 새로).
if open -n "$TARGET"; then
  log "새 버전 실행 완료: $TARGET"
else
  log "ERROR: open 실패 — 수동 실행 필요: $TARGET"
fi
`, parentPID, target, source, logPath)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return "", err
	}
	return path, nil
}
