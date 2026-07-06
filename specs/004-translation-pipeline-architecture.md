> **[이식 상태]** 이식 대상(Gemini 경로만) · 대상 스택: Go + Wails v2 (Windows/macOS)
> Provider/PipelineEvent 추상화와 reconciler+epoch 펜싱 불변식은 그대로 이식. Composed(Apple STT+MT) 경로는 v1 제외.
> 원본(macOS/Swift): `liveTranslate/specs/004-translation-pipeline-architecture.md` · 마스터 플랜: [000](000-cross-platform-porting-plan.md)

---

---
id: translation-pipeline-architecture
title: 번역 파이프라인 추상화 — 공통 API 레이어 & 엔진 플러그인 구조
type: design
version: 0.3.0
status: draft
scope: 어떤 STT/ASR·번역·LLM 엔진이든 끼울 수 있는 공통 추상 레이어와 단계(Stage) 파이프라인 설계 + 다단계 상태 동기화/레이스 정책 + 버퍼 적체/메모리 누수 방지. Gemini Live 무손상 리팩토링.
related: [gemini-live-translate-and-audio, ondevice-asr-research, liveTranslate-design]
updated: 2026-06-19
---

# 번역 파이프라인 추상화 — 공통 API 레이어 & 엔진 플러그인 구조

> 목표: **오디오 입력 → 음성/번역 엔진 → 결과 처리(자막/오디오/비용)** 흐름을,
> 어떤 엔진(클라우드 STT/번역, 온디바이스 ASR, 로컬 LLM 등)이든 갈아끼울 수 있도록
> **공통 API 레이어(추상 계층)** 아래에 실제 엔진을 정합하는 구조로 정리한다.
> Gemini Live 경로는 **동작/품질 무손상**으로 이 추상화의 한 구현이 된다.

---

## 1. 목표 / 비목표

### 목표
- 번역 백엔드를 **프로토콜로 추상화**해 `AppState`가 구체 타입(`GeminiLiveClient`)에 직접 결합되지 않게 한다.
- 두 가지 파이프라인 형태를 **하나의 공통 이벤트 모델**로 흡수:
  - (a) **통합형**: 오디오 → (번역된 텍스트 + 번역 오디오)가 한 엔진에서 나옴 (예: Gemini Live).
  - (b) **조합형**: 오디오 → ASR(원문 텍스트) → 번역 엔진/LLM(번역 텍스트) → (선택) TTS.
- ASR/번역/교정/합성을 **단계(Stage)** 로 모델링해 자유롭게 조합·치환·추가.
- "API 키 없는 사용자" 옵션(온디바이스)을 **새 파이프라인 구성**만으로 제공([[ondevice-asr-research]]).

### 비목표
- Gemini Live 대체가 아님. 품질/지연 회귀 금지.
- 이 문서는 **설계 스펙**이다. 구현 태스크/수용기준은 후속 impl 문서로 분리한다.
- 특정 온디바이스 엔진 선정은 003 조사 결과에 위임(여기선 "끼울 수 있는 자리"만 정의).

---

## 2. 현재 구조 요약 (리팩토링 출발점)

`Sources/App/AppState.swift` 기준 현재 배선(요지):

```
AudioInputManager ──onChunk([Float])──▶ GeminiLiveClient(actor)
                                            │  connect() -> AsyncStream<Event>
                                            ▼
                          AppState.handle(_ event: GeminiLiveClient.Event)
                            ├ .translation/.source/.turnComplete/.generationComplete ─▶ SubtitleEngine
                            ├ .outputAudio ─▶ TranslatedAudioPlayer
                            ├ .sentAudio/.outputTokens ─▶ CostEstimator
                            └ .state/.info/.permanentFailure/.interrupted ─▶ 상태/수렴
```

평가(결합도):

| 컴포넌트 | 결합도 | 추상화 상태 | 조치 |
|---------|-------|-----------|------|
| `AudioInputManager` / `AudioSource` | 중 | **이미 프로토콜화 ✓** | 그대로 재사용 (입력 어댑터) |
| `GeminiLiveClient` + `Event` | **높음** | 없음 (AppState가 직접 타입 참조 ~7곳) | **프로토콜 도입 핵심 대상** |
| `SubtitleEngine` (ingest*) | 낮 | 메서드 인터페이스 깔끔 | 이벤트 sink로 재사용 |
| `TranslatedAudioPlayer` | 낮 | enqueue/flush/stop 깔끔 | 출력 어댑터로 재사용 |
| `CostEstimator` | 낮 | add* 깔끔 | 비용 sink로 재사용 |

> 결론: **입력단/출력단은 이미 충분히 추상적**이다. 핵심은 가운데 "번역 엔진"을
> 프로토콜로 빼고, 이벤트 모델을 백엔드 독립적으로 통일하는 것.

---

## 3. 설계 원칙

