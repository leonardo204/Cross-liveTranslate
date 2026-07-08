package main

// settings.go — U1: 설정 창(`-role settings`) 프로세스의 백엔드.
//
// 원본 liveTranslate는 제어 HUD(FloatingPanel)와 설정 창(NSWindow 760×580,
// "liveTranslate 설정")이 완전히 별개의 창이다. Wails는 프로세스당 단일 WebviewWindow만
// 허용하므로, 단일 바이너리를 `-role settings`로 재실행해 설정 창을 별도 프로세스로 띄운다.
//
// 프로세스 관계(specs/013-ui-parity-rewrite.md):
//   - controller(메인)가 settings 자식을 StartHidden으로 spawn·감독한다.
//   - 트레이 "설정…" 또는 HUD 설정 버튼 → controller.ShowSettings() → settings 자식 stdin으로
//     control("show") 신호 → settings가 runtime.WindowShow.
//   - 설정 단일 소스는 settings.json(파일). settings 프로세스가 config.Load/Save를 직접 하고
//     (원자적 쓰기), 저장 시 stdout으로 control("changed")를 controller에 알린다 → controller가
//     reload+반영(provider/selection/audio/style). 파일이 단일 소스라 프로세스 분리에도 정합.
//
// 이 파일은 SettingsAPI(설정 창 프론트가 호출하는 바인딩) + 제어 채널(stdin control 수신,
// stdout "changed" 송신)을 정의한다. U1은 계약 확정이 목표이며, 픽셀 정합 폼은 U3에서 재작성한다.

import (
	"context"
	"os"
	"sync"
	"time"

	"cross-livetranslate/internal/audio"
	"cross-livetranslate/internal/config"
	"cross-livetranslate/internal/display"
	"cross-livetranslate/internal/ipc"
	"cross-livetranslate/internal/permission"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ModelInfo describes a selectable translation model (설정 창 모델 카탈로그).
// U1은 Gemini Live 1개만 노출한다(온디바이스 경로 미지원 — specs/000).
type ModelInfo struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Engine          string `json:"engine"`
	ModelIdentifier string `json:"modelIdentifier"`
}

// PermissionInfo carries OS permission states for the 권한 카테고리.
// Microphone은 AVCaptureDevice 실측(permission.MicStatus 문자열), ScreenRecording은
// 사전 조회 API가 없어 항상 "unknown"(원본 systemAudio와 동일).
type PermissionInfo struct {
	Microphone string `json:"microphone"` // notDetermined|authorized|denied|restricted|unknown
	// SystemAudio: 시스템 오디오 캡처(Core Audio Process Tap) 권한/가용성.
	// authorized(사용 가능) | denied(권한 필요) | restricted(macOS 14.4 미만 미지원).
	// tap 경량 프로브(audio.SystemTapStatus)로 실측한다.
	SystemAudio string `json:"systemAudio"`
	// ScreenRecording: 하위호환 유지(프론트 구버전). SystemAudio와 동일 값을 채운다.
	ScreenRecording string `json:"screenRecording"`
}

// SettingsAPI is the Wails-bound struct for the settings window
// (frontend: window.go.main.SettingsAPI.<Method>). 설정 창 프론트(U3)가 사용하는
// 완전한 바인딩 계약을 정의한다. 파이프라인은 소유하지 않으며(그것은 controller),
// 설정 파일 Load/Save + API 키 + 디바이스/모델/버전 조회만 담당한다.
type SettingsAPI struct {
	ctx  context.Context
	app  *App       // 버전/업데이트 위임용(App도 이 프로세스에 바인드됨).
	out  *os.File   // controller가 읽는 제어 채널(stdout). control 신호 송신.
	outM sync.Mutex // out(stdout) 단일 writer 규율 — 바인딩 메서드들이 서로 다른 goroutine에서
	// 호출될 수 있어 control 라인의 인터리브를 막는다.
}

// newSettingsAPI creates the settings-window binding backed by stdout control.
func newSettingsAPI(app *App) *SettingsAPI {
	return &SettingsAPI{app: app, out: os.Stdout}
}

// setCtx captures the Wails runtime context (OnStartup).
func (s *SettingsAPI) setCtx(ctx context.Context) { s.ctx = ctx }

// notifyChanged tells the controller (our parent) that settings.json changed so
// it reloads + applies (provider/selection/audio/style). Best-effort(로그 없음 —
// controller가 죽었으면 stdout write가 실패해도 무해).
func (s *SettingsAPI) notifyChanged() {
	s.sendControl("changed")
}

