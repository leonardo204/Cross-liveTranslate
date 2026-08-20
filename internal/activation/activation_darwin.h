#ifndef LT_ACTIVATION_DARWIN_H
#define LT_ACTIVATION_DARWIN_H

#ifdef __cplusplus
extern "C" {
#endif

// lt_activation_set switches the running app's activation policy.
//   policy 0 = NSApplicationActivationPolicyRegular   (Dock 아이콘 표시)
//   policy 1 = NSApplicationActivationPolicyAccessory (Dock 아이콘 숨김)
//
// regular로 올릴 때는 원본 SettingsWindowController.show()와 동일하게 앱을 unhide +
// activate 해 창이 다른 앱 뒤에 가리지 않게 한다(accessory 앱은 그냥 창을 띄우면 뒤에
// 깔릴 수 있다). Hide-on-close(X 버튼)로 [NSApp hide:]된 상태에서 다시 열 때도 이
// unhide가 있어야 창이 보인다.
//
// AppKit 호출은 메인 스레드에서만 안전하므로, 비메인 스레드에서 부르면 메인 큐로
// 비동기 홉(dispatch_async)한다 — 호출자를 블로킹하지 않고 데드락 위험도 없다.
void lt_activation_set(int policy);

// lt_activation_watch_hide registers an observer for
// NSApplicationDidHideNotification and drops the app back to accessory when it
// fires. Wails의 HideWindowOnClose 경로는 창 닫기(X)에서 [NSApp hide:nil]만 부르고
// Go 콜백을 주지 않기 때문에(WindowDelegate.m windowShouldClose), 그 알림을 직접 듣는
// 것이 원본 windowWillClose(→ .accessory 복귀)에 대응하는 유일한 훅이다.
// 멱등이다(중복 호출해도 옵저버는 하나).
void lt_activation_watch_hide(void);

#ifdef __cplusplus
}
#endif

#endif // LT_ACTIVATION_DARWIN_H
