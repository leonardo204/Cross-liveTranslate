package config

import (
	"errors"
	"strings"
	"testing"
)

func TestNoKeyErrorNamesEngine(t *testing.T) {
	err := noKeyError{engine: EngineSoniox}
	if !errors.Is(err, ErrNoAPIKey) {
		t.Fatal("errors.Is(ErrNoAPIKey) 가 거짓")
	}
	if !strings.Contains(err.Error(), "Soniox") || strings.Contains(err.Error(), "Gemini") ||
		!strings.Contains(err.Error(), SonioxAPIKeyEnv) {
		t.Fatalf("문구=%q", err.Error())
	}
	if NormalizeEngine(" Soniox ") != EngineSoniox || NormalizeEngine("x") != EngineGemini {
		t.Fatal("NormalizeEngine")
	}
}
