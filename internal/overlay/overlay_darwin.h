#ifndef LT_OVERLAY_DARWIN_H
#define LT_OVERLAY_DARWIN_H

#ifdef __cplusplus
extern "C" {
#endif

// Applies subtitle-overlay attributes to the NSWindow whose title matches
// `title` (UTF-8). Falls back to [NSApp mainWindow] / the first window when no
// title matches. `monitorIndex` selects the NSScreen to cover; out-of-range
// values fall back to the main screen.
//
// Attributes applied (mirrors liveTranslate SubtitleOverlayWindow):
//   level                = NSScreenSaverWindowLevel
//   collectionBehavior   = FullScreenAuxiliary | CanJoinAllSpaces | Stationary
//   ignoresMouseEvents   = YES   (click-through)
//   opaque               = NO
//   backgroundColor      = clearColor
//   hasShadow            = NO
//   frame                = [NSScreen screens][monitorIndex].frame
//
// Work is marshalled onto the main queue. Returns 0 on success (window found),
// -1 when no NSWindow could be located.
int lt_overlay_apply(const char *title, int monitorIndex);

#ifdef __cplusplus
}
#endif

#endif // LT_OVERLAY_DARWIN_H
