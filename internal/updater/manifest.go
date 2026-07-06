// Package updater implements self-update: fetch latest.json,
// verify minisign Ed25519 signature, download .dmg (macOS),
// mount/extract and replace the running .app bundle, then relaunch.
// The pubkey + endpoint constants are wired in update.go (package main)
// and passed in, so this package stays independent of release-specific config.
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Platform mirrors the per-OS entry in latest.json.
type Platform struct {
	Signature string `json:"signature"`
	URL       string `json:"url"`
}

// Manifest is the top-level latest.json document.
type Manifest struct {
	Version   string              `json:"version"`
	Notes     string              `json:"notes,omitempty"`
	PubDate   time.Time           `json:"pub_date,omitempty"`
	Platforms map[string]Platform `json:"platforms"`
}

// FetchManifest downloads and parses latest.json from the given endpoint.
func FetchManifest(ctx context.Context, endpoint string) (*Manifest, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("updater endpoint가 비어있습니다")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("manifest 다운로드 실패: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("manifest HTTP %d: %s", resp.StatusCode, string(body))
	}
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest 파싱 실패: %w", err)
	}
	return &m, nil
}

// PlatformKey returns the OS/arch key for the latest.json platforms map.
// darwin-aarch64, darwin-x86_64, windows-x86_64, linux-x86_64 등을 반환한다.
func PlatformKey() string {
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "darwin-aarch64"
		}
		return "darwin-x86_64"
	case "windows":
		if runtime.GOARCH == "arm64" {
			return "windows-aarch64"
		}
		return "windows-x86_64"
	case "linux":
		if runtime.GOARCH == "arm64" {
			return "linux-aarch64"
		}
		return "linux-x86_64"
	}
	return ""
}

// IsNewer returns true when remote is strictly greater than current using
// a simple dotted-numeric compare (semver-like but ignores pre-release suffixes).
func IsNewer(remote, current string) bool {
	a := splitDigits(remote)
	b := splitDigits(current)
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		ai, bi := at(a, i), at(b, i)
		if ai > bi {
			return true
		}
		if ai < bi {
			return false
		}
	}
	return false
}

// IsPortable returns false on non-Windows; kept for potential future use.
func IsPortable() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return true
	}
	exe = strings.ToLower(filepath.Clean(exe))
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		if strings.HasPrefix(exe, strings.ToLower(filepath.Clean(base))+string(filepath.Separator)) {
			return false
		}
	}
	return true
}

func splitDigits(v string) []int {
	out := []int{0}
	cur := 0
	started := false
	for _, c := range v {
		if c >= '0' && c <= '9' {
			cur = cur*10 + int(c-'0')
			started = true
			continue
		}
		if started {
			out[len(out)-1] = cur
			out = append(out, 0)
			cur = 0
			started = false
		}
	}
	if started {
		out[len(out)-1] = cur
	}
	return out
}

func at(v []int, i int) int {
	if i >= len(v) {
		return 0
	}
	return v[i]
}
