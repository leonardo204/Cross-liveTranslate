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

// 제목으로 창을 찾는다(없으면 mainWindow → 첫 창 순으로 폴백). 위 배치 함수와 같은 규칙이다.
static NSWindow *lt_hudpos_find(NSString *title) {
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
    return target;
}

static int lt_hudpos_get_on_main(NSString *title, double *outX, double *outY) {
    NSWindow *target = lt_hudpos_find(title);
    if (target == nil) { return -1; }
    NSRect wf = [target frame];
    if (outX != NULL) { *outX = (double)wf.origin.x; }
    if (outY != NULL) { *outY = (double)wf.origin.y; }
    return 0;
}

// 저장한 자리를 되돌린다. 그 자리를 담고 있는 화면이 하나도 없으면(모니터 분리) 건드리지
// 않는다 — 창이 보이지 않는 곳에 놓이면 트레이 말고는 되찾을 방법이 없다.
static int lt_hudpos_set_on_main(NSString *title, double x, double y) {
    NSWindow *target = lt_hudpos_find(title);
    if (target == nil) { return -1; }

    NSRect wf = [target frame];
    NSRect candidate = NSMakeRect((CGFloat)x, (CGFloat)y, wf.size.width, wf.size.height);

    BOOL onScreen = NO;
    for (NSScreen *screen in [NSScreen screens]) {
        if (NSIntersectsRect([screen visibleFrame], candidate)) { onScreen = YES; break; }
    }
    if (!onScreen) { return -2; }

    [target setFrameOrigin:NSMakePoint((CGFloat)x, (CGFloat)y)];
    return 0;
}

int lt_hudpos_get_origin(const char *title, double *outX, double *outY) {
    NSString *ns = (title != NULL) ? [NSString stringWithUTF8String:title] : nil;
    __block int rc = 0;
    if ([NSThread isMainThread]) {
        rc = lt_hudpos_get_on_main(ns, outX, outY);
    } else {
        dispatch_sync(dispatch_get_main_queue(), ^{ rc = lt_hudpos_get_on_main(ns, outX, outY); });
    }
    return rc;
}

int lt_hudpos_set_origin(const char *title, double x, double y) {
    NSString *ns = (title != NULL) ? [NSString stringWithUTF8String:title] : nil;
    __block int rc = 0;
    if ([NSThread isMainThread]) {
        rc = lt_hudpos_set_on_main(ns, x, y);
    } else {
        dispatch_sync(dispatch_get_main_queue(), ^{ rc = lt_hudpos_set_on_main(ns, x, y); });
    }
    return rc;
}
