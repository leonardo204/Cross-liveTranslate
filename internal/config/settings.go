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
}

// PositionSettings holds subtitle placement (원본 subtitle.screenID + verticalPosition).
type PositionSettings struct {
	MonitorIndex int    `json:"monitorIndex"` // 0=주 화면.
	Vertical     string `json:"vertical"`     // top|middle|bottom, 기본 bottom.
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

// Settings is the full persisted user-settings model.
// 모든 후속 웨이브 기능이 여기에 필드를 꽂는다.
type Settings struct {
	Language  LanguageSettings  `json:"language"`
	Input     InputSettings     `json:"input"`
	Subtitle  SubtitleSettings  `json:"subtitle"`
	Position  PositionSettings  `json:"position"`
	Audio     AudioSettings     `json:"audio"`
	Cost      CostSettings      `json:"cost"`
	Recording RecordingSettings `json:"recording"`
	VAD       VADSettings       `json:"vad"`
}

// DefaultSettings returns the deterministic first-run defaults.
// 값은 원본 SettingsStore(register defaults) / SubtitleStyle StyleDefault / AudioDefault에서 이식.
// Date/난수 없음(결정적).
func DefaultSettings() Settings {
	return Settings{
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
		},
		Position: PositionSettings{
			MonitorIndex: 0,        // 주 화면(원본 subtitleScreenID nil 폴백)
			Vertical:     "bottom", // 원본 subtitleVerticalPosition 기본
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
			Enabled: false, // Wave 3에서 배선 — 기본 bypass
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
	return s, nil
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
