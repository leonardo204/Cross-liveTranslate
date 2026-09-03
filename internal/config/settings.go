package config

// settings.go — Wave 1(A1): 전체 사용자 설정 모델 + JSON 영속.
//
// 원본 이식: liveTranslate/Sources/Settings/SettingsStore.swift(키 그룹·기본값·결정성)
// + Overlay/SubtitleStyle.swift(자막 스타일 기본값). 모든 기본값은 원본에서 실제로 읽어
// 반영했다(Date/난수 없는 결정적 상수).
//
// 영속 위치: os.UserConfigDir()/Cross-liveTranslate/settings.json (원자적 temp+rename).
// 색은 sRGB 8bit "#RRGGBBAA" 문자열. **API 키는 이 JSON에 저장하지 않는다**(Keychain 전용).
//
// 이 파일은 순수 패키지 규약을 지킨다(cgo 없음 → windows 크로스빌드 가능).

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
)

// configDirName is the per-user config subdirectory for this app.
const configDirName = "Cross-liveTranslate"

// settingsFileName is the JSON file that holds all persisted user settings.
const settingsFileName = "settings.json"

// configDirOverride, when non-empty, replaces os.UserConfigDir() for locating
// settings.json. 테스트에서 격리된 임시 디렉토리를 주입하기 위한 훅(프로덕션은 빈 값).
var configDirOverride string

// ModelSettings holds the Gemini 모델 식별자 선택(소프트코딩 오버라이드).
// ID가 비면 config.GeminiModel 폴백 기본값을 쓴다(설정 UI 미노출 — settings.json 직접 편집).
type ModelSettings struct {
	ID string `json:"id"` // Gemini 모델 식별자(models/...). ""이면 GeminiModel 기본값 사용.
}

// LanguageSettings holds translation language selection (원본 SettingsStore 언어 그룹).
type LanguageSettings struct {
	Target     string `json:"target"`     // 번역 대상 언어(BCP-47), 기본 "ko".
	Source     string `json:"source"`     // 소스 언어("auto"=서버 자동 감지), 기본 "auto".
	ShowSource bool   `json:"showSource"` // 원문 동시 표시(기본 false).
}

// InputSettings holds the capture source selection (원본 input.selection.*).
type InputSettings struct {
	Mode     string `json:"mode"`     // auto|mic|loopback|device
	DeviceID string `json:"deviceID"` // Mode=="device"일 때만 의미 있음.
}

// SubtitleSettings holds subtitle rendering style (원본 subtitle.style.* / SubtitleStyle.swift).
// 이번 웨이브는 모델·영속만 담당하고, 실제 렌더 반영은 Wave 2(A2)에서 IPC로 오버레이에 전달한다.
type SubtitleSettings struct {
	FontFamily    string  `json:"fontFamily"`    // ""=시스템 rounded.
	FontSize      float64 `json:"fontSize"`      // pt, UI 16..72, 기본 34.
	FontWeight    string  `json:"fontWeight"`    // regular|medium|semibold|bold|heavy|black.
	TextColor     string  `json:"textColor"`     // #RRGGBBAA.
	AltTextColor  string  `json:"altTextColor"`  // #RRGGBBAA — 화자 교대 2색 중 보조 색(설정 UI 미노출).
	StrokeEnabled bool    `json:"strokeEnabled"` // 외곽선(그림자) 사용.
	StrokeColor   string  `json:"strokeColor"`   // #RRGGBBAA.
	StrokeWidth   float64 `json:"strokeWidth"`   // 외곽선 두께 힌트(원본은 고정 다중그림자, Wave2 사용).
	GlowEnabled   bool    `json:"glowEnabled"`   // 글로우 사용(기본 off).
	GlowColor     string  `json:"glowColor"`     // #RRGGBBAA.
	GlowRadius    float64 `json:"glowRadius"`    // UI 0..30, 기본 8.
	BgEnabled     bool    `json:"bgEnabled"`     // 배경 박스 사용.
	BgColor       string  `json:"bgColor"`       // #RRGGBBAA(원본은 검정+opacity).
	BgOpacity     float64 `json:"bgOpacity"`     // 0..1, 기본 0.35.
	Align         string  `json:"align"`         // leading|center|trailing.
	MaxLines      int     `json:"maxLines"`      // UI 1..4, 기본 2.

	// SpeakerColorAlternate 는 화자 전환 근사(turnComplete/무음 2초) 2색 교대 표시의
	// 숨은 스위치다(기본 true). 설정 UI에는 노출하지 않는다(원본 UI 100% 재현 원칙) —
	// settings.json을 직접 편집해 false로 두면 기존 단색 표시로 돌아간다.
	SpeakerColorAlternate bool `json:"speakerColorAlternate"`
}

