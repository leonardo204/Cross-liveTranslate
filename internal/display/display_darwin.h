#ifndef LT_DISPLAY_DARWIN_H
#define LT_DISPLAY_DARWIN_H

#ifdef __cplusplus
extern "C" {
#endif

// Enumerates connected screens in [NSScreen screens] order (the exact order
// overlay_darwin.m indexes with monitorIndex, so display Index == overlay
// monitorIndex == Position.MonitorIndex).
//
// On success returns a heap-allocated, newline-free, tab/newline-delimited
// UTF-8 string the caller must free with lt_display_free():
//
//   one record per screen, records separated by '\n':
//     "<index>\t<primary 0|1>\t<localizedName>"
//
// index 0 is treated as the primary screen (menu-bar display; the one
// "자동 (주 화면)" maps to). Returns NULL if no screens are available.
char *lt_display_list(void);

// Frees a string returned by lt_display_list().
void lt_display_free(char *s);

#ifdef __cplusplus
}
#endif

#endif // LT_DISPLAY_DARWIN_H