1. **단방향 데이터 흐름 + 단일 이벤트 스트림**: 모든 백엔드/파이프라인은 `AsyncStream<PipelineEvent>` 하나로 결과를 방출한다. `AppState`는 이벤트 종류만 알면 된다(엔진 무지).
2. **단계(Stage) 합성**: 파이프라인 = Stage들의 조합. Stage는 입력 타입 → 출력 이벤트로 정의. 통합형은 1-Stage, 조합형은 N-Stage.
3. **능력 선언(Capability)**: 각 백엔드/Stage가 무엇을 생산하는지(원문/번역/오디오), 키 필요 여부, 스트리밍 여부, 지원 언어를 **선언**한다. 상위가 이를 보고 자막/오디오/비용 UI를 조정.
4. **무손상 어댑터**: 기존 구체 컴포넌트는 삭제하지 않고 **어댑터로 감싸** 새 프로토콜에 적합화. Gemini 경로는 1:1로 보존.
5. **동시성 안전**: Stage 간 경계는 `actor` + `AsyncStream`(또는 `AsyncChannel류`)로 격리. 오디오 콜백은 `@Sendable`, UI 반영은 `@MainActor` hop(현행 규약 유지).
6. **결정적 구성**: 파이프라인 구성은 설정에서 결정(엔진 선택). Date/난수 미사용(현행 규약).

---

## 4. 공통 데이터 모델

### 4.1 입력 — 오디오 청크
현행 유지: `[Float]` 16kHz mono Float32 100ms(1600샘플). 별칭만 도입해 의미 명확화.

```swift
/// 16kHz mono Float32, 100ms(1600 samples) 청크. 실시간 오디오 스레드에서 전달될 수 있음.
typealias AudioChunk = [Float]
```

### 4.2 출력 — 통합 파이프라인 이벤트
백엔드 독립적인 단일 이벤트 enum. (현행 `GeminiLiveClient.Event`의 상위 집합 — 매핑 §8.1)

```swift
enum PipelineEvent: Sendable {
    // 연결/수명
    case state(PipelineState)              // idle/preparing/ready/reconnecting/error
    case info(String)                      // 사람이 읽는 상태 메시지(키 비포함)
    case permanentFailure(reason: String)  // 복구 불가 — 상위가 정지/수렴
    case interrupted                       // 진행 중 응답 폐기 신호

    // 텍스트 결과 (델타 누적 모델 — SubtitleEngine과 호환)
    case sourceText(delta: String)         // 원문(전사) delta
    case translatedText(delta: String)     // 번역문 delta
    case turnComplete                      // 발화 단위 종료
    case generationComplete                // 재생성(revise) 경계

    // 오디오 결과 (통합형/TTS가 생산)
    case outputAudio(Data)                 // 24kHz mono Int16 LE PCM

    // 계측(비용/사용량) — 엔진별 의미는 metric 종류로 구분
    case usage(UsageMetric)
}

enum PipelineState: Sendable { case idle, preparing, ready, reconnecting, error }

enum UsageMetric: Sendable {
    case sentAudio(sampleCount: Int)       // 송신 오디오 샘플 수(클라우드 입력비용)
    case outputAudioTokens(Int)            // 출력 오디오 토큰(클라우드 출력비용)
    case localCompute(stage: String, ms: Int)  // 온디바이스 추론 시간(비용 0, 통계용)
}
```

> 설계 의도: `SubtitleEngine`은 `sourceText/translatedText/turnComplete/generationComplete`만,
> `TranslatedAudioPlayer`는 `outputAudio`만, `CostEstimator`는 `usage`만 소비한다.
> 온디바이스 백엔드는 `outputAudio`/`usage(sentAudio)`를 내지 않아도 되며(능력 선언으로 표시),
> 그 경우 비용은 0·재생은 비활성으로 자연스럽게 처리된다.

### 4.3 능력 선언

```swift
struct EngineCapabilities: Sendable {
    var producesSourceText: Bool       // 원문 자막 가능
    var producesTranslatedText: Bool   // 번역 자막 가능
    var producesTranslatedAudio: Bool  // 번역 오디오 재생 가능
    var requiresAPIKey: Bool           // 키 필요(없으면 옵션 비활성/안내)
    var isStreaming: Bool              // 실시간 부분결과 지원
    var supportedTargetLanguages: [String]?  // nil=제약 없음/미상
    var supportedSourceLanguages: [String]?
}
```

---

## 5. 추상 계층 — 핵심 프로토콜

### 5.1 최상위: 번역 제공자 (파이프라인 1개 = 제공자 1개)

```swift
/// 오디오를 받아 PipelineEvent 스트림을 내는 "완성된" 번역 경로.
/// 통합형 엔진 1개일 수도, 여러 Stage를 합성한 것일 수도 있다. AppState는 이것만 안다.
protocol TranslationProvider: AnyObject, Sendable {
    var capabilities: EngineCapabilities { get }

    /// 결과 이벤트 스트림 시작. 내부 연결/모델로드/Stage 배선을 수행.
    /// (구현 주: actor 채택체가 Swift 6 strict concurrency를 만족하려면 `async`여야 한다 — P0에서 확정.)
    func start() async -> AsyncStream<PipelineEvent>

    /// 오디오 청크 주입(실시간). 오디오 스레드에서 호출 가능 → nonisolated 권장.
    nonisolated func send(_ chunk: AudioChunk)

    /// 정지(완전 종료까지 await — 좀비/중복 방지, 현행 stopGemini 규약 유지).
    func stop() async

    /// 런타임 토글(번역 오디오 재생 on/off 등). 미지원이면 no-op.
    func setTranslatedAudioPlayback(_ on: Bool) async
}
```

