package updater

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DownloadAndExtract fetches the platform asset, verifies the minisign
// signature, and extracts or mounts the asset into a fresh temp directory.
//
// Supported asset formats:
//   - .dmg  (darwin only)  — mounted via hdiutil, .app extracted with ditto
//   - .zip                 — extracted as-is (Windows portable / NSIS installer)
//   - .exe / .msi          — saved directly (Windows installer)
//
// The returned path is a temp directory containing the ready-to-swap .app
// bundle (darwin) or installer executable (windows).
func DownloadAndExtract(ctx context.Context, p Platform, pubkey *PublicKey) (string, error) {
	Logf("download start url=%s", p.URL)
	body, err := fetchBytes(ctx, p.URL)
	if err != nil {
		Logf("download FAILED: %v", err)
		return "", fmt.Errorf("자산 다운로드 실패: %w", err)
	}
	Logf("downloaded %d bytes", len(body))
	if err := VerifyContent(body, p.Signature, pubkey); err != nil {
		Logf("verify FAILED: %v", err)
		return "", fmt.Errorf("서명 검증 실패: %w", err)
	}
	Logf("signature verified")

	dir, err := os.MkdirTemp("", "cross-livetranslate-update-*")
	if err != nil {
		return "", fmt.Errorf("temp 디렉토리 생성 실패: %w", err)
	}

	lower := strings.ToLower(p.URL)
	switch {
	case strings.HasSuffix(lower, ".dmg"):
		// DMG 마운트 → .app 추출 (darwin 전용 구현 / 비-darwin stub)
		if err := extractDMG(body, dir); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("DMG 처리 실패: %w", err)
		}
	case strings.HasSuffix(lower, ".zip"):
		if err := extractZip(body, dir); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("zip 압축 해제 실패: %w", err)
		}
	case strings.HasSuffix(lower, ".exe") || strings.HasSuffix(lower, ".msi"):
		assetPath := filepath.Join(dir, filepath.Base(p.URL))
		if err := os.WriteFile(assetPath, body, 0o644); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("자산 저장 실패: %w", err)
		}
	default:
		os.RemoveAll(dir)
		return "", fmt.Errorf("지원하지 않는 자산 포맷: %s", p.URL)
	}
	Logf("extracted to %s", dir)
	return dir, nil
}

// FindAppBundle returns the first `*.app` directory inside root, recursively.
func FindAppBundle(root string) (string, error) {
	var found string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && strings.HasSuffix(info.Name(), ".app") {
			found = path
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("추출 결과에서 .app 번들을 찾지 못했습니다 (%s)", root)
	}
	return found, nil
}

// FindWindowsInstaller returns the first `*.exe` or `*.msi` under root.
func FindWindowsInstaller(root string) (string, error) {
	var found string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := strings.ToLower(info.Name())
		if strings.HasSuffix(name, ".exe") || strings.HasSuffix(name, ".msi") {
			found = path
			return io.EOF // sentinel to stop early
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("추출 결과에서 인스톨러(.exe/.msi)를 찾지 못했습니다 (%s)", root)
	}
	return found, nil
}

func extractZip(data []byte, dest string) error {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, f := range r.File {
		clean := filepath.Clean(f.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("의심스러운 경로: %s", f.Name)
		}
		target := filepath.Join(dest, clean)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
	}
	return nil
}

func fetchBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
