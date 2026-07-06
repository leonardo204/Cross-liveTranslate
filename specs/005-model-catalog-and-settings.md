> **[이식 상태]** 이식 대상(적응 필요) · 대상 스택: Go + Wails v2 (Windows/macOS)
> models.json 카탈로그 유지, `minOS`→플랫폼 게이팅 필드로 일반화. 설정 저장은 UserDefaults→JSON 파일.
> 원본(macOS/Swift): `liveTranslate/specs/005-model-catalog-and-settings.md` · 마스터 플랜: [000](000-cross-platform-porting-plan.md)

---

---
id: model-catalog-and-settings
title: 모델 카탈로그(레지스트리) + '모델' 설정 탭 + 능력 기반 UI
type: design
version: 0.1.0
status: draft
scope: 하드코딩 없는 모델 목록(번들 JSON, 원격 fetch 대비)과 '모델' 설정 탭, 모델 특성(capability)에 따른 UI 활성/비활성, 향후 번역/LLM 엔진 설정 슬롯. 현 Gemini Live 3.5 + client/server VAD를 이 구조로 흡수.
related: [translation-pipeline-architecture, ondevice-asr-research, gemini-live-translate-and-audio]
updated: 2026-06-19
---

# 모델 카탈로그 + '모델' 설정 탭 + 능력 기반 UI

> 목표: 모델을 **하드코딩하지 않고** JSON(soft-copy)로 선언 → UI가 목록을 자동으로 뿌리고,
> **모델별 특성(capability)** 에 따라 설정 UI를 활성/비활성한다. 현재는 Gemini Live 3.5
> Translate 1종만 존재하며, 앞으로 모델/번역엔진/LLM이 추가돼도 JSON 항목 추가(또는 원격
> fetch)만으로 노출되도록 한다. [[translation-pipeline-architecture]](spec 004) P1의 일부.

---

## 1. 결정 사항 (확정)

- **저장 형식**: 앱 번들 `Resources/models.json`. 추가는 JSON 항목 append만. 스키마에 원격
  fetch/사용자 오버라이드(App Support)를 **확장 가능**하게 두되, 1차는 번들 JSON이 진실원.
- **VAD 의미**: "서버 VAD ↔ 클라이언트 Silero 게이트" 2택. 모델 디스크립터의 `vad` 능력으로
  옵션을 게이팅(미지원이면 비활성). 과거 storm 났던 activity-신호 경로는 노출하지 않음.
- 로드 실패/누락 시 **내장 기본 디스크립터**(Gemini)로 폴백 → 앱이 절대 빈 목록으로 깨지지 않음.

---

## 2. 모델 디스크립터 스키마 (Resources/models.json)

```json
{
  "schemaVersion": 1,
  "models": [
    {
      "id": "gemini-3.5-live-translate",
      "displayName": "Gemini Live 3.5 Translate",
      "summary": "Google 클라우드 실시간 음성→번역(자막+번역음성). API 키 필요.",
      "engine": "geminiLive",
      "modelIdentifier": "models/gemini-3.5-live-translate-preview",
      "pipeline": "integrated",
      "requiresAPIKey": true,
      "available": true,
      "capabilities": {
        "sourceText": true,
        "translatedText": true,
        "translatedAudio": true,
        "streaming": true
      },
      "vad": { "server": true, "clientGate": true, "default": "client" },
      "engineSlots": { "translation": false, "llm": false },
      "targetLanguages": null,
      "sourceLanguages": null
    }
  ]
}
```

필드 의미:

