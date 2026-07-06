package config

import (
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