### 5.2 단계(Stage) 프로토콜 — 조합형의 빌딩블록

세 가지 입력/출력 형태를 구분한다.

```swift
/// (a) 음성→번역 통합 단계 (예: Gemini Live). 오디오 in → 번역(+원문+오디오) 이벤트.
protocol SpeechTranslationStage: AnyObject, Sendable {
    var capabilities: EngineCapabilities { get }
    func start() -> AsyncStream<PipelineEvent>
    nonisolated func send(_ chunk: AudioChunk)
    func stop() async
}

/// (b) 음성→원문텍스트 단계 (ASR/STT). 오디오 in → sourceText delta + 경계.
protocol SpeechToTextStage: AnyObject, Sendable {
    var capabilities: EngineCapabilities { get }
    func start() -> AsyncStream<TextSegmentEvent>   // 원문 텍스트 전용 이벤트
    nonisolated func send(_ chunk: AudioChunk)
    func stop() async
}

/// (c) 텍스트→텍스트 단계 (번역 엔진/LLM 교정·번역). 원문 in → 번역 delta.
protocol TextTransformStage: AnyObject, Sendable {
    var capabilities: EngineCapabilities { get }
    /// 확정/부분 원문 세그먼트를 받아 번역 텍스트 이벤트를 낸다(스트리밍/배치 모두 허용).
    func transform(_ input: AsyncStream<TextSegmentEvent>) -> AsyncStream<TextSegmentEvent>
    func stop() async
}

/// (d) 텍스트→음성 단계 (TTS, 선택). 번역 텍스트 in → outputAudio.
protocol SpeechSynthesisStage: AnyObject, Sendable {
    var capabilities: EngineCapabilities { get }
    func synthesize(_ input: AsyncStream<TextSegmentEvent>) -> AsyncStream<Data>  // PCM
    func stop() async
}

/// Stage 간 텍스트 전달용 경량 이벤트.
enum TextSegmentEvent: Sendable {
    case delta(String)        // 부분 텍스트
    case turnComplete         // 발화 단위 종료
    case generationComplete   // 재생성 경계
    case info(String)
    case failure(String)
}
```

### 5.3 합성기 — Stage들을 TranslationProvider로 묶기

```swift
/// 조합형 파이프라인: STT → (Transform: 번역/교정) → (선택)TTS 를 묶어
/// 단일 PipelineEvent 스트림으로 변환한다. 능력은 구성 Stage들로부터 합산.
final class ComposedTranslationProvider: TranslationProvider {
    init(stt: SpeechToTextStage,
         transform: TextTransformStage,
         tts: SpeechSynthesisStage? = nil)
    // start(): stt.start() → transform.transform(...) → (tts.synthesize) 를 배선하고
    //          TextSegmentEvent/PCM 을 PipelineEvent 로 사상해 방출.
}

/// 통합형 파이프라인: SpeechTranslationStage 1개를 그대로 Provider로.
final class IntegratedTranslationProvider: TranslationProvider {
    init(stage: SpeechTranslationStage)   // 예: GeminiLiveStage
}
```

### 5.4 두 파이프라인 형태 (목표 시나리오)

```
통합형 (현행 Gemini):
  AudioInputManager → IntegratedTranslationProvider(GeminiLiveStage) → PipelineEvent
      └ translatedText/sourceText/outputAudio/usage 모두 한 곳에서.

조합형 (키 없는 온디바이스):
  AudioInputManager → ComposedTranslationProvider(
        stt:       AppleSpeechSTTStage | WhisperKitSTTStage | FluidAudioSTTStage,
        transform: AppleTranslationStage | LocalLLMStage,
        tts:       nil | AppleTTSStage )
      → PipelineEvent
      └ sourceText(ASR) → translatedText(MT/LLM) → (선택)outputAudio(TTS).
```

LLM 교정/번역도 `TextTransformStage`의 한 구현으로 자연스럽게 들어온다(예: 원문→교정→번역 체인을 Transform 내부에서 처리하거나 Transform 2개를 직렬).

---

## 6. 엔진 선택 & 팩토리

```swift
enum TranslationEngineKind: String, Codable, Sendable {
    case geminiLive          // 통합형(클라우드, 키 필요)
    case onDeviceTranscribe  // 조합형: ASR만 (원문 자막 전용, 키 불필요)
    case onDeviceTranslate   // 조합형: ASR + 온디바이스 MT (키 불필요)
    // 향후: cloudSTTPlusLLM 등
}

/// 설정 + 키 가용성으로부터 Provider를 생성. AppState는 이 팩토리만 호출.
@MainActor
struct TranslationProviderFactory {
    func make(kind: TranslationEngineKind,
              settings: SettingsStore,
              apiKey: String?) -> TranslationProvider?
    // geminiLive: 키 없으면 nil(상위가 안내/대체 제안)
    // onDevice*: 키 무관, 모델 준비 상태에 따라 준비 이벤트로 진행
}
```

