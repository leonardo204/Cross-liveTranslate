//go:build darwin
#import <Cocoa/Cocoa.h>
#import "tray_darwin.h"

// Go-exported callbacks (see tray_darwin.go). Invoked on the main thread from
// the menu action target.
extern void lt_tray_go_start(void);
extern void lt_tray_go_stop(void);
extern void lt_tray_go_show(void);
extern void lt_tray_go_quit(void);

// Target object for menu items. A single retained instance owns the status item
// so ARC does not release it while the app runs.
@interface LTTrayTarget : NSObject
@property(nonatomic, strong) NSStatusItem *statusItem;
- (void)onStart:(id)sender;
- (void)onStop:(id)sender;
- (void)onShow:(id)sender;
- (void)onQuit:(id)sender;
@end

@implementation LTTrayTarget
- (void)onStart:(id)sender { lt_tray_go_start(); }
- (void)onStop:(id)sender { lt_tray_go_stop(); }
- (void)onShow:(id)sender { lt_tray_go_show(); }
- (void)onQuit:(id)sender { lt_tray_go_quit(); }
@end

// Single global target (one tray per process).
static LTTrayTarget *gTrayTarget = nil;

void lt_tray_install(const char *ctitle) {
    NSString *title = ctitle ? [NSString stringWithUTF8String:ctitle] : @"LiveTranslate";
    dispatch_async(dispatch_get_main_queue(), ^{
        if (gTrayTarget != nil) {
            return; // already installed
        }
        LTTrayTarget *target = [[LTTrayTarget alloc] init];

        NSStatusItem *item =
            [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
        item.button.title = @"LT";
        item.button.toolTip = title;

        NSMenu *menu = [[NSMenu alloc] initWithTitle:title];

        NSMenuItem *start = [[NSMenuItem alloc] initWithTitle:@"Start"
                                                       action:@selector(onStart:)
                                                keyEquivalent:@""];
        start.target = target;
        [menu addItem:start];

        NSMenuItem *stop = [[NSMenuItem alloc] initWithTitle:@"Stop"
                                                      action:@selector(onStop:)
                                               keyEquivalent:@""];
        stop.target = target;
        [menu addItem:stop];

        [menu addItem:[NSMenuItem separatorItem]];

        NSMenuItem *show = [[NSMenuItem alloc] initWithTitle:@"Show HUD"
                                                      action:@selector(onShow:)
                                               keyEquivalent:@""];
        show.target = target;
        [menu addItem:show];

        [menu addItem:[NSMenuItem separatorItem]];

        NSMenuItem *quit = [[NSMenuItem alloc] initWithTitle:@"Quit"
                                                      action:@selector(onQuit:)
                                               keyEquivalent:@"q"];
        quit.target = target;
        [menu addItem:quit];

        item.menu = menu;
        target.statusItem = item;
        gTrayTarget = target; // retained globally
    });
}

void lt_tray_set_status(const char *ctext) {
    NSString *text = ctext ? [NSString stringWithUTF8String:ctext] : @"";
    dispatch_async(dispatch_get_main_queue(), ^{
        if (gTrayTarget == nil) {
            return;
        }
        gTrayTarget.statusItem.button.toolTip = text;
    });
}
