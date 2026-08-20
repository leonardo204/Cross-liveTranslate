//go:build darwin

package updater

// translocate_darwin.go — App Translocation 판정 + 원본 경로 해석.
//
// macOS 10.12+는 quarantine이 붙은 앱을 Finder로 옮기지 않고 실행하면
// /private/var/folders/<..>/T/AppTranslocation/<UUID>/d/<App>.app 라는 **읽기전용 랜덤
// 마운트**에서 실행한다(Gatekeeper Path Randomization). 그 경로는 덮어쓸 수 없어서
// 자동 업데이트가 실패했고, 예전 코드는 임시 폴더의 새 앱을 직접 실행하는 잘못된 폴백으로
// 빠졌다. 여기서 원본 경로를 되찾아 "원래 있던 자리"에 설치할 수 있게 한다.
//
// # 왜 dlopen/dlsym인가
//
// SecTranslocateIsTranslocatedURL / SecTranslocateCreateOriginalPathForURL 은 Security
// 프레임워크의 SPI다(공개 헤더가 없다). 링크타임 의존을 만들지 않도록 런타임에
// dlopen+dlsym으로 찾고, 없으면(구버전/변경) 조용히 실패해 경로 문자열 판정으로 폴백한다.
// 비-MAS(Developer ID) 배포 앱이라 SPI 사용에 제약이 없다 — Sparkle도 같은 계열을 쓴다.

/*
#cgo LDFLAGS: -framework CoreFoundation
#include <dlfcn.h>
#include <stdlib.h>
#include <string.h>
#include <dispatch/dispatch.h>
#include <CoreFoundation/CoreFoundation.h>

typedef Boolean (*is_translocated_fn)(CFURLRef, bool *, CFErrorRef *);
typedef CFURLRef (*original_path_fn)(CFURLRef, CFErrorRef *);

// dlopen은 dispatch_once로 1회만 수행한다(멀티 goroutine에서 동시 호출돼도 안전).
static void *lt_security_handle(void) {
    static void *handle;
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        handle = dlopen("/System/Library/Frameworks/Security.framework/Security", RTLD_LAZY);
    });
    return handle;
}

// lt_translocation_query fills *isTrans (0/1) and returns a newly allocated UTF-8
// original path (caller frees) when resolvable. Returns NULL when the original
// path is unavailable. rc: 0 = SPI 사용 가능, -1 = SPI 없음(폴백 필요).
static int lt_translocation_query(const char *path, int *isTrans, char **original) {
    *isTrans = 0;
    *original = NULL;

    void *h = lt_security_handle();
    if (h == NULL) return -1;

    is_translocated_fn isFn = (is_translocated_fn)dlsym(h, "SecTranslocateIsTranslocatedURL");
    original_path_fn origFn = (original_path_fn)dlsym(h, "SecTranslocateCreateOriginalPathForURL");
    // 두 심볼이 모두 있어야 "SPI 사용 가능"이다 — 판정만 되고 원본 경로를 못 얻으면
    // 결국 /Applications 이설 경로로 가야 하므로, 그 사실을 호출자에게 정직하게 알린다.
    if (isFn == NULL || origFn == NULL) return -1;

    CFStringRef cfPath = CFStringCreateWithCString(NULL, path, kCFStringEncodingUTF8);
    if (cfPath == NULL) return -1;
    CFURLRef url = CFURLCreateWithFileSystemPath(NULL, cfPath, kCFURLPOSIXPathStyle, true);
    CFRelease(cfPath);
    if (url == NULL) return -1;

    bool trans = false;
    CFErrorRef err = NULL;
    if (isFn(url, &trans, &err) && trans) {
        *isTrans = 1;
    }
    if (err != NULL) { CFRelease(err); err = NULL; }

    if (*isTrans) {
        CFURLRef orig = origFn(url, &err);
        if (err != NULL) { CFRelease(err); err = NULL; }
        if (orig != NULL) {
            char buf[4096];
            if (CFURLGetFileSystemRepresentation(orig, true, (UInt8 *)buf, sizeof(buf))) {
                *original = strdup(buf);
            }
            CFRelease(orig);
        }
    }
    CFRelease(url);
    return 0;
}
*/
import "C"

import "unsafe"

// translocationInfo 는 SPI 조회 결과다.
type translocationInfo struct {
	Translocated bool   // App Translocation 마운트에서 실행 중인지.
	Original     string // 해석된 원본 .app 경로("" = 해석 실패).
	SPIAvailable bool   // SPI를 실제로 호출했는지(false면 문자열 판정으로 폴백했다).
}

// queryTranslocation 은 주어진 번들 경로의 translocation 상태와 원본 경로를 조회한다.
// SPI를 못 쓰면 경로 문자열(/AppTranslocation/) 판정으로 폴백하고 원본은 비운다.
func queryTranslocation(path string) translocationInfo {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))

	var isTrans C.int
	var original *C.char
	rc := C.lt_translocation_query(cPath, &isTrans, &original)
	if rc != 0 {
		// SPI 부재 — 경로 형태로만 판정한다(원본 해석 불가 → /Applications 이설 경로).
		return translocationInfo{Translocated: isTranslocatedPath(path)}
	}
	info := translocationInfo{Translocated: isTrans != 0, SPIAvailable: true}
	if original != nil {
		info.Original = C.GoString(original)
		C.free(unsafe.Pointer(original))
	}
	// SPI가 false를 줘도 경로가 명백히 translocation이면 보수적으로 true로 본다.
	if !info.Translocated && isTranslocatedPath(path) {
		info.Translocated = true
	}
	return info
}
