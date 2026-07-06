# Cross-liveTranslate

**Windows/macOS 크로스플랫폼** 실시간 자막 번역 앱입니다. 컴퓨터에서 흘러나오는 소리(또는 마이크
입력)를 실시간으로 받아 **Gemini 3.5 Live Translate**(클라우드, API 키)로 번역하고, 그 결과를
**영화 자막처럼 화면 위에 roll-up 오버레이**로 띄웁니다. 선택적으로 **번역 음성도 재생**할 수 있습니다.

시스템 트레이 상주 앱으로, 창 없이 가볍게 동작합니다.

> 이 저장소는 macOS 네이티브 앱 [`liveTranslate`](https://github.com/leonardo204/liveTranscript)(Swift/AppKit)를
> **Go + Wails v2** 기반 크로스플랫폼(Windows/macOS)으로 이식하는 프로젝트입니다.
> 이식 방안·범위·로드맵은 **[마스터 플랜(000)](specs/000-cross-platform-porting-plan.md)** 을 참조하세요.

---

## 상태 (이식 진행 중)

이 프로젝트는 원본을 단계적으로 이식하는 중입니다. 진행 상황은 [마스터 플랜 §7 로드맵](specs/000-cross-platform-porting-plan.md#7-단계별-로드맵-phase)을 참조하세요.

- **엔진**: Gemini Live 전용 (클라우드). Apple 온디바이스 오프라인 엔진은 크로스플랫폼 등가물이 없어 이 앱에서는 **지원하지 않습니다**.

---

## 주요 기능 (이식 대상)

- **시스템 오디오 직접 캡처(루프백)** — macOS는 Core Audio Process Tap, Windows는 WASAPI loopback으로
  시스템 출력 오디오를 직접 캡처합니다. 가상 장치 설치나 화면 녹화 권한이 불필요합니다.
- **다양한 입력 소스** — 시스템 오디오 루프백 · 마이크 · 가상 루프백 장치를 지원하며, 자동 선택도 가능합니다.
- **Gemini Live 번역** — Gemini 3.5 Live Translate(클라우드, API 키)로 실시간 번역합니다.
- **VAD(음성 활동 감지)** — 발화 구간만 전송해 API 비용을 절감합니다(서버 VAD 기본, 클라이언트 Silero VAD 옵션).
- **영화 자막식 roll-up 오버레이** — 최상위·클릭 통과 투명 창에 자막을 여러 줄로 누적해 위로 굴려 표시하고,
  무음 시 자동 정리합니다.
- **자막 스타일 설정** — 폰트/크기/두께/글자색/외곽선/글로우/배경 박스/정렬/최대 줄수를 실시간 미리보기로 조정합니다.
- **자막 위치** — 표시 모니터 선택 + 상/중/하 세로 위치(드래그 이동, 영속화).
- **원문 동시 표시** 토글(기본 OFF).
- **번역 음성 재생(선택)** — 번역 음성(24kHz)을 재생하고, 출력 장치 선택과 원문 오디오 덕킹(자동 게인 보상)을 지원합니다.
- **자막 녹화** — 확정 자막을 `[HH:MM:SS] 원문 → 번역` 텍스트 파일로 저장(이어붙이기/새로 쓰기).
- **실시간 비용 추정** — 세션 비용(전송/수신/총 USD) 표시 + 누적 비용 영속화.
- **무중단 재연결** — `sessionResumption`/`goAway` 핸드오버 + 세션 한도 전 선제 재연결.
- **자동 업데이트** — GitHub Releases + minisign(Ed25519) 서명 검증 기반 in-app 자동 업데이트(self-apply).

---

## 요구사항

- **Windows 10/11 (x64)** 또는 **macOS 12 이상**
- **Gemini API 키** — [Google AI Studio](https://aistudio.google.com)에서 발급
- 빌드: **Go 1.22+**, [Wails v2](https://wails.io) CLI (`~/go/bin/wails`), Node(프론트엔드 빌드)
- macOS→Windows 크로스빌드 시: `mingw-w64`(오디오 cgo 계층) 또는 GitHub Actions Windows 러너

---

## 빌드 & 실행

```bash
wails dev                                        # 개발 모드(핫리로드)
wails build                                       # 현재 플랫폼 빌드
wails build -platform darwin/universal            # macOS universal
wails build -platform windows/amd64               # Windows x64 (mac에서 크로스빌드)
```

빌드 시 버전 주입:

```bash
wails build -platform darwin/universal -ldflags "-X main.appVersion=<version>"
```

빠른 검증:

```bash
go build ./...                                    # 네이티브 컴파일
GOOS=windows GOARCH=amd64 go build ./...          # Windows 빌드 태그 검증
go vet ./... && go test ./...
```

---

## 릴리스 & 자동 업데이트

GitHub Releases에 `latest.json` 매니페스트 + 플랫폼별 자산(macOS `.dmg`, Windows `.exe`)을 게시하고,
각 자산은 **minisign(Ed25519)** 개인키로 서명합니다. 앱은 임베드된 공개키로 다운로드를 **실행 전 검증**합니다.

```bash
scripts/release-macos-dmg.sh <version> --upload   # macOS 빌드/서명/노터라이즈/minisign/업로드
scripts/add-windows-asset.sh <version>            # Windows exe 빌드/서명/매니페스트 병합
```

> minisign 개인키는 **CI 시크릿에만** 보관하고 저장소에 커밋하지 않습니다.
> 상세는 [마스터 플랜 §5](specs/000-cross-platform-porting-plan.md#5-자동-업데이트-sparkle--minisign-self-apply)를 참조하세요.

---

## 설정

- **API 키** — OS 자격증명 저장소(macOS Keychain / Windows Credential Manager)에 저장합니다(평문 보관 금지).
- **번역 대상 언어** — 기본 `ko`(한국어), BCP-47 코드로 변경. 소스 언어는 자동 감지.
- **입력 소스** — 자동 / 시스템 오디오 루프백 / 특정 장치.
- **VAD** — on/off.
- **자막 스타일·위치·원문 표시·음성 재생·덕킹·녹화·비용·자동 업데이트** — 설정 창에서 조정.

---

## 문서

- **[마스터 플랜 (000)](specs/000-cross-platform-porting-plan.md)** — 이식 방안·기능 패리티 매트릭스·로드맵·빌드/배포·리스크
- [설계 스펙 (001)](specs/001-liveTranslate-design.md) — 전체 아키텍처, 마일스톤, 비용 계획
- [Gemini Live & 오디오 (002)](specs/002-gemini-live-translate-and-audio.md) — Live Translate 사용법, 오디오 피드백 루프 차단
- [번역 파이프라인 추상화 (004)](specs/004-translation-pipeline-architecture.md) — Provider/Stage 파이프라인 + reconciler 불변식
- [모델 카탈로그 + 설정 (005)](specs/005-model-catalog-and-settings.md) — 모델 레지스트리 + 능력 기반 UI
- [자막 표시 아키텍처 (008)](specs/008-subtitle-rendering-architecture.md) — roll-up + heartbeat 무음 처리 + 글로우 클립 렌더

---

## 비용 안내

비용은 **Gemini 엔진**에 적용됩니다. 오디오 입력 $3.50 / 1M tokens, 오디오 출력 $21.00 / 1M tokens이며,
출력 오디오는 재생하지 않아도 생성·과금됩니다. **무음도 과금**되므로 VAD로 발화 구간만 전송해 비용을 절감합니다.
앱의 비용 HUD와 설정의 누적 비용 표시로 사용량을 확인하세요.
