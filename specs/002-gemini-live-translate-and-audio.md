> **[이식 상태]** 그대로 이식(유효) · 대상 스택: Go + Wails v2 (Windows/macOS)
> Gemini Live는 순수 WebSocket+JSON+base64 PCM 프로토콜이라 언어만 바꿔 거의 무손실 이식. 오디오 피드백 루프 차단 규칙은 반드시 보존.
> 원본(macOS/Swift): `liveTranslate/specs/002-gemini-live-translate-and-audio.md` · 마스터 플랜: [000](000-cross-platform-porting-plan.md)

---

# Gemini 3.5 Live Translate 사용법 & 오디오 피드백 루프 — 검증된 사실과 재발 방지 노트

> 상태: **검증 완료(구현 반영)** · 작성일 2026-06-18 · 대상 플랫폼 macOS 14.0+(직접 캡처 14.4+)
>
> 본 문서는 추측이 아니라 **실제 WebSocket 테스트와 구현으로 확인된 사실**만 담는다.
> 코드 근거: `Sources/Gemini/GeminiLiveClient.swift`, `Sources/Audio/SystemTapAudioSource.swift`.

---

## A. Gemini 3.5 Live Translate 사용법 (검증된 사실)

### A.1 엔드포인트 / 모델

- 모델: `models/gemini-3.5-live-translate-preview` (`AppConfig.geminiModel`).
- 전송: Gemini Live API WebSocket — `BidiGenerateContent`.
  - URL: `wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent?key=<APIKEY>`
  - 구현: `URLSessionWebSocketTask` + `URLSessionWebSocketDelegate`(핸드셰이크/종료/에러 콜백).
  - **보안**: API 키가 URL 쿼리에 들어가므로 URL/에러 원문을 절대 로그하지 않는다(마스킹된 표현만 사용).

### A.2 setup 메시지 — ⚠️ 핵심 규칙: `translationConfig`는 `generationConfig` 내부

직접 WebSocket 테스트로 확인된 결정적 사실:

- **top-level `translationConfig`는 서버가 close `1007`("Unknown name translationConfig at 'setup'")로 거부**한다.
- **`generationConfig` 내부에 두어야** `setupComplete`를 받는다 → 구현 기본값 `useNestedTranslationConfig = true`.

검증된 setup 구조(요지):

```json
{
  "setup": {
    "model": "models/gemini-3.5-live-translate-preview",
    "generationConfig": {
      "responseModalities": ["AUDIO"],
      "translationConfig": {
        "targetLanguageCode": "ko",
        "echoTargetLanguage": true
      }
    },
    "outputAudioTranscription": {},
    "sessionResumption": {},
    "realtimeInputConfig": {
      "automaticActivityDetection": { "disabled": false }
    }
  }
}
```

필드별 검증 사항:

| 필드 | 값/규칙 | 근거 |
|------|---------|------|
| `responseModalities` | `["AUDIO"]` | translate 모델은 **AUDIO만** 지원. 텍스트는 transcription으로 받는다. |
| `generationConfig.translationConfig` | `{ targetLanguageCode, echoTargetLanguage: true }` | **반드시 generationConfig 내부**. top-level은 1007 거부. |
| `echoTargetLanguage` | `true` | 공식 translate 예제(gemini-live-translate-livekit) 기본값과 일치. 입력이 이미 목표 언어면 그대로 따라 말한다. |
| `outputAudioTranscription` | `{}` (항상) | **번역 자막 본문**을 텍스트 delta로 받는 경로. 자막 전용 앱이라 항상 요청. |
| `inputAudioTranscription` | **원문 동시 표시 시에만** `{}` | 기본 off → 키 **생략**(공식 translate 예제와 동일). 켜야 원문(`source`) delta가 온다. |
| `realtimeInputConfig.automaticActivityDetection.disabled` | 기본 `false`(서버 VAD ON) | 항상 명시 전송. 클라이언트 VAD 모드일 때만 `true`로 두고 수동 `activityStart`/`activityEnd` 전송. |
| `sessionResumption` | `{}`(새 세션) 또는 `{handle}`(재개) | M2c 무중단 재연결. 핸들이 있으면 그 시점부터 컨텍스트를 잇는다. |

> **소스 언어**: setup에 source 언어를 **지정하지 않는다** → 서버가 자동 감지한다.
> 다국어/코드스위칭도 자동 처리되지만 **프리뷰**이므로 실측 권장.

### A.3 오디오 송신/수신 포맷

- **입력(송신)**: 16kHz mono **Int16 LE PCM**, base64, `mimeType: "audio/pcm;rate=16000"`, **100ms 청크**(1600 샘플).
  - 내부 파이프라인은 Float32(-1...1)로 캡처 → 송신 직전 `floatToInt16LEData()`로 변환.
  - `realtimeInput.audio` 메시지로 전송.
- **출력(수신)**: 24kHz mono **Int16 LE PCM**(`modelTurn.parts[].inlineData`).
  - 자막 전용일 때(`playbackEnabled=false`)는 **디코드도 생략하고 폐기**(비용은 이미 발생하지만 처리 0).
  - 번역 음성 재생 ON이면 `TranslatedAudioPlayer`가 24kHz로 재생.

### A.4 연속 생성(continuous generation) 특성 — turnComplete 비신뢰

translate 모델은 **연속 생성**이며, 다음 동작이 관측된다:

