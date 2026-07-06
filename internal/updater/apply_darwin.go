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
	script := fmt.Sprintf(`#!/bin/sh
set -eu
PARENT=%d
TARGET=%q
SOURCE=%q

# Wait for the running app to exit (up to ~30s).
for i in $(seq 1 60); do
  if ! kill -0 "$PARENT" 2>/dev/null; then break; fi
  sleep 0.5
done

# Atomic-ish swap via ditto: rsync of bundle contents + perms.
BACKUP="${TARGET}.bak.$$"
mv "$TARGET" "$BACKUP" || true
ditto "$SOURCE" "$TARGET"
RC=$?
if [ "$RC" -ne 0 ]; then
  # Rollback on failure.
  rm -rf "$TARGET"
  mv "$BACKUP" "$TARGET"
  exit "$RC"
fi
rm -rf "$BACKUP"

open "$TARGET"
`, parentPID, target, source)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return "", err
	}
	return path, nil
}
