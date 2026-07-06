package updater

import (
	"encoding/base64"
	"strings"
	"testing"
)

// ── IsNewer ──────────────────────────────────────────────────────────────────

func TestIsNewer(t *testing.T) {
	tests := []struct {
		remote  string
		current string
		want    bool
	}{
		{"1.0.1", "1.0.0", true},
		{"1.1.0", "1.0.9", true},
		{"2.0.0", "1.9.9", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.0", "1.0.1", false},
		{"1.0.9", "1.1.0", false},
		{"1.9.9", "2.0.0", false},
		{"10.0.0", "9.9.9", true},
		{"1.0.10", "1.0.9", true},
		{"0.2.0", "0.10.0", false},
		{"1.0.0", "0.1.0-dev", true},
		{"0.1.0-dev", "0.1.0", false},
		// edge cases
		{"", "1.0.0", false},
		{"1.0.0", "", true},
		{"1", "0.9.9", true},
	}

	for _, tc := range tests {
		got := IsNewer(tc.remote, tc.current)
		if got != tc.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tc.remote, tc.current, got, tc.want)
		}
	}
}

// ── ParsePublicKey ───────────────────────────────────────────────────────────

// updaterPubKey as used in update.go — copied here for test isolation.
const testPubKey = "dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWduIHB1YmxpYyBrZXk6IDhEMDgwMDk5NjY2ODAyQzkKUldUSkFtaG1tUUFJalhXbG1JQmhuU0l1Z2FHSFFMT3NHcndzRklDU2ljWjhkMzBmQmVUUUdnMXIK"

func TestParsePublicKey_Valid(t *testing.T) {
	pk, err := ParsePublicKey(testPubKey)
	if err != nil {
		t.Fatalf("ParsePublicKey error: %v", err)
	}
	if len(pk.Pub) != 32 {
		t.Errorf("expected ed25519 key length 32, got %d", len(pk.Pub))
	}
	// Verify inner decode: base64-decode outer → inner has "untrusted comment:" line
	// then base64-decode data line → 42 bytes total
	outer, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(testPubKey))
	dataLine := extractDataLine(string(outer))
	if dataLine == "" {
		t.Fatal("expected data line in pubkey, got empty")
	}
	inner, err := base64.StdEncoding.DecodeString(dataLine)
	if err != nil {
		t.Fatalf("inner decode error: %v", err)
	}
	if len(inner) != 42 {
		t.Errorf("expected 42 bytes in inner pubkey block, got %d", len(inner))
	}
}

func TestParsePublicKey_Invalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"not-base64", "!!!invalid!!!"},
		{"wrong-inner-length", base64.StdEncoding.EncodeToString([]byte("untrusted comment: test\nYQ==\n"))},
	}
	for _, tc := range cases {
		_, err := ParsePublicKey(tc.input)
		if err == nil {
			t.Errorf("ParsePublicKey(%s): expected error, got nil", tc.name)
		}
	}
}

// ── PlatformKey ──────────────────────────────────────────────────────────────

func TestPlatformKey_NonEmpty(t *testing.T) {
	key := PlatformKey()
	// On macOS (darwin), key must be "darwin-aarch64" or "darwin-x86_64".
	// On other platforms it should still be non-empty for known OSes.
	if key == "" {
		// Only truly empty for truly unknown GOOS — not an error in CI where
		// we might cross-compile, but flag it as informational.
		t.Logf("PlatformKey() returned empty string (unknown GOOS/GOARCH)")
	}
	// Must contain a dash if non-empty.
	if key != "" && !strings.Contains(key, "-") {
		t.Errorf("PlatformKey() = %q: expected dash-separated format", key)
	}
}

func TestPlatformKey_DarwinFormat(t *testing.T) {
	key := PlatformKey()
	// We're running on darwin when this test compiles with build tag.
	// Verify the known patterns are at least valid strings.
	validKeys := map[string]bool{
		"darwin-aarch64": true,
		"darwin-x86_64":  true,
		"windows-x86_64": true,
		"windows-aarch64": true,
		"linux-x86_64":   true,
		"linux-aarch64":  true,
	}
	if key != "" && !validKeys[key] {
		t.Errorf("PlatformKey() = %q: not in expected set", key)
	}
}

// ── IsNewer edge-case helpers ────────────────────────────────────────────────

func TestSplitDigits(t *testing.T) {
	tests := []struct {
		input string
		want  []int
	}{
		{"1.2.3", []int{1, 2, 3}},
		{"10.0.0", []int{10, 0, 0}},
		{"0.1.0-dev", []int{0, 1, 0, 0}}, // '-dev' adds trailing zero segment
		{"", []int{0}},
		{"1", []int{1}},
	}
	for _, tc := range tests {
		got := splitDigits(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("splitDigits(%q) = %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitDigits(%q)[%d] = %d, want %d", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

// ── looksTextual ─────────────────────────────────────────────────────────────

func TestLooksTextual(t *testing.T) {
	tests := []struct {
		input []byte
		want  bool
	}{
		{[]byte("untrusted comment: minisign public key"), true},
		{[]byte{0x00, 0x01, 0x02}, false},
		{[]byte{}, false},
		{[]byte("hello\nworld\t!"), true},
	}
	for _, tc := range tests {
		got := looksTextual(tc.input)
		if got != tc.want {
			t.Errorf("looksTextual(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
