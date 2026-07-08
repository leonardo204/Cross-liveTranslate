//go:build darwin

#import <Cocoa/Cocoa.h>
#import "hudpos_darwin.h"

// 창을 제목으로 찾아 주 모니터(screens[0]) visibleFrame 우상단에 배치한다.
// AppKit은 좌하단 원점(bottom-up)이므로 top-right는 y = maxY - height - margin.
static int lt_hudpos_on_main(NSString *title, int margin) {
    NSWindow *target = nil;
    if (title != nil && [title length] > 0) {
        for (NSWindow *win in [NSApp windows]) {
            if ([[win title] isEqualToString:title]) { target = win; break; }
        }
    }
    if (target == nil) { target = [NSApp mainWindow]; }
    if (target == nil && [[NSApp windows] count] > 0) {
        target = [[NSApp windows] objectAtIndex:0];
    }
    if (target == nil) { return -1; }

    // 주 모니터(메뉴바가 있는 screens[0]). visibleFrame은 메뉴바/독을 제외한 영역.
    NSArray<NSScreen *> *screens = [NSScreen screens];
    NSScreen *primary = ([screens count] > 0) ? [screens objectAtIndex:0] : [NSScreen mainScreen];
    if (primary == nil) { return -1; }

    NSRect vf = [primary visibleFrame];
    NSRect wf = [target frame];
    CGFloat x = NSMaxX(vf) - wf.size.width - (CGFloat)margin;
    CGFloat y = NSMaxY(vf) - wf.size.height - (CGFloat)margin;
    // 방어적 클램프: 음수/영역 밖이면 visibleFrame 안으로 되돌린다.
    if (x < NSMinX(vf)) { x = NSMinX(vf); }
    if (y < NSMinY(vf)) { y = NSMinY(vf); }
    [target setFrameOrigin:NSMakePoint(x, y)];
    return 0;
}

int lt_hudpos_top_right(const char *title, int margin) {
    NSString *ns = (title != NULL) ? [NSString stringWithUTF8String:title] : nil;
    __block int rc = 0;
    if ([NSThread isMainThread]) {
        rc = lt_hudpos_on_main(ns, margin);
    } else {
        dispatch_sync(dispatch_get_main_queue(), ^{ rc = lt_hudpos_on_main(ns, margin); });
    }
    return rc;
}
