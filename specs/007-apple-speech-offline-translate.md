> **[이식 상태]** v1 범위 외(참고 보존) · 대상 스택: Go + Wails v2 (Windows/macOS)
> Apple Speech+Translation은 macOS 26 전용이라 v1 제외. 오프라인 엔진 재도입 시 참고.
> 원본(macOS/Swift): `liveTranslate/specs/007-apple-speech-offline-translate.md` · 마스터 플랜: [000](000-cross-platform-porting-plan.md)

---

---
id: apple-speech-offline-translate
title: Apple Speech 온디바이스 오프라인 번역 엔진 (키 불필요) + P1-A Stage 골격
type: design
version: 0.1.0
status: draft
scope: Apple SpeechTranscriber(STT, macOS 26+) + Apple Translation(MT, macOS 15+)을 ComposedTranslationProvider로 합성해 "키 없이 오프라인 번역" 모델을 추가. 이 과정에서 P1-A Stage 프로토콜/합성기를 실제 엔진으로 구현·검증.
related: [translation-pipeline-architecture, model-catalog-and-settings, ondevice-asr-research, logging-policy]
updated: 2026-06-19
---

# Apple Speech 오프라인 번역 엔진 + P1-A Stage 골격

> 결정(사용자): **(B) 오프라인 번역 바로** — Apple Speech STT(원문) → Apple Translation(번역) →
> 번역 자막(+원문 옵션). **키 불필요.** 엔진 STT는 **SpeechAnalyzer/SpeechTranscriber(macOS 26+)**,
> macOS 15~25는 모델을 "macOS 26 필요"로 비활성. **최소 macOS = 15**(이하 미지원).
> 이 작업이 [[translation-pipeline-architecture]] **P1-A(Stage 프로토콜 + ComposedProvider)** 를 실제로 구현·검증하는 지점이다.

---

## 1. 범위 / 결정

- **딜리버리**: `onDeviceTranslate` 모델 1종 — "오프라인 번역(Apple)". 키 불필요.
- **STT**: `SpeechTranscriber`+`SpeechAnalyzer`(macOS 26+). 15~25 폴백(SFSpeechRecognizer)은 **후속**.
- **MT**: `TranslationSession`(Translation framework, macOS 15+, 온디바이스).
- **최소 OS**: deploymentTarget **26.0(Tahoe)** (사용자 결정 변경 — 이전 버전은 OS가 실행 차단).
  → SpeechTranscriber/SpeechAnalyzer/Translation이 항상 가용하므로 `@available(macOS 26)` 분기/15~25 폴백/minOS 게이팅이 **불필요**(infra는 향후용으로 유지하되 현재 모델은 전부 가용).
- 기존 Gemini/통합형 경로는 무손상.

## 2. 아키텍처 — P1-A Stage를 실제 구현

```
AudioInputManager(onChunk: AudioChunk 16k mono f32)
   └▶ ComposedTranslationProvider (TranslationProvider)
         ├ stt:       AppleSpeechSTTStage (SpeechToTextStage)      // 오디오 → 원문 세그먼트
         └ transform: AppleTranslationStage (TextTransformStage)   // 원문 → 번역 세그먼트
   → PipelineEvent: .sourceText(원문) / .translatedText(번역) / .state / .info / .permanentFailure
```

### 2.1 Stage 프로토콜 (spec 004 §5.2 확정 구현)
```swift
/// 단계 간 텍스트 전달 이벤트. STT/MT는 delta가 아니라 "세그먼트 누적/확정" 모델임에 주의(§5).
enum TextSegmentEvent: Sendable {
    case segment(text: String, isFinal: Bool)  // 현재 세그먼트 전체 텍스트(누적/대체). isFinal=확정.
    case info(String)
    case failure(String)
}

protocol SpeechToTextStage: AnyObject, Sendable {
    var sourceLocale: Locale { get }
    func start() async -> AsyncStream<TextSegmentEvent>   // 원문 세그먼트 스트림
    nonisolated func send(_ chunk: AudioChunk)
    func stop() async
}

protocol TextTransformStage: AnyObject, Sendable {
    /// 원문 세그먼트 스트림 → 번역 세그먼트 스트림(세그먼트 단위 번역, isFinal 보존).
    func transform(_ input: AsyncStream<TextSegmentEvent>) -> AsyncStream<TextSegmentEvent>
    func stop() async
}
// SpeechSynthesisStage(TTS)는 이번 범위 밖(번역 오디오는 Apple 경로 미지원 → capability translatedAudio=false).
```