// PositionSettings holds subtitle placement (원본 subtitle.screenID + verticalPosition
// + verticalOffset). 세로 위치 = 영역(top/middle/bottom) + 영역 내 세부위치(offset 0~1)의
// 조합이다(원본 SubtitleOverlayView: t=(areaIdx+offset)/3).
type PositionSettings struct {
	MonitorIndex int     `json:"monitorIndex"` // 0=주 화면.
	Vertical     string  `json:"vertical"`     // top|middle|bottom, 기본 bottom.
	Offset       float64 `json:"offset"`       // 영역 내 세부 세로위치 0(위)~1(아래), 기본 0.5.
}

// AudioSettings holds translated-audio playback + ducking (원본 audio.playback/duck.*).
type AudioSettings struct {
	PlaybackEnabled bool    `json:"playbackEnabled"` // 번역 오디오 재생(기본 false).
	OutputDeviceID  string  `json:"outputDeviceID"`  // ""=시스템 기본 출력.
	SoftVolume      float64 `json:"softVolume"`      // 0..1, 기본 1.0.
	DuckEnabled     bool    `json:"duckEnabled"`     // 원문 덕킹(기본 true).
	DuckVolume      float64 `json:"duckVolume"`      // 0..1, 기본 0.3.
}

// CostSettings holds session/cumulative cost HUD state (원본 cost.*).
type CostSettings struct {
	HUDEnabled    bool    `json:"hudEnabled"`    // 비용 HUD 표시(기본 true).
	CumulativeUSD float64 `json:"cumulativeUSD"` // 누적 비용(USD, 영속).
}

// RecordingSettings holds subtitle-recording output location (원본 recording.directory).
type RecordingSettings struct {
	Directory string `json:"directory"` // 기본 사용자 Documents.
}

// VADSettings holds voice-activity-detection gate toggle (Wave 3에서 배선).
type VADSettings struct {
	Enabled bool `json:"enabled"` // 기본 false(미배선 — Wave 3에서 활성).
}

// 엔진 튜닝 기본값/마이그레이션 상수.
const (
	// DefaultAudioBoundarySilenceMs 는 오디오 도메인 화자 경계 임계 기본값(ms).
	DefaultAudioBoundarySilenceMs = 400
	// legacyAudioBoundarySilenceMs 는 이전 기본값이다. 이 키는 설정 UI에 노출된 적이 없어
	// 사용자가 직접 넣었을 수 없고(설정 창이 전체 구조를 되저장하면서 기본값이 파일에
	// 박힌다), 그 값이 그대로 남아 있으면 새 기본값이 영영 적용되지 않는다.
	// 그래서 Load 시 **옛 기본값과 정확히 같을 때만** 새 기본값으로 1회 마이그레이션한다.
	legacyAudioBoundarySilenceMs = 700
)

