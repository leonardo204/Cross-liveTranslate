# 010 — P2: 루프백 + reconciler/epoch + 자막 엔진

> 마스터 플랜 [000](000-cross-platform-porting-plan.md) §7 로드맵의 **P2**. P1([009](009-p1-core-translation-headless.md)) 위에 쌓는다.
> 원본 근거: `liveTranslate/Sources/App/AppState.swift`(reconciler/epoch), `Sources/Subtitle/SubtitleEngine.swift` + `specs/008`(자막), `Sources/Audio/SystemTapAudioSource.swift` + `AudioInputManager.swift`(루프백/선택).

## 스테이징

- **P2a (검증 쉬운 코어)**: 자막 엔진 + reconciler/epoch + 오디오 장치 열거/선택 + Windows 시스템 루프백(malgo WASAPI loopback) + headless 배선.
- **P2b (macOS 무설치 탭)**: Core Audio Process Tap(cgo) — 원본의 차별점(무설치·화면권한 불필요 시스템 캡처) + 피드백 루프 차단(자기 프로세스 제외). 가장 어려운 cgo 단일 항목이라 분리.

## 보존 필수 불변식 (원본)
1. **reconciler 단일직렬 + epoch 펜싱** — 활성 provider ≤1, teardown 무중첩, 세대 토큰으로 stale 이벤트 폐기.
2. **피드백 루프 차단** — 번역음성 재생 시(P4) 자기 출력이 탭에 재유입되지 않도록 자기 프로세스 제외(mac) / 별도 출력 라우팅(win).
3. **무음 정리는 STT/스트림 heartbeat 기준**(VAD offset 아님).

## 모듈 계약

### `internal/subtitle` — 자막 엔진 (순수, P2a)
원본 `SubtitleEngine.swift`(+spec 008) 이식. **누적(delta)·세그먼트 두 입력을 하나의 roll-up 표시로 통일**.
- 상태: `DisplayTranslation string`, `DisplaySource string`, `Visible bool`, `RollupLines []string`(최대 `MaxLines`).
- 입력 API: `IngestTranslatedDelta(text)`, `IngestSourceDelta(text)`, `IngestTranslatedSegment(text, final)`, `IngestSourceSegment(text, final)`, `TurnComplete()`, `GenerationComplete()`, `Interrupted()`, `Heartbeat(now)`(무음 정리 판정), `Reset()`.
- 로직(원본 보존): delta 누적 + **dedup**(모델 비연속 반복 완화), **charBreak**(길이 초과 시 분절), roll-up FIFO(위로 굴림 + suffix maxLines 클립), **STT/스트림 heartbeat 기반 연속 무음 시 자동 정리**, turnComplete 비신뢰 방어(무음 폴백).
- 결정적(시간 입력은 인자로 주입 — `Date.now()`/난수 금지). **완전 단위 테스트 대상**: dedup, charBreak, roll-up 클립, heartbeat 정리, 경계.
- 렌더링(오버레이 창)은 P3 — 엔진은 표시 문자열/줄 상태만 제공.

### `internal/app` — reconciler + epoch (P2a)
원본 `AppState.swift` reconciler 이식(§7 불변식).
- `type Epoch uint64`(atomic). desired(사용자 의도: 실행/정지, 입력소스, 언어) vs actual(현재 provider/source) 상태.
- `Reconcile()` 단일 goroutine 직렬화: desired≠actual이면 이전 provider/source teardown → 새로 start. 활성 provider ≤1 보장, teardown 무중첩.
- 이벤트 소비 시 **epoch 태그로 stale 폐기**: provider 시작 시 epoch 캡처, 이벤트 처리 시 현재 epoch와 불일치면 폐기(재연결/전환 중 옛 이벤트 무효화).
- `pipeline.Provider` + `audio.Source`를 오케스트레이트. 테스트: fake provider/source로 전환 시 이전 것 teardown·stale 이벤트 폐기 검증.

### `internal/audio` — 장치 선택 + Windows 루프백 (P2a)
- `EnumerateDevices() []DeviceInfo{ID, Name, IsLoopbackCandidate}` — malgo 캡처 장치 열거. 이름 휴리스틱("blackhole"/"loopback"/aggregate+virtual)으로 루프백 후보 표시(원본 `AudioDevice.swift`).
- `NewMalgoSource(deviceID)` 확장 — 특정 장치 선택 캡처(마이크/BlackHole 등 가상 입력).
- `loopback_windows.go`(`//go:build windows`): malgo **WASAPI loopback 백엔드**로 시스템 출력 캡처 → 동일 `Source` 계약(16kHz/mono/1600).
- `SelectSource(sel)` — auto(BlackHole 감지→그 장치 / 없으면 mac은 systemTap[P2b]·win은 loopback / 아니면 기본 마이크) / mic / device(id) / loopback.

### `internal/audio` — macOS Core Audio Process Tap (cgo, P2b)
`loopback_darwin.go` + ObjC/C 브리지. 원본 `SystemTapAudioSource.swift` 이식:
- `CATapDescription(monoGlobalTapButExcludeProcesses:[자기PID])` → `AudioHardwareCreateProcessTap` → `AudioHardwareCreateAggregateDevice`(private+tapautostart) → `kAudioTapPropertyFormat`(보통 48kHz float) → `AudioDeviceCreateIOProcIDWithBlock` → `AudioDeviceStart`.
- IO 블록에서 48kHz→16kHz mono Float32 변환 → 1600 청크. 역순 해제 + 멱등 teardown(-10877 무해화).
- **피드백 차단**: 자기 프로세스 제외(`muteBehavior=.unmuted` 원음 유지). macOS 14.4+ TCC(오디오 캡처 권한)만 필요.

### headless 배선 (P2a)
- `cmd/headless`에 `-input auto|mic|loopback|device:<id>` 추가. reconciler 경유로 source 선택.
- 자막 엔진을 이벤트 루프에 삽입: raw delta 대신 **정리된 roll-up 줄**을 콘솔 출력(dedup·줄바꿈 적용). `-list-devices`로 장치 열거 출력.

## 검증 (수용 기준)
- **P2a**: `go build ./...`(네이티브)+`go vet`+`go test -race ./internal/...` 통과. subtitle/reconciler **단위 테스트 필수**. 순수 패키지 windows 크로스빌드 유지. 라이브: `-list-devices` 동작, mic 입력 시 자막 엔진이 중복 제거된 줄을 출력(수동 스모크). Windows 루프백은 win 환경 필요(문서화).
- **P2b**: 네이티브 빌드+`go test` 통과. 라이브(mac, 수동): 시스템에서 오디오 재생 중 `-input loopback` → 시스템 소리가 번역됨. 번역음성 재생(P4) 전이라 피드백 차단은 구조만 확보.

## 범위 밖(후속)
- 오버레이 렌더(P3), 번역음성/덕킹(P4), 클라이언트 VAD/비용HUD/녹화(P5), 릴리스(P6).
