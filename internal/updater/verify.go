package updater

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// algoEd25519 / algoEd25519Prehashed mirror the minisign signature prefix bytes.
// Both variants are accepted to cover different signing tool versions.
const (
	algoEd25519          = "Ed"
	algoEd25519Prehashed = "ED"
)

// PublicKey is the parsed minisign public key.
type PublicKey struct {
	KeyID [8]byte
	Pub   ed25519.PublicKey
}

// ParsePublicKey accepts the Tauri-style base64-wrapped minisign pubkey.
// The double encoding is: base64(text including "untrusted comment:\n<base64-keyblock>").
func ParsePublicKey(b64 string) (*PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("pubkey base64 디코딩 실패: %w", err)
	}
	keyLine := extractDataLine(string(raw))
	if keyLine == "" {
		return nil, errors.New("pubkey 본문 라인을 찾지 못했습니다")
	}
	bin, err := base64.StdEncoding.DecodeString(keyLine)
	if err != nil {
		return nil, fmt.Errorf("pubkey 내부 디코딩 실패: %w", err)
	}
	if len(bin) != 42 {
		return nil, fmt.Errorf("pubkey 길이가 비정상: %d (expected 42)", len(bin))
	}
	algo := string(bin[:2])
	if algo != algoEd25519 && algo != algoEd25519Prehashed {
		return nil, fmt.Errorf("지원하지 않는 알고리즘: %s", algo)
	}
	pk := &PublicKey{Pub: ed25519.PublicKey(bin[10:])}
	copy(pk.KeyID[:], bin[2:10])
	return pk, nil
}

// signatureBlock decodes a minisign signature string.
type signatureBlock struct {
	algo            string
	keyID           [8]byte
	sig             []byte // 64 bytes
	trustedComment  string
	globalSignature []byte // 64 bytes, optional
}

func parseSignature(raw string) (*signatureBlock, error) {
	// Tauri historically wraps the .sig contents in another base64 layer; try
	// to decode that first, falling back to raw if the result isn't text-like.
	if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw)); err == nil && looksTextual(decoded) {
		raw = string(decoded)
	}
	lines := splitLines(raw)
	if len(lines) < 2 {
		return nil, errors.New("signature 본문이 너무 짧습니다")
	}
	sb := &signatureBlock{}
	// Standard minisign layout:
	//   line 0: "untrusted comment: ..."
	//   line 1: base64(sig block: 2 algo + 8 keyid + 64 sig)
	//   line 2: "trusted comment: ..."           (optional)
	//   line 3: base64(global sig: 64 bytes)     (optional)
	sigB64 := dataLineAt(lines, 0)
	if sigB64 == "" {
		return nil, errors.New("signature body line 누락")
	}
	bin, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, fmt.Errorf("signature 본문 디코딩 실패: %w", err)
	}
	if len(bin) != 74 {
		return nil, fmt.Errorf("signature 길이가 비정상: %d (expected 74)", len(bin))
	}
	sb.algo = string(bin[:2])
	copy(sb.keyID[:], bin[2:10])
	sb.sig = bin[10:]

	for i := 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "trusted comment:") {
			sb.trustedComment = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "trusted comment:"))
			if i+1 < len(lines) {
				if g, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[i+1])); err == nil && len(g) == 64 {
					sb.globalSignature = g
				}
			}
			break
		}
	}
	return sb, nil
}

// VerifyContent checks `content` against the minisign `signature` using
// `pubkey`. Returns nil on success.
func VerifyContent(content []byte, signature string, pubkey *PublicKey) error {
	sb, err := parseSignature(signature)
	if err != nil {
		return err
	}
	if sb.algo != algoEd25519 && sb.algo != algoEd25519Prehashed {
		return fmt.Errorf("signature 알고리즘 미지원: %s", sb.algo)
	}
	if sb.keyID != pubkey.KeyID {
		return fmt.Errorf("key id 불일치 (sig=%x, key=%x)", sb.keyID, pubkey.KeyID)
	}

	var msg []byte
	if sb.algo == algoEd25519Prehashed {
		h, err := blake2b.New512(nil)
		if err != nil {
			return fmt.Errorf("blake2b 초기화 실패: %w", err)
		}
		h.Write(content)
		msg = h.Sum(nil)
	} else {
		msg = content
	}
	if !ed25519.Verify(pubkey.Pub, msg, sb.sig) {
		return errors.New("Ed25519 서명 검증 실패")
	}

	// Optional global sig over (sig || trusted_comment) — only when present.
	if len(sb.globalSignature) == 64 && sb.trustedComment != "" {
		combined := append([]byte{}, sb.sig...)
		combined = append(combined, []byte(sb.trustedComment)...)
		if !ed25519.Verify(pubkey.Pub, combined, sb.globalSignature) {
			return errors.New("global signature 검증 실패")
		}
	}
	return nil
}

func extractDataLine(text string) string {
	for _, line := range splitLines(text) {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "untrusted comment:") || strings.HasPrefix(s, "trusted comment:") {
			continue
		}
		return s
	}
	return ""
}

func dataLineAt(lines []string, ordinal int) string {
	idx := -1
	for _, line := range lines {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "untrusted comment:") || strings.HasPrefix(s, "trusted comment:") {
			continue
		}
		idx++
		if idx == ordinal {
			return s
		}
	}
	return ""
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(s, "\n")
}

func looksTextual(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	limit := len(b)
	if limit > 64 {
		limit = 64
	}
	for _, c := range b[:limit] {
		if c == '\n' || c == '\r' || c == '\t' {
			continue
		}
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