// EngineSettings holds subtitle-engine 튜닝값이다. 설정 UI에 노출하지 않는 숨은 설정만
// 모으며(스타일이 아니므로 subtitle 블록과 분리 — STYLE_DEFAULT 동기화 대상이 아니다),
// settings.json을 직접 편집해 조정한다.
type EngineSettings struct {
	// TurnBoundarySilenceMs 는 델타 갭이 이만큼(ms) 벌어지면 발화(턴)가 끊긴 것으로 보고
	// 자막 줄을 확정 + 화자 색을 교대하는 임계다(기본 1000).
	//
	// 근거(실측): gemini-3.5-live-translate는 turnComplete를 보내지 않아 경계 신호가 없다.
	// 수신 델타 간격은 연속 발화 중 0.8~0.9s에 몰리고 실제 발화 경계는 1.0~1.7s에 나타나
	// 0.9s/1.0s 사이에 절벽이 있다.
	//
	// **0 이하 = 이 트리거 비활성**(기존 2초 무음 확정 동작으로 폴백). 키가 아예 없으면
	// Load()가 DefaultSettings() 위에 덮어쓰므로 기본 1000이 유지된다(0으로 죽지 않는다).
	TurnBoundarySilenceMs int `json:"turnBoundarySilenceMs"`

	// AudioBoundarySilenceMs 는 **오디오 도메인** 화자 경계 임계다(기본 700).
	// 캡처 오디오의 실무음이 이만큼(ms) 지속된 뒤 새 발화가 시작되면 화자가 바뀐 것으로 보고
	// 자막 줄을 확정 + 색을 교대한다. 텍스트 델타 갭(turnBoundarySilenceMs)과 달리 서버
	// 스트리밍 주기·원문/번역 인터리빙의 영향을 받지 않아 1차 트리거로 쓴다.
	//
	// **0 이하 = 오디오 경계 관찰 비활성**(델타 갭 트리거만 남는다). 키가 없으면 기본값 유지.
	AudioBoundarySilenceMs int `json:"audioBoundarySilenceMs"`

	// QuestionBoundary 는 "물음표로 끝난 줄 뒤에 새 델타가 오면 질문→답변 전환" 휴리스틱
	// 스위치다(기본 true, 설정 UI 미노출). 인터뷰/대화에서 오디오 무음만으로는 놓치는
	// 화자 교대를 텍스트로 보완한다. 키가 없으면 Load가 기본값(true) 위에 덮어쓰므로 켜진 상태다.
	QuestionBoundary bool `json:"questionBoundary"`
}

// UpdateSettings holds auto-update-check preference (원본 Sparkle SUEnableAutomaticChecks).
// 원본 liveTranslate는 Info.plist SUEnableAutomaticChecks=true + SUScheduledCheckInterval=86400
// 으로 앱 실행 시 + 24시간 주기 자동 확인을 하며, 사용자 토글(automaticallyChecksForUpdates)로
// on/off 한다. 이 필드가 그 토글을 영속한다.
type UpdateSettings struct {
	AutoCheck bool `json:"autoCheck"` // 자동 주기 업데이트 확인(원본 기본 true).
}

// HUDSettings holds the control-HUD window visibility (트레이 "제어 HUD 표시" 토글).
//
// 원본 liveTranslate는 HUDController.isVisible을 영속하지 않고 항상 숨김으로 시작하지만,
// Windows는 macOS 메뉴바만큼 트레이가 눈에 띄지 않아 "앱이 실행됐는지" 알기 어렵다.
// 그래서 마지막 표시 상태를 저장했다가 다음 실행에서 복원한다(기본 true = 표시).
type HUDSettings struct {
	Visible bool `json:"visible"` // 제어 HUD 창 표시 여부(기본 true).
}

