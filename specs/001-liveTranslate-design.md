> **[이식 상태]** 이식 대상(적응 필요) · 대상 스택: Go + Wails v2 (Windows/macOS)
> 전체 아키텍처는 유효하나 구현 스택이 Swift/AppKit→Go/Wails로 바뀜. v1은 Gemini Live 엔진만 이식(Apple 온디바이스 경로 제외).
> 원본(macOS/Swift): `liveTranslate/specs/001-liveTranslate-design.md` · 마스터 플랜: [000](000-cross-platform-porting-plan.md)

---

# liveTranslate 설계 스펙 — Gemini 3.5 Live 기반 macOS 실시간 자막 번역 앱

> 상태: **초안(검토 대기)** · 작성일 2026-06-17 · 대상 플랫폼 macOS

---

## 1. 개요

**liveTranslate** — 맥북에서 흘러나오는 소리(또는 마이크 입력)를 실시간으로 받아 한국어 등 지정 언어로 번역하고, 그 결과를 영화 자막처럼 화면 위에 오버레이로 띄우는 경량 단일 앱.

| 항목 | 값 |
|------|-----|
| 핵심 엔진 | Google **Gemini 3.5 Live Translate** (`gemini-3.5-live-translate-preview`) |
| 오디오 소스 | ① 시스템 출력 루프백(BlackHole) ② **Core Audio Tap 시스템 오디오 직접 캡처(macOS 14.4+)** ③ 마이크 |
| 출력 | 화면 오버레이 자막 (영화 자막 스타일, 표시/페이드아웃) |
| 플랫폼 | macOS **14+** (시스템 오디오 직접 캡처는 14.4+, 그 이하는 BlackHole) |
| 형태 | 경량 단일 네이티브 앱 — **메뉴바 상주(menu bar) 앱** |

---

## 2. 요구사항 분석

### 2.1 기능 요구사항(FR)

| ID | 요구사항 | 비고 |
|----|----------|------|
| FR-1 | 시스템 출력 오디오 캡처 — **자동 선택**: BlackHole 감지 시 사용, 없으면 Core Audio Tap 직접 캡처(macOS 14.4+) | 추가 설치 없이도 동작 |
| FR-2 | 마이크 입력을 직접 캡처 | 입력 소스 선택 UI 제공 |
| FR-3 | 캡처 오디오를 Gemini Live로 스트리밍하여 실시간 번역 | WebSocket 양방향 |
| FR-4 | 번역 대상 언어 설정 (기본: 한국어) | Gemini 지원 70+ 언어 중 선택 |
| FR-5 | 번역 텍스트를 화면 오버레이 자막으로 표시 | 항상 위, 클릭 통과 |
| FR-6 | 자막의 등장/유지/페이드아웃 (영화 자막 방식) | 문장 단위 타이밍 |
| FR-7 | 자막 스타일 설정: 폰트, 크기, 색상, 두께(weight), 외곽선/그림자, 광원(글로우) 효과 | 실시간 미리보기 |
| FR-8 | 자막 모드: **기본 번역만**, 설정에서 원문 동시 표시 토글 | `inputAudioTranscription` 활용 |
| FR-9 | **메뉴바 상주** UI: 아이콘에서 시작/정지·입력소스·언어·설정 빠른 접근 | 본체 윈도우 없음 |

### 2.2 비기능 요구사항(NFR)

| ID | 요구사항 | 목표치 |
|----|----------|--------|
| NFR-1 | 종단 지연(speech→자막) | 2~4초 이내 (Gemini Live 특성상 수 초 후행) |
| NFR-2 | 경량성 | 메모리 < 200MB, 유휴 CPU 낮음 — Electron 미사용 |
| NFR-3 | 세션 연속성 | 15분 세션 제한을 자동 재연결로 무중단 처리 |
| NFR-4 | 자막 가독성 | 어떤 배경 위에서도 읽힘 (외곽선/그림자 기본 ON) |
| NFR-5 | 보안 | API 키를 Keychain에 저장, 평문 보관 금지 |

---

## 3. 기술 스택 및 선정 근거

### 3.1 권고 스택: **Swift + SwiftUI 네이티브**

