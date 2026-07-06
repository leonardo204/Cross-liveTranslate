# 000 — Cross-liveTranslate 크로스플랫폼 이식 방안 (마스터 플랜)

> 원본 `liveTranslate`(macOS 네이티브 Swift/AppKit) → `Cross-liveTranslate`(Windows/macOS 크로스플랫폼)
> 목표: **원본의 모든 기능을 동등하게 이식**, mac 개발 환경에서 **Windows까지 빌드/배포**, **자동 업데이트·루프백** 유지.

---

## 1. 스코프 결정 (확정)

| 항목 | 결정 | 근거 |
|---|---|---|
| 기반 스택 | **Go + Wails v2** | 팀 자산(confUploader/flipMd-Go)이 mac→win 빌드 + minisign self-apply 자동업데이트를 검증된 형태로 제공. malgo(miniaudio)가 WASAPI/CoreAudio 루프백을 추상화. |
| v1 번역 엔진 | **Gemini Live 전용** | 순수 WebSocket+JSON+base64 PCM이라 언어만 바꿔 거의 무손실 이식. Apple 온디바이스 STT+번역(macOS 26 전용)은 크로스플랫폼 등가물이 없어 **v1 범위 제외**. |
| UI | Wails 웹뷰(제어 HUD·설정) + **네이티브 오버레이 창**(자막) | 클릭통과·최상위·전체화면 위 투명 오버레이는 웹뷰만으로 어려워 플랫폼별 네이티브 창 shim으로 분리. |
| 자동 업데이트 | GitHub Releases + `latest.json` + **minisign(Ed25519)** + self-apply | Sparkle(mac 전용) 대체. confUploader 방식 그대로. |
| 시크릿 저장 | **go-keyring** (mac Keychain / win Credential Manager) | 원본 Keychain 일원화 정책 유지. |
| 설정 저장 | JSON 파일(`os.UserConfigDir`) | 원본 UserDefaults 대체. 결정적 기본값 유지. |

**미지원(이 크로스앱 범위 아님):** Apple 온디바이스 오프라인 엔진(SpeechTranscriber/Translation)은 크로스플랫폼 등가물이 없고 지원 계획도 없어 **완전 제외**한다. 관련 원본 스펙 003/007은 **삭제**했다. whisper.cpp STT·로컬 MT(NLLB/CTranslate2)도 현재 대상이 아니다(향후 필요 시 신규 스펙으로 재도입).

---

## 2. 원본 기능 → 이식 매핑 (기능 패리티 매트릭스)

이식성 3계층: 🟢 거의 무손실 · 🟡 플랫폼별 재작성 · 🔴 재설계

