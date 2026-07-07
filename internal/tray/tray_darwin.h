#ifndef LT_TRAY_DARWIN_H
#define LT_TRAY_DARWIN_H

#ifdef __cplusplus
extern "C" {
#endif

// Installs an NSStatusBar item with the 원본 메뉴(번역 시작/정지, 제어 HUD 표시,
// 설정…, 종료). Safe to call from any goroutine; work is dispatched to the main
// queue so it coexists with the Wails-owned NSApp run loop. Menu selections are
// routed back to Go via the exported lt_tray_go_* callbacks.
void lt_tray_install(const char *title);

// Updates the status-bar item's tooltip text (main-queue dispatched).
void lt_tray_set_status(const char *text);

// Updates the 번역 시작/정지 menu item title (running!=0 → "번역 정지").
void lt_tray_set_running(int running);

// Updates the 제어 HUD 표시 menu item check mark (visible!=0 → ✓).
void lt_tray_set_hud_visible(int visible);

#ifdef __cplusplus
}
#endif

#endif
