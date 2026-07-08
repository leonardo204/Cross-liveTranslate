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
