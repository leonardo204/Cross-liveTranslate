//go:build darwin

package overlay

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "overlay_darwin.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Apply stamps the subtitle-overlay attributes onto the realized NSWindow
// whose Wails title matches `title`, covering the monitor at monitorIndex.
// Intended to be called from Wails' OnDomReady.
func Apply(title string, monitorIndex int) error {
	ctitle := C.CString(title)
	defer C.free(unsafe.Pointer(ctitle))

	rc := C.lt_overlay_apply(ctitle, C.int(monitorIndex))
	if rc != 0 {
		return fmt.Errorf("overlay: no NSWindow found for title %q (rc=%d)", title, int(rc))
	}
	return nil
}