| # | 기능 | 원본 구현 | 이식 방식 | 계층 |
|---|---|---|---|---|
| F1 | Gemini Live 번역 | `GeminiLiveClient`(URLSessionWebSocket) | `gorilla/websocket` 재구현, 프로토콜 무변경 | 🟢 |
| F2 | 자막 엔진(roll-up/dedup/heartbeat) | `SubtitleEngine` | 순수 로직 Go 포팅 | 🟢 |
| F3 | 비용 추정 | `CostEstimator` | Go 포팅 | 🟢 |
| F4 | 자막 녹화 | `SubtitleRecorder` | Go `os.File` | 🟢 |
| F5 | 모델 카탈로그 | `models.json`+`ModelDescriptor` | JSON 그대로, `minOS`→플랫폼 게이팅 필드로 일반화 | 🟢 |
| F6 | 설정 영속화 | `SettingsStore`(UserDefaults) | JSON 파일, 결정적 기본값 | 🟢 |
| F7 | API 키 저장 | `KeychainAPIKeyProvider` | `go-keyring` | 🟡 |
| F8 | 마이크/장치 캡처 | `EngineAudioSource`(AVAudioEngine) | `malgo`(miniaudio) capture | 🟡 |
| F9 | **시스템 오디오 루프백** | `SystemTapAudioSource`(Core Audio Process Tap) | mac: Core Audio Tap(cgo) / win: **WASAPI loopback**(malgo loopback backend) | 🟡🔴 |
| F10 | 피드백 루프 차단 | 자기 프로세스 탭 제외 | mac: process-exclude / win: 별도 출력장치 or 프로세스 loopback 제외 | 🟡 |
| F11 | 번역 음성 재생(24kHz) | `TranslatedAudioPlayer`(AVAudioPlayerNode) | `malgo` playback + 링버퍼 | 🟡 |
| F12 | 원음 덕킹 | `SystemAudioDucker`(kAudioDevicePropertyVolumeScalar) | mac: CoreAudio 볼륨 / win: `ISimpleAudioVolume`(세션 덕킹) | 🟡 |
| F13 | VAD(발화 게이트) | FluidAudio Silero(CoreML) | **Silero VAD ONNX**(onnxruntime-go) — 파라미터 4096/0.85/pad0.2 그대로. *v1은 Gemini 서버 VAD로 대체 가능, 클라이언트 VAD는 비용절감 옵션* | 🟡 |
| F14 | 자막 오버레이(클릭통과·최상위·투명) | `SubtitleOverlayWindow`(NSPanel screenSaver level) | **네이티브 오버레이 창**: mac NSPanel(cgo) / win `WS_EX_LAYERED\|TRANSPARENT\|TOPMOST` | 🔴 |
| F15 | 제어 HUD | `FloatingPanel`+`MonitorHUD` | Wails 프레임리스 웹뷰 창 | 🟡 |
| F16 | 설정 창 | `SettingsWindowController` | Wails 웹뷰 창 | 🟡 |
| F17 | 메뉴바 상주 | `MenuBarExtra`(LSUIElement) | 시스템 트레이(mac 메뉴바 / win 트레이) | 🟡 |
| F18 | 멀티모니터/위치 | `NSScreen`+오버레이 컨트롤러 | 플랫폼 모니터 열거 | 🟡 |
| F19 | 무중단 재연결 | sessionResumption/goAway/14분 선제 | F1과 함께 포팅 | 🟢 |
| F20 | 자동 업데이트 | Sparkle | minisign + self-apply | 🟡 |
| ~~F21~~ | ~~Apple 온디바이스 STT+MT~~ | ~~AppleSpeech+Translation~~ | **미지원(삭제)** | 🔴 |

### 반드시 보존할 정확성 불변식 (원본 spec 근거)
1. **reconciler 단일직렬 + epoch 펜싱** — 활성 provider ≤1, teardown 무중첩, stale 이벤트 폐기 (원본 004 §7).
2. **피드백 루프 차단** — 번역 음성 재생 시 자기 출력이 루프백에 재유입되어 무한 재번역되는 것을 반드시 차단 (원본 002).
3. **STT/스트림 heartbeat 기반 무음 정리** — VAD offset이 아닌 엔진 출력 heartbeat 기준 (원본 008).
4. **turnComplete 비신뢰 방어** — delta dedup / 무음 폴백 / 길이 분절 (원본 002).
5. **오디오 계약** — 입력 16kHz mono Float32 100ms(1600샘플) 청크, 출력 재생 24kHz mono Int16 LE.

---

## 3. 목표 아키텍처 (Go 모듈 구성)