| 레이어 | 기술 | 근거 |
|--------|------|------|
| 언어/UI | Swift 5.10+, SwiftUI (+ 일부 AppKit) | 네이티브 = 최고의 경량성, 단일 .app |
| 오디오 캡처 | **AVAudioEngine**(마이크/BlackHole) + **Core Audio Tap**(시스템 오디오, 14.4+) | 환경에 따라 자동 선택 |
| 메뉴바 UI | **NSStatusItem** + SwiftUI MenuBarExtra | 본체 윈도우 없는 상주 앱 |
| 오버레이 창 | **NSWindow** (borderless, `.floating`/`screenSaver` level, clear, click-through) | 자막 오버레이의 표준 방식 |
| 네트워크 | **URLSessionWebSocketTask** | 외부 의존성 없이 Live API WebSocket 연결 |
| 오디오 변환 | AVAudioConverter | 입력 → 16kHz/16-bit/mono PCM 변환 |
| **VAD(음성 감지)** | **Silero VAD v6 (CoreML)** via FluidAudio SDK (`from: 0.12.4`) | 음악·소음 걸러 발화만 송신 → 비용↓ (§5.6) |
| 키 저장 | Keychain Services | API 키 보안 |
| 빌드 | XcodeGen + Xcode | dotclaude 컨벤션 정합 |

### 3.2 대안 비교 (기각)

| 후보 | 장점 | 기각 사유 |
|------|------|-----------|
| Electron | 크로스플랫폼, 웹기술 | "lightweight" 위반(수백 MB), 오디오/오버레이 결국 네이티브 브리지 필요 |
| Tauri(Rust) | 가벼움 | macOS 오디오 캡처·click-through 오버레이는 결국 네이티브 코드 → 복잡도만 증가 |
| Python+Qt | 빠른 프로토타입 | 배포·서명 어려움, 무거움, 실시간 오디오 파이프라인 비효율 |

> **결론:** macOS 단독 + 경량 + 시스템 오디오/오버레이 깊은 통합 → **Swift 네이티브가 명확한 정답.**

---

## 4. 시스템 아키텍처

```
┌─────────────────────────────────────────────────────────────┐
│                       liveTranslate.app                       │
│                                                               │
│  ┌────────────┐   PCM 16k    ┌──────────────┐   WebSocket    │
│  │ AudioInput │ ───────────▶ │ GeminiLive   │ ──────────────▶│──▶ Gemini 3.5
│  │  Manager   │   100ms청크  │  Client      │ ◀──────────────│◀── Live Translate
│  │ mic/BH/SCK │              │ (URLSession) │  translation    │     API (WSS)
│  └────────────┘              └──────┬───────┘  transcription  │
│   ▲ 입력선택                         │ text events            │
│   │ 마이크/BlackHole/CoreAudioTap     ▼                        │
│  ┌────────────┐              ┌──────────────┐                 │
│  │ Settings   │              │ Subtitle     │                 │
│  │ (스타일/언어)│ ──────────▶ │ Engine       │ (등장/페이드)    │
│  └────────────┘   스타일      │ (타이밍/큐)   │                 │
│                              └──────┬───────┘                 │
│                                     ▼                         │
│                              ┌──────────────┐                 │
│                              │ Overlay      │ 항상 위/클릭통과  │
│                              │ Window(NSWin)│                 │
│                              └──────────────┘                 │
└─────────────────────────────────────────────────────────────┘
```

### 4.1 모듈 책임

- **AudioInputManager**: 입력 소스 판별/선택(`AudioSource` 프로토콜: 마이크 · BlackHole · Core Audio Tap), PCM 수집, 16kHz mono **Float32** 변환, 100ms 청크화. **VAD(Silero) 게이트**로 발화 구간만 통과.
- **GeminiLiveClient**: WebSocket 연결·setup 메시지 전송·오디오 청크 송신·수신 이벤트(번역/원문 transcription) 파싱·15분 세션 자동 재연결.
- **SubtitleEngine**: 수신 텍스트를 자막 큐로 변환, 문장 분할·표시 시간 산정·페이드 타이밍 관리.
- **OverlayWindow**: 투명·클릭통과·최상위 창에 스타일 적용된 자막 렌더링.
- **SettingsStore**: 언어/스타일/입력소스/API키 영속화 + 실시간 반영.