> ⚠️ **delta가 아니라 segment 모델.** Gemini는 append-delta였지만 SpeechTranscriber는 **현재
> 가설 전체**(volatile)를 반복 갱신하고 final로 확정한다. 번역도 "변하는 원문 세그먼트의 최신 번역"이라
> 누적-append가 아니라 **교체**다. → 자막 엔진에 segment 경로 필요(§5).

### 2.2 ComposedTranslationProvider
```swift
final class ComposedTranslationProvider: TranslationProvider {   // actor 권장
    init(stt: SpeechToTextStage, transform: TextTransformStage?, capabilities: EngineCapabilities)
    // start(): sttStream = stt.start(); (transform ? transform.transform(sttStream) : sttStream)
    //   - 원문 세그먼트(stt) → showSource면 .sourceText로 방출
    //   - 번역 세그먼트(transform) → .translatedText로 방출
    //   - TextSegmentEvent.segment(text,isFinal) → PipelineEvent 매핑(§5의 segment 경로)
    // send(): stt.send(chunk)
    // stop(): transform?.stop(); stt.stop()  (각 Stage 자원 해제 — §7.13)
    // setTranslatedAudioPlayback(): no-op(미지원)
}
```
transform=nil이면 전사 전용(원문을 .translatedText로). 이번엔 transform 있음(번역).

## 3. AppleSpeechSTTStage (macOS 26+)

```swift
@available(macOS 26, *)
actor AppleSpeechSTTStage: SpeechToTextStage {
    let sourceLocale: Locale
    private var transcriber: SpeechTranscriber?
    private var analyzer: SpeechAnalyzer?
    private var inputBuilder: AsyncStream<AnalyzerInput>.Continuation?
    private var analyzerFormat: AVAudioFormat?
    private var converter: AVAudioConverter?  // 16k mono f32 → analyzerFormat

    func start() async -> AsyncStream<TextSegmentEvent> {
        // 1) 권한: SFSpeechRecognizer.requestAuthorization (Speech 인증 공유)
        // 2) transcriber = SpeechTranscriber(locale: sourceLocale,
        //       transcriptionOptions: [], reportingOptions: [.volatileResults], attributeOptions: [])
        // 3) (필요시) 로케일 모델 설치 보장: AssetInventory/ensureModel (미설치 시 다운로드)
        // 4) analyzer = SpeechAnalyzer(modules: [transcriber])
        //    analyzerFormat = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: [transcriber])
        // 5) let (seq, builder) = AsyncStream<AnalyzerInput>.makeStream(); inputBuilder = builder
        //    try await analyzer.start(inputSequence: seq)
        // 6) Task: for try await r in transcriber.results {
        //        yield .segment(text: String(r.text.characters), isFinal: r.isFinal) }
    }

    nonisolated func send(_ chunk: AudioChunk) {
        // [Float]16k → AVAudioPCMBuffer → converter.convertBuffer(_, to: analyzerFormat)
        // inputBuilder.yield(AnalyzerInput(buffer: converted))   (actor hop)
    }

    func stop() async {
        // inputBuilder.finish(); try? await analyzer.finalizeAndFinishThroughEndOfInput()
        // 자원 nil-out, results Task cancel (§7.13 수명 계약)
    }
}
```
- `send`는 오디오 스레드 → 변환은 가볍게, 큐 상한(§7.13) 적용. 포맷은 `bestAvailableAudioFormat` 매칭 필수.
- 로케일 = **소스 언어**(전사 대상). §6 설정.

