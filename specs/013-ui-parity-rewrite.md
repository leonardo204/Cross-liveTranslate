# 013 — UI/UX 원본 100% 재현 (대개편)

> **원칙**: 원본 `~/work/liveTranslate`(SwiftUI)의 UI/UX를 **픽셀 수준 100% 동일**하게 재현한다. 임의 디자인 절대 금지.
> 정밀 스펙은 원본 소스 분석 결과(메뉴바·제어 HUD·설정 창·자막 오버레이)를 따른다 — 아래 요약 + 각 에이전트에 상세 전달.
> 배경: 기존 구현이 원본을 무시하고 일반적 HUD/설정 UI를 임의로 만들었음 → 전면 재작성.

## 창 구성 (원본과 동일하게 분리)

원본은 **별도 창 4개**. Wails 단일창 제약 → **단일 바이너리 `--role` 3-프로세스 + 트레이**로 재현:

| 원본 창 | 재현 방식 | 크기/스타일 |
|---|---|---|
| 메뉴바 MenuBarExtra | 트레이(NSStatusBar, 기존 cgo) 메뉴 | 아이콘 MenuBarGlyph(템플릿). 메뉴: **번역 시작↔정지 / (구분) / ✓제어 HUD 표시 / 설정… ⌘, / (구분) / 종료 ⌘Q** |
| 제어 HUD(FloatingPanel) | `--role controller` 창 | **260×150**(비용행 시 176), frameless·투명·always-on-top·드래그 이동, 우상단, cornerRadius 14 + ultraThinMaterial(CSS backdrop-filter 근사) + 흰 테두리 12% |
| 설정 창 | `--role settings` 창(트레이/HUD가 spawn·토글) | **760×580**, 표준 타이틀바 "liveTranslate 설정", 사이드바(9 카테고리)+grouped Form. 리사이즈 불가 |
| 자막 오버레이 | `--role overlay`(기존) | 전체화면 투명·클릭통과. 렌더 구조 원본 정합(외곽선 3겹 r1/3/6, 글로우 2겹 r/r×1.6, 원문 65%·85%) |

## 백엔드 계약 (U1 — 먼저 확정)

### main.go
- `-role controller|settings|overlay`(기본 controller). controller=제어 HUD, settings=설정 창, overlay=오버레이.
- 각 role별 frontend sub-FS: `frontend/controller`(HUD), `frontend/settings`, `frontend/overlay`.
- controller 창: 260×150, `Frameless:true, AlwaysOnTop:true, BackgroundColour A:0, Mac.WebviewIsTransparent`. 우상단 배치(runtime).
- settings 창: 760×580, 표준 타이틀바, StartHidden(트레이/HUD가 show).

### 프로세스 관계
- controller(메인)가 overlay + settings 자식 spawn·감독(기존 overlay 패턴 확장). settings는 StartHidden → 트레이 "설정…" 또는 HUD 설정버튼이 `WindowShow`.
- 파이프라인(reconciler·자막엔진·player·ducker·cost·recording·vad)은 controller 소유(기존 유지).
- 설정 변경은 어느 창에서든 → controller가 단일 소스로 Settings Load/Save + 반영(IPC/이벤트로 창 간 동기화).

### 트레이 메뉴 (원본 항목·순서 정확히)
`번역 시작`↔`번역 정지`(isRunning) / 구분 / `제어 HUD 표시`(체크) / `설정…` / 구분 / `종료`. 콜백을 controller로 브릿지.

### 바인딩/이벤트 (프론트가 사용)
- controller HUD 바인딩: `ToggleCapture/Start/Stop/IsRunning`, 상태 조회. **HUD 상태 이벤트**(주기적 emit): 캡처상태(캡처중/정지), 입력레벨(0~1, RMS), VAD 발화여부, activeSourceLabel, geminiStatus/apiKeyLoaded, 비용(전송/수신/총 USD), 녹화중, costHUDEnabled. → HUD가 원본 레이아웃대로 표시.
- settings 바인딩: `GetSettings/SaveSettings/SaveAPIKey/TestAPIKey/HasAPIKey/ListDevices/ListOutputDevices/RefreshDevices`, 모델 카탈로그, 권한 상태, 버전/업데이트, resetAll. 9 카테고리 폼이 사용.

## 프론트 재작성 (원본 정밀 스펙 준수)

### U2 — 제어 HUD (frontend/controller)
원본 MonitorHUD 레이아웃 그대로: ① header(상태점 8px red/secondary + "캡처중/정지" 11pt semibold + 우측 발화 인디케이터 "발화/무음") ② 레벨미터(높이 6, capsule, >0.85빨강/>0.6주황/else초록, 0.08s linear) ③ footer(activeSourceLabel 10pt / VAD상태 9pt / 번역상태 or "API키 없음" 주황 9pt) ④ 비용행(costHUDEnabled 시, $ 아이콘 + 전송/수신/총 monospaced 9pt) ⑤ Divider 0.2 ⑥ 버튼행(시작/정지 borderedProminent tint red/accent + 녹화 토글 record.circle + 설정 gearshape.fill). SF Symbol은 동등 아이콘/유니코드/인라인 SVG로. 폰트 SF, 크기 8/9/10/11.

### U3 — 설정 창 (frontend/settings)
NavigationSplitView 재현: 좌측 사이드바 List(폭 ~190) 9항목(모델 cpu / 입력 mic / 자막 captions.bubble / 오디오 speaker.wave.2 / 제어 HUD macwindow / 비용 dollarsign.circle / 권한 lock.shield / API 키 key / 일반 gearshape), 우측 grouped Form. 각 카테고리 컨트롤은 원본 스펙 그대로(라벨 텍스트·컨트롤 종류·범위·caption 전부). grouped Form 룩(섹션 헤더, 흰 카드, LabeledContent 좌라벨/우값). 기본 선택 "입력". 자막 스타일 섹션은 실시간 미리보기 포함.

### U4 — 오버레이 렌더 정합
A2 기반 위에 원본 렌더 구조 확인·보정: 폰트 SF rounded(fontName 빈값 시), 외곽선 shadow 3겹(strokeColor opacity 0.9/0.8/0.6, r 1/3/6, y 0/1/2), 글로우 2겹(glowColor, r/r×1.6), roll-up 클립(maxLines) 먼저→외곽선→글로우 순, 원문 65%크기·85%불투명·간격6, 배경 RoundedRect r12 black.opacity, 바깥 패딩 가로60/세로8, 박스 패딩 가로20/세로12, 세로위치 3등분+offset. 스타일 기본값(fontSize 34, bold, textColor #FFFFFFFF, strokeEnabled #000000E6, glow off #00E5FFCC r8, bg on 0.35, center, maxLines 2).

## 공통 디자인 토큰(원본)
- 폰트: San Francisco(자막은 SF Rounded). 시스템 다이내믹 컬러(.red/.green/.orange/.accentColor/.secondary/.tertiary) → macOS 표준값 매핑. HUD 배경 ultraThinMaterial → `backdrop-filter: blur()` + 반투명.
- 코너: HUD 14, 자막박스 12(continuous). 버튼: borderedProminent/bordered/toggle button.
- 다크/라이트 자동 대응(시스템 컬러). 자막은 고정 색.

## 검증
각 창 실행 → **원본 실행 화면과 스크린샷 대조**(픽셀 근접). `go build/vet/test` + `wails build darwin`. 로직(파이프라인) 회귀 없음(E2E 번역 유지).