---

## 5. 핵심 기술 상세

### 5.1 Gemini 3.5 Live Translate 연동

- **엔드포인트**: Gemini Live API WebSocket (WSS) — `URLSessionWebSocketTask`로 연결.
- **setup 메시지(요지)**:

```json
{
  "setup": {
    "model": "models/gemini-3.5-live-translate-preview",
    "generationConfig": {
      "responseModalities": ["AUDIO"],
      "inputAudioTranscription": {},
      "outputAudioTranscription": {},
      "translationConfig": {
        "targetLanguageCode": "ko",
        "echoTargetLanguage": false
      }
    }
  }
}
```

- **자막 추출 원리**: native audio 모델은 응답 modality가 `AUDIO`뿐이지만,
  - `outputAudioTranscription` → **번역된 텍스트(자막 본문)**
  - `inputAudioTranscription` → **원문 텍스트**(원문+번역 동시 표시용)
  를 텍스트 이벤트로 함께 수신. **오디오 출력(PCM 24kHz)은 재생하지 않고 폐기**(자막 전용 앱이므로). 향후 "번역 음성 듣기" 옵션 시 재사용 가능.
- **언어 설정**: `targetLanguageCode`(BCP-47, 기본 `ko`). 소스 언어는 자동 감지.
- **오디오 송신 포맷**: Raw 16-bit PCM, 16kHz, mono, little-endian, `audio/pcm;rate=16000`, 100ms 청크.

### 5.2 오디오 입력 (루프백 & 마이크)

3가지 입력 소스를 지원하며, 시스템 소리 캡처는 **환경 자동 선택**한다.

- **시스템 소리 캡처 (자동 선택)**:
  - **(A) BlackHole 감지 시**: 입력 장치 목록에 BlackHole이 있으면 이를 캡처(사용자가 다중 출력 장치를 구성해 둔 환경). AVAudioEngine으로 처리.
  - **(B) BlackHole 미설치 시**: **Core Audio Process Tap**(macOS 14.4+, `CATapDescription` + `AudioHardwareCreateProcessTap`)으로 시스템 오디오를 직접 캡처 — *추가 설치 불필요, 화면 녹화 권한 불필요*(오디오 캡처 권한만). 참고 구현: AudioCap/AudioTee.
    - ※ 당초 ScreenCaptureKit을 검토했으나, 오디오-only 캡처 시 더미 비디오 캡처를 강제로 끼워야 하고 화면 녹화 권한이 필요해 부적합 → 오디오 전용인 본 앱은 Core Audio Tap이 정답.
    - macOS 14.0~14.3 사용자는 Tap API 미지원 → BlackHole 안내로 폴백.
  - 앱은 두 경로를 자동 판별하고, 사용자에게 현재 사용 중인 캡처 방식을 표시. (수동 강제 선택도 허용)
- **마이크**: 시스템 기본/선택 입력 장치 직접 캡처. `NSMicrophoneUsageDescription` 권한 필요.
- **공통 파이프라인**: 소스 → (AVAudioEngine tap 또는 SCStream audio buffer) → AVAudioConverter로 16kHz/16-bit/mono PCM 변환 → 100ms 버퍼 누적 → GeminiLiveClient로 송신. 캡처 백엔드는 `AudioSource` 프로토콜로 추상화해 동일 파이프라인에 연결.

### 5.3 자막 오버레이 창

- **NSWindow 구성**: `styleMask = .borderless`, `backgroundColor = .clear`, `isOpaque = false`, `level = .screenSaver`(또는 `.floating`), `ignoresMouseEvents = true`(클릭 통과), `collectionBehavior`에 `.canJoinAllSpaces`·`.fullScreenAuxiliary`(전체화면 영상 위에도 표시).
- **위치**: 화면 하단 중앙 기본, 드래그/설정으로 조정.
- **렌더링**: SwiftUI Text + 스타일 모디파이어. 광원(글로우)은 `.shadow` 다중 적용 또는 Core Animation/Metal 후처리로 구현.

