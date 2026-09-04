//go:build darwin && cgo

package hudpos

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "hudpos_darwin.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// hudMargin은 화면 가장자리에서의 여백(pt). 원본 HUDController 우상단 20pt 안쪽.
const hudMargin = 20

// positionPrimaryTopRight moves the titled window to the primary screen's
// top-right via NSScreen(visibleFrame). 창을 못 찾으면 에러를 반환한다.
func positionPrimaryTopRight(title string) error {
	ctitle := C.CString(title)
	defer C.free(unsafe.Pointer(ctitle))
	if rc := C.lt_hudpos_top_right(ctitle, C.int(hudMargin)); rc != 0 {
		return fmt.Errorf("hudpos: 창 배치 실패(title=%q, rc=%d)", title, int(rc))
	}
	return nil
}

// windowOrigin reads the titled window's origin in global screen coordinates
// (AppKit 좌하단 원점). 창을 못 찾으면 에러.
func windowOrigin(title string) (int, int, error) {
	ctitle := C.CString(title)
	defer C.free(unsafe.Pointer(ctitle))
	var cx, cy C.double
	if rc := C.lt_hudpos_get_origin(ctitle, &cx, &cy); rc != 0 {
		return 0, 0, fmt.Errorf("hudpos: 창 위치 조회 실패(title=%q, rc=%d)", title, int(rc))
	}
	return int(cx), int(cy), nil
}

// setWindowOrigin restores a saved origin. 그 자리가 지금 붙어 있는 어떤 화면에도 걸치지
// 않으면 ErrOffScreen 을 돌려주고 창은 그대로 둔다.
func setWindowOrigin(title string, x, y int) error {
	ctitle := C.CString(title)
	defer C.free(unsafe.Pointer(ctitle))
	rc := C.lt_hudpos_set_origin(ctitle, C.double(x), C.double(y))
	switch int(rc) {
	case 0:
		return nil
	case -2:
		return ErrOffScreen
	default:
		return fmt.Errorf("hudpos: 창 위치 지정 실패(title=%q, rc=%d)", title, int(rc))
	}
}
