package config

import (
	"errors"
	"fmt"
	"os"

	"cross-livetranslate/internal/credstore"
)

// 설정 상수 + API 키 조회. 값은 원본 liveTranslate에서 이식:
//   GeminiModel           ← AppConfig.geminiModel / models.json modelIdentifier
//   DefaultTargetLanguage ← AppConfig.defaultTargetLanguageCode ("ko")

const (
	// GeminiModel is the Gemini Live Translate 모델 식별자.
	//
	// 원본 값(AppConfig.swift / Resources/models.json). **프리뷰 모델명은 변동 가능**하므로
	// 서버가 거부(404/모델 없음)하면 최신 프리뷰 식별자로 갱신할 것.
	GeminiModel = "models/gemini-3.5-live-translate-preview"

	// DefaultTargetLanguage is the 기본 번역 대상 언어(BCP-47).
	DefaultTargetLanguage = "ko"

	// DefaultSourceLanguage "auto" means 서버 자동 감지(setup에 source 미지정).
	DefaultSourceLanguage = "auto"

	// APIKeyEnv is the 개발용 환경변수 이름.
	APIKeyEnv = "GEMINI_API_KEY"

	// CredKey is the OS 자격증명 저장소 키(credstore.ServiceName 서비스 하위).
	CredKey = "gemini_api_key"
)

// ErrNoAPIKey is returned when no API key is found in env nor the credential store.
var ErrNoAPIKey = errors.New("config: Gemini API 키를 찾을 수 없습니다 — 환경변수 " +
	APIKeyEnv + " 를 설정하거나 OS 자격증명 저장소(" + credstore.ServiceName + "/" + CredKey + ")에 저장하세요")

// APIKey resolves the Gemini API key: 환경변수 GEMINI_API_KEY(개발) →
// credstore.Load(ServiceName, gemini_api_key)(배포). 둘 다 없으면 ErrNoAPIKey.
func APIKey() (string, error) {
	if v := os.Getenv(APIKeyEnv); v != "" {
		return v, nil
	}
	v, err := credstore.Load(credstore.ServiceName, CredKey)
	if err != nil {
		return "", fmt.Errorf("config: 자격증명 저장소 조회 실패: %w", err)
	}
	if v == "" {
		return "", ErrNoAPIKey
	}
	return v, nil
}