### 5.4 영화 자막식 표시 로직 (SubtitleEngine)

- Gemini는 부분(partial) → 확정(final) 텍스트를 점진 전송 → **현재 줄은 라이브 업데이트**, 문장 확정 시 **고정 후 표시 시간 경과 → 페이드아웃**.
- 표시 시간 = `기본(예 1.5s) + 글자수 × 계수`, 최대 2줄 유지(오래된 줄 밀어내기).
- 페이드 인/아웃 애니메이션(예 0.25s)으로 영화 자막 질감.

### 5.5 자막 스타일 설정 (FR-7)

| 설정 | 구현 |
|------|------|
| 폰트 | 시스템/설치 폰트 선택(NSFontManager) |
| 크기 | 슬라이더(pt) |
| 색상 | 글자색·외곽선색·배경 박스색 ColorPicker |
| 두께(weight) | Font.Weight 매핑 |
| 외곽선/그림자 | 스트로크 + drop shadow |
| 광원(글로우) | 다중 blur shadow 또는 Metal glow |
| 배경 박스 | 반투명 박스 on/off + 불투명도 |
| 정렬/위치/최대 줄수 | 옵션 |

→ 모든 설정은 오버레이에 **실시간 미리보기** 반영, SettingsStore에 영속화.

### 5.6 음성 활동 감지 (VAD) — 비용·품질 핵심

데시벨(RMS) 기반 직접 구현은 음악·키보드 타건음·환경음을 발화로 오인 → 무음에도 API 호출되어 비용이 샌다. **AI 기반 검증된 오픈소스**를 채택한다.

- **채택: Silero VAD** — 딥러닝 기반, 사람 목소리와 배경 소음(음악/타건/환경음) 분리 정확도 최상. 모델 1~2MB, **MIT 라이선스**(상업적 사용 자유). 영화·유튜브 배경음을 걸러야 하는 본 앱에 최적.

**통합 방식 비교 (Gemini가 제시한 양자택일을 교정):**

| 방식 | 내용 | 평가 |
|------|------|------|
| **① FluidAudio Swift SDK (권고)** | 사전 변환된 **CoreML** Silero VAD를 SPM으로 통합. ANE/GPU 네이티브 실행, **onnxruntime 불필요**. MIT/Apache 2.0 | **경량성·성능·통합 모두 우위.** 단 SDK 성숙도 확인 필요 |
| ② onnxruntime-swift + Silero ONNX | MS 공식 SPM, 검증·크로스플랫폼. stateful(`h0`/`c0`) 직접 관리 | 안정적이나 **ORT 바이너리가 무거움**(경량성 NFR과 상충) |
| ③ 직접 CoreML 변환 | coremltools로 직접 변환 | **비권장** — 변환 난이도·알려진 버그, stateful `MLState`는 macOS 15+ 필요, **이미 사전 변환본 존재**(바퀴 재발명) |

> **질문에 대한 답:** "ONNX 직접 로드 vs 직접 CoreML 변환"은 둘 다 차선. **사전 변환된 CoreML(FluidAudio SDK)** 활용이 빌드 파이프라인(SPM 한 줄, Python 변환 단계 불필요)·경량성·네이티브 성능에서 모두 우위. 직접 변환은 macOS 14 타겟에서 MLState도 못 쓰고 변환 리스크만 떠안음.
> → **1순위 ① FluidAudio, 폴백 ② onnxruntime-swift.** ①의 SDK 안정성/모델 번들(첫 실행 시 HF 자동 다운로드 → 오프라인 위해 **번들 동봉** 권장) 검증 후 확정.

- **파이프라인 위치**: AudioInputManager의 PCM 청크 → VAD 판정 → **발화 구간만** GeminiLiveClient로 송신. 무음 구간은 송신 차단(입력·출력 비용 동시 절감).

---

## 6. 주요 기술 과제 & 해결 방안