자동 폴백 정책(권장 기본):
- 키 있음 → `geminiLive`(현행 동작 100% 보존).
- 키 없음 → 설정의 온디바이스 옵션이 켜져 있으면 `onDeviceTranscribe`/`onDeviceTranslate`, 아니면 기존처럼 "키 필요" 안내.

---

## 7. 동시성 & 상태 동기화 정책 (핵심)

> 단계가 늘수록 "언제 무엇을 죽이고/세우고/버리는가"가 정확성을 좌우한다.
> 목표: **사용자가 제어 HUD에서 중지/시작을 무작위로 난타하거나, 번역 중 설정에서
> 엔진/언어/키를 바꿔도 깨지지 않고 자연스럽게 수렴**한다. 1~2초의 버벅임/끊김은 허용.

### 7.0 기본 동시성 모델
- **Stage 경계**: 각 Stage는 `actor`/내부 격리. Stage 간은 `AsyncStream`으로 연결(역압=버퍼 정책은 Stage 소유, 현행 VADGate/플레이어 백프레셔 재사용).
- **오디오 주입**: `send(_:)`는 `nonisolated`(실시간 스레드) → 내부 actor hop(현행 `sendAudio`와 동일).
- **이벤트 소비**: `AppState`가 `for await event in provider.start()`를 **단일 Task**로 소비, `@MainActor`에서 sink로 분배.

### 7.1 불변식 (절대 규칙)
1. **단일 진실원천 = 의도(Desired)**. UI/이벤트는 의도만 바꾸고, 실제 자원은 reconciler만 만진다.
2. **활성 Provider는 항상 0개 또는 1개**. 두 Provider가 동시에 오디오를 받거나 이벤트를 내는 상태는 존재하지 않는다.
3. **상태 전이는 단일 직렬 reconciler 1개만 수행**. 동시에 두 전이가 진행되지 않는다.
4. **교체는 무중첩**: 새 Provider를 세우기 전에 이전 Provider를 `await stop()`으로 **완전히** 내린다.
5. **stale 비동기 결과는 epoch로 폐기**. 지난 세대의 이벤트/콜백/모델로드 완료는 무시한다.
6. 모든 전이는 **멱등·재진입 안전**. 같은 의도를 여러 번 적용해도 결과 동일.

### 7.2 상태 모델

```swift
/// 사용자/설정이 표현하는 "원하는 상태". 값 타입 — 비교로 변경 감지.
struct DesiredState: Equatable {
    var running: Bool
    var engine: TranslationEngineKind
    var targetLanguage: String
    var showSource: Bool
    var playback: Bool
    var keyFingerprint: String?   // 키 자체가 아니라 해시/식별자(로그/비교용, 평문 금지)
}

/// 실제로 가동 중인 파이프라인의 구성 스냅샷.
struct ActualState: Equatable {
    var running: Bool
    var config: ProviderConfig    // engine/lang/showSource/playback/keyFingerprint 묶음
}
```

`AppState`(@MainActor)가 보유:
```swift
private var desired = DesiredState(...)        // UI/설정이 갱신
private var actual  = ActualState(running:false, ...)
private var epoch: UInt64 = 0                  // 세대 토큰(교체마다 +1)
private var provider: TranslationProvider?     // 활성 1개 또는 nil
private var eventTask: Task<Void, Never>?      // 활성 Provider 이벤트 소비 Task
private var reconcileTask: Task<Void, Never>?  // 단일 직렬 수렴 루프
```

### 7.3 단일 직렬 reconciler (일반화)

```swift
/// UI/설정/이벤트는 desired만 바꾸고 이걸 호출. 이미 돌고 있으면 새로 안 띄운다.
private func kickReconcile() {
    guard reconcileTask == nil else { return }   // 직렬 보장
    reconcileTask = Task { @MainActor in
        while needsTransition() { await stepOnce() }
        reconcileTask = nil   // ← while(false) 직후 동기 대입(no await): 그 사이 토글 끼어듦 차단
    }
}

private func needsTransition() -> Bool {
    if desired.running != actual.running { return true }
    if desired.running && actual.running && desired.config != actual.config { return true } // 핫 리로드
    return false
}

private func stepOnce() async {
    if desired.running && !actual.running {
        await bringUp()                       // 0 → 1
    } else if !desired.running && actual.running {
        await tearDown()                      // 1 → 0
    } else if desired.running && actual.running && desired.config != actual.config {
        await tearDown(); await bringUp()     // 핫 스왑(엔진/언어/키 변경)
    }
}
```

> **재진입 핵심**: `reconcileTask = nil`을 루프 종료 직후 **동기적으로** 대입한다.
> 그 직전 사용자가 토글해 desired가 또 바뀌었다면, 다음 `kickReconcile()`이 새 루프를
> 띄워 마저 수렴시킨다(현행 AppState 규약을 그대로 일반화).

### 7.4 전이 표

