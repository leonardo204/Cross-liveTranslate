// native_windows.cpp — Win32 layered-window subtitle overlay with GDI+.
//
// Rendering model (per-pixel alpha, the crux):
//   1. Draw the caption with GDI+ (SmoothingModeAntiAlias) into a 32-bit
//      *premultiplied* ARGB DIB section (top-down, BI_RGB). We wrap the DIB
//      bits in a Gdiplus::Bitmap with PixelFormat32bppPARGB so GDI+ writes
//      premultiplied pixels directly into the DIB memory.
//   2. Push the DIB to the window with UpdateLayeredWindow + BLENDFUNCTION
//      { AC_SRC_OVER, 255, AC_SRC_ALPHA }. This is per-pixel alpha — every
//      pixel carries its own alpha, so anti-aliased text edges and the
//      translucent background box blend correctly over the desktop.
//
// We deliberately do NOT use SetLayeredWindowAttributes(LWA_ALPHA): that is a
// single uniform alpha for the whole window and would render the overlay as an
// opaque/black rectangle (the exact Win10 failure this file works around).
//
// Text is drawn via Gdiplus::GraphicsPath (AddString) + FillPath, with an
// optional Pen pass for the outline. Path filling with anti-aliasing yields
// correct premultiplied alpha regardless of the text-rendering hint, which is
// the reliable way to get clean glyph alpha on a layered window.

#include <windows.h>
#include <gdiplus.h>
#include <string>
#include <vector>

#include "native_windows.h"

using namespace Gdiplus;

// LT_WM_REPAINT is posted by the update functions (any thread) to ask the UI
// thread to re-render. WM_APP is the first message id safe for app use.
#define LT_WM_REPAINT (WM_APP + 1)

static const wchar_t *kClassName = L"LiveTranslateNativeOverlay";

// ---------------------------------------------------------------------------
// Shared render state (guarded by g_cs).
// ---------------------------------------------------------------------------

struct State {
    // content
    std::wstring lines;   // translated lines joined by '\n'
    std::wstring source;  // in-progress source line
    bool visible;

    // style (mirrors ipc.StyleMsg)
    std::wstring fontFamily;
    double fontSize;      // px
    std::wstring fontWeight;
    std::wstring textColor;
    bool strokeEnabled;
    std::wstring strokeColor;
    double strokeWidth;
    bool glowEnabled;
    std::wstring glowColor;
    double glowRadius;
    bool bgEnabled;
    std::wstring bgColor;
    double bgOpacity;
    std::wstring align;    // leading|center|trailing
    int maxLines;
    int monitorIndex;
    std::wstring vertical; // top|middle|bottom
    double offset;         // 0..1
};

static CRITICAL_SECTION g_cs;
static State g_state;
static HWND g_hwnd = NULL;
static ULONG_PTR g_gdiplusToken = 0;

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

static std::wstring utf8to16(const char *s) {
    if (s == NULL || s[0] == '\0') return std::wstring();
    int need = MultiByteToWideChar(CP_UTF8, 0, s, -1, NULL, 0);
    if (need <= 0) return std::wstring();
    std::wstring w;
    w.resize(need - 1); // exclude terminating NUL
    MultiByteToWideChar(CP_UTF8, 0, s, -1, &w[0], need);
    return w;
}

static double clamp01(double v) {
    if (v < 0.0) return 0.0;
    if (v > 1.0) return 1.0;
    return v;
}

// hexNibble returns 0..15 for a hex digit, or -1 if not a hex digit.
static int hexNibble(wchar_t c) {
    if (c >= L'0' && c <= L'9') return c - L'0';
    if (c >= L'a' && c <= L'f') return 10 + (c - L'a');
    if (c >= L'A' && c <= L'F') return 10 + (c - L'A');
    return -1;
}