| 필드 | 의미 / UI 영향 |
|------|---------------|
| `id` | 고유 키. `SettingsStore.selectedModelID`로 영속. |
| `displayName`/`summary` | 모델 탭 목록/설명 표시. |
| `engine` | `geminiLive` \| `onDeviceTranscribe` \| `onDeviceTranslate`([[translation-pipeline-architecture]] §6). 팩토리 분기 키. |
| `modelIdentifier` | 엔진에 넘길 실제 모델 ID(예: Gemini WebSocket setup의 `model`). |
| `pipeline` | `integrated`(1-Stage) \| `composed`(STT→번역/LLM→TTS). UI가 엔진 슬롯 노출 판단. |
| `requiresAPIKey` | false면 API 키 없이 사용 가능(온디바이스). API 키 탭 안내/요구 완화. |
| `available` | false면 목록에 "준비 중"으로 비활성 표시(추가 예정 모델 미리 노출 가능). |
| `capabilities.sourceText` | 원문 자막 가능 → '원문 표시' 토글 활성화 조건. |
| `capabilities.translatedAudio` | 번역 오디오 재생 가능 → '오디오' 탭 재생/덕킹 섹션 활성화 조건. |
| `capabilities.translatedText` | 번역 자막 가능(기본 true). |
| `capabilities.streaming` | 실시간 부분결과 여부(표시용). |
| `vad.server`/`vad.clientGate` | VAD 옵션 활성화 게이트. 둘 중 미지원은 비활성. |
| `vad.default` | 초기 선택값(`client`=Silero 게이트 / `server`=Gemini 자동). |
| `engineSlots.translation`/`llm` | composed에서 별도 번역/LLM 엔진 설정 섹션 노출 여부(현재 false → 숨김/준비중). |
| `targetLanguages`/`sourceLanguages` | null=제약 없음/미상. 값이 있으면 언어 Picker를 제한. |

---

## 3. 타입 (Swift)

```swift
struct ModelCatalogFile: Decodable, Sendable { let schemaVersion: Int; let models: [ModelDescriptor] }

struct ModelDescriptor: Decodable, Sendable, Identifiable, Equatable {
    let id: String
    let displayName: String
    let summary: String
    let engine: TranslationEngineKind
    let modelIdentifier: String
    let pipeline: PipelineShape          // integrated | composed
    let requiresAPIKey: Bool
    let available: Bool
    let capabilities: Caps
    let vad: VADSupport
    let engineSlots: EngineSlots
    let targetLanguages: [String]?
    let sourceLanguages: [String]?

    struct Caps: Decodable, Sendable, Equatable { let sourceText, translatedText, translatedAudio, streaming: Bool }
    struct VADSupport: Decodable, Sendable, Equatable { let server, clientGate: Bool; let `default`: String }
    struct EngineSlots: Decodable, Sendable, Equatable { let translation, llm: Bool }
    enum PipelineShape: String, Decodable, Sendable { case integrated, composed }

    /// spec 004 EngineCapabilities로 사상(provider 능력 노출용).
    var engineCapabilities: EngineCapabilities { /* caps/requiresAPIKey/streaming 매핑 */ }
}
```

```swift
/// 번들 JSON 로드 + 폴백 + 조회. (1차: 번들. 추후: 원격 fetch/사용자 오버라이드 병합.)
@MainActor
struct ModelCatalog {
    static let shared = ModelCatalog()
    let models: [ModelDescriptor]                 // 로드 실패 시 [builtInGemini]
    func model(id: String) -> ModelDescriptor?
    func resolved(id: String?) -> ModelDescriptor // id 없거나 미존재 → 첫 available 모델
    static let builtInGemini: ModelDescriptor      // 코드 내장 폴백(JSON 누락/손상 대비)
}
```

---

## 4. 설정 영속 & 팩토리 배선

- `SettingsStore.selectedModelID: String`(기본 = 첫 모델 id). `resetAll`에 포함.
- `TranslationProviderFactory.make(settings:apiKey:)`:
  1. `desc = ModelCatalog.shared.resolved(id: settings.selectedModelID)`
  2. `switch desc.engine`:
     - `.geminiLive` → `GeminiTranslationProvider(apiKey:, model: desc.modelIdentifier, targetLanguageCode:, requestInputTranscription: desc.capabilities.sourceText && settings.showSourceText)` (capabilities는 desc에서)
     - `.onDeviceTranscribe`/`.onDeviceTranslate` → **현재 nil**(spec 003 미적용 — 호출자는 "준비 중" 처리). P2에서 구현.
- `GeminiTranslationProvider`에 `model:` 파라미터 추가(현 하드코딩 `AppConfig.geminiModel` 제거, 디스크립터값 사용).

