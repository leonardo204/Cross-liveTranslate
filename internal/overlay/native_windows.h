// native_windows.h — C API for the Windows native subtitle overlay.
//
// The overlay is a frameless WS_POPUP, per-pixel-alpha layered window
// (WS_EX_LAYERED | WS_EX_TRANSPARENT | WS_EX_TOPMOST | WS_EX_TOOLWINDOW |
// WS_EX_NOACTIVATE) drawn with GDI+ into a 32-bit premultiplied ARGB DIB and
// pushed to the window via UpdateLayeredWindow (AC_SRC_ALPHA). This is the same
// approach C# WPF/WinForms use for transparent overlays and works on all of
// Windows 10/11 (unlike WebView2 per-pixel transparency, which is broken on
// Win10). All strings are UTF-8 and converted to UTF-16 inside the C++ layer.
//
// Threading: lt_native_init + lt_native_run_loop MUST run on the same OS thread
// (the Go caller locks it with runtime.LockOSThread). The lt_native_update_*
// functions are safe to call from another goroutine/thread: they mutate shared
// state under a critical section and PostMessage a repaint request to the UI
// thread (no cross-thread GDI).

#ifndef LT_NATIVE_WINDOWS_H
#define LT_NATIVE_WINDOWS_H

#ifdef __cplusplus
extern "C" {
#endif

// lt_native_init initializes GDI+, registers the window class, and creates the
// layered overlay window covering the primary monitor. Returns 0 on success,
// non-zero on failure.
int lt_native_init(void);

// lt_native_update_subtitle replaces the current caption content.
//   lines   — UTF-8, translated caption lines joined by '\n' (roll-up order).
//   source  — UTF-8, the in-progress source line ("" when hidden).
//   visible — non-zero to show the caption, 0 to clear to fully transparent.
void lt_native_update_subtitle(const char *lines, const char *source, int visible);

// lt_native_update_style replaces the full rendering style + placement. Mirrors
// ipc.StyleMsg. Colors are "#RRGGBBAA" (6/8 hex tolerated). fontSize is in px.
void lt_native_update_style(
    const char *fontFamily, double fontSize, const char *fontWeight,
    const char *textColor,
    int strokeEnabled, const char *strokeColor, double strokeWidth,
    int glowEnabled, const char *glowColor, double glowRadius,
    int bgEnabled, const char *bgColor, double bgOpacity,
    const char *align, int maxLines,
    int monitorIndex, const char *vertical, double offset);

// lt_native_run_loop runs the Win32 message loop until WM_QUIT. Blocks.
void lt_native_run_loop(void);

#ifdef __cplusplus
}
#endif

#endif // LT_NATIVE_WINDOWS_H
