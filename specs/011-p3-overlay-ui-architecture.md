# 011 — P3: 오버레이 UI 아키텍처 (2-프로세스)

> 마스터 플랜 [000](000-cross-platform-porting-plan.md) §7 로드맵의 **P3**. Wails v2.12.0 소스 조사(architect) 결론 반영.
> 원본 근거: `liveTranslate/Sources/Overlay/SubtitleOverlayWindow.swift`, `FloatingPanel.swift`, `SettingsWindowController.swift`, `liveTranslateApp.swift`(MenuBarExtra).

## 배경 제약 (Wails v2.12.0, 소스 확정)
- **프로세스당 단일 WebviewWindow** — 다중 창 생성 공개 API 없음.
- **네이티브 핸들 미노출** — NSWindow/HWND는 `internal/`에 은닉. 자체 cgo에서 `[NSApp windows]`(mac) / `FindWindowW(WindowClassName)`(win)로 직접 획득.
- **클릭통과·screenSaver level·트레이 미지원** — 전부 자체 cgo/외부 라이브러리.
- 옵션으로 되는 것: `Frameless`, `AlwaysOnTop`(=NSFloatingWindowLevel), `StartHidden`, `Mac.WebviewIsTransparent`+`BackgroundColour{A:0}`(투명), `Windows.WindowClassName`(FindWindow용). `Mac.WindowIsTranslucent`는 **끈다**(vibrancy 블러 방지).

## 아키텍처: 2-프로세스, 단일 바이너리 `--role` 분기

| 프로세스 | Wails 창 | 역할 |
|---|---|---|
| **controller**(메인) | 제어 HUD(frameless 소형·이동) + 설정(같은 창 라우트) | 시작/정지·상태·설정. 시스템 트레이 소유. overlay 자식 프로세스 spawn·감독. 번역 파이프라인(reconciler) 구동 |
| **overlay** | 전체화면 투명·frameless·always-on-top·**클릭통과** | IPC로 받은 자막/스타일/대상 모니터 렌더. cgo로 level·collectionBehavior·ignoreMouse·setFrame 적용 |

- 단일 바이너리 + `--role=controller|overlay`(기본 controller). 코드사이닝 1개.
- IPC(controller→overlay): stdio JSON 라인 또는 로컬 소켓. 자막 텍스트/스타일/모니터 인덱스/표시토글 push.
- cgo 네이티브 적용 타이밍: **`OnDomReady`**(창 realize 후). 대상 창은 `[NSApp windows]`에서 타이틀/클래스로 식별.

## 모듈/파일
```
main.go                    // --role 분기 → runController() / runOverlay()
internal/overlay/
  overlay.go               // 인터페이스: ApplyOverlayWindow(opts) — SetClickThrough/LevelScreenSaver/CoverScreen(idx)/Transparent
  overlay_darwin.go/.h/.m  //go:build darwin, #cgo LDFLAGS: -framework Cocoa
  overlay_windows.go       //go:build windows — FindWindowW+WS_EX_TRANSPARENT|LAYERED|TOOLWINDOW, HWND_TOPMOST, EnumDisplayMonitors
  overlay_other.go         //go:build !darwin && !windows (stub)
internal/tray/
  tray.go + tray_darwin.{go,h,m}(NSStatusBar cgo) + tray_windows.go(energye/systray goroutine)
internal/ipc/              // controller<->overlay 라인 프로토콜
frontend/                  // 정적 HTML/vanilla JS. overlay.html(투명 자막 렌더) + controller UI. Wails EventsEmit로 갱신
```

## 오버레이 창 옵션 프리셋
```
Frameless: true, AlwaysOnTop: true, StartHidden: true,
BackgroundColour: {R:0,G:0,B:0,A:0},
Mac:     { WebviewIsTransparent: true, WindowIsTranslucent: false },
Windows: { WebviewIsTransparent: true, WindowClassName: "LiveTranslateOverlay" },
```
cgo(OnDomReady)에서 추가: mac `setLevel:NSScreenSaverWindowLevel`, `collectionBehavior = FullScreenAuxiliary|CanJoinAllSpaces|Stationary`, `setIgnoresMouseEvents:YES`, `setOpaque:NO`+clear color, `setFrame:[NSScreen screens][idx].frame`(full, 메뉴바 포함).

## 자막 렌더(overlay 프론트)
- 원본 `SubtitleOverlayView`(spec 008): roll-up 여러 줄, 외곽선/글로우(클립→효과), 배경 박스, 정렬, maxLines. P3는 스타일 코어(폰트/크기/색/외곽선/줄수)부터.
- `internal/subtitle` 엔진의 `RollupLines()`/`DisplayTranslation()`을 IPC로 overlay에 전달 → JS가 DOM 렌더. `pointer-events:none`.

## 스테이징
- **P3a (PoC·최고 리스크 선검증, mac)**: `--role=overlay` Wails 앱 + mac cgo(클릭통과·screenSaver level·투명·모니터 커버) + 테스트 자막 렌더. **육안 검증**: 전체화면 위 투명 배경에 자막 표시, 마우스 클릭이 밑으로 통과. Windows 오버레이 코드는 작성하되 런타임 검증은 win 환경에서(문서화).
- **P3b (controller + IPC + 파이프라인 배선)**: controller Wails 창(제어 HUD+설정) + 트레이 + IPC로 overlay spawn/감독 + P2 reconciler·자막엔진 → overlay 자막 push. 시작/정지·입력선택·언어·표시토글.
- **P3c (자막 스타일)**: 폰트/크기/색/외곽선/글로우/배경/정렬/위치(모니터·상하)/원문표시 설정 → overlay 렌더 + 설정 영속(P5 config와 연계).

## 검증
- **P3a**: `wails build -platform darwin/universal` 성공 + 실행 시 투명 클릭통과 오버레이 육안 확인(스크린샷). `go build ./...`/`go vet` 통과. 순수 패키지 windows 크로스빌드 유지(cgo는 빌드태그 격리).
- **P3b/P3c**: 실행 시 실제 번역 자막이 오버레이에 roll-up 표시(실키). 제어 HUD로 시작/정지.

## 리스크(architect)
- win: energye/systray + Wails 공존, WebView2 투명+WS_EX_TRANSPARENT는 **win 실측 필요**(코드만 선작성).
- 모니터 좌표: `runtime.ScreenGetAll` 순서가 NSScreen과 1:1 아님 → 대상 모니터는 **cgo에서 직접 열거**.
- NSScreenSaverWindowLevel도 일부 DRM 전체화면 위엔 정책상 안 뜰 수 있음(원본 동일 한계).
