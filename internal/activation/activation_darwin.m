//go:build darwin

#import <Cocoa/Cocoa.h>
#import "activation_darwin.h"

// lt_activation_apply_on_main must run on the main thread (AppKit 규칙).
static void lt_activation_apply_on_main(int policy) {
    if (policy == 0) {
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        // 원본 SettingsWindowController.show(): 정책을 올린 뒤 앱을 앞으로.
        // hide-on-close로 [NSApp hide:]된 상태였다면 unhide가 있어야 창이 다시 보인다
        // (unhide는 숨김 상태가 아니면 무해한 no-op).
        [NSApp unhide:nil];
        [NSApp activateIgnoringOtherApps:YES];
    } else {
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
    }
}

void lt_activation_set(int policy) {
    if ([NSThread isMainThread]) {
        lt_activation_apply_on_main(policy);
        return;
    }
    // 비메인 스레드(예: stdin control 루프 goroutine) → 메인 큐로 비동기 홉.
    // dispatch_sync를 쓰면 메인 스레드가 이 goroutine을 기다리는 상황에서 데드락이
    // 날 수 있어 async로 던진다(반환값이 필요 없는 호출이라 손해가 없다).
    dispatch_async(dispatch_get_main_queue(), ^{
        lt_activation_apply_on_main(policy);
    });
}

void lt_activation_watch_hide(void) {
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        [[NSNotificationCenter defaultCenter]
            addObserverForName:NSApplicationDidHideNotification
                        object:nil
                         queue:[NSOperationQueue mainQueue]
                    usingBlock:^(NSNotification *note) {
                        // 창 닫기(X)/Cmd+H 로 앱이 숨겨졌다 → 보이는 창이 없으므로
                        // Dock 아이콘을 내린다(원본 windowWillClose → .accessory).
                        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
                    }];
    });
}
