package config

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestStyleDefaultKeyParity 는 설정 UI의 STYLE_DEFAULT(스타일 리셋 값)가
// config.SubtitleSettings의 모든 JSON 키를 담고 있는지 검사한다. 키가 빠지면
// '스타일 기본값으로 리셋' 시 그 필드가 zero value로 영구 저장된다(UI 미노출 필드 포함).
func TestStyleDefaultKeyParity(t *testing.T) {
	b, err := json.Marshal(DefaultSettings().Subtitle)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	html, err := os.ReadFile("../../frontend/settings/index.html")
	if err != nil {
		t.Skipf("설정 UI 파일을 읽을 수 없음: %v", err)
	}
	block := regexp.MustCompile(`(?s)const STYLE_DEFAULT = \{(.*?)\};`).FindSubmatch(html)
	if block == nil {
		t.Fatal("STYLE_DEFAULT 블록을 찾지 못했다")
	}
	body := string(block[1])

	var missing []string
	for k := range m {
		if !regexp.MustCompile(`(?m)(^|[\s{,])` + regexp.QuoteMeta(k) + `\s*:`).MatchString(body) {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("STYLE_DEFAULT에 누락된 키: %s\n(frontend/settings/index.html의 STYLE_DEFAULT에 추가할 것)",
			strings.Join(missing, ", "))
	}
}