- 한 turn 안에서 **여러 generation으로 같은 구절을 반복 재번역(revise)** 한다. 각 generation은 "현재까지 최선의 번역"을 처음부터 다시 내보낸다.
- **연속 오디오(무음 없는 영상 등)에서는 `turnComplete`/`generationComplete`를 신뢰성 있게 보내지 않는다.**
- 따라서 outputTranscription delta를 그대로 누적하면 **무한 누적/반복**이 발생한다.

구현된 방어책(`GeminiLiveClient` + `SubtitleEngine`):

1. delta는 **그대로 delta로 방출**(클라이언트가 누적/치환하지 않음). `turnComplete`/`generationComplete`는 별도 이벤트로 명시.
2. `generationComplete` 경계에서 **다음 delta가 오면 직전 generation 버퍼를 리셋(대체)** 한다.
3. **무음 폴백**: 2초간 새 delta가 없으면 현재 줄을 자동 확정(무한 누적 차단).
4. **길이 분절**: 누적이 `maxCharsBeforeBreak`(기본 50자)를 넘으면 확정 후 리셋.
5. **강제 turn 경계**(클라이언트 VAD 모드): 한 발화 세그먼트가 `maxActivitySegmentSeconds`(6초)를 넘으면 강제로 `activityEnd`를 보내 turn을 끊는다.
6. 자막 전역 중복 제거(dedup).

> 종료의 1차 신호는 `turnComplete`이고, `generationComplete`는 모델별 유무가 다른 **보조 신호**다.

### A.5 비용 (프리뷰 단가, 변동 가능)

| 항목 | 단가 | 비고 |
|------|------|------|
| 오디오 입력 | **$3.50 / 1M tokens** | `AppConfig` 상수, 25 tokens/초 |
| 오디오 출력 | **$21.00 / 1M tokens** | 전체 비용의 **~85%** |

- **출력 오디오는 AUDIO 강제**라 재생하지 않아도 생성·과금된다.
- **무음도 과금**된다 → **클라이언트 VAD(Silero)** 로 발화 구간만 송신해 입력·출력 동시 절감.
- 비용 추정(`CostEstimator`): 입력 = 송신 누적시간 × 25 tokens/s × 단가, 출력 = `usageMetadata`의 출력 오디오 토큰(AUDIO modality 우선, 없으면 `responseTokenCount` 폴백).

### A.6 무중단 재연결 (M2c)

- `sessionResumptionUpdate(resumable=true)`의 `newHandle`을 보관 → 재연결 시 setup의 `sessionResumption.handle`로 컨텍스트를 잇는다.
- `goAway` 수신 시 보관 핸들로 **선제 핸드오버**.
- 세션 한도(약 15분) 전 **선제 재연결 타이머**(14분)로 무중단 갱신.
- 핸드셰이크 단계에서 핸들 재개가 실패하면 핸들을 폐기하고 새 세션으로 재시도(영구 실패 방지).

---

## B. 오디오 피드백 루프 (재발 방지 노트)

### B.1 증상

- **번역 오디오 재생이 ON일 때만** 자막/오디오가 무한 반복된다.
- 재생이 OFF면 깨끗하다.
- VAD 전략·setup 내용과 **무관**하게 재현된다 → "playback ON일 때만"이 결정적 단서.

### B.2 원인

- `SystemTapAudioSource`가 `CATapDescription(monoGlobalTapButExcludeProcesses: [])` 로 **전체 시스템 출력**을 캡처한다.
- 번역 음성 재생을 켜면 **우리 앱이 출력하는 번역 음성도 전체 시스템 탭에 다시 잡혀** 입력으로 되돌아간다(loopback).
- 모델이 자기 출력을 재번역 → 같은 구절이 무한 반복.

### B.3 해결 (커밋 `4eb9215`)

`SystemTapAudioSource.setupTap()`에서 **우리 앱 자신의 프로세스를 탭에서 제외**한다:

- `kAudioHardwarePropertyTranslatePIDToProcessObject`로 자기 PID → `AudioObjectID` 변환(`currentProcessAudioObjectID()`).
- 변환한 ID를 `CATapDescription(monoGlobalTapButExcludeProcesses: [selfID])` 로 탭에서 제외.
- 결과: 원본 콘텐츠 소리만 캡처되고 번역 출력은 잡히지 않아 피드백이 끊긴다.
- 조회 실패 시 빈 목록으로 폴백(기존 동작 유지).

> BlackHole 입력을 쓰는 경우, 출력 라우팅 분리(번역 음성을 탭 대상이 아닌 장치로 보내기)는 **사용자 책임**이다.

### B.4 오답노트 (증상 완화 ≠ 근본 해결)

다음은 모두 **증상을 줄였을 뿐** 근본 원인이 아니었다:

- VAD 전략 변경 / double-VAD 제거
- `generationComplete`·`turnComplete` 신호 처리
- 강제 분절(maxActivitySegment)
- 자막 dedup

**결정적 단서는 "playback ON일 때만 발생"** 이었고, 근본 해결은 **탭에서 자기 프로세스 제외**였다.
이후 같은 증상이 재발하면 먼저 **탭 제외 목록에 자기 PID가 들어가 있는지**부터 확인할 것.

---

## 참고

- 설계 전반: [001-liveTranslate-design.md](001-liveTranslate-design.md)
- 릴리스/자동 업데이트: [../ref-docs/claude/release-guide.md](../ref-docs/claude/release-guide.md)
- 코드: `Sources/Gemini/GeminiLiveClient.swift`, `Sources/Audio/SystemTapAudioSource.swift`, `Sources/Subtitle/SubtitleEngine.swift`, `Sources/Cost/CostEstimator.swift`