## 4. AppleTranslationStage (macOS 15+) — SwiftUI 세션 호스팅 우회

⚠️ **`TranslationSession`은 직접 생성 불가** — SwiftUI `.translationTask(config){ session in }`로만 발급된다.
메뉴바(AppKit) 앱이라 **숨은 SwiftUI 호스트**로 세션을 받아 actor에 브리지한다.

```swift
@MainActor
final class TranslationSessionHost {           // 숨은 NSHostingView(0pt) 보유
    // SwiftUI: View { Color.clear.translationTask(config) { session in box.set(session) } }
    // config: TranslationSession.Configuration(source: srcLang?, target: tgtLang)
    func ensureSession(source: Locale.Language?, target: Locale.Language) async -> TranslationSession?
    // LanguageAvailability().status(from:to:) 확인, 필요시 session.prepareTranslation()(다운로드 시트)
}

actor AppleTranslationStage: TextTransformStage {
    // transform(input): for await seg in input {
    //   guard case .segment(text, isFinal) = seg
    //   let session = await host.ensureSession(...)
    //   let out = (try? await session.translate(text).targetText) ?? text
    //   yield .segment(text: out, isFinal: isFinal) }
    // stop(): host teardown
}
```
- **언어 모델 다운로드**: 최초 1회 `prepareTranslation()`/시트 동의 → 이후 오프라인. 시작 전 선제 다운로드 안내 권장.
- **세그먼트 번역 비용**: volatile마다 번역하면 과부하 → **isFinal 세그먼트만 번역**하거나, volatile은 원문 표시·final에서 번역 교체(레이턴시/품질 균형, §8 튜닝).

## 5. 자막 엔진 — segment(교체) 경로 추가

SubtitleEngine은 현재 append-delta 모델. STT/MT는 세그먼트 교체이므로 **새 경로** 추가:
```swift
// SubtitleEngine
func ingestSegment(translation: String, source: String?, isFinal: Bool)
// - 현재 세그먼트의 current(translation/source)를 "교체"(append 아님).
// - isFinal=true면 confirmTurn 경계 처리(기존 dedup/hold 재사용), false면 라이브 갱신.
```
ComposedProvider→AppState.handle에서 segment 이벤트를 이 경로로 라우팅. (delta 경로는 Gemini 유지 — 두 모델 공존.)
→ **PipelineEvent에 segment 표현 추가** 필요: 예) `.sourceText`/`.translatedText`에 `isFinal`/`replace` 플래그를 줄지, 별도 case를 둘지 §8 결정. (권장: `.translatedText(delta:)`와 별개로 `.translatedSegment(text:isFinal:)`/`.sourceSegment(...)` 추가.)

## 6. 설정 / 카탈로그 / UI

- **소스 언어 설정**: `SettingsStore.sourceLanguageCode`(신규, 기본 "en" 또는 auto). 전사 로케일 + 번역 source.
  - 모델 탭에 "소스(전사) 언어" Picker — `onDeviceTranslate` 모델일 때만 노출(capability/슬롯).
- **models.json 항목 추가**:
  ```json
  {
    "id": "apple-offline-translate",
    "displayName": "오프라인 번역 (Apple)",
    "summary": "온디바이스 Apple Speech 전사 + Apple 번역. 키 불필요. macOS 26+ 필요.",
    "engine": "onDeviceTranslate",
    "modelIdentifier": "apple.speech+translation",
    "pipeline": "composed",
    "requiresAPIKey": false,
    "available": true,
    "minOS": "26.0",
    "capabilities": { "sourceText": true, "translatedText": true, "translatedAudio": false, "streaming": true },
    "vad": { "server": false, "clientGate": true, "default": "client" },
    "engineSlots": { "translation": false, "llm": false },
    "targetLanguages": null, "sourceLanguages": null
  }
  ```
  - `ModelDescriptor`에 `minOS: String?` 추가. 카탈로그/팩토리/UI가 현재 OS와 비교해 **미달이면 available=false 취급**(모델 탭에 "macOS 26+ 필요" 비활성).
  - VAD: 서버 VAD 없음(클라우드 아님) → `server:false`. 클라이언트 Silero 게이트만(비용 무관하나 무음 억제).
