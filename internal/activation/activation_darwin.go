//go:build darwin

package activation

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include "activation_darwin.h"
*/
import "C"

// Set switches the app's activation policy (Dock 아이콘 표시 여부).
// 어느 goroutine에서 불러도 안전하다 — AppKit 호출은 내부에서 메인 큐로 홉한다.
// Regular로 올릴 때는 앱을 unhide + activate 해 창이 앞으로 나오게 한다(원본 동작).
func Set(p Policy) {
	C.lt_activation_set(C.int(p))
}

// WatchHide 는 앱이 숨겨질 때(창 닫기 X / Cmd+H) 자동으로 Accessory로 되돌리는 옵저버를
// 건다. Wails의 HideWindowOnClose는 창 닫기에서 [NSApp hide:]만 호출하고 Go 콜백을 주지
// 않으므로, 원본의 windowWillClose(→ .accessory 복귀)를 재현하려면 이 훅이 필요하다.
// 멱등이며 설정 프로세스 시작 시 1회 호출하면 된다.
func WatchHide() {
	C.lt_activation_watch_hide()
}