// parseColorARGB parses "#RRGGBBAA" (or "#RRGGBB", '#' optional) into an
// 0xAARRGGBB value suitable for Gdiplus::Color(ARGB). Falls back to def.
static DWORD parseColorARGB(const std::wstring &in, DWORD def) {
    std::wstring s = in;
    // trim leading '#'
    size_t start = 0;
    while (start < s.size() && (s[start] == L'#' || s[start] == L' ')) start++;
    s = s.substr(start);
    if (s.size() != 6 && s.size() != 8) return def;
    int v[8];
    for (size_t i = 0; i < s.size(); i++) {
        int n = hexNibble(s[i]);
        if (n < 0) return def;
        v[i] = n;
    }
    int r = v[0] * 16 + v[1];
    int g = v[2] * 16 + v[3];
    int b = v[4] * 16 + v[5];
    int a = (s.size() == 8) ? (v[6] * 16 + v[7]) : 255;
    return ((DWORD)a << 24) | ((DWORD)r << 16) | ((DWORD)g << 8) | (DWORD)b;
}

static bool isBoldWeight(const std::wstring &w) {
    // GDI+ FontStyle only has Regular/Bold; map semibold+ to bold.
    return w == L"semibold" || w == L"bold" || w == L"heavy" || w == L"black";
}

// areaIndex: top=0, middle=1, bottom=2 (default bottom). Mirrors the web renderer.
static int areaIndex(const std::wstring &v) {
    if (v == L"top") return 0;
    if (v == L"middle") return 1;
    return 2;
}

static std::vector<std::wstring> splitLines(const std::wstring &s) {
    std::vector<std::wstring> out;
    if (s.empty()) return out;
    std::wstring cur;
    for (size_t i = 0; i < s.size(); i++) {
        if (s[i] == L'\n') {
            out.push_back(cur);
            cur.clear();
        } else {
            cur.push_back(s[i]);
        }
    }
    out.push_back(cur);
    return out;
}

// makeFamily returns a heap FontFamily for the requested name, falling back to
// "Segoe UI" then the generic sans-serif family. Caller must delete it.
static FontFamily *makeFamily(const std::wstring &name) {
    std::wstring n = name.empty() ? std::wstring(L"Segoe UI") : name;
    FontFamily *ff = new FontFamily(n.c_str());
    if (ff->GetLastStatus() == Ok && ff->IsAvailable()) return ff;
    delete ff;
    ff = new FontFamily(L"Segoe UI");
    if (ff->GetLastStatus() == Ok && ff->IsAvailable()) return ff;
    delete ff;
    return FontFamily::GenericSansSerif()->Clone();
}

// ---------------------------------------------------------------------------
// Monitor enumeration (same order as EnumDisplayMonitors, matching the
// monitorIndex mapping used elsewhere in the app).
// ---------------------------------------------------------------------------

static BOOL CALLBACK monEnumProc(HMONITOR hMon, HDC, LPRECT, LPARAM lp) {
    MONITORINFO mi;
    mi.cbSize = sizeof(mi);
    if (GetMonitorInfo(hMon, &mi)) {
        std::vector<RECT> *v = reinterpret_cast<std::vector<RECT> *>(lp);
        v->push_back(mi.rcMonitor);
    }
    return TRUE;
}

static bool getMonitorRect(int index, RECT *out) {
    std::vector<RECT> rects;
    EnumDisplayMonitors(NULL, NULL, monEnumProc, reinterpret_cast<LPARAM>(&rects));
    if (rects.empty()) {
        // No monitors enumerated (should not happen): use the virtual screen.
        out->left = GetSystemMetrics(SM_XVIRTUALSCREEN);
        out->top = GetSystemMetrics(SM_YVIRTUALSCREEN);
        out->right = out->left + GetSystemMetrics(SM_CXVIRTUALSCREEN);
        out->bottom = out->top + GetSystemMetrics(SM_CYVIRTUALSCREEN);
        return true;
    }
    if (index < 0 || index >= (int)rects.size()) index = 0;
    *out = rects[index];
    return true;
}

// ---------------------------------------------------------------------------
// Drawing.
// ---------------------------------------------------------------------------

