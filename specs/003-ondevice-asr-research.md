> **[이식 상태]** v1 범위 외(참고 보존) · 대상 스택: Go + Wails v2 (Windows/macOS)
> 온디바이스 ASR은 v1 제외. 향후 whisper.cpp/sherpa-onnx 대체 시 참고 자료.
> 원본(macOS/Swift): `liveTranslate/specs/003-ondevice-asr-research.md` · 마스터 플랜: [000](000-cross-platform-porting-plan.md)

---

---
id: ondevice-asr-research
title: 온디바이스 STT/ASR 엔진 조사 (API 키 없는 사용자용 옵션)
type: design
version: 0.1.0
status: draft
scope: Gemini Live를 대체하지 않고, API 키 없는 사용자에게 제공할 Swift-native 온디바이스 ASR/번역 엔진 후보 조사
related: [gemini-live-translate-and-audio, translation-pipeline-architecture]
updated: 2026-06-19
---

# 온디바이스 STT/ASR 엔진 조사

> 목적: **Gemini Live 대체가 아님.** API 키가 없는 사용자에게도 "키 없이 쓰는 옵션"을
> 제공하기 위한 온디바이스(무료) 음성 인식/번역 엔진 후보를 조사한다.
> 조사일 2026-06-19 · 대상 macOS 14.0+ (앱 현재 최소 사양).

---

## 0. 핵심 결론 (TL;DR)

- **1순위: Apple Speech 프레임워크** — 완전 네이티브, 의존성/모델 번들 0, 무료, 한국어 지원.
  - 신 API `SpeechAnalyzer`/`SpeechTranscriber`는 **macOS 26+**(저지연 장문 스트리밍, 한국어 포함 11개 언어).
  - 구 API `SFSpeechRecognizer`는 macOS 14~15 폴백(단 온디바이스는 받아쓰기 언어팩 필요, 정확도·길이 제약).
- **정확도/언어 커버리지 최강: WhisperKit** — Korean+99개 언어, MIT, macOS 14+, Apple Silicon. 단 OSS판은 실시간 스트리밍이 약해 추가 구현 필요, 모델 다운로드 필요.
- **통합비용 최저: FluidAudio ASR (Parakeet)** — **이미 VAD로 의존 중**이라 새 의존성 0, 실시간 스트리밍 우수. 단 **한국어 미지원**(영어/유럽어+일본어).
- ⚠️ **ASR ≠ 번역.** 키 없는 모드를 "번역"으로 만들려면 ASR + 온디바이스 MT(Apple Translation 등) 결합이 필요하다(§4).

---

## 1. 후보 비교표

| 엔진 | 네이티브/의존성 | 한국어 | 스트리밍 | 최소 OS | 모델 다운로드 | 라이선스 |
|------|------|:---:|------|------|------|------|
| **Apple Speech** (SpeechAnalyzer / SFSpeechRecognizer) | ✅ OS 내장, 의존성 0 | ✅ | ✅ | 14+(구) / **26+**(신) | 불필요(OS 음성팩) | OS 기본 |
| **WhisperKit** (argmaxinc) | SPM, Apple Silicon | ✅ (99+) | △ OSS 약함 | 14+ | base~140MB / large~0.6–1.5GB | MIT |
| **FluidAudio ASR** (Parakeet) | ✅ **이미 사용 중** | ❌ | ✅ 우수 | 14+ | ~수백 MB | Apache-2.0 |
| sherpa-onnx | ONNX 런타임 브리지 | ✅ | ✅ | 광범위 | 모델별 | Apache-2.0 |
| whisper.cpp | C++ 브리지 | ✅ | △ | 광범위 | GGUF | MIT |

---

## 2. 후보별 상세

### 2.1 Apple Speech 프레임워크 — 1순위 추천

- **신 API (macOS 26+ / iOS 26+)**: `SpeechAnalyzer`가 분석 모듈을 조율.
  - `SpeechTranscriber`(장문 스트리밍, 저지연 부분결과), `DictationTranscriber`(짧은 발화, SFSpeechRecognizer 대응), `SpeechDetector`(VAD).
  - 라이브 캡션 지원 언어 11개(영어 변형, 표준 중국어, 광둥어, 스페인어, 프랑스어, 일본어, 독일어, **한국어** 등).
  - `start(inputSequence:)`로 온디바이스 스트리밍. 실시간 자막에 가장 적합.
- **구 API (macOS 14~15 폴백)**: `SFSpeechRecognizer` + `SFSpeechAudioBufferRecognitionRequest`(버퍼 스트리밍).
  - `requiresOnDeviceRecognition = true` → 네트워크 미전송(오프라인). `supportsOnDeviceRecognition`로 가능 여부 확인.
  - ⚠️ macOS 온디바이스는 **해당 언어 받아쓰기(Dictation) 언어팩이 설치돼 있어야** 동작. 과거 1분 길이 제한·정확도 저하 이슈 존재.
- 장점: 의존성/모델 번들 0, 바이너리 증가 거의 없음, 무료, 한국어 O — "키 없는 옵션" 취지에 가장 부합.
- 단점: 최신 스트리밍 품질은 macOS 26 게이트, 구버전은 언어팩/길이 제약.

### 2.2 WhisperKit (argmaxinc) — 정확도·언어 커버리지 최강

