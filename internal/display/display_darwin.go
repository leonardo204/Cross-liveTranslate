//go:build darwin

package display

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "display_darwin.h"
*/
import "C"

import (
	"strconv"
	"strings"
)

// ListScreens returns connected monitors in [NSScreen screens] order, matching
// overlay_darwin.m's monitorIndex indexing exactly (display Index == overlay
// monitorIndex == Position.MonitorIndex). Index 0 is the primary (menu-bar)
// screen that "자동 (주 화면)" maps to.
func ListScreens() ([]ScreenInfo, error) {
	c := C.lt_display_list()
	if c == nil {
		return []ScreenInfo{}, nil
	}
	defer C.lt_display_free(c)

	raw := C.GoString(c)
	if raw == "" {
		return []ScreenInfo{}, nil
	}

	var screens []ScreenInfo
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		// Record layout: "<index>\t<primary 0|1>\t<name>". SplitN keeps tabs in
		// the name intact (there shouldn't be any — the .m strips them).
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		idx, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		screens = append(screens, ScreenInfo{
			Index:   idx,
			Name:    fields[2],
			Primary: fields[1] == "1",
		})
	}
	if screens == nil {
		screens = []ScreenInfo{}
	}
	return screens, nil
}