// roundRect builds a rounded-rectangle path (radius r) into path.
static void roundRect(GraphicsPath &path, REAL x, REAL y, REAL w, REAL h, REAL r) {
    if (r * 2 > w) r = w / 2;
    if (r * 2 > h) r = h / 2;
    REAL d = r * 2;
    path.StartFigure();
    path.AddArc(x, y, d, d, 180, 90);              // top-left
    path.AddArc(x + w - d, y, d, d, 270, 90);      // top-right
    path.AddArc(x + w - d, y + h - d, d, d, 0, 90); // bottom-right
    path.AddArc(x, y + h - d, d, d, 90, 90);        // bottom-left
    path.CloseFigure();
}

// drawTextLine paints one caption line at (x, y) with glow → stroke → fill.
static void drawTextLine(Graphics &g, const std::wstring &text, REAL x, REAL y,
                         REAL size, FontFamily *ff, INT fontStyle,
                         const State &st, bool isSource) {
    StringFormat fmt(StringFormat::GenericTypographic());
    GraphicsPath path;
    path.AddString(text.c_str(), -1, ff, fontStyle, size, PointF(x, y), &fmt);

    // Glow (approximation): a wide, translucent round pen behind the glyphs.
    // TODO: a true Gaussian blur would match the CSS blur() glow more closely;
    // this outer-halo approximation is cheap and visually close enough.
    if (st.glowEnabled && st.glowRadius > 0) {
        DWORD c = parseColorARGB(st.glowColor, 0xFF00E5FF);
        Pen glowPen(Color(c), (REAL)(st.glowRadius * 2.0));
        glowPen.SetLineJoin(LineJoinRound);
        g.DrawPath(&glowPen, &path);
    }

    // Stroke (outline). Pen straddles the path edge, so use 2× width and draw
    // before the fill so the inner half is covered by the glyph body.
    if (st.strokeEnabled && st.strokeWidth > 0) {
        DWORD c = parseColorARGB(st.strokeColor, 0xE6000000);
        Pen pen(Color(c), (REAL)(st.strokeWidth * 2.0));
        pen.SetLineJoin(LineJoinRound);
        g.DrawPath(&pen, &path);
    }

    // Fill (glyph body). Source line is dimmed to 0.85 alpha (원본 .line.source).
    DWORD c = parseColorARGB(st.textColor, 0xFFFFFFFF);
    if (isSource) {
        BYTE a = (BYTE)(((c >> 24) & 0xFF) * 0.85);
        c = (c & 0x00FFFFFF) | ((DWORD)a << 24);
    }
    Color fillColor(c);
    SolidBrush brush(fillColor);
    g.FillPath(&brush, &path);
}