| 트리거 | 동작 | 사용자 체감 |
|--------|------|-----------|
| 시작 클릭 | `desired.running=true` → bringUp | 즉시 HUD, 1~2s 내 준비 |
| 중지 클릭 | `desired.running=false` → tearDown(완전 await) | 즉시 정지 |
| 시작/중지 난타 | desired만 갱신, reconciler가 **최종 의도로 수렴**(중간 상태 건너뜀) | 마지막 누름이 이김 |
| 엔진 변경(번역 중) | `desired.engine` 변경 → tearDown+bringUp(핫스왑) | 1~2s 끊김 후 새 엔진 |
| 언어/키 변경(번역 중) | `desired.config` 변경 → 핫스왑 | 1~2s 끊김 후 반영 |
| `showSource`/`playback` 토글 | config 변경이지만 **경량 적용 우선**(§7.8) | 무중단 즉시 |
| `permanentFailure` | `desired.running=false` 또는 폴백(§7.11) | 안내 + 정지/대체 |

### 7.5 epoch(세대) 토큰 — stale 비동기 무효화

```swift
private func bringUp() async {
    epoch &+= 1
    let myEpoch = epoch
    let cfg = ProviderConfig(from: desired)
    guard let p = factory.make(cfg) else { desired.running = false; return }
    provider = p
    let stream = p.start()
    eventTask = Task { @MainActor in
        for await event in stream {
            guard myEpoch == self.epoch else { continue }   // ← stale 이벤트 폐기
            self.route(event)
        }
    }
    audio.onChunk = { [weak self] chunk in
        guard let self, myEpoch == self.epoch else { return } // ← 지난 세대로 송신 금지
        self.provider?.send(chunk)
    }
    audio.requestPermissionAndStart()
    applyAudioOutputPolicy()
    actual = ActualState(running: true, config: cfg)
}

private func tearDown() async {
    epoch &+= 1                       // 이 시점 이후 모든 stale 결과 무효
    eventTask?.cancel(); eventTask = nil
    audio.onChunk = nil
    audio.stop()
    if let p = provider { provider = nil; await p.stop() } // 완전 종료까지 await
    translatedAudioPlayer.stop()
    systemAudioDucker.restore()
    subtitles.reset()
    actual = ActualState(running: false, config: actual.config)
}
```

- epoch는 **bringUp/tearDown 양쪽에서 증가** → 교체 경계가 항상 새 세대로 갈린다.
- 모델 다운로드/연결 같은 느린 비동기도 완료 시 `myEpoch == epoch` 가드로 자기 세대만 반영.

### 7.6 무중첩 teardown → build 보장
- 핫스왑은 `await tearDown()` **완료 후** `bringUp()`. 두 Provider가 겹쳐 사는 구간 없음(불변식 2).
- `provider.stop()`은 **모든 Stage가 끝날 때까지 await**(`ComposedProvider.stop()`은 STT/Transform/TTS Stage를 순서 무관 전부 await; 각 Stage는 자기 자원만 책임).
- 끊김 최소화가 필요하면 후속 최적화로 "프리워밍"(다음 Provider를 백그라운드 준비 후 원자 스왑)을 검토하되, **기본은 안전한 무중첩**(1~2s 허용 범위).

### 7.7 빠른 토글 coalescing
- 난타는 이벤트마다 desired만 갱신 → reconciler는 **현재 desired**만 본다(과거 누름은 자동 무시).
- 진행 중 전이는 끝까지 수행(중단 비동기 취소 대신 epoch로 결과 폐기) → 자원 누수/반쪽 상태 없음.
- 결과: "시작→중지→시작"을 빠르게 눌러도 최종 의도(=마지막)로만 수렴.

### 7.8 엔진/설정 즉시 반영 — 경량 vs 핫스왑 구분
변경을 두 부류로 나눈다(현행 "불가능한 것 제외 즉시반영" 원칙 유지):
- **경량(무중단)**: 활성 Provider가 런타임 토글을 지원하는 항목.
  - `playback` → `await provider.setTranslatedAudioPlayback(on)` (재구축 불필요).
  - 입력 소스 hot-swap → `AudioInputManager`가 처리(Provider 무관, 현행 유지).
  - 자막 스타일/위치 → @Observable로 즉시(엔진 무관).
- **핫스왑(1~2s 끊김)**: 엔진 종류/대상언어/원문표시 모드/키 변경 → `desired.config` 갱신 후 reconciler가 tearDown+bringUp.
- 분기 규칙: 변경 핸들러가 "경량으로 처리 가능?"을 먼저 시도, 아니면 config만 바꾸고 `kickReconcile()`.

### 7.9 이벤트 라우팅 가드
- 이벤트 소비 Task는 자기 `myEpoch`를 캡처 → 세대 불일치 이벤트는 `continue`로 폐기.
- `route(event)`는 sink 분배만 담당(부수효과는 sink 내부). 실패/인터럽트도 현재 세대일 때만 상태 반영.

### 7.10 Stage 수준 동시성/취소
- 각 Stage `start()`/`transform()`이 만든 내부 Task는 Stage `stop()`에서 취소+정리.
- Stage 간 `AsyncStream`은 상류 종료 시 자연 종료(`finish()`) → 하류도 루프 탈출.
- 모델 로드 등 장시간 작업은 Stage 내부 epoch/취소로 보호(상위 epoch와 독립이지만, 상위 tearDown이 stop()을 await하므로 정합).

