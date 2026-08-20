// Package activation — macOS 앱 활성화 정책(Dock 아이콘 표시) 제어 shim.
//
// # 왜 필요한가
//
// 이 앱은 한 번들을 세 프로세스(controller/settings/overlay)가 공유하는 구조라, 기본
// 상태에서는 Dock 아이콘이 **3개** 뜬다. 원본 liveTranslate는 메뉴바(accessory) 앱이다:
//
//   - Info.plist LSUIElement=true → 평소 Dock 아이콘 0개, 트레이(NSStatusBar)로만 제어.
//   - 설정 창을 열 때만 NSApp.setActivationPolicy(.regular)로 잠시 올려 창을 앞으로
//     가져오고, 창을 닫으면 .accessory로 되돌린다(Dock 아이콘 상시 노출 방지).
//     (원본 Sources/App/SettingsWindowController.swift show()/windowWillClose)
//
// LSUIElement만으로 세 프로세스 모두 accessory가 되지만, 그 상태에서는 설정 창이 다른 앱
// 뒤에 가려 뜰 수 있다. 그래서 원본과 동일하게 **설정 창을 보여줄 때만** regular로 올린다.
//
// Wails v2.12.0의 mac.Options에는 ActivationPolicy 필드가 주석 처리(미지원)되어 있어
// 옵션으로는 지정할 수 없다. 그래서 이 패키지가 [NSApp setActivationPolicy:]를 직접 부른다.
//
// # 구현 메모
//
//	activation_darwin.go/.h/.m  darwin  (Cocoa, cgo)
//	activation_other.go         그 외    (no-op — Dock 개념이 없는 플랫폼)
//
// cgo 호출은 반드시 메인 스레드에서 실행된다(내부에서 dispatch_async(main queue)로 홉).
// 과거 트레이 콜백이 비메인 스레드에서 AppKit을 만져 SIGSEGV가 났던 전례가 있어, 이
// 패키지의 Go API는 어느 goroutine에서 불러도 안전하도록 만들었다.
package activation

// Policy 는 macOS 앱 활성화 정책이다.
type Policy int

const (
	// Regular 는 Dock 아이콘 + 메뉴바를 가진 일반 앱(NSApplicationActivationPolicyRegular).
	Regular Policy = 0
	// Accessory 는 Dock 아이콘 없는 보조 앱(NSApplicationActivationPolicyAccessory).
	// LSUIElement=true인 번들의 기본 상태다.
	Accessory Policy = 1
)

func (p Policy) String() string {
	if p == Regular {
		return "regular"
	}
	return "accessory"
}