// drawContent lays out and paints the caption box + lines for state st into a
// window of size w×h. Mirrors frontend/overlay/index.html layout.
static void drawContent(Graphics &g, const State &st, int w, int h) {
    // Build the display item list: last maxLines translated lines, then the
    // source line (0.65× size) below, matching the web renderer.
    struct Item { std::wstring text; REAL size; bool isSource; };
    std::vector<Item> items;

    std::vector<std::wstring> tls = splitLines(st.lines);
    int maxL = st.maxLines > 0 ? st.maxLines : 1;
    int startIdx = (int)tls.size() > maxL ? (int)tls.size() - maxL : 0;
    for (int i = startIdx; i < (int)tls.size(); i++) {
        if (tls[i].empty()) continue;
        Item it;
        it.text = tls[i];
        it.size = (REAL)st.fontSize;
        it.isSource = false;
        items.push_back(it);
    }
    if (!st.source.empty()) {
        Item it;
        it.text = st.source;
        it.size = (REAL)(st.fontSize * 0.65);
        it.isSource = true;
        items.push_back(it);
    }
    if (items.empty()) return;

    INT fontStyle = isBoldWeight(st.fontWeight) ? FontStyleBold : FontStyleRegular;
    FontFamily *ff = makeFamily(st.fontFamily);

    StringFormat mfmt(StringFormat::GenericTypographic());
    mfmt.SetFormatFlags(mfmt.GetFormatFlags() | StringFormatFlagsMeasureTrailingSpaces);

    // Measure each line's width; line height mirrors the web's line-height 1.25.
    std::vector<REAL> widths(items.size(), 0);
    std::vector<REAL> heights(items.size(), 0);
    REAL maxW = 0, totalH = 0;
    for (size_t i = 0; i < items.size(); i++) {
        Font font(ff, items[i].size, fontStyle, UnitPixel);
        RectF b;
        g.MeasureString(items[i].text.c_str(), -1, &font, PointF(0, 0), &mfmt, &b);
        widths[i] = b.Width;
        heights[i] = items[i].size * 1.25f;
        if (b.Width > maxW) maxW = b.Width;
        totalH += heights[i];
    }

    // Box geometry (원본 padding 12px 20px, stage padding 8px/60px edges).
    const REAL padX = 20, padY = 12;
    const REAL edgeX = 60, edgeY = 8;
    REAL boxW = maxW + 2 * padX;
    REAL boxH = totalH + 2 * padY;

    // Horizontal placement of the box within the monitor.
    REAL boxX;
    if (st.align == L"leading") {
        boxX = edgeX;
    } else if (st.align == L"trailing") {
        boxX = w - edgeX - boxW;
    } else {
        boxX = (w - boxW) / 2;
    }
    if (boxX < 0) boxX = 0;

    // Vertical placement: t=(areaIdx+offset)/3, topPad = (avail-boxH)*t.
    REAL off = (REAL)clamp01(st.offset);
    REAL t = (areaIndex(st.vertical) + off) / 3.0f;
    REAL avail = (REAL)h - 2 * edgeY;
    REAL travel = avail - boxH;
    if (travel < 0) travel = 0;
    REAL boxY = edgeY + travel * t;

    // Background box (BgColor RGB + BgOpacity alpha), 원본 radius 12.
    if (st.bgEnabled) {
        DWORD rgb = parseColorARGB(st.bgColor, 0x000000FF);
        int a = (int)(clamp01(st.bgOpacity) * 255.0 + 0.5);
        Color bg((BYTE)a, (BYTE)((rgb >> 16) & 0xFF),
                 (BYTE)((rgb >> 8) & 0xFF), (BYTE)(rgb & 0xFF));
        GraphicsPath box;
        roundRect(box, boxX, boxY, boxW, boxH, 12);
        SolidBrush b(bg);
        g.FillPath(&b, &box);
    }

    // Lines.
    REAL y = boxY + padY;
    for (size_t i = 0; i < items.size(); i++) {
        REAL x;
        if (st.align == L"leading") {
            x = boxX + padX;
        } else if (st.align == L"trailing") {
            x = boxX + boxW - padX - widths[i];
        } else {
            x = boxX + (boxW - widths[i]) / 2;
        }
        drawTextLine(g, items[i].text, x, y, items[i].size, ff, fontStyle,
                     st, items[i].isSource);
        y += heights[i];
    }

    delete ff;
}

