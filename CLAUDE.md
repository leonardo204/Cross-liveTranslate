# Claude Code 개발 가이드

> 공통 규칙(Agent Delegation, 커밋 정책, Context DB 등)은 글로벌 설정(`~/.claude/CLAUDE.md`)을 따릅니다.
> 글로벌 미설치 시: `curl -fsSL https://raw.githubusercontent.com/leonardo204/dotclaude/main/install.sh | bash`

---

## Slim 정책

이 파일은 **100줄 이하**를 유지한다. 새 지침 추가 시:
1. 매 턴 참조 필요 → 이 파일에 1줄 추가
2. 상세/예시/테이블 → ref-docs/*.md에 작성 후 여기서 참조
3. ref-docs 헤더: `# 제목 — 한 줄 설명` (모델이 첫 줄만 보고 필요 여부 판단)

---

## PROJECT

### 개요

**Cross-liveTranslate** — macOS 네이티브 실시간 자막 번역 앱 `liveTranslate`(Swift/AppKit)를 Windows/macOS 크로스플랫폼으로 이식하는 프로젝트. 시스템 오디오(루프백)/마이크를 캡처해 Gemini Live로 번역하고 클릭통과 오버레이에 자막을 표시한다.

| 항목 | 값 |
|------|-----|
| 기술 스택 | Go 1.22+, Wails v2, 웹뷰 프론트엔드, malgo(오디오), go-keyring |
| 빌드 방법 | `wails build` / mac→win: `wails build -platform windows/amd64` |
| 자동 업데이트 | GitHub Releases + `latest.json` + minisign(Ed25519) + self-apply |
| v1 엔진 | Gemini Live 전용 (Apple 온디바이스 경로는 v1 제외) |
| 상태 | 이식 초기 (P0 스캐폴드) |
| 원본 | `~/work/liveTranslate` (macOS/Swift, Sparkle) |
| 레퍼런스 | `~/work/confUploader`, `~/work/flipMd-Go` (Go/Wails 자동업데이트) |

**단일 진실원**: [마스터 플랜 (000)](specs/000-cross-platform-porting-plan.md) — 기능 패리티 매트릭스·로드맵·빌드/배포·리스크.

### 보존 필수 불변식 (이식 시)

- reconciler 단일직렬 + epoch 펜싱(stale 이벤트 폐기)
- 피드백 루프 차단(번역음성 재생 시 자기 출력 재캡처 방지)
- STT/스트림 heartbeat 기반 무음 정리
- 오디오 계약: 입력 16kHz mono Float32 1600샘플 청크, 출력 24kHz mono Int16 LE

### 문서 구조 (소유권 분리)

- **하니스 문서** (`ref-docs/claude/` 하위) — 🔒 dotclaude 소유. `dotclaude-update`가 덮어쓰니 **수정 금지**.
- **프로젝트 스펙** (`specs/` 하위) — 📝 자유롭게 작성. → [SDD 가이드라인](ref-docs/claude/sdd.md) · `/spec-guard`로 정합성 분석

### 하니스 상세 문서 (ref-docs/claude/)

- [Context DB](ref-docs/claude/context-db.md) — SQLite 기반 세션/태스크/결정 저장소
- [Context Monitor](ref-docs/claude/context-monitor.md) — HUD + compaction 감지/복구
- [Hooks](ref-docs/claude/hooks.md) — 5개 자동 실행 Hook 상세
- [컨벤션](ref-docs/claude/conventions.md) — 커밋, 주석, 로깅 규칙
- [셋업](ref-docs/claude/setup.md) — 새 환경 초기 설정
- [Agent Delegation](ref-docs/claude/agent-delegation.md) — 에이전트 위임/파이프라인 상세
- [SDD 가이드라인](ref-docs/claude/sdd.md) — 스펙 문서 작성/관리 규약

> 프로젝트 스펙은 `specs/`에 작성하고, 하니스 문서(`ref-docs/claude/`)는 건드리지 마세요.

### 핵심 규칙

- 플랫폼 분기 코드는 `//go:build darwin` / `//go:build windows` 태그 파일로 분리 (`_darwin.go`/`_windows.go`).
- 시크릿(minisign 개인키, API 키)은 저장소에 커밋 금지 — CI 시크릿 / OS keyring 사용.
- 빌드 검증: `go build ./...` + `GOOS=windows GOARCH=amd64 go build ./...` 양쪽 통과 필수.
- 커밋 컨벤션: `[Feature]`, `[Fix]`, `[Refactor]`, `[Docs]` (사용자 명시 요청 전까지 커밋 금지).

---

*최종 업데이트: 2026-07-06*