- **팩토리**: `.onDeviceTranslate` → (macOS 26 가용 시) `ComposedTranslationProvider(stt: AppleSpeechSTTStage(locale: source), transform: AppleTranslationStage(source,target), caps)`. 미가용(OS<26)이면 nil → "준비 중/26 필요"로 수렴(기존 nil 처리 재사용).
- **능력 게이팅**(기존): translatedAudio=false → 오디오 탭 비활성. requiresAPIKey=false → API 키 탭 "이 모델은 키 불필요".

## 7. 권한 / Info.plist / 의존성

- `NSSpeechRecognitionUsageDescription` 추가(권한 문구). `SFSpeechRecognizer.requestAuthorization` 호출.
- Translation 언어 다운로드는 시스템 시트가 처리(별도 plist 키 불필요로 보이나 검증).
- 새 SPM 의존성 없음(Speech/Translation은 OS 프레임워크). `import Speech`, `import Translation`.
- deploymentTarget 15.0로 상향 → CLAUDE.md/스펙의 14.0/14.4 언급 갱신(systemTap은 14.4+였고 15에서 정상).

## 8. 리스크 / 튜닝 포인트

- **세그먼트↔delta 모델 차이**(§5): 자막 교체 경로를 정확히 — 잘못하면 중복/깜빡임. 검증 필수.
- **volatile 번역 빈도**: 매 volatile 번역은 비싸다 → final-only 또는 디바운스(§4). 레이턴시/품질 균형.
- **세그먼트 경계**: SpeechTranscriber final 시점이 turn 경계. hold/dedup 안전망 재사용.
- **언어 다운로드 UX**: 최초 오프라인 모델 다운로드 동의 시트. 시작 전 prepare 안내.
- **macOS 26 게이트**: 15~25 사용자에겐 이 모델 비활성(가시적으로 안내). 폴백(SFSpeechRecognizer) 후속.
- **TranslationSession 수명**: SwiftUI 호스트 생명주기에 묶임 → 핫스왑/정지 시 호스트도 teardown(§7.13).
- **피드백 루프**: 번역 오디오 재생이 없으므로(translatedAudio=false) systemTap 루프 위험 낮음. 단 원문 오디오 캡처는 동일.

## 9. 구현 단계 (검증 단위)

- **7.1**: deploymentTarget 15 + Info.plist 권한 + `ModelDescriptor.minOS` + 카탈로그 항목 + 팩토리 게이팅(아직 nil=준비중) + 소스언어 설정/UI + 문서 14→15 갱신. (빌드/회귀 0)
- **7.2**: Stage 프로토콜 + `ComposedTranslationProvider` + 자막 segment 경로(PipelineEvent segment case + SubtitleEngine.ingestSegment + handle 라우팅). mock STT/transform로 합성 검증.
- **7.3**: `AppleSpeechSTTStage`(SpeechTranscriber) 실제 — 전사만 우선 확인(번역 bypass로 원문→translatedText).
- **7.4**: `AppleTranslationStage`(SwiftUI 세션 호스트) 실제 — 번역 자막 완성 + final-only/디바운스 튜닝.
- 각 단계 `make build` SUCCEEDED + 해당 시나리오 수동 확인.

## 10. 출처
- SpeechAnalyzer/SpeechTranscriber: createwithswift(implementing-advanced-speech-to-text), Apple Forums thread 819555
- Translation: developer.apple.com/documentation/translation/translationsession, polpiella.dev/swift-translation-api, WWDC24 10117
</content>
