// Package tray — 시스템 트레이(mac 메뉴바 NSStatusBar / win systray / other stub).
//
// controller 프로세스가 Init(Handlers)로 상태바 아이콘 + 메뉴(Start/Stop, Show HUD,
// Quit)를 설치한다. 메뉴 콜백은 네이티브(메인 스레드)에서 Go Handlers로 브릿지된다.
//
// 플랫폼 격리(빌드태그):
//
//	tray_darwin.go/.h/.m   darwin  — NSStatusBar(cgo). Wails의 NSApp 런루프와 공존
//	                                  (별도 런루프 없이 main 큐에 status item 부착).
//	tray_windows.go        windows — 최소 stub(실측 대기; systray는 별도 런루프라 보류).
//	tray_other.go          그 외    — stub.
//
// 이 파일(순수)은 공용 타입만 정의해 모든 플랫폼에서 공유한다.
package tray

// Handlers holds the menu action callbacks invoked from the native tray.
// 각 콜백은 nil일 수 있다(해당 메뉴 항목 비활성). 콜백은 네이티브 스레드에서
// 호출될 수 있으므로 구현은 짧게 유지하고 무거운 작업은 위임한다.
type Handlers struct {
	OnStart   func()
	OnStop    func()
	OnShowHUD func()
	OnQuit    func()
}

// handlers is the process-global callback set (single tray per process).
var handlers Handlers