// Settings is the full persisted user-settings model.
// 모든 후속 웨이브 기능이 여기에 필드를 꽂는다.
type Settings struct {
	Model     ModelSettings     `json:"model"`
	Language  LanguageSettings  `json:"language"`
	Input     InputSettings     `json:"input"`
	Subtitle  SubtitleSettings  `json:"subtitle"`
	Position  PositionSettings  `json:"position"`
	Audio     AudioSettings     `json:"audio"`
	Cost      CostSettings      `json:"cost"`
	Recording RecordingSettings `json:"recording"`
	VAD       VADSettings       `json:"vad"`
	Engine    EngineSettings    `json:"engine"`
	Update    UpdateSettings    `json:"update"`
	HUD       HUDSettings       `json:"hud"`
}

// DefaultSettings returns the deterministic first-run defaults.
// 값은 원본 SettingsStore(register defaults) / SubtitleStyle StyleDefault / AudioDefault에서 이식.
// Date/난수 없음(결정적).
func DefaultSettings() Settings {
	return Settings{
		Model: ModelSettings{
			ID: GeminiModel, // 폴백 기본값 = 상수. settings.json에서 덮어써 모델 교체.
		},
		Language: LanguageSettings{
			Target:     DefaultTargetLanguage, // "ko" (AppConfig.defaultTargetLanguageCode)
			Source:     DefaultSourceLanguage, // "auto" (서버 자동 감지 — 기존 파이프라인)
			ShowSource: false,                 // 원본 showSourceText 기본 off
		},
		Input: InputSettings{
			Mode:     "auto",
			DeviceID: "",
		},
		Subtitle: SubtitleSettings{
			FontFamily:    "",          // StyleDefault.fontName
			FontSize:      34.0,        // StyleDefault.fontSize
			FontWeight:    "bold",      // StyleDefault.weight
			TextColor:     "#FFFFFFFF", // StyleDefault.textColorHex
			AltTextColor:  "#FFD866FF", // 화자 교대 보조 색(흰색 대비 가독 유지되는 부드러운 노랑).
			StrokeEnabled: true,        // StyleDefault.strokeEnabled
			StrokeColor:   "#000000E6", // StyleDefault.strokeColorHex
			StrokeWidth:   2.0,         // 원본은 고정 다중그림자(1/3/6); Wave2 힌트값.
			GlowEnabled:   false,       // StyleDefault.glowEnabled
			GlowColor:     "#00E5FFCC", // StyleDefault.glowColorHex
			GlowRadius:    8.0,         // StyleDefault.glowRadius
			BgEnabled:     true,        // StyleDefault.backgroundEnabled
			BgColor:       "#000000FF", // 원본 배경은 검정 + opacity
			BgOpacity:     0.35,        // StyleDefault.backgroundOpacity
			Align:         "center",    // StyleDefault.align
			MaxLines:      2,           // StyleDefault.maxLines

			SpeakerColorAlternate: true, // 화자 전환 근사 2색 교대 기본 on(UI 미노출).
		},
		Position: PositionSettings{
			MonitorIndex: 0,        // 주 화면(원본 subtitleScreenID nil 폴백)
			Vertical:     "bottom", // 원본 subtitleVerticalPosition 기본
			Offset:       0.5,      // 원본 subtitleVerticalOffset 기본(영역 중간). register 0 충돌 방지값.
		},
		Audio: AudioSettings{
			PlaybackEnabled: false, // AudioDefault.playbackEnabled
			OutputDeviceID:  "",    // 시스템 기본 출력
			SoftVolume:      1.0,   // AudioDefault.volume
			DuckEnabled:     true,  // AudioDefault.duckingEnabled
			DuckVolume:      0.3,   // AudioDefault.duckVolume
		},
		Cost: CostSettings{
			HUDEnabled:    true, // 원본 costHUDEnabled 기본 on
			CumulativeUSD: 0.0,
		},
		Recording: RecordingSettings{
			Directory: defaultRecordingDir(),
		},
		VAD: VADSettings{
			Enabled: true, // 원본 VAD 기본 on(발화 구간만 전송해 비용 절감). 끄면 전 구간 통과.
		},
		Engine: EngineSettings{
			// 1000ms — 실측 델타 갭 분포의 절벽(연속 발화 0.8~0.9s vs 발화 경계 1.0~1.7s).
			// 오디오 경계(아래)가 1차 트리거이고 이 값은 백업으로 남는다.
			TurnBoundarySilenceMs: 1000,
			// 400ms — 실측상 대화 중 화자 교대 갭은 200~600ms에 분포한다(700ms는 1.4s급
			// 긴 쉼만 잡아 3.5분 세션에서 경계가 3건뿐이었다). 관찰 여운도 300ms로 줄였다.
			AudioBoundarySilenceMs: DefaultAudioBoundarySilenceMs,
			// 물음표 휴리스틱 기본 on(질문→답변 전환은 무음이 짧아도 화자가 바뀐다).
			QuestionBoundary: true,
		},
		Update: UpdateSettings{
			AutoCheck: true, // 원본 SUEnableAutomaticChecks=true — 기본 자동 확인 on.
		},
		HUD: HUDSettings{
			Visible: true, // 첫 실행은 제어 HUD를 띄운다(트레이만으로는 발견성이 낮다).
		},
	}
}