| 과제 | 영향 | 해결 |
|------|------|------|
| **15분 세션 제한** | 장시간 사용 중 끊김 | 세션 만료 전 새 WebSocket 선제 연결 → 무중단 핸드오버. (필요 시 `sessionResumption` 활용) |
| **종단 지연** | 자막이 영상보다 수 초 후행 | Live Translate 특성상 불가피 — UX로 "수 초 지연" 안내, 라이브 partial로 체감 단축 |
| **루프백 설정 난이도** | 일반 사용자 진입장벽 | BlackHole 없으면 **Core Audio Tap(14.4+) 자동 폴백**으로 무설치 동작. BlackHole 사용자는 자동 감지 |
| **전체화면 영상 위 표시** | 자막이 가려짐 | `collectionBehavior` + 적절한 window level |
| **API 비용/키 관리** | 사용량 과금 (출력 오디오가 비용 ~85%, **§9 참조**) | 키 Keychain, VAD 무음 억제, 경제 모드(옵션 B), 비용 가시화·예산 한도 |
| **무음/침묵 처리** | 불필요한 트래픽·비용 | 입력 VAD로 무음 시 송신 스킵 |

---

## 7. 구현 계획 (마일스톤)

| 단계 | 산출물 | 핵심 검증 |
|------|--------|-----------|
| **M0** 프로젝트 셋업 | XcodeGen 프로젝트, 앱 스캐폴드, 권한 설정 | 빈 앱 빌드/실행 |
| **M1a** 오디오 파이프라인 | `AudioSource` 프로토콜 + 마이크/BlackHole 캡처(AVAudioEngine, 입력장치 선택), 16k mono Float32 변환, 100ms 청크, 메뉴바 소스선택+레벨미터 | 마이크/BlackHole 캡처 레벨미터 확인 |
| **M1b** VAD 게이트 | FluidAudio SPM 통합, Silero VAD 스트리밍, 발화 구간만 통과(모델 번들 동봉) | 음악/소음 구간 송신 차단 확인 |
| **M1c** 시스템 오디오 직접 캡처 | Core Audio Tap(14.4+) `AudioSource` 구현 + 자동선택(BlackHole 없으면 Tap) | 무설치 환경 시스템 소리 캡처 확인 |
| **M1.5** UX 개선 | 미니 HUD(상시 플로팅: 레벨·VAD·캡처상태·입력소스), 설정 창 동작, 권한 거부 시 시스템 설정 deep link | HUD 상시 표시·설정창·권한 열기 동작 |
| **M2a** Gemini Live 연동 | GeminiLiveClient(WebSocket): setup/발화청크 Int16 송신/transcription 수신, 번역 텍스트 HUD 표시 | 음성 → 번역 텍스트 실시간 수신 |
| **M2b** 비용 추정 | CostEstimator(송신시간+usageMetadata), HUD 세션비용 `전송/수신/총 $`, 설정 누적, on/off | 캡처 중 비용 실시간 증가 확인 |
| **M2c** 무중단 재연결 | sessionResumption 핸들 + goAway 대응, 15분 선제 재연결 | 15분+ 연속 무중단 |
| **M3** 자막 오버레이 + HUD 재구성 | **제어 HUD**(상태+시작/정지 버튼+설정, 번역문 제외) / **자막 HUD**(영화자막식 최상위 클릭통과 오버레이, 페이드) / 메뉴 간소화 / 자막 위치 설정(모니터·상하) | 최상위 자막 표시 + 제어 HUD 조작 |
| **M4** 스타일 설정 | SettingsStore + 설정 UI + 실시간 미리보기 | 폰트/색/글로우 변경 즉시 반영 |
| **M5** 견고화 | 15분 재연결, VAD, 에러/재시도, 온보딩 | 30분+ 연속 무중단, 키 Keychain |
| **M6** 배포 | 코드사인/공증, 아이콘, 메뉴바 앱화 | 서명된 .app 정상 실행 |

---

## 8. 리스크 & 미결 사항

### 8.1 리스크
- `gemini-3.5-live-translate-preview`는 **프리뷰** — API 스펙/모델명 변경 가능. 클라이언트의 setup/파싱을 설정화하여 대응.
- 루프백 설정은 사용자 환경 의존 → 온보딩 품질이 채택률 좌우.
- 시스템 오디오 직접 캡처(Core Audio Tap, 14.4+)는 화면 권한 불필요해 적합하나 저수준 API라 복잡 → M1c로 분리. 14.4 미만은 BlackHole 안내.