### 7.11 실패 / 폴백 정책
- `permanentFailure`(현재 세대): 기본 `desired.running=false` + 사유 노출 후 정지.
- 선택 폴백: 키 만료/네트워크 실패 시 `desired.engine=onDeviceTranscribe`로 전환 제안/자동(설정에 따름) → 핫스왑 경로 재사용.
- 폴백도 동일 reconciler/epoch를 타므로 별도 동시성 경로가 생기지 않는다.

### 7.12 검증 기준 (수용)
- **난타 테스트**: 시작/중지를 빠르게 N회 → 최종 상태가 마지막 의도와 일치, 활성 Provider ≤1, 잔류 Task/오디오 0.
- **핫스왑 테스트**: 번역 중 엔진/언어 변경 → 1~2s 내 새 구성으로 전환, 이전 세대 이벤트가 자막에 섞이지 않음.
- **stale 테스트**: 느린 연결/모델로드가 tearDown 이후 완료해도 UI/오디오에 영향 없음.
- **누수 테스트**: 반복 전이 후 메모리/오디오 노드/소켓 수가 기준선으로 회귀.

### 7.13 자원 수명 · 버퍼 적체 · 메모리 누수 방지 (필수)

> 단계가 늘고 세션이 길어지고 엔진 교체가 반복될수록, **플러시되지 않은 버퍼**와
> **해제되지 않은 자원/Task**가 누적될 위험이 커진다. 아래를 설계·구현·검증의 강제 항목으로 둔다.

#### 7.13.1 위험 인벤토리 (어디서 쌓이는가)

| # | 위험 | 발생 시나리오 | 영향 |
|---|------|--------------|------|
| R1 | **무한 버퍼 AsyncStream** | Stage 간 스트림이 소비자보다 빠른 생산자를 무제한 버퍼링 | 시간 누적형 메모리 증가 |
| R2 | **오디오 청크 큐 적체** | VAD/ASR/송신 큐가 역압 없이 누적(느린 추론/네트워크) | 지연 폭증 + 메모리 |
| R3 | **stale 청크 잔류** | 교체 직전 이전 Provider 큐에 남은 청크가 flush 안 됨 | 옛 오디오가 새 세대에 영향/메모리 |
| R4 | **Task 미취소** | `eventTask`/Stage 내부 Task가 stop에서 취소 안 됨 | Task 누수 + 강한 self 참조 |
| R5 | **retain cycle** | `audio.onChunk`/콜백/continuation이 self 강참조 | Provider/AppState 영구 잔류 |
| R6 | **Core Audio 자원 누수** | AVAudioEngine 노드/tap, aggregate device, CATap 미해제 | OS HAL 자원 누수(교체 반복 시 치명) |
| R7 | **모델 가중치 상주** | ASR/MT/LLM/TTS 모델을 static 싱글톤이 무한 캐시 | 교체할수록 RAM 누적 |
| R8 | **무한 히스토리** | 자막 confirmed/전역 dedup 집합, 오디오 dedup 윈도우가 무한 성장 | 장시간 세션형 누수 |
| R9 | **스트림 미종료** | stop 시 `continuation.finish()` 누락 → 소비 Task가 영원히 대기 | Task/메모리 누수 |
| R10 | **출력 PCM 적체** | 재생보다 빠른 생성으로 `TranslatedAudioPlayer` 큐 적체 | 메모리 + 누적 지연 |

#### 7.13.2 방지 원칙 (설계 강제)

1. **모든 버퍼는 생성 시점부터 상한(bounded)** — 무제한 `AsyncStream` 버퍼링 금지. Stage 간 스트림은 `.bufferingNewest(n)`(또는 동등 역압) + **drop-oldest** 정책. R1/R2.
   - 청크 큐 상한은 기존 패턴 재사용: VADGate `maxPendingChunks`, 플레이어 `maxInFlightFrames`. 신규 Stage(ASR 등)도 **반드시 큐 상한 + 드롭 카운터(로그 스로틀)**.
2. **단일 소유 + 결정적 teardown** — 모든 자원(Task/소켓/오디오노드/모델/큐)은 **정확히 한 Stage가 소유**하고 그 Stage의 `stop()`에서 해제. `provider.stop()`은 모든 Stage `stop()`을 await(불변식 4). R4/R6/R9.
3. **Stage 수명 계약(필수 구현)** — 모든 Stage는 다음을 보장:
   ```
   start():  내부 Task/자원 생성. 멱등(중복 호출 무해).
   flush():  진행 중 부분버퍼/큐/dedup윈도우를 비운다(경계/인터럽트 시).
   stop():   1) 내부 Task 전부 cancel  2) 큐/버퍼 flush  3) OS자원 해제
             4) 출력 스트림 continuation.finish()  — 완료까지 await 가능.
   ```
   `flush`는 generation/interrupt 경계에서, `stop`은 교체/정지에서 호출.