```
Cross-liveTranslate/
├── main.go                 # 진입점, appVersion(ldflags), Wails app 부트스트랩
├── wails.json              # 앱 메타 + 프론트 빌드 설정
├── go.mod / go.sum
├── frontend/               # 웹 UI (제어 HUD + 설정). go:embed
│   └── ...                 # vanilla 또는 경량 프레임워크
├── internal/
│   ├── app/                # AppState 오케스트레이터 + reconciler(epoch 펜싱)
│   ├── audio/              # AudioSource 인터페이스 + 백엔드
│   │   ├── source.go       # 공통 계약(16kHz/mono/1600청크)
│   │   ├── capture_malgo.go        # 마이크/장치 캡처
│   │   ├── loopback_darwin.go      # //go:build darwin — Core Audio Tap(cgo)
│   │   ├── loopback_windows.go     # //go:build windows — WASAPI loopback
│   │   ├── player.go       # 번역음성 재생(24kHz)
│   │   └── ducker_darwin.go / ducker_windows.go
│   ├── vad/                # Silero ONNX 게이트 (선택)
│   ├── gemini/             # Gemini Live WebSocket 클라이언트
│   ├── pipeline/           # PipelineEvent + Provider 추상화
│   ├── subtitle/           # 자막 엔진(roll-up/dedup/heartbeat)
│   ├── overlay/            # 네이티브 오버레이 창 shim
│   │   ├── overlay_darwin.go   # NSPanel screenSaver level(cgo)
│   │   └── overlay_windows.go  # layered/transparent/topmost
│   ├── cost/               # 비용 추정
│   ├── recording/          # 자막 녹화
│   ├── config/             # 설정 저장(JSON) + 기본값
│   ├── secrets/            # go-keyring 래퍼
│   ├── tray/               # 시스템 트레이(플랫폼별)
│   └── updater/            # latest.json + minisign 검증 + self-apply
└── scripts/
    ├── release-macos-dmg.sh    # mac 빌드/서명/노터라이즈/minisign/gh upload
    ├── add-windows-asset.sh    # win 크로스빌드/서명/매니페스트 병합
    └── keygen-minisign.sh
```

**동시성 매핑:** Swift actor/AsyncStream → Go goroutine + channel. epoch 세대 토큰 → `atomic` 카운터. 실시간 오디오 콜백 → 논블로킹 채널 송신(백프레셔 드롭 유지).

---

## 4. 빌드 & 배포 전략 (mac → win)

### 크로스컴파일 현실
- **순수 Go 경로**(Gemini WS, 자막, 설정 등): `wails build -platform windows/amd64`가 macOS에서 **크로스컴파일 성공**(Wails 2.12+ 순수 Go WebView2 로더). Windows 머신 불필요.
- **cgo 경로**(malgo 오디오, Core Audio Tap, ONNX): macOS→Windows cgo 크로스컴파일은 **mingw-w64 툴체인 필요**(`brew install mingw-w64`, `CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc`). malgo(single-header)는 가능하나 ONNX 런타임은 플랫폼별 prebuilt 라이브러리 필요.

### 권장: GitHub Actions 매트릭스 (robust)
- `macos-latest` 러너 → macOS universal DMG 빌드/코드사인/노터라이즈/스테이플 → minisign 서명.
- `windows-latest` 러너 → Windows exe 빌드/(선택)Authenticode 서명 → minisign 서명.
- 두 아티팩트를 하나의 GitHub Release + 병합된 `latest.json`으로 게시.
- **"mac에서"의 의미**: 개발/릴리스 트리거는 mac에서 태그 푸시 → CI가 양 플랫폼 산출. 단일 mac 머신 완결이 필요하면 mingw 크로스컴파일 경로로 축소 가능(오디오까지).

### 버전 주입
```go
var appVersion = "0.1.0-dev" // ldflags로 오버라이드
```
`wails build ... -ldflags "-X main.appVersion=<ver>"`. semver 단일 진실원(원본의 정수 sparkle:version → semver로 통일).

---

## 5. 자동 업데이트 (Sparkle → minisign self-apply)

confUploader 하드닝 방식을 그대로 채택:
- **매니페스트**: `…/releases/latest/download/latest.json`, `platforms[darwin-aarch64|darwin-x86_64|windows-x86_64]{version,url,signature}`.
- **신뢰**: 앱에 임베드된 minisign **공개키**로 다운로드 자산을 실행 전 반드시 검증(Ed25519+BLAKE2b).
- **macOS 적용**: 실행 중 .app은 자기 덮어쓰기 불가 → detached `/bin/sh` + `ditto` 스왑 + `.bak` 롤백.
- **Windows 적용**: PowerShell/배치 헬퍼 금지(AV/EDR가 죽임) → **self-apply**(`--apply-update --target <old>`로 검증된 자기 exe 재실행), 버전리스 파일명.
- **관측성**: 앱 프로세스가 각 단계(check→download→verify→swap→relaunch) 로그 기록.
- **주의(원본 결함 승계 금지)**: 원본은 `sparkle_private_key.txt`가 gitignore 누락 → 크로스 이식 시 minisign 개인키는 **CI 시크릿에만** 저장, 저장소 커밋 금지.

