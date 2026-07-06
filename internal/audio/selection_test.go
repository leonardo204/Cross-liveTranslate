package audio

import "testing"

// 루프백 후보 이름 휴리스틱을 원본 규칙대로 판정하는지 검증한다(AudioDevice.swift 이식).
func TestLooksLikeLoopback(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"BlackHole 2ch", true},
		{"blackhole 16ch", true},
		{"Loopback Audio", true},
		{"Soundflower (2ch)", true},
		{"My Aggregate (Virtual)", true}, // aggregate + virtual 동시 포함
		{"MacBook Pro Microphone", false},
		{"External Headphones", false},
		{"Aggregate Device", false},   // aggregate 단독은 후보 아님
		{"Virtual Camera Mic", false}, // virtual 단독도 아님
		{"", false},
	}
	for _, c := range cases {
		if got := looksLikeLoopback(c.name); got != c.want {
			t.Errorf("looksLikeLoopback(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// 대소문자 무관 substring 매칭을 검증한다.
func TestLooksLikeLoopback_CaseInsensitive(t *testing.T) {
	for _, n := range []string{"BLACKHOLE", "BlackHole", "blackHOLE"} {
		if !looksLikeLoopback(n) {
			t.Errorf("looksLikeLoopback(%q) = false, want true", n)
		}
	}
}

// pure contains 헬퍼의 경계 동작.
func TestContainsHelper(t *testing.T) {
	if !contains("hello world", "") {
		t.Error("empty substring should match")
	}
	if contains("ab", "abc") {
		t.Error("longer substring must not match")
	}
	if !contains("abc", "abc") {
		t.Error("exact match should match")
	}
	if !contains("xxabcyy", "abc") {
		t.Error("mid substring should match")
	}
}

// SelectionMode.String 라벨.
func TestSelectionModeString(t *testing.T) {
	cases := map[SelectionMode]string{
		SelectAuto:     "auto",
		SelectMic:      "mic",
		SelectDevice:   "device",
		SelectLoopback: "loopback",
	}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Errorf("SelectionMode(%d).String() = %q, want %q", m, got, want)
		}
	}
}