4. **epoch 펜싱으로 stale 차단(이중 방어)** — §7.5의 epoch 가드를 **주입 지점(`send`)과 적재 지점(큐 enqueue) 양쪽**에 둔다. 교체 시 이전 Provider는 stop()에서 큐를 **명시적으로 비우고** 새 청크 수신을 거부. R3.
5. **약한 콜백 + onTermination 정리** — 장수명 클로저(`audio.onChunk`, Stage 콜백)는 `[weak self]`. `AsyncStream`은 `continuation.onTermination`에서 자원 해제 등록 → 소비자 취소 시에도 누수 없음. R5.
6. **히스토리 상한** — 자막 전역 dedup 집합/confirmed 누적, 오디오 dedup 윈도우는 **고정 크기 슬라이딩 윈도우/최근 N개**로 캡. 장시간 세션에서도 O(1) 메모리. R8.
   - 점검: `SubtitleEngine.dedupGlobalSentences`가 현재 비교 대상(confirmed) 외 별도 무한 집합을 들지 않는지 확인, 들면 윈도우 캡 적용.
7. **모델 캐시 정책** — 모델 가중치는 **명시적 소유/해제**. 기본: Stage `stop()`에서 언로드. 잦은 교체 끊김이 문제면 **상한 있는 warm 캐시(LRU, 최대 1~2개)** 만 허용하고 초과 시 해제. R7.
8. **메모리 압력 대응** — `DispatchSource.makeMemoryPressureSource`(또는 OS 경고) 구독 → warm 모델 캐시/dedup 윈도우 즉시 축소. R7/R8.
9. **출력 재생 역압** — `TranslatedAudioPlayer`의 `maxInFlightFrames` 유지 + 교체/정지 시 `stop()`(현행). 생성 폭주 시 drop-oldest. R10.

#### 7.13.3 교체(핫스왑) 시 플러시 체크리스트
tearDown에서 **반드시** 순서대로 수행(누락 시 누수):
```
1. epoch += 1                         // 이후 stale 전부 무효
2. eventTask.cancel()                 // 소비 중단(R4)
3. audio.onChunk = nil                // 송신 경로 차단(R3/R5)
4. audio.stop()                       // 캡처 정지 + 입력 큐 flush
5. provider.stop() (await)            // 각 Stage: Task cancel→큐 flush→OS자원 해제→stream finish (R4/R6/R9)
6. translatedAudioPlayer.stop()       // 출력 PCM 큐 flush(R10)
7. systemAudioDucker.restore()        // 시스템 볼륨 원복
8. subtitles.reset()                  // 부분/확정/ dedup 윈도우 초기화(R8)
9. provider = nil                     // 마지막 강참조 해제(R5)
```
> 6~8은 sink 측이라 Provider 교체와 무관하게 항상 깨끗한 상태로 리셋된다. bringUp은 이 체크리스트 완료 후에만 진행(무중첩).

#### 7.13.4 계측·검증 (수용 기준 확장)
- **Soak 테스트**: 장시간(예: 1~2시간) 연속 번역 후 메모리/큐 깊이가 **평탄(flat)** 유지(우상향 금지) → R1/R8/R10.
- **스왑 반복 테스트**: 엔진 A↔B를 N회(예: 100회) 교체 후 RSS/Task수/오디오노드/소켓/모델로드 수가 **기준선 회귀** → R4/R6/R7.
- **누수 도구**: Instruments **Leaks/Allocations**로 retain cycle 0 확인(R5). DEBUG 빌드에 `deinit` 로그/자원 카운터를 두어 교체 시 해제 추적.
- **드롭 가시화**: 큐 드롭/스트림 버퍼 드롭은 **로그로 노출**(은폐 금지) — 적체가 정책적 드롭인지 누수인지 구분.
- **stale 무영향**: tearDown 이후 도착한 옛 청크/이벤트가 새 세대 버퍼에 적재되지 않음(카운터=0).

---

## 8. 기존 구성요소 매핑 (무손상 리팩토링)

### 8.1 `GeminiLiveClient.Event` → `PipelineEvent`

| 현행 | 신규 |
|------|------|
| `.translation(delta)` | `.translatedText(delta)` |
| `.source(delta)` | `.sourceText(delta)` |
| `.turnComplete` | `.turnComplete` |
| `.generationComplete` | `.generationComplete` |
| `.outputAudio(Data)` | `.outputAudio(Data)` |
| `.sentAudio(n)` | `.usage(.sentAudio(n))` |
| `.outputTokens(n)` | `.usage(.outputAudioTokens(n))` |
| `.state(...)` | `.state(...)` |
| `.info` / `.permanentFailure` / `.interrupted` | 동일 |

→ `GeminiLiveStage`(=현 `GeminiLiveClient`를 감싼 어댑터, 또는 직접 채택)가 위 사상을 수행.
`IntegratedTranslationProvider(GeminiLiveStage)`로 노출.

### 8.2 sink (변경 최소)
- `SubtitleEngine`: `ingestTranslationDelta/ingestSourceDelta/ingestTurnComplete/ingestGenerationComplete` 그대로. `AppState.handle`이 `PipelineEvent`를 이 메서드로 분배.
- `TranslatedAudioPlayer`: `.outputAudio` → `enqueue(int16LE:)` 그대로.
- `CostEstimator`: `.usage(.sentAudio)` → `addSentAudio`, `.usage(.outputAudioTokens)` → `addOutputTokens` 그대로.

### 8.3 입력단
- `AudioInputManager`/`AudioSource`/`VADGate` **변경 없음**. `audio.onChunk = { provider.send($0) }`로만 연결.

