package config

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

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

	// EngineGemini / EngineSoniox 는 번역 엔진 식별자다(settings.json model.engine).
	EngineGemini = "gemini"
	EngineSoniox = "soniox"

	// SonioxAPIKeyEnv / SonioxCredKey 는 Soniox 키의 환경변수 이름과 자격증명 저장소 키다.
	SonioxAPIKeyEnv = "SONIOX_API_KEY"
	SonioxCredKey   = "soniox_api_key"
)

// NormalizeEngine 은 모르는 값·빈 값을 기본 엔진(gemini)으로 돌린다.
func NormalizeEngine(e string) string {
	if strings.EqualFold(strings.TrimSpace(e), EngineSoniox) {
		return EngineSoniox
	}
	return EngineGemini
}

// EngineLabel 은 사용자에게 보여 줄 엔진 이름이다.
func EngineLabel(engine string) string {
	if NormalizeEngine(engine) == EngineSoniox {
		return "Soniox"
	}
	return "Gemini"
}

// noKeyError 는 Gemini 외 엔진의 키가 없을 때의 오류다. errors.Is(err, ErrNoAPIKey)는 참이다.
type noKeyError struct{ engine string }

func (e noKeyError) Error() string {
	env, cred := keyNames(e.engine)
	return "config: " + EngineLabel(e.engine) + " API 키를 찾을 수 없습니다 — 환경변수 " + env +
		" 를 설정하거나 OS 자격증명 저장소(" + credstore.ServiceName + "/" + cred + ")에 저장하세요"
}

func (e noKeyError) Is(target error) bool { return target == ErrNoAPIKey }

// keyNames returns (환경변수 이름, 자격증명 저장소 키) for the engine.
func keyNames(engine string) (env, cred string) {
	if NormalizeEngine(engine) == EngineSoniox {
		return SonioxAPIKeyEnv, SonioxCredKey
	}
	return APIKeyEnv, CredKey
}

// EngineAPIKey resolves the API key of the given engine: 환경변수 → 자격증명 저장소.
// 둘 다 없으면 ErrNoAPIKey 계열 오류(엔진 이름 포함)를 돌려준다.
func EngineAPIKey(engine string) (string, error) {
	env, cred := keyNames(engine)
	if v := os.Getenv(env); v != "" {
		return v, nil
	}
	v, err := credstore.Load(credstore.ServiceName, cred)
	if err != nil {
		return "", fmt.Errorf("config: 자격증명 저장소 조회 실패: %w", err)
	}
	if v == "" {
		if NormalizeEngine(engine) == EngineGemini {
			return "", ErrNoAPIKey
		}
		return "", noKeyError{engine: NormalizeEngine(engine)}
	}
	return v, nil
}

// SaveEngineAPIKey stores (빈 값이면 삭제) the API key of the given engine.
func SaveEngineAPIKey(engine, key string) error {
	_, cred := keyNames(engine)
	key = strings.TrimSpace(key)
	if key == "" {
		if err := credstore.Delete(credstore.ServiceName, cred); err != nil {
			return fmt.Errorf("config: 자격증명 저장소 삭제 실패: %w", err)
		}
		return nil
	}
	if err := credstore.Save(credstore.ServiceName, cred, key); err != nil {
		return fmt.Errorf("config: 자격증명 저장소 저장 실패: %w", err)
	}
	return nil
}

// EngineKeySource reports where the engine's key comes from: "환경변수" | "키체인" | "없음".
func EngineKeySource(engine string) string {
	env, cred := keyNames(engine)
	if os.Getenv(env) != "" {
		return "환경변수"
	}
	if v, err := credstore.Load(credstore.ServiceName, cred); err == nil && v != "" {
		return "키체인"
	}
	return "없음"
}

// TestEngineAPIKey verifies a key against the engine's REST endpoint.
func TestEngineAPIKey(ctx context.Context, engine, key string) error {
	if NormalizeEngine(engine) == EngineSoniox {
		return TestSonioxAPIKey(ctx, key)
	}
	return TestAPIKey(ctx, key)
}

// TestSonioxAPIKey verifies a Soniox key: GET https://api.soniox.com/v1/models 가 200이면 유효.
// 키는 Authorization 헤더에만 실리고 로그/오류에 노출하지 않는다.
func TestSonioxAPIKey(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("API 키가 비어 있습니다")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.soniox.com/v1/models", nil)
	if err != nil {
		return errors.New("요청 생성에 실패했습니다")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return errors.New("네트워크 오류로 연결하지 못했습니다")
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return errors.New("API 키가 거부되었습니다")
	case resp.StatusCode == http.StatusPaymentRequired:
		return errors.New("Soniox 계정 잔액이 없습니다 — 콘솔에서 충전해 주세요")
	case resp.StatusCode == http.StatusTooManyRequests:
		return errors.New("요청 한도를 초과했습니다")
	default:
		return fmt.Errorf("연결에 실패했습니다 (HTTP %d)", resp.StatusCode)
	}
}

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

// SaveAPIKey stores the Gemini API key in the OS credential store (macOS Keychain).
// 공백은 trim한다. 빈 값이면 저장된 키를 삭제한다(초기화). 키 값은 로그/에러에 노출하지 않는다.
func SaveAPIKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		if err := credstore.Delete(credstore.ServiceName, CredKey); err != nil {
			return fmt.Errorf("config: 자격증명 저장소 삭제 실패: %w", err)
		}
		return nil
	}
	if err := credstore.Save(credstore.ServiceName, CredKey, key); err != nil {
		return fmt.Errorf("config: 자격증명 저장소 저장 실패: %w", err)
	}
	return nil
}

// HasAPIKey reports whether a usable Gemini API key exists (환경변수 또는 키체인).
// 키 값 자체는 반환하지 않는다(존재 여부만).
func HasAPIKey() bool {
	if os.Getenv(APIKeyEnv) != "" {
		return true
	}
	v, err := credstore.Load(credstore.ServiceName, CredKey)
	return err == nil && v != ""
}

// TestAPIKey verifies a Gemini API key by listing models over REST.
// 원본 GeminiConnectionTester와 동일한 목적(키 유효성 확인)을 REST로 간단·확실하게 수행한다:
// GET https://generativelanguage.googleapis.com/v1beta/models?key=<key> 가 200이면 유효.
//
// 보안: 키 값은 URL 쿼리에만 쓰이고 **로그/에러 메시지에 절대 노출하지 않는다**(HTTP 상태만).
func TestAPIKey(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("API 키가 비어 있습니다")
	}
	reqURL := "https://generativelanguage.googleapis.com/v1beta/models?key=" + url.QueryEscape(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return errors.New("요청 생성에 실패했습니다")
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// 원문(키 포함 URL 가능)을 노출하지 않기 위해 일반 메시지만 반환한다.
		return errors.New("네트워크 오류로 연결하지 못했습니다")
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusBadRequest,
		resp.StatusCode == http.StatusUnauthorized,
		resp.StatusCode == http.StatusForbidden:
		return errors.New("API 키가 거부되었습니다")
	case resp.StatusCode == http.StatusTooManyRequests:
		return errors.New("요청 한도를 초과했습니다")
	default:
		return fmt.Errorf("연결에 실패했습니다 (HTTP %d)", resp.StatusCode)
	}
}
