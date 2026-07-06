#ifndef LT_TRAY_DARWIN_H
#define LT_TRAY_DARWIN_H

#ifdef __cplusplus
extern "C" {
#endif

// Installs an NSStatusBar item with a menu (Start/Stop, Show HUD, Quit).
// Safe to call from any goroutine; work is dispatched to the main queue so it
// coexists with the Wails-owned NSApp run loop. Menu selections are routed back
// to Go via the exported lt_tray_on_* callbacks.
void lt_tray_install(const char *title);

// Updates the status-bar item's tooltip/title text (main-queue dispatched).
void lt_tray_set_status(const char *text);

#ifdef __cplusplus
}
#endif

#endif