### 4.1 핫스왑 연동 (spec 004 §7)
- `AppState.ProviderConfig`에 `modelID`(또는 engine+modelIdentifier) 추가 → 모델 변경 시 config diff로 **핫스왑**(provider 교체, audio 유지).
- VAD 변경(`audio.vadEnabled`)은 **경량 즉시 적용**(provider 재생성 불필요, §7.8). 언어/키/모델만 핫스왑.

---

## 5. '모델' 설정 탭 + 능력 기반 UI

### 5.1 사이드바
`SettingsCategory`에 `.model` 추가(아이콘 `cpu`/`brain`). 위치는 `.input` 위(번역의 핵심 선택).

### 5.2 모델 탭 구성
- **모델 선택**: 카탈로그 목록 Picker/List. `available==false`는 비활성 + "준비 중". 선택 시
  `settings.selectedModelID` 갱신 → `reloadTranslationSession()`(번역 중이면 핫스왑).
- **모델 정보**: 선택 모델의 `summary`, 능력 배지(원문/번역텍스트/번역오디오/스트리밍/키 필요).
- **VAD**(기존 vadSection 이관): `vad.server/clientGate`로 옵션 게이팅. 미지원 옵션 비활성.
- **번역 엔진**(슬롯): `engineSlots.translation==true`일 때만 노출(현재 숨김). 향후 번역엔진 선택/설정.
- **LLM 엔진/교정**(슬롯): `engineSlots.llm==true`일 때만 노출(현재 숨김). 향후 LLM 선택/프롬프트/키.

### 5.3 능력 기반 활성/비활성 (다른 탭에도 반영)
선택 모델 capability를 단일 진실원으로 삼아 UI를 게이팅:

| UI | 활성 조건 | 비활성 시 |
|----|----------|----------|
| 자막 탭 '원문 표시' 토글 | `caps.sourceText` | 비활성 + "이 모델은 원문 전사를 제공하지 않음" |
| 오디오 탭 재생/덕킹/출력장치 | `caps.translatedAudio` | 비활성 + 안내 |
| 모델 탭 VAD: 클라이언트 | `vad.clientGate` | 비활성 |
| 모델 탭 VAD: 서버 | `vad.server` | 비활성 |
| 번역 엔진 섹션 | `engineSlots.translation` | 숨김 |
| LLM 섹션 | `engineSlots.llm` | 숨김 |
| API 키 탭 요구 | `requiresAPIKey` | false면 "이 모델은 키 불필요" |

- `AppState`는 `selectedModel: ModelDescriptor`(computed, settings.selectedModelID 기반)를 노출해 SettingsView가 게이팅에 사용.
- 모델 변경으로 capability가 줄면(예: translatedAudio 불가 모델), 관련 설정은 **시각적 비활성**만 하고 값은 보존(다시 가능한 모델로 바꾸면 복구).

---

## 6. 리소스 번들링
- `Resources/models.json`을 타깃에 포함(`project.yml`의 `sources: - path: Sources`가 비소스 파일을 resources 빌드페이즈로 분류 — JSON은 Copy Bundle Resources에 자동 포함). 확인 후 미포함 시 명시적 `resources` 항목 추가.
- 로드: `Bundle.main.url(forResource:"models", withExtension:"json")` → 실패 시 `builtInGemini` 폴백.

---

## 7. 마이그레이션 / 범위
- **이번(P1-모델)**: 스키마/카탈로그/모델탭/능력게이팅/팩토리 디스크립터 배선/VAD 이관 + Gemini 1종. spec 003(실제 온디바이스 엔진)은 **미적용**(`.onDevice*`는 팩토리 nil + "준비 중").
- **후속**: P1-A Stage/ComposedProvider(mock) → P2 실제 온디바이스 STT/번역(여기서 `.onDevice*` 모델을 JSON에 추가하면 자동 노출).

---

## 8. 오픈 이슈
- 원격 fetch 시 서명/캐시/오프라인 폴백 정책(추후).
- 사용자 오버라이드 JSON 병합 규칙(id 충돌 우선순위).
- composed 모델의 언어 제약(`targetLanguages`)과 언어 Picker 동기화.
- capability 축소 시 진행 중 자막/오디오의 깔끔한 전환(§7 핫스왑 경로 재사용).
</content>
