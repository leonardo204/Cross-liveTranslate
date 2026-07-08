#ifndef LT_HUDPOS_DARWIN_H
#define LT_HUDPOS_DARWIN_H

#ifdef __cplusplus
extern "C" {
#endif

// lt_hudpos_top_right finds the NSWindow with the given title and moves it to
// the PRIMARY screen(NSScreen.screens[0])의 visibleFrame 우상단(메뉴바/독 제외)에
// 배치한다. 창 크기는 유지하고 원점만 이동한다. 성공 0, 창 미발견 -1.
// AppKit 접근이므로 메인 스레드에서 수행한다(비메인 호출은 dispatch_sync hop).
int lt_hudpos_top_right(const char *title, int margin);

#ifdef __cplusplus
}
#endif

#endif // LT_HUDPOS_DARWIN_H