// sendControl writes one control command to the controller (stdout) under outM
// (단일 writer 규율). Best-effort — controller가 죽었으면 write 실패해도 무해.
func (s *SettingsAPI) sendControl(cmd string) {
	if s.out == nil {
		return
	}
	s.outM.Lock()
	defer s.outM.Unlock()
	_ = ipc.WriteControl(s.out, ipc.ControlMsg{Cmd: cmd})
}

// SetTestSubtitle toggles the '테스트 자막 표시'(고정 미리보기) preview. 설정 파일에
// 저장하지 않는 일시 상태이며(앱 재시작 시 off), controller에 test-subtitle-on/off control을
// 보내 오버레이에 샘플 자막을 표시/해제하게 한다. 실제 표시 여부(번역 중이면 무시)는
// controller가 판정한다(원본 AppState.setTestSubtitle의 !isRunning 가드 이식).
func (s *SettingsAPI) SetTestSubtitle(on bool) {
	if on {
		s.sendControl("test-subtitle-on")
	} else {
		s.sendControl("test-subtitle-off")
	}
}

// -----------------------------------------------------------------------------
// Settings + API-key (U3 폼이 사용하는 계약)
// -----------------------------------------------------------------------------

// GetSettings returns the current persisted settings (파일 단일 소스에서 로드).
func (s *SettingsAPI) GetSettings() config.Settings {
	cfg, err := config.Load()
	if err != nil {
		return config.DefaultSettings()
	}
	return cfg
}

// SaveSettings validates, persists settings.json, and signals the controller to
// reload+apply. 반환 에러는 저장 실패 시에만.
func (s *SettingsAPI) SaveSettings(cfg config.Settings) error {
	cfg = normalizeSettings(cfg)
	if err := cfg.Save(); err != nil {
		return err
	}
	s.notifyChanged()
	return nil
}

// SaveAPIKey stores(or clears, when empty) the Gemini API key in the OS keyring.
// 키는 settings.json이 아니라 keyring에만 저장된다. controller가 키 상태를 갱신하도록 신호.
func (s *SettingsAPI) SaveAPIKey(key string) error {
	if err := config.SaveAPIKey(key); err != nil {
		return err
	}
	s.notifyChanged()
	return nil
}

// TestAPIKey verifies a key and returns "" on success or a user-facing(키 비포함)
// error message. 키 값은 결코 로그/반환값에 노출하지 않는다.
func (s *SettingsAPI) TestAPIKey(key string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := config.TestAPIKey(ctx, key); err != nil {
		return err.Error()
	}
	return ""
}

// HasAPIKey reports whether a usable Gemini API key exists (env 또는 keyring).
func (s *SettingsAPI) HasAPIKey() bool { return config.HasAPIKey() }

// KeySourceLabel returns a human-readable API-key source (환경변수/키체인/없음).
func (s *SettingsAPI) KeySourceLabel() string {
	if os.Getenv(config.APIKeyEnv) != "" {
		return "환경변수"
	}
	if config.HasAPIKey() {
		return "키체인"
	}
	return "없음"
}

// -----------------------------------------------------------------------------
// Devices / models / permissions / version (U3 폼이 사용하는 계약)
// -----------------------------------------------------------------------------

// ListInputDevices enumerates capture devices for the 입력 카테고리 picker.
func (s *SettingsAPI) ListInputDevices() []audio.DeviceInfo {
	devs, err := audio.EnumerateDevices()
	if err != nil {
		return []audio.DeviceInfo{}
	}
	return devs
}

// ListOutputDevices enumerates output devices for the 오디오 카테고리 picker.
// U1: 출력 디바이스 열거 백엔드가 아직 없어 빈 목록을 반환한다(시스템 기본 출력 사용).
func (s *SettingsAPI) ListOutputDevices() []audio.DeviceInfo {
	return []audio.DeviceInfo{}
}

// RefreshDevices is a hook for the UI to force device re-enumeration.
// U1은 열거가 무상태(매 호출 실측)라 별도 캐시 무효화가 필요 없어 no-op이다.
func (s *SettingsAPI) RefreshDevices() {}

// ListScreens enumerates connected monitors for the 자막 "표시 화면" picker so it
// can show real display names(예: "Built-in Retina Display") instead of index
// labels. 반환 Index는 overlay.Apply(monitorIndex) 및 Position.MonitorIndex와
// 동일한 [NSScreen screens] 순서라, 사용자가 고른 화면에 오버레이가 정확히 뜬다.
// 열거 실패/미지원 플랫폼이면 빈 목록을 반환한다(프론트는 "자동 (주 화면)"만 표시).
func (s *SettingsAPI) ListScreens() []display.ScreenInfo {
	screens, err := display.ListScreens()
	if err != nil || screens == nil {
		return []display.ScreenInfo{}
	}
	return screens
}