### 8.2 결정 사항 (검토 반영 완료)
1. ✅ **앱 형태**: **메뉴바 상주 앱**으로 확정.
2. ✅ **자막 표시**: **기본 번역만 + 설정에서 원문 토글**로 확정.
3. ✅ **루프백 캡처**: **혼합/자동** — BlackHole 감지 시 사용, 없으면 Core Audio Tap(14.4+) 직접 캡처(무설치 동작).

### 8.3 남은 결정 (구현 전 확인)
1. **오디오 출력**: 자막 전용(번역 음성 미재생)으로 확정? 향후 "번역 음성 듣기" 옵션 필요 여부. (권고: 1차 미재생)
2. **API 경로**: Gemini Developer API(AI Studio 키) vs Vertex AI(서비스 계정). (권고: 1차 AI Studio 키)
3. **macOS 최소 버전**: 앱 최소 macOS 14.0(마이크/BlackHole 동작). 시스템 오디오 직접 캡처는 Core Audio Tap이 요구하는 **14.4+** 에서만 — 14.0~14.3은 BlackHole 폴백. 확정 필요.

---

## 9. 비용 및 예산 계획

> ⚠️ `gemini-3.5-live-translate-preview`는 **프리뷰** — 단가는 변동 가능. 아래는 2026-06 공식 가격 페이지 기준.

### 9.1 공식 단가 (Paid Tier)

| 항목 | 1M 토큰당 | 분당 환산 | 비고 |
|------|-----------|-----------|------|
| **오디오 입력** | $3.50 | **$0.0053/분** | 25 tokens/초 |
| **오디오 출력** | $21.00 | **$0.0315/분** | 25 tokens/초 — 비용의 ~85% |
| **합산(연속)** | — | **≈ $0.0368/분 (≈ $2.21/시간)** | 입력+출력 동시 |

- **Free Tier**: 존재하나 입력 데이터가 Google 제품 개선에 사용됨 → 개인/실험용 외 비권장.

### 9.2 ⚠️ 핵심 비용 구조 문제 (자막 전용 앱)

native audio 모델은 응답 modality가 `AUDIO`로 **강제**된다. 즉 자막(텍스트)만 필요해도 **번역 음성이 생성되고 출력 토큰으로 과금**된다.
→ 출력 오디오가 전체 비용의 **약 85%**. "재생하지도 않는 음성"에 비용 대부분이 나가는 구조.

**두 모델의 아키텍처·품질·비용 비교:**

| 구분 | **A. 기본 모드** `gemini-3.5-live-translate-preview` | **B. 경제 모드** `gemini-3.1-flash-live` 등 |
|------|------|------|
| 아키텍처 | **Native Speech-to-Speech** (음성 자체 직접 이해) | Speech→Text→Text 파이프라인 |
| 번역 품질 | **최상 (동시통역 수준)** — 어조·감정·억양 등 비언어 맥락 반영 | 우수 (일반 텍스트 번역) — 음성 뉘앙스·농담 유실 가능 |
| 지연 | **짧음** — partial 자막을 매우 빠르게 출력 | 보통~약간 김 — 오디오→텍스트 인식 후 번역 시작 |
| 출력 | AUDIO 강제(폐기) + transcription | TEXT only(번역 지시 프롬프트) |
| 분당 비용 | ~$0.037/분 | ~$0.005/분 이하 |
| 한계 | 재생 안 할 오디오에 비용 85% 지불 | 3.5 Live의 혁신적 번역 퀄리티 미활용 |

**▶ 전략(궤도 수정·확정):** 이 앱의 정체성은 **"Gemini 3.5 Live 기반 실시간 번역"**이다. 경제 모드를 기본값으로 두면 앱의 핵심 가치가 퇴색된다.
→ **기본값은 무조건 A(`3.5 Live Translate`)로 확정.** 비용 리스크는 모델 다운그레이드가 아니라 **UX와 안전장치**로 해소한다:
- 최초 실행 온보딩에서 "고품질 번역 모드 동작 중, API 사용량 주의" 명확히 고지.
- "비용 절감 모드(Flash, B)"는 설정에서 **사용자가 직접 선택하는 옵션으로만** 제공(기본 OFF).
- §9.4의 VAD·자동 일시정지·비용 가시화·예산 soft cap으로 A 모드의 비용을 통제.

