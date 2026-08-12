package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// withTempConfigDir isolates settings.json into a temp dir for the duration of a test.
func withTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := configDirOverride
	configDirOverride = dir
	t.Cleanup(func() { configDirOverride = prev })
	return dir
}

// TestLoadMissingReturnsDefaults: 파일이 없으면 기본값을 반환한다(에러 없음).
func TestLoadMissingReturnsDefaults(t *testing.T) {
	withTempConfigDir(t)
	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, DefaultSettings()) {
		t.Fatalf("Load() = %+v, want DefaultSettings", got)
	}
}

// TestSaveLoadRoundTrip: 기본값 → 저장 → 로드 시 동일해야 한다.
func TestSaveLoadRoundTrip(t *testing.T) {
	withTempConfigDir(t)

	want := DefaultSettings()
	want.Language.Target = "ja"
	want.Language.ShowSource = true
	want.Input.Mode = "device"
	want.Input.DeviceID = "dev-123"
	want.Subtitle.FontSize = 48
	want.Subtitle.TextColor = "#11223344"
	want.Audio.PlaybackEnabled = true
	want.Audio.DuckVolume = 0.5
	want.Cost.CumulativeUSD = 1.2345
	want.VAD.Enabled = true

	if err := want.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n got = %+v\nwant = %+v", got, want)
	}
}

// TestSaveIsAtomicFile: Save가 실제로 settings.json 파일을 생성하고 temp 잔재가 없어야 한다.
func TestSaveIsAtomicFile(t *testing.T) {
	dir := withTempConfigDir(t)
	if err := DefaultSettings().Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	appDir := filepath.Join(dir, configDirName)
	if _, err := os.Stat(filepath.Join(appDir, settingsFileName)); err != nil {
		t.Fatalf("settings.json missing after Save: %v", err)
	}
	entries, err := os.ReadDir(appDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != settingsFileName {
			t.Fatalf("unexpected leftover file after Save: %q", e.Name())
		}
	}
}

// TestLoadCorruptReturnsDefaults: 손상된 JSON은 기본값으로 폴백한다(에러 없음).
func TestLoadCorruptReturnsDefaults(t *testing.T) {
	dir := withTempConfigDir(t)
	appDir := filepath.Join(dir, configDirName)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, settingsFileName), []byte("{ this is not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil (corrupt→default)", err)
	}
	if !reflect.DeepEqual(got, DefaultSettings()) {
		t.Fatalf("corrupt Load() = %+v, want DefaultSettings", got)
	}
}