// render composes the current state into a premultiplied-ARGB DIB and pushes it
// with UpdateLayeredWindow. Runs on the UI thread only.
static void render() {
    if (g_hwnd == NULL) return;

    // Snapshot the shared state under lock; do all GDI work lock-free.
    State st;
    EnterCriticalSection(&g_cs);
    st = g_state;
    LeaveCriticalSection(&g_cs);

    RECT mon;
    if (!getMonitorRect(st.monitorIndex, &mon)) return;
    int w = mon.right - mon.left;
    int h = mon.bottom - mon.top;
    if (w <= 0 || h <= 0) return;

    // Re-cover the target monitor if position/size changed (monitor switch or
    // resolution change).
    RECT cur;
    GetWindowRect(g_hwnd, &cur);
    if (cur.left != mon.left || cur.top != mon.top ||
        (cur.right - cur.left) != w || (cur.bottom - cur.top) != h) {
        SetWindowPos(g_hwnd, HWND_TOPMOST, mon.left, mon.top, w, h,
                     SWP_NOACTIVATE);
    }

    HDC screen = GetDC(NULL);
    if (!screen) return;

    BITMAPINFO bmi;
    ZeroMemory(&bmi, sizeof(bmi));
    bmi.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
    bmi.bmiHeader.biWidth = w;
    bmi.bmiHeader.biHeight = -h; // top-down
    bmi.bmiHeader.biPlanes = 1;
    bmi.bmiHeader.biBitCount = 32;
    bmi.bmiHeader.biCompression = BI_RGB;

    void *bits = NULL;
    HBITMAP hbmp = CreateDIBSection(screen, &bmi, DIB_RGB_COLORS, &bits, NULL, 0);
    if (!hbmp) {
        ReleaseDC(NULL, screen);
        return;
    }
    HDC memDC = CreateCompatibleDC(screen);
    HGDIOBJ oldbmp = SelectObject(memDC, hbmp);

    // CreateDIBSection zeroes the memory → fully transparent premultiplied.
    {
        // Wrap the DIB bits in a premultiplied-ARGB Bitmap so GDI+ writes
        // premultiplied pixels straight into the DIB for UpdateLayeredWindow.
        Bitmap bmp(w, h, w * 4, PixelFormat32bppPARGB, (BYTE *)bits);
        Graphics g(&bmp);
        g.SetSmoothingMode(SmoothingModeAntiAlias);
        g.SetTextRenderingHint(TextRenderingHintAntiAlias);
        g.SetInterpolationMode(InterpolationModeHighQuality);
        g.SetPageUnit(UnitPixel);
        if (st.visible) {
            drawContent(g, st, w, h);
        }
        g.Flush(FlushIntentionSync);
    }
    GdiFlush();

    POINT ptSrc = {0, 0};
    SIZE size = {w, h};
    POINT ptDst = {mon.left, mon.top};
    BLENDFUNCTION bf;
    bf.BlendOp = AC_SRC_OVER;
    bf.BlendFlags = 0;
    bf.SourceConstantAlpha = 255; // use per-pixel alpha only
    bf.AlphaFormat = AC_SRC_ALPHA;
    UpdateLayeredWindow(g_hwnd, screen, &ptDst, &size, memDC, &ptSrc, 0, &bf,
                        ULW_ALPHA);

    SelectObject(memDC, oldbmp);
    DeleteObject(hbmp);
    DeleteDC(memDC);
    ReleaseDC(NULL, screen);
}

static void requestRepaint() {
    if (g_hwnd) PostMessage(g_hwnd, LT_WM_REPAINT, 0, 0);
}

// ---------------------------------------------------------------------------
// Window procedure + lifecycle.
// ---------------------------------------------------------------------------

static LRESULT CALLBACK wndProc(HWND hwnd, UINT msg, WPARAM wp, LPARAM lp) {
    switch (msg) {
    case LT_WM_REPAINT:
        render();
        return 0;
    case WM_DISPLAYCHANGE:
        render();
        return 0;
    case WM_NCHITTEST:
        // Belt-and-suspenders click-through (WS_EX_TRANSPARENT already does it).
        return HTTRANSPARENT;
    case WM_DESTROY:
        PostQuitMessage(0);
        return 0;
    default:
        return DefWindowProc(hwnd, msg, wp, lp);
    }
}

