//go:build darwin

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// extractDMG writes the DMG bytes to a temp file, mounts it with hdiutil,
// locates the .app bundle inside the mounted volume, copies it with ditto
// into dest, then detaches the volume.
//
// After this call, dest will contain a *.app directory that SwapAndRelaunch
// can use directly.
func extractDMG(dmgBytes []byte, dest string) error {
	// 1. Write DMG to a temp file.
	dmgFile, err := os.CreateTemp("", "cross-livetranslate-*.dmg")
	if err != nil {
		return fmt.Errorf("DMG 임시파일 생성 실패: %w", err)
	}
	dmgPath := dmgFile.Name()
	if _, err := dmgFile.Write(dmgBytes); err != nil {
		dmgFile.Close()
		os.Remove(dmgPath)
		return fmt.Errorf("DMG 파일 쓰기 실패: %w", err)
	}
	dmgFile.Close()
	defer os.Remove(dmgPath)

	// 2. Create a temp mount point.
	mountPoint, err := os.MkdirTemp("", "cross-livetranslate-mount-*")
	if err != nil {
		return fmt.Errorf("마운트 포인트 생성 실패: %w", err)
	}
	// Best-effort cleanup of mount point directory.
	defer os.RemoveAll(mountPoint)

	// 3. Mount the DMG.
	attachCmd := exec.Command(
		"hdiutil", "attach",
		"-nobrowse",
		"-noverify",
		"-noautoopen",
		dmgPath,
		"-mountpoint", mountPoint,
	)
	if out, err := attachCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("DMG 마운트 실패: %w\n%s", err, string(out))
	}

	// 4. Ensure detach happens on return (force if necessary).
	defer func() {
		detachCmd := exec.Command("hdiutil", "detach", mountPoint)
		if err := detachCmd.Run(); err != nil {
			// Force detach on failure.
			_ = exec.Command("hdiutil", "detach", "-force", mountPoint).Run()
		}
	}()

	// 5. Find the .app bundle inside the mounted volume.
	appPath, err := findAppInDir(mountPoint)
	if err != nil {
		return fmt.Errorf("마운트된 DMG에서 .app을 찾지 못했습니다: %w", err)
	}

	// 6. Copy the .app bundle into dest using ditto (preserves metadata/perms).
	destApp := filepath.Join(dest, filepath.Base(appPath))
	dittoCmd := exec.Command("ditto", appPath, destApp)
	if out, err := dittoCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ditto 복사 실패: %w\n%s", err, string(out))
	}

	return nil
}

// findAppInDir locates the first *.app directory directly inside root
// (top-level only — DMG bundles always place the .app at the volume root).
// Falls back to a recursive search for non-standard layouts.
func findAppInDir(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	// Top-level first (standard DMG layout).
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".app") {
			return filepath.Join(root, e.Name()), nil
		}
	}
	// Recursive fallback.
	return FindAppBundle(root)
}