// OpenSystemSettings opens the macOS Privacy & Security pane for the given
// permission (원본 PermissionHelper deep link 이식). pane: "microphone" |
// "screencapture" | "privacy". NSWorkspace openURL을 쓰는 permission.OpenPrivacyPane에
// 위임한다 — 서명된 번들 안에서 exec("open")보다 확실히 열린다(원본과 동일 경로).
// 비-darwin OS에서는 no-op(마이크/화면 캡처 pane은 macOS 전용 개념).
func (s *SettingsAPI) OpenSystemSettings(pane string) {
	permission.OpenPrivacyPane(pane)
}

// Models returns the model catalog (U1: Gemini Live 1개).
func (s *SettingsAPI) Models() []ModelInfo {
	return []ModelInfo{
		{
			ID:              "gemini-3.5-live-translate",
			Name:            "Gemini 3.5 Live Translate",
			Engine:          "geminiLive",
			ModelIdentifier: config.GeminiModel,
		},
	}
}

// PermissionStatus returns OS permission states (원본 PermissionHelper 실이식).
//   - Microphone: AVCaptureDevice 실측 상태(notDetermined|authorized|denied|
//     restricted|unknown). 프론트가 한국어 라벨(미요청/허용됨/거부됨/제한됨/확인불가)로 매핑.
//   - ScreenRecording: 원본 시스템 오디오 캡처와 동일하게 사전 조회 API가 없어 항상
//     "unknown"(첫 캡처 시 OS 프롬프트). 무상태 조회라 프론트가 재호출하면 최신값이 반영된다.
func (s *SettingsAPI) PermissionStatus() PermissionInfo {
	sysAudio := audio.SystemTapStatus()
	return PermissionInfo{
		Microphone:      string(permission.MicrophoneStatus()),
		SystemAudio:     sysAudio,
		ScreenRecording: sysAudio, // 하위호환(구 프론트가 screenRecording 참조).
	}
}

// CurrentVersion returns the running application version (설정 일반 카테고리).
func (s *SettingsAPI) CurrentVersion() string { return appVersion }

// InstallUpdate requests the controller(본체) to run the self-update install. 설정
// 프로세스에서 직접 App.DownloadAndInstallUpdate를 부르면 settings만 종료되고 본체가
// 살아남아 스왑/재실행이 실패한다. control("install-update")로 controller에 위임해
// 앱 전체가 종료·교체·재실행되게 한다.
func (s *SettingsAPI) InstallUpdate() { s.sendControl("install-update") }

// CheckUpdate delegates to the shared updater (App.CheckUpdate) so the settings
// window can trigger an update check without duplicating the pipeline.
func (s *SettingsAPI) CheckUpdate() (*UpdateInfo, error) {
	return s.app.CheckUpdate()
}

// ResetAll restores default settings(파일)을 되돌리고, includeAPIKey면 keyring 키도 삭제한다.
// 저장 후 controller에 reload 신호를 보낸다.
func (s *SettingsAPI) ResetAll(includeAPIKey bool) error {
	def := config.DefaultSettings()
	if err := def.Save(); err != nil {
		return err
	}
	if includeAPIKey {
		if err := config.SaveAPIKey(""); err != nil {
			return err
		}
	}
	s.notifyChanged()
	return nil
}

// -----------------------------------------------------------------------------
// control channel (stdin: controller → settings, stdout: settings → controller)
// -----------------------------------------------------------------------------

// runSettingsControlLoop reads control commands from the controller (stdin) and
// applies them to the settings window: show/hide/quit. stdin EOF(controller 종료)면
// 설정 프로세스도 종료한다. 별도 goroutine에서 구동한다.
func runSettingsControlLoop(ctx context.Context) {
	ipc.Dispatch(os.Stdin, ipc.Handler{
		OnControl: func(m ipc.ControlMsg) {
			switch m.Cmd {
			case "show":
				wruntime.WindowShow(ctx)
				wruntime.WindowUnminimise(ctx)
				wruntime.WindowSetAlwaysOnTop(ctx, true)
				wruntime.WindowSetAlwaysOnTop(ctx, false)
			case "hide":
				wruntime.WindowHide(ctx)
			case "quit":
				wruntime.Quit(ctx)
			case "running-on":
				// 번역 실행 중 — '테스트 자막 표시' 토글을 비활성화하도록 프론트에 통지.
				wruntime.EventsEmit(ctx, "running:update", true)
			case "running-off":
				// 번역 정지 — '테스트 자막 표시' 토글을 다시 활성화하도록 프론트에 통지.
				wruntime.EventsEmit(ctx, "running:update", false)
			}
		},
	})
	// stdin closed → parent(controller) exited. Tear down this child.
	wruntime.Quit(ctx)
}