### 9.3 사용 시나리오별 예상 비용

발화율(실제 소리 나는 비율)에 따라 입력·출력 모두 비례 감소(무음 시 VAD로 송신 억제 가정).

| 사용 강도 | 월 사용시간 | 연속(100%) | 발화율 70% | 발화율 50% |
|-----------|-------------|------------|------------|------------|
| 라이트 | 10시간 | ~$22 | ~$15 | ~$11 |
| 미들(하루 2h×20일) | 40시간 | ~$88 | ~$62 | ~$44 |
| 헤비 | 100시간 | ~$221 | ~$155 | ~$110 |

> 옵션 B(경제 모드) 적용 시 출력 비용이 제거되어 위 금액의 **약 1/6~1/7** 수준으로 추정(텍스트 출력 단가 기준, 추후 실측 필요).

### 9.4 비용 절감 전략 (구현 반영)

1. **VAD 무음 억제** (Silero VAD, §5.6): 음악·소음을 거르고 발화 구간만 송신 → 입력·출력 동시 절감. 영화·회의 등 발화율 50~70% 환경에서 30~50% 절감. **A 모드 비용 통제의 핵심 수단.**
2. **자동 일시정지**: 일정 시간 무음/유휴 시 세션 일시정지(스트리밍 중단).
3. **경제 모드(옵션 B)**: 출력 오디오 제거 → 최대 절감 수단.
4. **비용 가시화**: HUD에 **세션 비용 `전송 $x / 수신 $x / 총 $x`** 실시간 표시(설정에서 비용 HUD on/off). 설정에 **누적 비용**(전체/일·월) 표시. 통화는 **USD**(환율 환산 없음). 입력 비용=송신 오디오 누적시간×단가(정확), 출력 비용=`usageMetadata` 출력 토큰(없으면 수신 길이 추정·표기). 단가는 설정/상수(§9.1, 프리뷰 변동 대비).
5. **월 예산 한도(soft cap)**: 설정한 추정 비용 도달 시 경고/자동 정지 옵션.

### 9.5 비용 관련 결정 (검토 반영)

1. ✅ **기본 모델**: **A(`3.5 Live Translate`) 확정** — 앱 정체성. B(Flash 경제 모드)는 설정 옵션(기본 OFF). 비용은 UX/안전장치로 통제(§9.2).
2. **Free Tier 허용 여부**: 데이터 학습 사용 동의 전제이므로, 앱에서 Free Tier 사용 시 명확한 고지 필요. (권고: Paid 기본, Free는 명시 동의 시)
3. **예산 한도 기본값**: soft cap 기본 활성화 여부 및 기본 금액. (권고: 기본 활성, 사용자 설정 금액)

---

## 10. 참고 자료

- [Gemini Live API 개요](https://ai.google.dev/gemini-api/docs/live-api)
- [Live translation with Gemini Live API](https://ai.google.dev/gemini-api/docs/live-api/live-translate)
- [Live API 기능 가이드](https://ai.google.dev/gemini-api/docs/live-api/capabilities)
- [Gemini 3.5 Live Translate 발표(Google Blog)](https://blog.google/innovation-and-ai/models-and-research/gemini-models/gemini-live-3-5-translate/)
- [Gemini Developer API 가격](https://ai.google.dev/gemini-api/docs/pricing)
- [BlackHole (가상 오디오 루프백)](https://github.com/ExistentialAudio/BlackHole)
- [Silero VAD (MIT)](https://github.com/snakers4/silero-vad)
- [FluidAudio — CoreML 오디오 SDK (VAD 포함)](https://github.com/FluidInference/FluidAudio)
- [silero-vad-coreml (사전 변환 모델)](https://huggingface.co/FluidInference/silero-vad-coreml)

---

*본 문서는 초안이며, §8.2 결정 사항 확정 후 구현 계획서/인터페이스 스펙으로 분화 예정.*
