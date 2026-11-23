package main

// controller_engine.go — 번역 엔진(Gemini / Soniox) 선택에 따른 controller 보조 함수.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"cross-livetranslate/internal/config"
	"cross-livetranslate/internal/pipeline"
	"cross-livetranslate/internal/soniox"
	"cross-livetranslate/internal/subtitle"
)

// newSonioxProvider builds a Soniox provider from the persisted Soniox settings.
func newSonioxProvider(key, target string, s config.SonioxSettings) pipeline.Provider {
	terms := make([]soniox.TranslationTerm, 0, len(s.TranslationTerms))
	for _, t := range s.TranslationTerms {
		if strings.TrimSpace(t.Source) == "" || strings.TrimSpace(t.Target) == "" {
			continue
		}
		terms = append(terms, soniox.TranslationTerm{Source: strings.TrimSpace(t.Source), Target: strings.TrimSpace(t.Target)})
	}
	return soniox.NewClient(soniox.Config{
		APIKey:           key,
		Model:            strings.TrimSpace(s.Model),
		TargetLanguage:   target,
		LanguageHints:    cleanList(s.LanguageHints),
		Context:          strings.TrimSpace(s.Context),
		Terms:            cleanList(s.Terms),
		TranslationTerms: terms,
	})
}

// cleanList trims entries and drops empty/duplicate ones.
func cleanList(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// sonioxFingerprint returns a short hash of the Soniox options (Gemini면 빈 문자열).
// ProviderConfig 비교용이라 내용을 담지 않고 지문만 담는다(로그에 용어집이 새지 않게).
func sonioxFingerprint(engine string, s config.SonioxSettings) string {
	if engine != config.EngineSoniox {
		return ""
	}
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// speakerLabel renders 원문 줄 앞의 화자·언어 표시: "화자 2 · IT".
func speakerLabel(speaker, lang string) string {
	if speaker == "" {
		return ""
	}
	l := "화자 " + speaker
	if lang != "" {
		l += " · " + strings.ToUpper(lang)
	}
	return l
}

// noticeNoAPIKeyFor is the 화면 알림 문구 for a missing key of the engine.
func noticeNoAPIKeyFor(engine string) string {
	if engine == config.EngineSoniox {
		return "Soniox API 키가 없습니다 — 설정 창의 API 키 항목에서 입력하세요"
	}
	return noticeNoAPIKey
}

// engineSnapshot returns the current engine (정규화된 값).
func (c *Controller) engineSnapshot() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return config.NormalizeEngine(c.settings.Model.Engine)
}

// applyEngineMode tunes the subtitle engine for the current translation engine.
// runLoop 단독 호출(엔진 단일 owner). 설정이 바뀌면 다음 tick에 반영된다.
//
//	Gemini — 화자 정보가 없어 무음·간격·물음표로 화자 교대를 추정한다(기존 동작).
//	Soniox — 실제 화자 번호로 줄을 나누고 색을 바꾼다. 토큰을 그대로 이어 붙이고, 추정 경계
//	         (델타 간격·물음표)는 끈다. 무음 2초 확정과 8초 화면 정리는 그대로 둔다.
func (c *Controller) applyEngineMode(eng *subtitle.Engine) {
	soniox := c.engineSnapshot() == config.EngineSoniox
	eng.SpeakerDriven = soniox
	eng.ExactDeltas = soniox
	if soniox {
		eng.TurnBoundarySilence = 0
		eng.QuestionBoundary = false
		return
	}
	eng.TurnBoundarySilence = c.turnBoundarySilence()
	eng.QuestionBoundary = c.wantQuestionBoundary()
}

// engineShortLabel is the compact engine name shown on the control HUD header.
func engineShortLabel(engine string) string {
	if engine == config.EngineSoniox {
		return "Soniox"
	}
	return "Gemini 3.5"
}
