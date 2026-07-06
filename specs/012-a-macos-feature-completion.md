# 012 — 대단위 A: macOS 기능 완성 (feature parity)

> 마스터 플랜 [000](000-cross-platform-porting-plan.md) 남은 대단위 **A**. P3b(앱 E2E 동작) 위에 원본 동등 기능을 쌓아 "매일 쓰는 앱" 수준으로.
> 원본 근거: `liveTranslate/Sources/Settings/SettingsStore.swift`, `Overlay/SubtitleOverlayView.swift`+`SubtitleStyle.swift`, `Audio/TranslatedAudioPlayer.swift`+`SystemAudioDucker.swift`, `Audio/VADGate.swift`, `Cost/CostEstimator.swift`, `Recording/SubtitleRecorder.swift`, `Config/KeychainAPIKeyProvider.swift`, `Pipeline/ModelCatalog.swift`.

## 실행 웨이브

### Wave 1 — 설정 기반 (A1): config 영속 + 키체인 + 설정 UI
모든 후속 기능이 여기에 필드를 꽂는다. **먼저 한다.**
- `internal/config/settings.go`: 전체 설정 모델 `Settings` 구조체 + 결정적 기본값 + `Load()/Save()`(JSON, `os.UserConfigDir()/Cross-liveTranslate/settings.json`). 원본 `SettingsStore` 키 그룹 이식(아래).
- API 키: `internal/credstore`로 mac Keychain 저장. 조회 순서 env→keyring 유지.
- controller: 설정 로드→적용, 변경 시 저장. 설정 UI(controller 프론트 별도 섹션/탭): 언어·입력·원문토글·자막스타일·위치·오디오·비용·녹화·**API 키 입력/연결테스트/저장**.
- 검증: 설정 변경→재실행 시 유지, 키체인에 키 저장 후 env 없이 동작.

### Wave 2 — 자막 스타일·위치(A2) + 번역음성 재생·덕킹(A3) (병렬 가능)
- **A2 자막 스타일**: overlay 프론트가 `Settings.Subtitle`을 IPC로 받아 렌더 — 폰트/크기/두께/글자색/외곽선/글로우/배경박스/정렬/최대줄수(원본 `SubtitleStyle`). 위치: 모니터 인덱스(cgo `setFrame` 대상 모니터) + 상/중/하 수직 위치. 실시간 미리보기.
- **A3 번역음성 재생**: `internal/audio/player.go`(malgo 24kHz Int16LE 재생 + 링버퍼, 백프레셔). gemini `EmitOutputAudio` 활성 + reconciler/controller가 `OutputAudio` 이벤트를 player로. 덕킹: `ducker_darwin.go`(CoreAudio `kAudioDevicePropertyVolumeScalar`) + 게인보상(1/duck, tanh 리미터, 원본 `AppState.applyAudioOutputPolicy`). 출력장치 선택. **피드백 차단**(F10)은 무설치 탭(P2b) 도입 시 필수 — BlackHole 경로는 사용자 라우팅.

### Wave 3 — VAD + 비용 + 녹화 + 모델카탈로그 (A4)
- **VAD(F13)**: Silero ONNX(onnxruntime-go) 클라이언트 게이트(원본 4096샘플/0.85/pad0.2). 로드 실패 시 bypass. 설정 on/off. *ONNX 플랫폼 라이브러리 부담 크면 서버 VAD 유지 + 훅만.*
- **비용(F3)**: `internal/cost` 추정(입력 $3.50/출력 $21.00 per 1M, 원본 단가) + 세션/누적 USD + HUD 표시 + 영속.
- **녹화(F4)**: `internal/recording` 확정 자막을 `[HH:MM:SS] 원문 → 번역` 파일 기록(이어붙이기/새로쓰기).
- **모델카탈로그(F5)**: `models.json` + 디스크립터, `minOS`→플랫폼 게이팅 일반화. 현재 Gemini 단일이라 최소.

### (선택) macOS 무설치 탭 (P2b/F9)
Core Audio Process Tap cgo — BlackHole 없이 시스템 캡처 + 자기 프로세스 제외(피드백 차단). A3(재생) 이후가 자연스러움. 별도 스펙/집중 작업.

## 설정 모델 (원본 SettingsStore 이식 — Wave 1에서 확정)
- Language: `target`(ko), `source`(auto), `showSource`(false)
- Input: `mode`(auto), `deviceID`
- Subtitle: `fontFamily, fontSize(16–72), fontWeight, textColor(#RRGGBBAA), strokeColor, strokeWidth, glowColor, glowRadius, bgColor, bgOpacity, align, maxLines`
- Position: `monitorIndex, vertical(top|middle|bottom)`
- Audio: `playbackEnabled, outputDeviceID, softVolume, duckEnabled, duckVolume`
- Cost: `hudEnabled, cumulativeUSD`
- Recording: `directory`
- VAD: `enabled`
- (API 키는 설정 JSON 아님 — Keychain)

## 보존 불변식
- 설정은 **결정적**(기본값 register, Date/난수 금지). 자막 색은 `#RRGGBBAA`.
- 오디오 재생 24kHz Int16LE, 백프레셔 드롭. 덕킹 미지원 장치는 자동 비활성.
- reconciler/epoch·자막엔진 heartbeat·피드백 차단 규칙 유지.

## 검증
각 웨이브: `go build ./... + go vet + go test -race` + 순수 패키지 windows 크로스빌드 + `wails build darwin`. 라이브(실키): 설정 변경 반영·스타일 렌더(스크린샷)·번역음성 청취·녹화 파일 확인. 빌드 통과만으로 완료 선언 금지.
