// +build darwin
#import <Cocoa/Cocoa.h>
#import "overlay_darwin.h"

// Locates the target NSWindow by title (falling back to mainWindow / first
// window) and stamps the subtitle-overlay attributes. Must touch AppKit on the
// main thread.
static int lt_overlay_apply_on_main(NSString *title, int monitorIndex) {
    NSWindow *target = nil;

    // Prefer an exact title match among the app's windows.
    if (title != nil && [title length] > 0) {
        for (NSWindow *win in [NSApp windows]) {
            if ([[win title] isEqualToString:title]) {
                target = win;
                break;
            }
        }
    }
    // Fallbacks: key/main window, then the first realized window.
    if (target == nil) {
        target = [NSApp mainWindow];
    }
    if (target == nil && [[NSApp windows] count] > 0) {
        target = [[NSApp windows] objectAtIndex:0];
    }
    if (target == nil) {
        return -1;
    }

    // Above floating panels and full-screen video (matches .screenSaver).
    [target setLevel:NSScreenSaverWindowLevel];

    // Visible on every Space + over other apps' full-screen, and pinned so
    // Mission Control / Exposé won't drag it away.
    [target setCollectionBehavior:(NSWindowCollectionBehaviorFullScreenAuxiliary |
                                   NSWindowCollectionBehaviorCanJoinAllSpaces |
                                   NSWindowCollectionBehaviorStationary)];

    // Click-through: the overlay never intercepts mouse events.
    [target setIgnoresMouseEvents:YES];

    // Transparent surface — only the subtitle text paints.
    [target setOpaque:NO];
    [target setBackgroundColor:[NSColor clearColor]];
    [target setHasShadow:NO];

    // Cover the requested monitor's full frame (menu-bar included).
    NSArray<NSScreen *> *screens = [NSScreen screens];
    NSScreen *screen = nil;
    if (monitorIndex >= 0 && (NSUInteger)monitorIndex < [screens count]) {
        screen = [screens objectAtIndex:(NSUInteger)monitorIndex];
    } else {
        screen = [NSScreen mainScreen];
    }
    if (screen != nil) {
        [target setFrame:[screen frame] display:YES];
    }

    return 0;
}

int lt_overlay_apply(const char *title, int monitorIndex) {
    NSString *ns = (title != NULL)
        ? [NSString stringWithUTF8String:title]
        : nil;

    __block int rc = 0;
    if ([NSThread isMainThread]) {
        rc = lt_overlay_apply_on_main(ns, monitorIndex);
    } else {
        // OnDomReady runs off the main thread; the main run loop is live, so a
        // synchronous hop is safe and lets us return the real result code.
        dispatch_sync(dispatch_get_main_queue(), ^{
            rc = lt_overlay_apply_on_main(ns, monitorIndex);
        });
    }
    return rc;
}