- OpenAI Whisper의 Apple Silicon 온디바이스 실행. Korean+99개 언어, MIT, SPM, macOS 14+/Apple Silicon.
- 모델: tiny(~40MB) / base(~140MB) / small(~480MB) / large-v3-turbo(~0.6–1.5GB).
- ⚠️ OSS판은 **네이티브 실시간 스트리밍이 약함**(슬라이딩 윈도우/청크를 직접 구성해야 함; 실시간+화자는 Pro SDK 별도). 라이브 자막에 추가 작업 필요.
- OS·언어팩에 구애받지 않고 일관된 품질이 필요할 때 최선. v1.0.0(2026-05-01).

### 2.3 FluidAudio ASR (Parakeet) — 통합비용 최저(이미 의존 중)

- 모델: Parakeet TDT v3(0.6b, 25개 유럽어+일본어), v2(영어 전용, 높은 recall), EOU(120m, end-of-utterance 스트리밍). 중국어용 SenseVoice/Paraformer도 지원.
- 실시간 스트리밍 우수(`SlidingWindowAsrManager`), RTF ~190x(M4 Pro). **새 의존성 0**(VADGate가 이미 FluidAudio 사용).
- ❌ **한국어 미지원.** 소스가 영어/유럽어/일본어인 시청 시나리오엔 즉시 투입 가능, 한국어 소스 전사는 불가.
- API 예시:
  ```swift
  let models = try await AsrModels.downloadAndLoad(version: .v3)
  let asrManager = AsrManager(config: .default)
  try await asrManager.loadModels(models)
  let result = try await asrManager.transcribe(samples)   // samples: 16kHz mono Float32
  ```

### 2.4 sherpa-onnx / whisper.cpp — 후순위

- 다언어·크로스플랫폼이며 스트리밍 모델(Zipformer/transducer 등)도 있으나, ONNX 런타임/C++ 브리지로 **Apple-네이티브성·통합 friction**이 커서 이 앱엔 후순위. 특정 언어/모델이 꼭 필요할 때만 고려.

---

## 3. liveTranslate 적용 관점 메모

- **입력 포맷 일치**: 현재 `AudioSource`가 내보내는 **16kHz mono Float32 100ms(1600샘플) 청크**는 위 ASR 엔진들이 요구하는 입력과 동일 → 오디오 입력단(캡처/피드백루프 제외/VAD)을 그대로 재사용 가능.
- **소스 언어가 1순위를 가른다**:
  - 시청 콘텐츠가 주로 **영어/유럽어** → FluidAudio ASR(이미 의존)로 최소비용 시작 가능.
  - **한국어 포함 광범위** → Apple Speech(한국어 O) 또는 WhisperKit.
- **macOS 26 게이트**: 최고의 네이티브 스트리밍(SpeechTranscriber)은 26+. 14~15는 SFSpeechRecognizer 폴백 또는 WhisperKit로 커버.

---

## 4. 중요: ASR ≠ 번역 — 키 없는 "모드" 설계 두 갈래

liveTranslate는 번역 앱이라, 키가 없으면 Gemini의 번역이 빠진다. ASR만 붙이면 **원문 언어 자막**만 가능하다.

- **(A) 전사 전용 모드**: 키 없을 때 "원문 자막"만 표시 → ASR 하나로 충분(가장 단순, 즉시 가치 제공).
- **(B) 오프라인 번역 모드**: ASR → **온디바이스 MT** 결합.
  - **Apple Translation 프레임워크**(macOS 15+, 무료·온디바이스)로 전사 텍스트를 번역하면 키 없이도 진짜 번역 자막 가능.
  - 대안: MADLAD-400(speech-swift 등) 같은 로컬 MT, 또는 로컬 LLM 교정/번역.
  - 단, 실시간 통합 품질·지연은 Gemini Live 전용 모델보다 불리할 수 있음.

---

## 5. 권고 (단계적)

1. **MVP**: 소스가 주로 영어/유럽어면 **FluidAudio ASR**(이미 의존)로 "전사 자막" 옵션을 최소비용 추가. 한국어 등 광범위 소스면 **Apple Speech** 전사 모드.
2. **본격**: **Apple Speech(신 API on 26 + 구 API 폴백)** 기본 온디바이스 엔진, 정확도 보강 옵션으로 **WhisperKit**.
3. **오프라인 번역까지**: ASR + **Apple Translation 프레임워크** 결합으로 "키 없이도 번역" 완성(옵트인).
4. **선결 조건**: 백엔드를 **추상화(프로토콜)**해 Gemini 무손상 상태로 엔진을 끼울 수 있게 한다 → [[translation-pipeline-architecture]] (specs/004) 참조.

---

## 6. 출처

- WhisperKit (argmaxinc): https://github.com/argmaxinc/WhisperKit
- FluidAudio (FluidInference): https://github.com/FluidInference/FluidAudio
- Apple SpeechAnalyzer 온디바이스 전사 (Callstack): https://www.callstack.com/blog/on-device-speech-transcription-with-apple-speechanalyzer
- SpeechAnalyzer vs SFSpeechRecognizer (Blake Crosley): https://blakecrosley.com/blog/speech-framework-vs-sfspeechrecognizer
- requiresOnDeviceRecognition (Apple Developer): https://developer.apple.com/documentation/speech/sfspeechrecognitionrequest/requiresondevicerecognition
- iOS 26 SpeechAnalyzer Guide (Gubarenko): https://antongubarenko.substack.com/p/ios-26-speechanalyzer-guide
- (참고) soniqo/speech-swift — MLX/CoreML 온디바이스 음성 툴킷: https://github.com/soniqo/speech-swift
