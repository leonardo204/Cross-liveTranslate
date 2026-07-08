#ifndef LT_SYSTEMTAP_DARWIN_H
#define LT_SYSTEMTAP_DARWIN_H

#ifdef __cplusplus
extern "C" {
#endif

// systemtap_darwin.h — Core Audio Process Tap 기반 시스템 오디오 직접 캡처 C 인터페이스.
//
// 원본 liveTranslate `SystemTapAudioSource.swift`(macOS 14.4+ CATapDescription /
// AudioHardwareCreateProcessTap / AudioHardwareCreateAggregateDevice)를 1:1 이식한다.
// BlackHole 등 가상 루프백 장치 설치 없이, 화면 녹화 권한 없이(오디오 캡처 TCC 권한만
// 첫 tap 생성 시 요구) 시스템 출력 오디오를 직접 캡처한다.
//
// IO 블록(실시간 오디오 스레드)에서 raw tap 버퍼를 AVAudioConverter로 16kHz mono
// Float32 로 변환하고 1600샘플(100ms) 청크로 누적한 뒤, 완성 청크마다 Go 함수
// `lt_systemtap_on_chunk(const float*, int)`를 호출한다(원본 TapCaptureSink 담당 로직을
// .m 내부에서 수행 — Go 쪽은 청크 단위로만 호출받아 실시간 스레드 부담 최소).

// lt_systemtap_start 반환 코드. 0 = 성공. 그 외 음수 = 실패.
// OSStatus(예: -10877)와 구분되도록 큰 음수 대역을 커스텀 코드로 사용한다.
#define LT_SYSTEMTAP_OK               0
#define LT_SYSTEMTAP_ERR_UNAVAILABLE  (-1000)  // macOS 14.4 미만
#define LT_SYSTEMTAP_ERR_ALREADY      (-1001)  // 이미 실행 중

// macOS 14.4+ (Core Audio Process Tap 지원) 여부를 값싸게 판단한다.
// 실제 tap 생성/권한은 lt_systemtap_start 시점에 이뤄진다. 지원 시 1, 아니면 0.
int lt_systemtap_available(void);

// 시스템 탭을 생성/시작한다(setupTap → setupAggregate → readFormat → setupConverter
// → setupIOProc → startIO). 성공 시 LT_SYSTEMTAP_OK(0), 실패 시 음수(OSStatus 또는
// LT_SYSTEMTAP_ERR_*)를 반환한다. 실패 시 부분 생성 자원을 역순으로 정리하고 반환한다.
int lt_systemtap_start(void);

// 직전 lt_systemtap_start 실패가 권한 거부/대기로 인한 것으로 추정되면 1, 아니면 0.
int lt_systemtap_last_error_was_permission(void);

// 시스템 오디오 캡처 권한/가용성을 경량 프로브로 조회한다(권한 카테고리 표시용).
// tap 하나만 생성했다가 즉시 파괴한다(aggregate/IO 없음 — 부작용 최소). 반환:
//    1 = 사용 가능(권한 OK — tap 생성 성공)
//    0 = macOS 14.4 미만(미지원)
//   -1 = 생성 실패(권한 미부여/대기 추정)
// 진행 중인 실제 캡처(gTapID 존재)라면 파괴하지 않고 1을 반환한다.
int lt_systemtap_probe(void);

// 캡처를 정지하고 모든 자원을 역순으로 해제한다(멱등: 중복 호출/미시작 시 무해).
void lt_systemtap_stop(void);

#ifdef __cplusplus
}
#endif

#endif // LT_SYSTEMTAP_DARWIN_H