extern "C" int lt_native_init(void) {
    InitializeCriticalSection(&g_cs);

    // Defaults mirror config.DefaultSettings / the web renderer defaults so the
    // overlay looks correct before the first style message arrives.
    EnterCriticalSection(&g_cs);
    g_state.visible = false;
    g_state.fontFamily = L"";
    g_state.fontSize = 34;
    g_state.fontWeight = L"bold";
    g_state.textColor = L"#FFFFFFFF";
    g_state.strokeEnabled = true;
    g_state.strokeColor = L"#000000E6";
    g_state.strokeWidth = 2;
    g_state.glowEnabled = false;
    g_state.glowColor = L"#00E5FFCC";
    g_state.glowRadius = 8;
    g_state.bgEnabled = true;
    g_state.bgColor = L"#000000FF";
    g_state.bgOpacity = 0.35;
    g_state.align = L"center";
    g_state.maxLines = 2;
    g_state.monitorIndex = 0;
    g_state.vertical = L"bottom";
    g_state.offset = 0.5;
    LeaveCriticalSection(&g_cs);

    GdiplusStartupInput gdiplusStartupInput;
    if (GdiplusStartup(&g_gdiplusToken, &gdiplusStartupInput, NULL) != Ok) {
        return 1;
    }

    HINSTANCE hInst = GetModuleHandle(NULL);
    WNDCLASSEXW wc;
    ZeroMemory(&wc, sizeof(wc));
    wc.cbSize = sizeof(wc);
    wc.style = CS_HREDRAW | CS_VREDRAW;
    wc.lpfnWndProc = wndProc;
    wc.hInstance = hInst;
    wc.hCursor = LoadCursor(NULL, IDC_ARROW);
    wc.hbrBackground = NULL; // painted via UpdateLayeredWindow
    wc.lpszClassName = kClassName;
    if (!RegisterClassExW(&wc)) {
        // 1410 = ERROR_CLASS_ALREADY_EXISTS is fine (idempotent init).
        DWORD err = GetLastError();
        if (err != ERROR_CLASS_ALREADY_EXISTS) return 2;
    }

    RECT mon;
    getMonitorRect(0, &mon);
    int w = mon.right - mon.left;
    int h = mon.bottom - mon.top;

    DWORD exStyle = WS_EX_LAYERED | WS_EX_TRANSPARENT | WS_EX_TOPMOST |
                    WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE;
    g_hwnd = CreateWindowExW(exStyle, kClassName, L"LiveTranslate Overlay",
                             WS_POPUP, mon.left, mon.top, w, h, NULL, NULL,
                             hInst, NULL);
    if (!g_hwnd) return 3;

    SetWindowPos(g_hwnd, HWND_TOPMOST, mon.left, mon.top, w, h,
                 SWP_NOACTIVATE);
    ShowWindow(g_hwnd, SW_SHOWNA);

    // Initial paint (transparent — nothing visible yet).
    render();
    return 0;
}

extern "C" void lt_native_update_subtitle(const char *lines, const char *source,
                                          int visible) {
    EnterCriticalSection(&g_cs);
    g_state.lines = utf8to16(lines);
    g_state.source = utf8to16(source);
    g_state.visible = (visible != 0);
    LeaveCriticalSection(&g_cs);
    requestRepaint();
}

extern "C" void lt_native_update_style(
    const char *fontFamily, double fontSize, const char *fontWeight,
    const char *textColor,
    int strokeEnabled, const char *strokeColor, double strokeWidth,
    int glowEnabled, const char *glowColor, double glowRadius,
    int bgEnabled, const char *bgColor, double bgOpacity,
    const char *align, int maxLines,
    int monitorIndex, const char *vertical, double offset) {
    EnterCriticalSection(&g_cs);
    g_state.fontFamily = utf8to16(fontFamily);
    g_state.fontSize = fontSize;
    g_state.fontWeight = utf8to16(fontWeight);
    g_state.textColor = utf8to16(textColor);
    g_state.strokeEnabled = (strokeEnabled != 0);
    g_state.strokeColor = utf8to16(strokeColor);
    g_state.strokeWidth = strokeWidth;
    g_state.glowEnabled = (glowEnabled != 0);
    g_state.glowColor = utf8to16(glowColor);
    g_state.glowRadius = glowRadius;
    g_state.bgEnabled = (bgEnabled != 0);
    g_state.bgColor = utf8to16(bgColor);
    g_state.bgOpacity = bgOpacity;
    g_state.align = utf8to16(align);
    g_state.maxLines = maxLines;
    g_state.monitorIndex = monitorIndex;
    g_state.vertical = utf8to16(vertical);
    g_state.offset = offset;
    LeaveCriticalSection(&g_cs);
    requestRepaint();
}

extern "C" void lt_native_run_loop(void) {
    MSG msg;
    while (GetMessage(&msg, NULL, 0, 0) > 0) {
        TranslateMessage(&msg);
        DispatchMessage(&msg);
    }
    if (g_gdiplusToken) {
        GdiplusShutdown(g_gdiplusToken);
        g_gdiplusToken = 0;
    }
}
