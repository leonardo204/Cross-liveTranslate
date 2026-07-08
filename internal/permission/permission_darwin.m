//go:build darwin

#import <AVFoundation/AVFoundation.h>
#import <AppKit/AppKit.h>
#import "permission_darwin.h"

// lt_permission_mic_status maps AVCaptureDevice authorization status to the
// stable integer contract in the header. 원본 PermissionHelper.microphoneStatus()
// 와 동일한 소스를 사용해 미요청/허용됨/거부됨/제한됨을 정확히 구분한다.
int lt_permission_mic_status(void) {
    AVAuthorizationStatus st =
        [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
    switch (st) {
        case AVAuthorizationStatusNotDetermined: return 0;
        case AVAuthorizationStatusAuthorized:    return 1;
        case AVAuthorizationStatusDenied:        return 2;
        case AVAuthorizationStatusRestricted:    return 3;
        default:                                 return 0;
    }
}

// lt_permission_request_mic explicitly asks for microphone access, which is what
// surfaces the TCC dialog on first run. malgo(miniaudio) never does this, so
// without this call the app would capture silence and hang at "연결 중…".
// requestAccess는 아무 스레드에서나 호출 가능하지만 안전하게 메인 스레드로 dispatch 한다.
// completion 핸들러는 다이얼로그를 띄우는 것이 목적이라 결과를 무시한다(fire-and-forget).
//
// 주의(ad-hoc 서명 한계): 개발 빌드는 ad-hoc 서명이라 재빌드 시 코드 해시가 바뀌어 TCC가
// 새로운 앱으로 인식 → 권한이 매번 초기화되어 재요청이 필요할 수 있다. 이는 개발 워크플로
// 한계이며 근본 해결은 안정적인 Developer ID 서명(별도 작업)으로 한다.
void lt_permission_request_mic(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio
                                 completionHandler:^(BOOL granted) {
            (void)granted; // 무시 — 목적은 다이얼로그 트리거.
        }];
    });
}

// lt_permission_open_pane opens the requested Privacy pane via NSWorkspace
// (원본 PermissionHelper.open()과 동일). NSWorkspace openURL은 서명된 번들 안에서도
// exec("open")보다 확실히 동작한다. UI 작업이므로 메인 스레드로 dispatch 한다.
void lt_permission_open_pane(int pane) {
    NSString *urlStr;
    switch (pane) {
        case 0:
            urlStr = @"x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone";
            break;
        case 1:
            urlStr = @"x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture";
            break;
        default:
            urlStr = @"x-apple.systempreferences:com.apple.preference.security";
            break;
    }
    dispatch_async(dispatch_get_main_queue(), ^{
        NSURL *url = [NSURL URLWithString:urlStr];
        if (url != nil) {
            [[NSWorkspace sharedWorkspace] openURL:url];
        }
    });
}
