# 009 — P1: 코어 번역 슬라이스 (headless)

> 마스터 플랜 [000](000-cross-platform-porting-plan.md) §7 로드맵의 **P1**.
> 목표: UI 없이 **마이크 → Gemini Live → 콘솔 자막**이 흐르는 최소 수직 슬라이스. 이후 P2(루프백/파이프라인)·P3(UI)의 토대.
> 원본 근거: `liveTranslate/Sources/Gemini/GeminiLiveClient.swift`, `specs/002-gemini-live-translate-and-audio.md`, 오디오 계약 `AppConfig`.

## 범위

포함: Gemini Live WS 클라이언트, 마이크 캡처(malgo), PipelineEvent 모델 + Provider 인터페이스, API 키 조회, headless CLI 진입점, 순수 로직 단위 테스트.
제외(후속): 시스템 루프백(P2), reconciler/epoch(P2), 자막 렌더/오버레이(P3), 번역음성 재생·덕킹(P4), 클라이언트 VAD(P5). **P1은 서버 VAD에 의존**(realtimeInput disabled=false).

## 모듈 계약

### `internal/audio` — 캡처 + PCM
- `type Chunk []float32` — 길이 1600(100ms @16kHz mono).
- `type Source interface { Start(ctx, onChunk func(Chunk)) error; Stop() error }`.
- `capture_malgo.go`(cgo): malgo capture 디바이스를 **16kHz / mono / f32**로 설정(miniaudio 내부 리샘플 활용) → 콜백 누적 버퍼를 1600 단위로 잘라 `onChunk` 방출. 실시간 콜백에서 논블로킹(채널 가득 시 드롭 + 카운터).
- `pcm.go`(순수, 테스트 대상): `Float32ToInt16LE([]float32) []byte`(클램프 [-1,1]), `Int16LEToFloat32([]byte) []float32`, `RMS([]float32) float32`. 24kHz→재생은 P4.
- 계약 상수는 `internal/config` 또는 패키지 const: `SampleRate=16000`, `ChunkSamples=1600`.

### `internal/gemini` — Live WebSocket 클라이언트
원본 `GeminiLiveClient.swift` 프로토콜을 **무변경 이식**.
- 엔드포인트: `wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent?key=<APIKEY>`.
- **setup(첫 메시지)**: `responseModalities:["AUDIO"]`(translate 모델은 AUDIO만; 텍스트는 transcription으로 수신), `generationConfig` **내부에 nested** `translationConfig{sourceLanguage?, targetLanguage}`(top-level은 1007 거부), `outputAudioTranscription:{}` 항상, `inputAudioTranscription:{}`은 원문 표시 시만, `sessionResumption:{handle?}`, `realtimeInputConfig`.
- **송신**: `Chunk` → `Float32ToInt16LE` → base64 → `{realtimeInput:{audio:{data, mimeType:"audio/pcm;rate=16000"}}}`.
- **수신 파싱**(`protocol.go` 구조체 + `client.go` 디스패치):
  - `serverContent.outputTranscription.text` → **번역 텍스트 delta**.
  - `serverContent.inputTranscription.text` → **원문 텍스트 delta**(옵션).
  - `serverContent.modelTurn.parts[].inlineData`(24kHz Int16 LE base64) → `PipelineEvent.OutputAudio`(P1은 폐기/무시 가능, 이벤트만 방출).
  - `serverContent.turnComplete` / `generationComplete` / `interrupted` → 경계 이벤트(**비신뢰 방어**: delta dedup는 자막엔진 몫이나 이벤트는 그대로 전달).
  - `usageMetadata` → `PipelineEvent.Usage`(토큰).
  - `sessionResumptionUpdate.newHandle` 저장; `goAway` → 재연결 예약.
- **견고성**(원본 그대로): 지수 백오프 재연결, `sessionResumption` 핸들 재사용, **14분 선제 재연결**(오디오 세션 ~15분 한도), `goAway` 핸드오버. 재연결은 자막 흐름을 끊지 않아야 함.
- 동시성: 수신 루프 goroutine → 이벤트 채널. 송신은 별도. `context` 취소로 정리.

### `internal/pipeline` — 이벤트 + Provider
- `type Event struct { Kind; Text string; Final bool; AudioPCM []byte; Usage *Usage; State; Err error }` — 원본 `PipelineEvent` 등가.
  Kind: `SourceDelta, TranslatedDelta, TurnComplete, GenerationComplete, Interrupted, OutputAudio, Usage, State, PermanentFailure`.
- `type Provider interface { Start(ctx) (<-chan Event, error); Send(Chunk) error; Stop() error }`.
- `gemini_provider.go`: gemini client를 Provider로 어댑트.

### `internal/config` — 설정 + 키
- Gemini 모델 식별자 상수(기본값은 원본 `AppConfig`의 값 참조; 프리뷰 모델명은 변동 가능하므로 const + 주석). 대상/소스 언어 기본(`target=ko`, `source=auto`).
- API 키 조회 순서: 환경변수 `GEMINI_API_KEY`(개발) → `credstore.Load("cross-livetranslate","gemini_api_key")`(배포). 키 없으면 명확한 에러.

### `cmd/headless` — 진입점
- `go run ./cmd/headless -target ko [-source en] [-show-source] [-duration 60s]`.
- 배선: 키 조회 → gemini Provider.Start → audio.Source.Start(onChunk→Provider.Send) → 이벤트 루프에서 원문/번역 delta를 콘솔에 라인 출력(원문은 `-show-source`시). RMS 레벨을 stderr에 주기적 출력(디버그).
- Ctrl-C/duration으로 정리.

## 의존성 추가
- `github.com/gorilla/websocket`
- `github.com/gen2brain/malgo`(miniaudio, cgo)

## 검증 (수용 기준)
1. `go build ./...`(네이티브, cgo on) 통과. *(Windows 크로스빌드는 malgo cgo로 인해 mingw 필요 → 이 단계는 네이티브 빌드만 필수, CI/mingw는 P6)*
2. `go test ./...` 통과 — **순수 로직 테스트 필수**: `pcm`(왕복 변환/클램프), `gemini/protocol`(setup 직렬화에서 translationConfig가 generationConfig 내부에 위치하는지, 수신 JSON 언마샬 디스패치), 청킹(1600 경계).
3. `go vet ./...` 통과.
4. **라이브 스모크(수동)**: `GEMINI_API_KEY` 설정 + 마이크 권한 후 `cmd/headless` 실행 → 말하면 번역 텍스트가 콘솔에 출력. *(키·마이크 필요, 세션 자동검증 불가 — 수동 절차로 문서화)*

## 참고
- Windows 크로스빌드가 malgo(cgo)로 깨지는 것은 예상된 것 — P1 완료 판정은 **네이티브 빌드+테스트**. 순수 패키지(pipeline/config/protocol)는 여전히 `GOOS=windows go build`가 통과해야 함(플랫폼 분리 태그 확인).
