#ifndef LT_PERMISSION_DARWIN_H
#define LT_PERMISSION_DARWIN_H

#ifdef __cplusplus
extern "C" {
#endif

// Returns the current microphone (AVMediaTypeAudio) authorization status as one
// of the following stable codes, mapping [AVCaptureDevice
// authorizationStatusForMediaType:AVMediaTypeAudio]:
//
//   0 = notDetermined  (아직 묻지 않음 — 요청하면 다이얼로그)
//   1 = authorized     (허용됨)
//   2 = denied         (거부됨)
//   3 = restricted     (정책 제한)
//
// 원본 PermissionHelper.microphoneStatus()와 동일한 소스(AVCaptureDevice)를 사용한다.
int lt_permission_mic_status(void);

// Explicitly triggers the macOS TCC microphone-permission dialog by calling
// [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio
// completionHandler:^(BOOL){}]. Fire-and-forget: the completion result is
// ignored — the sole purpose is to surface the system prompt on first run,
// because malgo(miniaudio) does not request TCC on its own (원본 AVAudioEngine이
// 자동 트리거하던 것을 우리가 대체). 이미 결정된 상태면 시스템이 다이얼로그를 띄우지
// 않으므로 언제 호출해도 안전(no-op)하다.
void lt_permission_request_mic(void);

// Opens the macOS "개인정보 보호 및 보안" pane for the given category via
// [[NSWorkspace sharedWorkspace] openURL:...] (원본과 동일 — exec("open")보다 확실):
//
//   0 = Privacy_Microphone
//   1 = Privacy_ScreenCapture (시스템 오디오 캡처 안내)
//   기타 = 개인정보 보호 및 보안 루트(폴백)
//
// 메인 스레드에서 openURL 하도록 dispatch_async(main) 한다.
void lt_permission_open_pane(int pane);

#ifdef __cplusplus
}
#endif

#endif // LT_PERMISSION_DARWIN_H
