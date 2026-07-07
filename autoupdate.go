// autoupdate.go — controller 소유 자동 주기 업데이트 확인(원본 Sparkle 패리티).
//
// 원본 liveTranslate는 Sparkle의 Info.plist `SUEnableAutomaticChecks=true` +
// `SUScheduledCheckInterval=86400`(24h)으로 앱 실행 시 + 24시간 주기 자동 확인을 하고,
// 사용자 설정 토글(`automaticallyChecksForUpdates`)로 on/off 한다. 이 파일은 그 동작을
// controller 프로세스(파이프라인·트레이 소유, 단일 소유점)에서 재현한다.
//
// 확인 로직 자체는 update.go의 checkUpdateWithCtx를 재사용한다(중복 구현 금지). 발견 시
// pendingUpd가 스테이징되고, 사용자가 HUD 배지/설정 창 버튼으로 App.DownloadAndInstallUpdate를
// 트리거한다. 실제 릴리스(latest.json)가 아직 없으므로 조회 실패는 조용히 로그만 남기고
// 다음 주기에 재시도한다(사용자 에러 스팸 금지).
package main

import (
	"time"

	"cross-livetranslate/internal/updater"
)

const (
	// autoUpdateInterval mirrors 원본 SUScheduledCheckInterval=86400(24h).
	autoUpdateInterval = 24 * time.Hour
	// autoUpdateInitialDelay는 앱 실행 직후 네트워크/UI가 안정된 뒤 최초 1회 확인하도록
	// 약간의 지연을 둔다(원본은 launch 시 확인 — 여기선 UI 뜬 직후 스팸 방지용 지연).
	autoUpdateInitialDelay = 8 * time.Second
)

// autoUpdateLoop runs the periodic auto-update check on the controller process.
// 앱 시작 후 autoUpdateInitialDelay 뒤 1회, 이후 autoUpdateInterval(24h) 주기로 확인한다.
// AutoCheck가 꺼져 있으면 각 wake에서 스스로 skip(주기 체크 중단), 설정에서 다시 켜지면
// reloadSettings가 autoUpdateReload로 loop를 깨워 즉시 확인한다(스케줄 재개).
func (c *Controller) autoUpdateLoop() {
	timer := time.NewTimer(autoUpdateInitialDelay)
	defer timer.Stop()
	ticker := time.NewTicker(autoUpdateInterval)
	defer ticker.Stop()

	done := func() <-chan struct{} {
		if c.ctx == nil {
			return nil
		}
		return c.ctx.Done()
	}()

	for {
		select {
		case <-done:
			return
		case <-timer.C:
			c.maybeAutoCheck()
		case <-ticker.C:
			c.maybeAutoCheck()
		case <-c.autoUpdateReload:
			// 설정에서 자동확인이 새로 켜졌다(off→on) — 곧바로 한 번 확인한다.
			c.maybeAutoCheck()
		}
	}
}

// maybeAutoCheck performs one auto-update check if AutoCheck is enabled, then
// updates the HUD update badge state. 조회 실패는 조용히 로그만(다음 주기 재시도).
func (c *Controller) maybeAutoCheck() {
	c.mu.Lock()
	on := c.settings.Update.AutoCheck
	c.mu.Unlock()
	if !on {
		return
	}

	info, err := checkUpdateWithCtx(c.ctx)
	if err != nil {
		// 릴리스 manifest가 아직 없거나 네트워크 실패 — 조용히 로그, 다음 주기 재시도.
		updater.Logf("auto update check failed (will retry next cycle): %v", err)
		return
	}

	available := info != nil && info.Available
	version := ""
	if available {
		version = info.Version
	}

	c.mu.Lock()
	changed := c.updateAvailable != available || c.updateVersion != version
	c.updateAvailable = available
	c.updateVersion = version
	c.mu.Unlock()

	if available {
		updater.Logf("auto update available: %s (current %s)", version, appVersion)
	}
	// 상태가 바뀐 경우에만 HUD를 갱신해 불필요한 이벤트를 줄인다.
	if changed {
		c.emitHUD()
	}
}

// signalAutoUpdateReload wakes autoUpdateLoop to check immediately (non-blocking).
// 설정에서 자동확인이 새로 켜졌을 때 reloadSettings/SaveSettings가 호출한다.
func (c *Controller) signalAutoUpdateReload() {
	select {
	case c.autoUpdateReload <- struct{}{}:
	default:
	}
}
