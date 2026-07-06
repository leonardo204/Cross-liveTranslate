//go:build darwin && cgo

// ducker_darwin.go — CoreAudio 기반 원음 덕킹(macOS). cgo(`-framework CoreAudio`).
//
// 원본 이식: SystemAudioDucker.swift. 기본 출력 장치의 kAudioDevicePropertyVolumeScalar
// (scope Output, element Main)를 get/set 한다. 마스터 볼륨 스칼라가 존재하고 settable한
// 장치에서만 동작하며(일부 aggregate/가상 출력은 미지원), 미지원이면 조용히 skip한다.
package audio

/*
#cgo darwin LDFLAGS: -framework CoreAudio
#include <CoreAudio/CoreAudio.h>

static AudioDeviceID caDefaultOutputDevice(void) {
    AudioDeviceID dev = 0;
    UInt32 size = sizeof(dev);
    AudioObjectPropertyAddress addr = {
        kAudioHardwarePropertyDefaultOutputDevice,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    OSStatus st = AudioObjectGetPropertyData(kAudioObjectSystemObject, &addr, 0, NULL, &size, &dev);
    if (st != noErr) return 0;
    return dev;
}

static AudioObjectPropertyAddress caVolumeAddress(void) {
    AudioObjectPropertyAddress addr = {
        kAudioDevicePropertyVolumeScalar,
        kAudioDevicePropertyScopeOutput,
        kAudioObjectPropertyElementMain
    };
    return addr;
}

// caVolumeSettable: 볼륨 스칼라 속성이 존재하고 설정 가능하면 1.
static int caVolumeSettable(AudioDeviceID dev) {
    AudioObjectPropertyAddress addr = caVolumeAddress();
    if (!AudioObjectHasProperty(dev, &addr)) return 0;
    Boolean settable = 0;
    if (AudioObjectIsPropertySettable(dev, &addr, &settable) != noErr) return 0;
    return settable ? 1 : 0;
}

// caGetVolume: 현재 마스터 출력 볼륨(0..1). 성공 시 1, out에 값 저장.
static int caGetVolume(AudioDeviceID dev, float *out) {
    AudioObjectPropertyAddress addr = caVolumeAddress();
    if (!AudioObjectHasProperty(dev, &addr)) return 0;
    UInt32 size = sizeof(float);
    return AudioObjectGetPropertyData(dev, &addr, 0, NULL, &size, out) == noErr ? 1 : 0;
}

// caSetVolume: 마스터 출력 볼륨 설정(0..1로 클램프). 성공 시 1.
static int caSetVolume(AudioDeviceID dev, float v) {
    AudioObjectPropertyAddress addr = caVolumeAddress();
    if (!AudioObjectHasProperty(dev, &addr)) return 0;
    Boolean settable = 0;
    if (AudioObjectIsPropertySettable(dev, &addr, &settable) != noErr || !settable) return 0;
    if (v < 0) v = 0;
    if (v > 1) v = 1;
    return AudioObjectSetPropertyData(dev, &addr, 0, NULL, sizeof(float), &v) == noErr ? 1 : 0;
}
*/
import "C"

import "sync"

// coreAudioDucker ducks the macOS system default output via CoreAudio.
type coreAudioDucker struct {
	mu       sync.Mutex
	saved    float32
	hasSaved bool
}

// NewDucker returns a CoreAudio-backed Ducker (darwin && cgo).
func NewDucker() Ducker { return &coreAudioDucker{} }

// IsSupported reports whether the current default output supports master volume.
func (d *coreAudioDucker) IsSupported() bool {
	dev := C.caDefaultOutputDevice()
	if dev == 0 {
		return false
	}
	return C.caVolumeSettable(dev) == 1
}

// Duck saves the current volume once and lowers it to `to` (0..1).
func (d *coreAudioDucker) Duck(to float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	dev := C.caDefaultOutputDevice()
	if dev == 0 {
		return
	}
	if !d.hasSaved {
		var v C.float
		if C.caGetVolume(dev, &v) == 1 {
			d.saved = float32(v)
			d.hasSaved = true
		}
	}
	C.caSetVolume(dev, C.float(to)) // 미지원이면 내부에서 no-op(0 반환).
}

// Restore restores the saved volume (없으면 no-op).
func (d *coreAudioDucker) Restore() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.hasSaved {
		return
	}
	dev := C.caDefaultOutputDevice()
	if dev != 0 {
		C.caSetVolume(dev, C.float(d.saved))
	}
	d.hasSaved = false
}
