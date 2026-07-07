// Package display — 연결된 모니터(화면) 열거 shim(플랫폼별).
//
// 설정 창의 자막 "표시 화면" Picker가 "화면 1/2" 같은 인덱스 라벨 대신 실제
// 모니터 이름(예: "Built-in Retina Display", "LG UltraFine")을 보여주기 위해
// OS에서 화면 목록을 읽어 온다.
//
// 결정적으로 중요한 불변식(overlay 패키지와의 정합):
//
//	display.ListScreens()가 매기는 Index == overlay.Apply(title, monitorIndex)가
//	쓰는 monitorIndex == config.Settings.Position.MonitorIndex.
//
// 세 값이 같은 순서(플랫폼 네이티브 화면 배열)를 가리켜야 사용자가 고른 화면에
// 자막 오버레이가 올바르게 뜬다. macOS에서 이 순서는 `[NSScreen screens]`이며
// (overlay_darwin.m과 동일), Windows에서는 `EnumDisplayMonitors` 순서다.
//
// 구현은 GOOS별로 컴파일된다:
//
//	display_darwin.go/.h/.m   darwin  (Cocoa, cgo — [NSScreen screens].localizedName)
//	display_windows.go        windows (스텁 — 실측 대기)
//	display_other.go          그 외    (빈 목록 스텁)
package display

// ScreenInfo describes one connected monitor for the settings picker.
//
// Index는 플랫폼 네이티브 화면 배열의 위치이며 overlay.Apply의 monitorIndex 및
// config Position.MonitorIndex와 동일한 값이다(위 불변식). Primary는 주 화면
// (macOS: [NSScreen screens][0] — 메뉴바가 있는 화면, "자동(주 화면)"이 가리키는
// 인덱스 0) 여부다.
type ScreenInfo struct {
	Index   int    `json:"index"`   // [NSScreen screens]/EnumDisplayMonitors 순서(overlay와 동일).
	Name    string `json:"name"`    // 사람이 읽는 화면 이름(localizedName 등).
	Primary bool   `json:"primary"` // 주 화면(인덱스 0) 여부 → UI에서 " (주 화면)" 접미.
}
