//go:build darwin
#import <Cocoa/Cocoa.h>
#import "tray_darwin.h"

// Go-exported callbacks (see tray_darwin.go). Invoked on the main thread from
// the menu action target.
extern void lt_tray_go_toggle_translate(void);
extern void lt_tray_go_toggle_hud(void);
extern void lt_tray_go_settings(void);
extern void lt_tray_go_quit(void);

// Target object for menu items. A single retained instance owns the status item
// so ARC does not release it while the app runs. It also retains the two
// dynamic items(번역 토글 / 제어 HUD 표시) so their title/state can be updated.
@interface LTTrayTarget : NSObject
@property(nonatomic, strong) NSStatusItem *statusItem;
@property(nonatomic, strong) NSMenuItem *toggleItem; // 번역 시작/정지
@property(nonatomic, strong) NSMenuItem *hudItem;    // ✓ 제어 HUD 표시
- (void)onToggleTranslate:(id)sender;
- (void)onToggleHUD:(id)sender;
- (void)onSettings:(id)sender;
- (void)onQuit:(id)sender;
@end

@implementation LTTrayTarget
- (void)onToggleTranslate:(id)sender { lt_tray_go_toggle_translate(); }
- (void)onToggleHUD:(id)sender { lt_tray_go_toggle_hud(); }
- (void)onSettings:(id)sender { lt_tray_go_settings(); }
- (void)onQuit:(id)sender { lt_tray_go_quit(); }
@end

// Single global target (one tray per process).
static LTTrayTarget *gTrayTarget = nil;

void lt_tray_install(const char *ctitle) {
    NSString *title = ctitle ? [NSString stringWithUTF8String:ctitle] : @"liveTranslate";
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

        // 번역 시작 ↔ 번역 정지 (isRunning에 따라 라벨 동적 — lt_tray_set_running).
        NSMenuItem *toggle = [[NSMenuItem alloc] initWithTitle:@"번역 시작"
                                                        action:@selector(onToggleTranslate:)
                                                 keyEquivalent:@""];
        toggle.target = target;
        [menu addItem:toggle];

        [menu addItem:[NSMenuItem separatorItem]];

        // ✓ 제어 HUD 표시 (표시 상태를 .state 체크 표식으로 — lt_tray_set_hud_visible).
        NSMenuItem *hud = [[NSMenuItem alloc] initWithTitle:@"제어 HUD 표시"
                                                     action:@selector(onToggleHUD:)
                                              keyEquivalent:@""];
        hud.target = target;
        hud.state = NSControlStateValueOn; // 시작 시 HUD 표시 상태.
        [menu addItem:hud];

        // 설정… ⌘,
        NSMenuItem *settings = [[NSMenuItem alloc] initWithTitle:@"설정…"
                                                          action:@selector(onSettings:)
                                                   keyEquivalent:@","];
        settings.target = target;
        [menu addItem:settings];

        [menu addItem:[NSMenuItem separatorItem]];

        // 종료 ⌘Q
        NSMenuItem *quit = [[NSMenuItem alloc] initWithTitle:@"종료"
                                                      action:@selector(onQuit:)
                                               keyEquivalent:@"q"];
        quit.target = target;
        [menu addItem:quit];

        item.menu = menu;
        target.statusItem = item;
        target.toggleItem = toggle;
        target.hudItem = hud;
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

void lt_tray_set_running(int running) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (gTrayTarget == nil) {
            return;
        }
        gTrayTarget.toggleItem.title = running ? @"번역 정지" : @"번역 시작";
    });
}

void lt_tray_set_hud_visible(int visible) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (gTrayTarget == nil) {
            return;
        }
        gTrayTarget.hudItem.state = visible ? NSControlStateValueOn : NSControlStateValueOff;
    });
}