// TestLoadPartialKeepsDefaults: 일부 필드만 있는 JSON은 나머지를 기본값으로 채운다(전방 호환).
func TestLoadPartialKeepsDefaults(t *testing.T) {
	dir := withTempConfigDir(t)
	appDir := filepath.Join(dir, configDirName)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	partial := `{"language":{"target":"fr"}}`
	if err := os.WriteFile(filepath.Join(appDir, settingsFileName), []byte(partial), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Language.Target != "fr" {
		t.Fatalf("Target = %q, want fr", got.Language.Target)
	}
	// 누락 필드는 기본값 유지.
	def := DefaultSettings()
	if got.Subtitle.FontSize != def.Subtitle.FontSize {
		t.Fatalf("FontSize = %v, want default %v", got.Subtitle.FontSize, def.Subtitle.FontSize)
	}
	if got.Audio.DuckVolume != def.Audio.DuckVolume {
		t.Fatalf("DuckVolume = %v, want default %v", got.Audio.DuckVolume, def.Audio.DuckVolume)
	}
}

// TestDefaultSettingsDeterministic: DefaultSettings는 호출마다 동일해야 한다(결정성).
func TestDefaultSettingsDeterministic(t *testing.T) {
	if !reflect.DeepEqual(DefaultSettings(), DefaultSettings()) {
		t.Fatal("DefaultSettings() not deterministic")
	}
}

// TestTurnBoundarySilenceDefaultAndMissingKey: 델타 갭 기반 턴 경계 임계(숨은 설정)는
// 기본 1000ms이고, **키가 없는 구버전 settings.json을 읽어도 0으로 죽지 않는다**
// (Load가 DefaultSettings 위에 덮어쓰므로 누락 키는 기본값 유지).
func TestTurnBoundarySilenceDefaultAndMissingKey(t *testing.T) {
	dir := withTempConfigDir(t)

	if got := DefaultSettings().Engine.TurnBoundarySilenceMs; got != 1000 {
		t.Fatalf("기본 TurnBoundarySilenceMs = %d, want 1000", got)
	}

	// engine 블록이 통째로 빠진 구버전 파일.
	legacy := `{"language":{"target":"ko"},"subtitle":{"fontSize":34}}`
	path := filepath.Join(dir, configDirName, settingsFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Engine.TurnBoundarySilenceMs != 1000 {
		t.Fatalf("키 누락 시 TurnBoundarySilenceMs = %d, want 1000(기본 유지)",
			got.Engine.TurnBoundarySilenceMs)
	}

	// 명시적으로 0을 넣으면 "비활성" 의도로 그대로 보존된다(기본값으로 되돌리지 않는다).
	off := DefaultSettings()
	off.Engine.TurnBoundarySilenceMs = 0
	if err := off.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err = Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Engine.TurnBoundarySilenceMs != 0 {
		t.Fatalf("명시적 0이 보존되지 않음: %d", got.Engine.TurnBoundarySilenceMs)
	}
}

// TestAudioBoundarySilenceDefaultAndMissingKey: 오디오 도메인 화자 경계 임계(숨은 설정)는
// 기본 700ms이고, 키가 없는 구버전 settings.json에서도 기본값이 유지된다(0으로 죽지 않음).
// 명시적 0은 "관찰 비활성" 의도로 보존한다.
func TestAudioBoundarySilenceDefaultAndMissingKey(t *testing.T) {
	dir := withTempConfigDir(t)

	if got := DefaultSettings().Engine.AudioBoundarySilenceMs; got != DefaultAudioBoundarySilenceMs {
		t.Fatalf("기본 AudioBoundarySilenceMs = %d, want %d", got, DefaultAudioBoundarySilenceMs)
	}

	// engine 블록에 turnBoundarySilenceMs만 있는(오디오 키 누락) 파일.
	legacy := `{"engine":{"turnBoundarySilenceMs":1200}}`
	path := filepath.Join(dir, configDirName, settingsFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Engine.AudioBoundarySilenceMs != DefaultAudioBoundarySilenceMs {
		t.Fatalf("키 누락 시 AudioBoundarySilenceMs = %d, want %d(기본 유지)",
			got.Engine.AudioBoundarySilenceMs, DefaultAudioBoundarySilenceMs)
	}
	if got.Engine.TurnBoundarySilenceMs != 1200 {
		t.Fatalf("명시된 turnBoundarySilenceMs가 유실됐다: %d", got.Engine.TurnBoundarySilenceMs)
	}

	// 명시적 0 = 관찰 비활성 → 그대로 보존.
	off := DefaultSettings()
	off.Engine.AudioBoundarySilenceMs = 0
	if err := off.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err = Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Engine.AudioBoundarySilenceMs != 0 {
		t.Fatalf("명시적 0이 보존되지 않음: %d", got.Engine.AudioBoundarySilenceMs)
	}
}

// TestAudioBoundaryLegacyDefaultMigrates: 설정 창이 전체 구조를 되저장하면서 파일에 박힌
// **옛 기본값(700)** 은 Load 시 새 기본값(400)으로 올라간다. 사용자가 직접 넣은 다른 값은
// 그대로 보존돼야 한다(마이그레이션이 사용자 의도를 덮지 않는다).
func TestAudioBoundaryLegacyDefaultMigrates(t *testing.T) {
	dir := withTempConfigDir(t)
	path := filepath.Join(dir, configDirName, settingsFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// 옛 기본값 그대로 → 새 기본값으로 갱신.
	write(`{"engine":{"audioBoundarySilenceMs":700}}`)
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Engine.AudioBoundarySilenceMs != DefaultAudioBoundarySilenceMs {
		t.Fatalf("옛 기본값 마이그레이션 실패: %d", got.Engine.AudioBoundarySilenceMs)
	}

	// 사용자가 명시적으로 고른 다른 값은 보존.
	for _, v := range []int{0, 250, 1200} {
		write(fmt.Sprintf(`{"engine":{"audioBoundarySilenceMs":%d}}`, v))
		got, err = Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Engine.AudioBoundarySilenceMs != v {
			t.Fatalf("사용자 값 %d 가 %d 로 덮였다", v, got.Engine.AudioBoundarySilenceMs)
		}
	}
}

// TestQuestionBoundaryDefaultAndMissingKey: 물음표 휴리스틱은 기본 on이고, 키가 없는
// 구버전 파일에서도 on이 유지된다(bool zero-value 함정 방지). 명시적 false는 보존된다.
func TestQuestionBoundaryDefaultAndMissingKey(t *testing.T) {
	dir := withTempConfigDir(t)
	if !DefaultSettings().Engine.QuestionBoundary {
		t.Fatal("기본 QuestionBoundary = false, want true")
	}

	path := filepath.Join(dir, configDirName, settingsFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// engine 블록은 있지만 questionBoundary 키가 없는 파일.
	if err := os.WriteFile(path, []byte(`{"engine":{"turnBoundarySilenceMs":1000}}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.Engine.QuestionBoundary {
		t.Fatal("키 누락 시 QuestionBoundary = false, want true(기본 유지)")
	}

	// 명시적 false는 사용자 의도이므로 보존.
	off := DefaultSettings()
	off.Engine.QuestionBoundary = false
	if err := off.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Engine.QuestionBoundary {
		t.Fatal("명시적 false가 보존되지 않았다")
	}
}