## 6. 자동 업데이트 외 "루프백" 유지
루프백 = 시스템 출력 오디오를 재캡처(F9). v1 필수. mac은 Core Audio Process Tap(무설치·화면권한 불필요) 유지, win은 WASAPI loopback. 번역 음성 재생 시 F10(피드백 차단) 필수 동반.

---

## 7. 단계별 로드맵 (Phase)

| Phase | 목표 | 산출물 | 검증 |
|---|---|---|---|
| **P0** | 스캐폴드 | Wails 앱 부팅, 트레이, 버전 주입, CI 뼈대, updater 스텁 | mac/win 빈 앱 실행 |
| **P1** | 코어 번역(headless) | Gemini WS 클라이언트(F1,F19) + 오디오 캡처(F8) + 마이크 입력 → 콘솔 자막 | 실측 번역 텍스트 출력 |
| **P2** | 루프백 + 파이프라인 | 시스템 루프백(F9,F10) + reconciler(epoch) + 자막엔진(F2) + 이벤트모델 | 시스템 소리 번역 |
| **P3** | UI | 오버레이 창(F14) + 제어 HUD(F15) + 설정창(F16) + 트레이(F17) + 멀티모니터(F18) | 자막 화면 표시 |
| **P4** | 오디오 출력 | 번역음성 재생(F11) + 덕킹(F12) + 게인보상 | 재생/덕킹 동작 |
| **P5** | 부가 | VAD(F13) + 비용(F3) + 녹화(F4) + 설정 영속(F6) + 키체인(F7) + 모델카탈로그(F5) | 기능 패리티 점검 |
| **P6** | 배포 | minisign 자동업데이트(F20) + DMG/exe 릴리스 + CI 매트릭스 | vN→vN+1 실 업데이트 검증 |

각 Phase 종료 시 `verify` 스킬로 실제 동작 확인(빌드 통과만으로 완료 선언 금지).

---

## 8. 리스크 & 완화

| 리스크 | 영향 | 완화 |
|---|---|---|
| 웹뷰 오버레이 클릭통과 난도 | 높음 | 자막 오버레이를 웹뷰 아닌 **네이티브 창 shim**으로 분리(F14) |
| cgo mac→win 크로스컴파일 | 중 | CI Windows 러너 우선, mingw는 대안 |
| WASAPI loopback 프로세스 제외 API 차이 | 중 | win은 별도 출력장치 라우팅 폴백 제공 |
| Gemini 오디오 세션 15분 한도 | 중 | 원본 14분 선제 재연결(F19) 그대로 이식 |
| ONNX VAD 플랫폼 라이브러리 | 낮음 | v1은 서버 VAD로 대체, 클라이언트 VAD는 P5 옵션 |
| 실시간 동시성 정확성 | 높음 | 원본 불변식(§2) 테스트로 고정, epoch 펜싱 우선 이식 |

---

## 9. 참고 자산
- **자동업데이트 레퍼런스**: `~/work/confUploader`(DMG+self-apply), `~/work/flipMd-Go`(tar.gz+helper). 스킬 `desktop-app-auto-update`(custom-go-wails).
- **빌드 매트릭스**: 스킬 `wails-cross-platform-go`.
- **원본 스펙**: `specs/001,002,004,005,006,008`(이식 시 참조), 특히 002(Gemini/오디오), 004(파이프라인), 008(자막). *(003/007=Apple 온디바이스는 미지원으로 삭제)*

---

*이 문서는 이식의 단일 진실원(마스터 플랜)이다. Phase 진행에 따라 갱신한다.*