// defaultRecordingDir mirrors 원본 SettingsStore.defaultRecordingDirectory
// (사용자 Documents, 없으면 홈). 결정적(Date/난수 없음).
func defaultRecordingDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "Documents")
	}
	return ""
}

// settingsDir returns the directory holding settings.json (creates nothing).
func settingsDir() (string, error) {
	if configDirOverride != "" {
		return filepath.Join(configDirOverride, configDirName), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, configDirName), nil
}

// settingsPath returns the absolute path to settings.json.
func settingsPath() (string, error) {
	dir, err := settingsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, settingsFileName), nil
}

// Load reads settings.json. 파일이 없으면 기본값을 반환한다(에러 아님).
// 손상(파싱 실패) 시 기본값으로 폴백하고 로그만 남긴다. 그 외 IO 오류만 에러로 전파.
// 로딩은 DefaultSettings() 위에 덮어써 미지의/누락 필드는 기본값을 유지한다(전방 호환).
func Load() (Settings, error) {
	path, err := settingsPath()
	if err != nil {
		return DefaultSettings(), err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultSettings(), nil
		}
		return DefaultSettings(), err
	}
	s := DefaultSettings()
	if err := json.Unmarshal(data, &s); err != nil {
		log.Printf("[config] settings.json 손상 — 기본값으로 폴백: %v", err)
		return DefaultSettings(), nil
	}
	return migrate(s), nil
}

// migrate 는 저장된 설정을 현재 스키마/기본값에 맞춘다(로드 경로 전용, 파일은 건드리지 않는다).
// 숨은 튜닝 키는 설정 UI가 전체 구조를 되저장하면서 **옛 기본값이 파일에 박히기** 때문에,
// 값이 옛 기본값과 정확히 같을 때만 새 기본값으로 올린다(사용자가 직접 바꾼 값은 보존).
func migrate(s Settings) Settings {
	if s.Engine.AudioBoundarySilenceMs == legacyAudioBoundarySilenceMs {
		log.Printf("[config] engine.audioBoundarySilenceMs %d → %d 로 갱신(옛 기본값 → 새 기본값)",
			legacyAudioBoundarySilenceMs, DefaultAudioBoundarySilenceMs)
		s.Engine.AudioBoundarySilenceMs = DefaultAudioBoundarySilenceMs
	}
	return s
}

// Save writes settings.json atomically (temp file + rename). 디렉토리는 없으면 생성한다.
// **API 키는 Settings에 없으므로 이 파일에 결코 기록되지 않는다.**
func (s Settings) Save() error {
	dir, err := settingsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, settingsFileName+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// 실패 경로에서 잔여 temp 파일을 정리한다(성공 시 rename으로 사라짐).
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	final := filepath.Join(dir, settingsFileName)
	return os.Rename(tmpName, final)
}
