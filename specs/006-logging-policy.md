> **[이식 상태]** 이식 대상(적응 필요) · 대상 스택: Go + Wails v2 (Windows/macOS)
> 로깅 정책·태그 체계 유지, os.Logger→Go 표준 로거로 대체.
> 원본(macOS/Swift): `liveTranslate/specs/006-logging-policy.md` · 마스터 플랜: [000](000-cross-platform-porting-plan.md)

---

---
id: logging-policy
title: 로깅 정책 — 계층 추적 + 체크포인트 + human-readable prefix
type: design
version: 0.1.0
status: draft
scope: 파이프라인/설정/엔진 전반의 로깅 규약. 계층(추상→하위→실제값) 추적, 필수 체크포인트, 일관된 prefix, 비밀값 금지.
related: [translation-pipeline-architecture, model-catalog-and-settings, gemini-live-translate-and-audio]
updated: 2026-06-19
---

# 로깅 정책

> 목표: 로그만 읽어도 **"어느 추상 레벨에서 호출 → 어느 하위 레벨로 내려가 → 실제 어떤 값으로
> 동작했는가"** 가 보이게 한다. 설정 로드/변경 등 핵심 체크포인트는 **필수 로깅**, prefix는
> 사람이 바로 읽을 수 있게. 기존 하니스 규칙([ref-docs/claude/conventions.md])과 충돌 없이 확장.

---

## 1. 도구 / 기본 규칙

- `os.Logger` 사용. `subsystem = AppConfig.bundleIdentifier`, `category =` 컴포넌트명.
- 메시지 맨 앞에 **human-readable prefix** `[<TAG>]`를 붙인다(category가 안 보이는 뷰어 대비).
- prefix 상수는 `Sources/Config/LogTag.swift`에 모아 오타/표류 방지(아래 §2).
- **비밀값 절대 금지**: API 키/키가 든 URL/원문 전체를 로그하지 않는다. 키는 `key=present|absent`
  또는 짧은 지문만. (현행 마스킹 규칙 유지 — [[gemini-live-translate-and-audio]] A.1)
- 레벨 규칙:
  - `.error` — 실패/영구 실패/폴백 발생.
  - `.info` — 상태 전이·체크포인트·**실제 확인값**(설정 로드/변경, 세션 start/stop/swap, provider 수명).
  - `.debug` — 고빈도/세부값(오디오 청크, delta 등). 반드시 스로틀.
- `privacy`: 사람이 읽을 비민감 값은 `privacy: .public`(기본이 private라 `<redacted>`로 가려짐). 민감값은 로그하지 않음(마스킹이 아니라 제외).

## 2. Prefix / 컴포넌트 태그

| 레이어 | TAG | category | 용도 |
|--------|-----|----------|------|
| 오케스트레이션(추상) | `[AppState]` | AppState | 사용자 의도/세션 진입점 |
| 상태머신 | `[Reconcile]` | AppState | 전이 판정/수행 |
| 카탈로그 | `[Catalog]` | ModelCatalog | 모델 로드/선택 |
| 팩토리 | `[Factory]` | Pipeline | provider 생성 분기 |
| 제공자(어댑터) | `[Provider]` | Pipeline | start/stop/이벤트 사상 |
| 엔진 | `[Gemini]` | GeminiLive | WebSocket/연결/수신 |
| 설정 | `[Settings]` | Settings | 로드/변경 |
| 오디오 입력 | `[Audio]` | Audio | 캡처/소스/VAD |
| 자막 | `[Subtitle]` | Subtitle | 누적/확정 경계 |
| 비용 | `[Cost]` | Cost | 사용량 누적 |

메시지 형식(권장): `"[TAG] <event> — k=v k=v"` (event는 동사구, 값은 `k=v` 나열).
예) `"[Factory] make — engine=geminiLive model=models/gemini-3.5-live-translate-preview caps(src=t txt=t audio=t) key=present"`

## 3. 계층 추적 (추상 → 하위 → 실제값)

하나의 흐름은 위에서 아래로 같은 사건이 **각 레이어에서 한 줄씩** 찍혀야 한다. 예: 번역 시작

```
[AppState]  start requested — desired=true isRunning→true
[Reconcile] step — desired=true actual=false → performStart
[AppState]  config resolved — model=gemini-3.5-live-translate engine=geminiLive lang=ko showSource=false playback=true vad=client key=present
[Factory]   make — engine=geminiLive model=models/...-preview caps(src=t txt=t audio=t)
[Provider]  start — engine=gemini model=models/...-preview
[Gemini]    connect — state=connecting
[Gemini]    setupComplete — state=ready
[AppState]  performStart done — actualRunning=true
```

핫스왑/정지/실패도 동일하게 각 레이어가 자기 줄을 남긴다(누락 금지).

## 4. 필수 체크포인트 (반드시 로깅)

1. **설정 로드**(`SettingsStore.init`): 해석된 **실제값** 요약 1줄 —
   `[Settings] loaded — model=.. lang=.. showSource=.. playback=.. duck=.. inputSel=.. vad=..`
2. **설정 변경**(각 의미 있는 setter): `[Settings] change — <key>: <old> → <new>`
   (대상: selectedModelID, targetLanguageCode, showSourceText, translatedAudioPlaybackEnabled/Volume,
    originalAudioDucking*, translatedAudioOutputDeviceUID, 입력 선택, subtitle 위치/스타일 핵심, vad).
3. **카탈로그 로드**(`ModelCatalog.init`): `[Catalog] loaded — count=N source=bundle|fallback default=<id>`;
   실패/폴백 시 `.error` 로 사유.
4. **세션 전이**(start/stop/swap): 진입/완료 + **해석된 config**(model/lang/vad/key present). (§3 예시)
5. **팩토리 분기**: `[Factory] make — engine/model/caps` 또는 미지원 시 `[Factory] unsupported engine — …`(준비 중).
6. **provider 수명**: `[Provider] start/stop`, 이벤트 사상 중 상태/실패는 `.info/.error`.
7. **엔진 핵심 포인트**: connect/setupComplete(ready)/reconnect/goAway/permanentFailure/close. (키/URL 비노출)
8. **키 상태 변화**: 저장/삭제/출처 — `[Settings] key — source=keychain|none present=true|false`(값 비노출).

## 5. 고빈도 경로 스로틀 (info 금지)

- 오디오 청크 송신, outputAudio enqueue, translation/source delta 누적은 **`.debug`**, 그리고
  **첫 1회 + N회마다 1회**(기존 `sendAudioDropCount`/`outputAudioEnqueueCount` 패턴 재사용)만 남긴다.
- 드롭/백프레셔 이벤트는 은폐하지 않되 스로틀 카운트로 요약(`dropped=K`).

## 6. 적용 범위 (이번 작업)

- 신규/변경 파이프라인 파일과 설정/카탈로그를 우선 정비: AppState(Reconcile/AppState), Pipeline
  (Factory/Provider), ModelCatalog, SettingsStore. 기존 `GeminiLiveClient` 로그는 prefix만 `[Gemini]`로 정렬(동작 무변경).
- 무분별한 신규 로그 추가 금지 — §4 체크포인트와 §3 계층 한 줄 원칙에 맞춰 **필요한 곳만**.

## 7. 검증

- 번역 시작→정지→언어변경(핫스왑)→정지를 수행하면 §3 형태의 계층 추적이 로그에 연속으로 남고,
  설정 변경 시 §4.2 라인이 남는지 콘솔(Console.app, subsystem 필터)로 확인.
- 키/URL/원문 전체가 어떤 로그에도 나타나지 않음을 grep로 점검.
</content>
