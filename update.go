// update.go — 자동 업데이트 백엔드 (package main)
//
// app.go / main.go를 건드리지 않기 위해 이 파일에 모든 업데이트 로직을 집중한다.
// App.ctx는 app.go가 소유하지만 동일 패키지(main)이므로 직접 접근 가능하다.
// pendingUpdate 상태는 App 필드 추가 없이 package-level 변수로 관리한다.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"cross-livetranslate/internal/updater"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// appVersion is set via -ldflags "-X main.appVersion=x.y.z" at build time.
var appVersion = "0.1.0-dev"

// updaterEndpoint is the URL of the latest.json manifest on GitHub Releases.
var updaterEndpoint = "https://github.com/leonardo204/Cross-liveTranslate/releases/latest/download/latest.json"

// updaterPubKey is the base64-wrapped minisign public key(키 ID 7FD33D7101545F02).
// Cross-liveTranslate 전용 키(무암호 표준 minisign)로, 릴리스 파이프라인(scripts/release.sh)이
// 대응 개인키(~/.tauri/cross-livetranslate.key)로 서명한다. ParsePublicKey가 base64 한 겹을
// 풀면 "untrusted comment:...\n<base64 keyblock>" minisign 공개키 텍스트가 나온다.
var updaterPubKey = "dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWduIHB1YmxpYyBrZXkgN0ZEMzNENzEwMTU0NUYwMgpSV1FDWDFRQmNUM1RmM3I0STg1bTZDTklEcTkvR0dwSFVJMHhuemQrTmNKa0VKWG03WDVJR1RmZwo="

// package-level pending update state — avoids modifying App struct in app.go.
var (
	pendingMu  sync.Mutex
	pendingUpd *pendingUpdateData
)

type pendingUpdateData struct {
	version  string
	platform updater.Platform
}

// UpdateInfo is the data returned to the frontend after CheckUpdate.
type UpdateInfo struct {
	Version        string `json:"version"`
	CurrentVersion string `json:"currentVersion"`
	Body           string `json:"body,omitempty"`
	Available      bool   `json:"available"`
}

// CurrentVersion returns the running application version string.
func (a *App) CurrentVersion() string {
	return appVersion
}

// CheckUpdate downloads the latest.json manifest, compares versions, and
// stages the platform-specific asset info for DownloadAndInstallUpdate.
// Returns UpdateInfo describing whether a newer version is available.
//
// 프론트(설정 창) 바인딩 진입점 — 실제 로직은 checkUpdateWithCtx로 공용화되어
// controller의 자동 주기 체크 goroutine과 코드를 공유한다(중복 구현 금지).
func (a *App) CheckUpdate() (*UpdateInfo, error) {
	return checkUpdateWithCtx(a.ctx)
}

// checkUpdateWithCtx performs the manifest fetch + version compare + pending-asset
// staging for a given context. App.CheckUpdate(설정 창) 및 controller의 자동 주기
// 체크가 이 단일 구현을 공유한다. pendingUpd(package-level)에 스테이징하므로 같은
// 프로세스 안에서 CheckUpdate → DownloadAndInstallUpdate 흐름이 정합한다.
func checkUpdateWithCtx(ctx context.Context) (*UpdateInfo, error) {
	if updaterEndpoint == "" || updaterPubKey == "" {
		return &UpdateInfo{
			Version:        appVersion,
			CurrentVersion: appVersion,
			Available:      false,
		}, nil
	}

	manifest, err := updater.FetchManifest(ctx, updaterEndpoint)
	if err != nil {
		return nil, fmt.Errorf("manifest 조회 실패: %w", err)
	}

	key := updater.PlatformKey()
	plat, ok := manifest.Platforms[key]
	if !ok {
		pendingMu.Lock()
		pendingUpd = nil
		pendingMu.Unlock()
		return &UpdateInfo{
			Version:        manifest.Version,
			CurrentVersion: appVersion,
			Available:      false,
			Body:           fmt.Sprintf("이 OS/아키텍처(%s)용 빌드가 manifest에 없습니다", key),
		}, nil
	}

	available := updater.IsNewer(manifest.Version, appVersion)

	pendingMu.Lock()
	if available {
		pendingUpd = &pendingUpdateData{
			version:  manifest.Version,
			platform: plat,
		}
	} else {
		pendingUpd = nil
	}
	pendingMu.Unlock()

	return &UpdateInfo{
		Version:        manifest.Version,
		CurrentVersion: appVersion,
		Body:           manifest.Notes,
		Available:      available,
	}, nil
}

// DownloadAndInstallUpdate runs the full update pipeline for the staged update:
// download → minisign verify → DMG mount+extract (darwin) / zip extract (windows)
// → swap via detached helper → quit the app so the helper can relaunch it.
func (a *App) DownloadAndInstallUpdate() error {
	pendingMu.Lock()
	pending := pendingUpd
	pendingMu.Unlock()

	if pending == nil {
		return errors.New("스테이징된 업데이트가 없습니다 — 먼저 CheckUpdate를 호출하세요")
	}

	updater.Logf("DownloadAndInstallUpdate start version=%s -> %s", appVersion, pending.version)

	pubkey, err := updater.ParsePublicKey(updaterPubKey)
	if err != nil {
		updater.Logf("ParsePublicKey FAILED: %v", err)
		return fmt.Errorf("pubkey 파싱 실패: %w", err)
	}

	dir, err := updater.DownloadAndExtract(a.ctx, pending.platform, pubkey)
	if err != nil {
		return fmt.Errorf("다운로드/추출 실패: %w", err)
	}

	if err := updater.SwapAndRelaunch(dir); err != nil {
		updater.Logf("SwapAndRelaunch FAILED: %v", err)
		return fmt.Errorf("교체 실패: %w", err)
	}
	updater.Logf("swap helper launched; quitting app in 300ms")

	// Give the detached helper a moment to start before we quit.
	go func() {
		time.Sleep(300 * time.Millisecond)
		if a.ctx != nil {
			wailsruntime.Quit(a.ctx)
		}
		// Safety net: the swap helper waits for THIS process to exit before it
		// can replace the (locked) running .exe. If Quit() did not fully
		// terminate us, force exit so the file lock is released and the helper
		// can finish the swap + relaunch.
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}()
	return nil
}
