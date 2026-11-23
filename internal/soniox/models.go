package soniox

// models.go — Soniox 실시간 모델 목록 조회와 사용할 모델 결정.
//
// Soniox는 새 모델(stt-rt-v5 → v6 …)을 낼 때마다 이름이 바뀐다. 이름을 코드에 박아 두면 새
// 모델이 나올 때마다 앱을 다시 배포해야 하므로, GET /v1/models 로 목록을 받아 고른다.
//
//   - 설정값(model)이 비어 있으면 "자동": 목록에서 버전 숫자가 가장 큰 실시간 모델을 쓴다.
//   - 설정값이 있으면 그 모델을 쓴다. 단, 목록을 받았는데 그 모델이 없으면(폐지) 자동으로 돌린다.
//   - 목록을 못 받으면(네트워크·키 문제) DefaultModel 을 쓴다.
//
// 목록 응답에는 "사용 중단 예정" 표시가 없다(2026-11 기준 v5·v6 모두 aliased_model_id=null).
// 그래서 "가장 높은 버전"을 최신으로 본다. 다른 모델을 가리키는 별칭(aliased_model_id 있음)은
// 목록에서 제외한다.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultModel 은 목록을 받지 못했을 때 쓰는 모델이다.
const DefaultModel = "stt-rt-v6"

const (
	modelsURL = "https://api.soniox.com/v1/models"
	// modelsTTL: 목록은 자주 바뀌지 않는다. 연결·재연결마다 부르지 않도록 이 시간 동안 재사용한다.
	modelsTTL = 1 * time.Hour
)

// ModelEntry 는 설정 창에 보여 줄 실시간 모델 하나다.
type ModelEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var rtVersionRe = regexp.MustCompile(`^stt-rt-v(\d+)$`)

// rtVersion returns N for "stt-rt-vN", -1 otherwise.
func rtVersion(id string) int {
	m := rtVersionRe.FindStringSubmatch(id)
	if m == nil {
		return -1
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return -1
	}
	return n
}

type modelsResponse struct {
	Models []struct {
		ID                string  `json:"id"`
		Name              string  `json:"name"`
		AliasedModelID    *string `json:"aliased_model_id"`
		TranscriptionMode string  `json:"transcription_mode"`
	} `json:"models"`
}

// parseRealtimeModels keeps real-time, non-alias models, newest version first.
func parseRealtimeModels(body []byte) ([]ModelEntry, error) {
	var r modelsResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("soniox: 모델 목록 해석 실패: %w", err)
	}
	var out []ModelEntry
	for _, m := range r.Models {
		if m.TranscriptionMode != "real_time" {
			continue
		}
		if m.AliasedModelID != nil && strings.TrimSpace(*m.AliasedModelID) != "" {
			continue
		}
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = id
		}
		out = append(out, ModelEntry{ID: id, Name: name})
	}
	sort.SliceStable(out, func(i, j int) bool {
		vi, vj := rtVersion(out[i].ID), rtVersion(out[j].ID)
		if vi != vj {
			return vi > vj
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// LatestModel returns the newest model in a list sorted by parseRealtimeModels.
// 목록이 비면 DefaultModel.
func LatestModel(models []ModelEntry) string {
	if len(models) == 0 {
		return DefaultModel
	}
	return models[0].ID
}

var cache struct {
	sync.Mutex
	keyHash string
	at      time.Time
	models  []ModelEntry
}

func keyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return string(sum[:8])
}

// FetchRealtimeModels returns the real-time models available to this key, newest first.
// 같은 키로 modelsTTL 안에 다시 부르면 저장해 둔 목록을 돌려준다. 키는 헤더에만 싣는다.
func FetchRealtimeModels(ctx context.Context, key string) ([]ModelEntry, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("soniox: API 키가 없습니다")
	}
	h := keyHash(key)
	cache.Lock()
	if cache.keyHash == h && time.Since(cache.at) < modelsTTL && len(cache.models) > 0 {
		out := append([]ModelEntry(nil), cache.models...)
		cache.Unlock()
		return out, nil
	}
	cache.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("soniox: 모델 목록 요청 실패: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("soniox: 모델 목록 응답 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("soniox: 모델 목록 읽기 실패: %w", err)
	}
	models, err := parseRealtimeModels(body)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("soniox: 실시간 모델이 목록에 없습니다")
	}
	cache.Lock()
	cache.keyHash, cache.at, cache.models = h, time.Now(), append([]ModelEntry(nil), models...)
	cache.Unlock()
	return models, nil
}

// ResolveModel decides the model to connect with.
//
//	pinned == ""             → 목록의 최신 모델(목록 실패 시 DefaultModel)
//	pinned 가 목록에 있음     → pinned
//	pinned 가 목록에 없음     → 최신 모델(폐지된 모델로 연결이 실패하지 않게)
//	목록 실패 + pinned 있음   → pinned(확인할 수 없으니 사용자 선택을 따른다)
//
// note 는 로그용 한 줄 설명이다.
func ResolveModel(ctx context.Context, key, pinned string) (model, note string) {
	pinned = strings.TrimSpace(pinned)
	models, err := FetchRealtimeModels(ctx, key)
	if err != nil {
		if pinned != "" {
			return pinned, fmt.Sprintf("선택한 모델 사용(목록 확인 실패: %v)", err)
		}
		return DefaultModel, fmt.Sprintf("기본 모델 사용(목록 확인 실패: %v)", err)
	}
	latest := LatestModel(models)
	if pinned == "" {
		return latest, "자동 선택(최신)"
	}
	for _, m := range models {
		if m.ID == pinned {
			return pinned, "선택한 모델 사용"
		}
	}
	return latest, fmt.Sprintf("선택한 모델 %q 이 목록에 없어 최신 모델로 대체", pinned)
}