### 8.4 AppState 변경 지점(요지)
- `private var gemini: GeminiLiveClient?` → `private var provider: TranslationProvider?`
- `startGemini(apiKey:)` → `startProvider()`(팩토리로 생성, kind 결정)
- `handle(_ event: GeminiLiveClient.Event)` → `handle(_ event: PipelineEvent)`
- `clearGeminiHandles()/stopGemini()` → provider 일반화
- 능력 기반 UI: `provider.capabilities`로 원문/번역/오디오/비용 표시 토글.

---

## 9. 설정/구성 추가

```swift
// SettingsStore 신규 키(안)
"engine.kind"                  // TranslationEngineKind (기본 geminiLive)
"engine.onDevice.sttKind"      // appleSpeech | whisperKit | fluidAudio
"engine.onDevice.mtKind"       // appleTranslation | localLLM | none(전사전용)
"engine.onDevice.ttsEnabled"   // 온디바이스 TTS 사용
```

`AppConfig`에 엔진별 기본값/모델명. 능력 선언과 합쳐 설정 UI에서 비가용 옵션은 비활성.

---

## 10. 마이그레이션 계획 (단계적·비파괴)

- **P0 — 이벤트/프로토콜 도입(무동작변경)**: ✅ **구현 완료**(커밋 1d77028). `PipelineEvent`/`TranslationProvider`/`EngineCapabilities` 추가, `GeminiTranslationProvider`(actor 어댑터)로 현 클라이언트 래핑, `TranslationProviderFactory`(kind=geminiLive 고정), `AppState`를 provider 기반으로 전환. 동작 100% 동일, BUILD SUCCEEDED.
- **§7 동시성 일반화** — ✅ **구현 완료**(커밋 d623287). needsGeminiReload→`ProviderConfig` 비교 핫스왑(`performProviderSwap`), epoch 펜싱, 누수 위생(performStop reset / swap flush), SHA256 keyFingerprint, `TranslationEngineKind`. reviewer 적대 검토 통과.
- **P1 — Stage 추상 정리**: `SpeechToTextStage`/`TextTransformStage`/`SpeechSynthesisStage`와 `ComposedTranslationProvider` 추가(아직 미사용). 단위테스트용 mock Stage로 합성기 검증.
- **P2 — 온디바이스 STT 1종**: 003 권고에 따라 1개 STT Stage 구현(소스 언어에 맞춰 FluidAudio 또는 Apple Speech). `onDeviceTranscribe`(전사 전용) 동작.
- **P3 — 온디바이스 MT 결합**: `AppleTranslationStage` 등으로 `onDeviceTranslate` 완성(키 없이 번역).
- **P4 — 폴백/UX**: 키 부재 시 자동 온디바이스 제안, 능력 기반 UI 정리, 모델 다운로드 진행 표시.
- 각 단계는 독립 PR. P0는 회귀 0이 수용 기준.

---

## 11. 리스크 / 오픈 이슈

- **지연/품질 회귀**: 조합형(ASR→MT)은 통합형보다 지연·정합성이 불리. 세그먼트 경계(turn/generation) 정책을 STT 경계와 어떻게 맞출지 별도 검증 필요(현행 dedup/generation 안전망 재사용).
- **세그먼트 경계 의미 차이**: Gemini의 `generationComplete`(재번역)와 ASR의 발화경계는 의미가 다름. `ComposedTranslationProvider`에서 경계 사상 규칙을 명확히 정의해야 함.
- **TextTransformStage 스트리밍성**: 일부 MT/LLM은 문장 단위 배치 → 부분결과 자막 흐름이 끊길 수 있음. "부분=원문 표시, 확정 시 번역 교체" 같은 표시 정책 검토.
- **macOS 버전 게이트**: Apple 신 Speech/Translation은 15~26 요구. 최소 사양 정책과 충돌 시 폴백 경로 필요([[ondevice-asr-research]]).
- **능력-UI 동기화**: 런타임 엔진 전환 시 자막/오디오/비용 토글이 즉시 일관되게 반영돼야 함(현행 즉시반영 원칙 유지).
- **다단계 레이스**: Stage가 늘수록 교체/난타 시 stale 결과·중첩 가동 위험 증가 → §7 동기화 정책(단일 reconciler + epoch + 무중첩 teardown)으로 정식 방어. 구현 시 §7.12 수용 기준으로 검증 필수.
- **버퍼 적체·메모리 누수**: 장시간 세션/반복 교체 시 미플러시 버퍼·미해제 자원·retain cycle 누적 위험 → §7.13(상한 버퍼 + Stage 수명 계약 + 플러시 체크리스트 + soak/스왑 반복 검증)으로 방어. 신규 Stage는 큐 상한·`stop()` 자원해제·`continuation.finish()` 구현이 머지 게이트.

---

## 12. 다음 단계

1. 본 설계 합의 후 **P0 impl 스펙**(태스크/수용기준) 작성 → `specs/`에 impl 문서로 분리.
2. P0 구현: 이벤트/프로토콜 + Gemini 래핑(회귀 0).
3. 003 조사 기반 STT 1종 선정 → P2 진행.
</content>
