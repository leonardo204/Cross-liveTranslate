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

// lt_hudpos_get_origin reads the titled window's frame origin in **global screen
// coordinates**(AppKit 좌하단 원점). 성공 0, 창 미발견 -1.
int lt_hudpos_get_origin(const char *title, double *outX, double *outY);

// lt_hudpos_set_origin moves the titled window to (x, y) in the same coordinate
// space as lt_hudpos_get_origin. 저장한 자리가 지금 붙어 있는 어떤 화면에도 걸치지
// 않으면(모니터를 뺐을 때) 아무것도 하지 않고 -2를 돌려준다 — 창을 화면 밖에 두지 않기
// 위해서다. 성공 0, 창 미발견 -1.
int lt_hudpos_set_origin(const char *title, double x, double y);

#ifdef __cplusplus
}
#endif

#endif // LT_HUDPOS_DARWIN_H
