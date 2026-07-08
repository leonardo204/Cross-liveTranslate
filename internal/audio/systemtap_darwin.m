//go:build darwin

// systemtap_darwin.m — Core Audio Process Tap 시스템 오디오 직접 캡처 구현.
//
// 원본 liveTranslate `SystemTapAudioSource.swift`(+ TapCaptureSink)를 1:1 이식한다.
// 흐름: setupTap → setupAggregateDevice → readTapFormat → setupConverter → setupIOProc
//      → startIO, teardown 은 역순(AudioDeviceStop → DestroyIOProcID →
//      DestroyAggregateDevice → DestroyProcessTap).
//
// 프로세스당 tap 1개이므로 전역 상태로 관리한다. IO 블록은 전용 시리얼 dispatch 큐
// (실시간 오디오 스레드)에서 순차 호출되므로 변환/누적 상태(gConverter/gPending 등)는
// 락 없이 그 큐에서만 접근한다. 완성된 100ms 청크만 Go(lt_systemtap_on_chunk)로 넘긴다.

#import <Foundation/Foundation.h>
#import <AVFoundation/AVFoundation.h>
#import <CoreAudio/CoreAudio.h>
#import <CoreAudio/CATapDescription.h>
#import <CoreAudio/AudioHardwareTapping.h>
#import <unistd.h>
#import <sys/types.h>

#import "systemtap_darwin.h"

// Go 로 완성 청크(정확히 16kHz mono Float32 1600샘플)를 넘기는 export 콜백(systemtap_darwin.go).
extern void lt_systemtap_on_chunk(float *samples, int n);

// 오디오 계약(불변, internal/audio/source.go 와 일치):
//   출력 16kHz mono Float32, 100ms = 1600샘플 청크.
static const double   kLTTargetSampleRate = 16000.0;
static const NSUInteger kLTChunkSamples   = 1600;

// ---- 전역 Core Audio 핸들 (생성 역순 해제 추적용). kAudioObjectUnknown/nil = 미생성. ----
static AudioObjectID       gTapID        = kAudioObjectUnknown;
static AudioObjectID       gAggregateID  = kAudioObjectUnknown;
static AudioDeviceIOProcID gIOProcID     = NULL;
static dispatch_queue_t    gIOQueue      = NULL;

// ---- 변환/누적 상태 (IO 시리얼 큐 단독 접근) ----
static AVAudioFormat    *gInputFormat  = nil;
static AVAudioFormat    *gTargetFormat = nil;
static AVAudioConverter *gConverter    = nil;
static NSMutableData    *gPending      = nil;   // 누적 float32 샘플(바이트)

static NSString *gTapUUID = nil;
static int gLastErrorWasPermission = 0;

// -10877(kAudioHardwareBadObjectError): 대상 객체가 이미 없음 → 정리 목적상 무해.
// teardown 은 멱등이므로 그런 상태는 조용히 무시한다.

// 우리 앱 자신의 Core Audio 프로세스 객체 ID(탭 제외 목록용). 실패 시 kAudioObjectUnknown.
// PID → AudioObjectID (kAudioHardwarePropertyTranslatePIDToProcessObject).
static AudioObjectID lt_current_process_object(void) {
    pid_t pid = getpid();
    AudioObjectPropertyAddress address = {
        kAudioHardwarePropertyTranslatePIDToProcessObject,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    AudioObjectID objectID = kAudioObjectUnknown;
    UInt32 size = (UInt32)sizeof(AudioObjectID);
    OSStatus status = AudioObjectGetPropertyData(
        (AudioObjectID)kAudioObjectSystemObject,
        &address,
        (UInt32)sizeof(pid_t),
        &pid,
        &size,
        &objectID);
    if (status != noErr || objectID == kAudioObjectUnknown) {
        return kAudioObjectUnknown;
    }
    return objectID;
}

// 역순 teardown (멱등). 각 단계는 유효할 때만 호출하고 즉시 무효화해 재진입이 무해하도록.
static void lt_teardown(void) {
    // 1) IO 정지 + 2) IOProc 제거 (aggregate 와 procID 모두 유효할 때만).
    if (gAggregateID != kAudioObjectUnknown && gIOProcID != NULL) {
        AudioDeviceStop(gAggregateID, gIOProcID);
        AudioDeviceDestroyIOProcID(gAggregateID, gIOProcID);
    }
    gIOProcID = NULL;

    // 3) aggregate device 파괴. 호출 직후 즉시 무효화 → 재진입 시 재파괴 안 함.
    if (gAggregateID != kAudioObjectUnknown) {
        AudioObjectID id = gAggregateID;
        gAggregateID = kAudioObjectUnknown;
        AudioHardwareDestroyAggregateDevice(id);
    }

    // 4) process tap 파괴 (14.4+ 에서만 생성했으므로 안전).
    if (gTapID != kAudioObjectUnknown) {
        AudioObjectID id = gTapID;
        gTapID = kAudioObjectUnknown;
        if (@available(macOS 14.4, *)) {
            AudioHardwareDestroyProcessTap(id);
        }
    }

    gInputFormat  = nil;
    gTargetFormat = nil;
    gConverter    = nil;
    gPending      = nil;
    gTapUUID      = nil;
    gIOQueue      = NULL;
}

int lt_systemtap_available(void) {
    if (@available(macOS 14.4, *)) {
        return 1;
    }
    return 0;
}

int lt_systemtap_probe(void) {
    if (@available(macOS 14.4, *)) {
        // 이미 실제 캡처가 진행 중이면(핸들 존재) 권한이 있는 상태 — 파괴하지 않고 OK.
        if (gTapID != kAudioObjectUnknown) {
            return 1;
        }
        // tap 하나만 생성해 권한 여부를 확인하고 즉시 파괴한다(aggregate/IO 없음).
        NSMutableArray<NSNumber *> *excluded = [NSMutableArray array];
        AudioObjectID selfObj = lt_current_process_object();
        if (selfObj != kAudioObjectUnknown) {
            [excluded addObject:@(selfObj)];
        }
        CATapDescription *tapDesc =
            [[CATapDescription alloc] initMonoGlobalTapButExcludeProcesses:excluded];
        tapDesc.name = @"liveTranslate System Tap Probe";
        [tapDesc setPrivate:YES];
        tapDesc.muteBehavior = CATapUnmuted;

        AudioObjectID probeTap = kAudioObjectUnknown;
        OSStatus st = AudioHardwareCreateProcessTap(tapDesc, &probeTap);
        if (st != noErr || probeTap == kAudioObjectUnknown) {
            return -1; // 권한 미부여/대기 추정.
        }
        AudioHardwareDestroyProcessTap(probeTap);
        return 1;
    }
    return 0; // macOS 14.4 미만.
}

int lt_systemtap_last_error_was_permission(void) {
    return gLastErrorWasPermission;
}

// IO 블록(실시간 스레드)에서 raw tap 버퍼를 16kHz mono Float32 로 변환 → 1600샘플 청크화 → Go.
// 원본 TapCaptureSink.process 를 그대로 이식한다.
static void lt_process(const AudioBufferList *inInputData) {
    if (gConverter == nil || gInputFormat == nil || gTargetFormat == nil) {
        return;
    }
    if (inInputData == NULL || inInputData->mNumberBuffers == 0) {
        return;
    }
    const AudioBuffer *firstBuffer = &inInputData->mBuffers[0];
    if (firstBuffer->mData == NULL || firstBuffer->mDataByteSize == 0) {
        return;
    }

    UInt32 bytesPerFrame = gInputFormat.streamDescription->mBytesPerFrame;
    if (bytesPerFrame == 0) {
        return;
    }
    AVAudioFrameCount frameCount = firstBuffer->mDataByteSize / bytesPerFrame;
    if (frameCount == 0) {
        return;
    }

    AVAudioPCMBuffer *inBuffer =
        [[AVAudioPCMBuffer alloc] initWithPCMFormat:gInputFormat frameCapacity:frameCount];
    if (inBuffer == nil) {
        return;
    }
    inBuffer.frameLength = frameCount;

    // 입력 AudioBufferList raw 바이트를 inBuffer 로 미러 복사(채널 레이아웃은 inputFormat 기술).
    AudioBufferList *dst = inBuffer.mutableAudioBufferList;
    UInt32 copyCount = MIN(dst->mNumberBuffers, inInputData->mNumberBuffers);
    for (UInt32 i = 0; i < copyCount; i++) {
        if (inInputData->mBuffers[i].mData == NULL || dst->mBuffers[i].mData == NULL) {
            continue;
        }
        UInt32 n = MIN(inInputData->mBuffers[i].mDataByteSize, dst->mBuffers[i].mDataByteSize);
        memcpy(dst->mBuffers[i].mData, inInputData->mBuffers[i].mData, n);
        dst->mBuffers[i].mDataByteSize = inInputData->mBuffers[i].mDataByteSize;
    }

    double ratio = gTargetFormat.sampleRate / gInputFormat.sampleRate;
    AVAudioFrameCount capacity = (AVAudioFrameCount)((double)frameCount * ratio) + 16;
    if (capacity == 0) {
        return;
    }
    AVAudioPCMBuffer *outBuffer =
        [[AVAudioPCMBuffer alloc] initWithPCMFormat:gTargetFormat frameCapacity:capacity];
    if (outBuffer == nil) {
        return;
    }

    // AVAudioConverter 입력 블록: inBuffer 를 1회 공급 후 소진(원본 TapConverterFeed 패턴).
    __block AVAudioPCMBuffer *feed = inBuffer;
    AVAudioConverterInputBlock inputBlock =
        ^AVAudioBuffer *(AVAudioPacketCount inNumberOfPackets, AVAudioConverterInputStatus *outStatus) {
            if (feed == nil) {
                *outStatus = AVAudioConverterInputStatus_NoDataNow;
                return nil;
            }
            AVAudioPCMBuffer *b = feed;
            feed = nil;
            *outStatus = AVAudioConverterInputStatus_HaveData;
            return b;
        };

    NSError *convError = nil;
    AVAudioConverterOutputStatus status =
        [gConverter convertToBuffer:outBuffer error:&convError withInputFromBlock:inputBlock];
    if ((status != AVAudioConverterOutputStatus_HaveData &&
         status != AVAudioConverterOutputStatus_InputRanDry) ||
        outBuffer.floatChannelData == NULL ||
        outBuffer.frameLength == 0) {
        return;
    }

    UInt32 frames = outBuffer.frameLength;
    float *ch0 = outBuffer.floatChannelData[0];
    [gPending appendBytes:ch0 length:(NSUInteger)frames * sizeof(float)];

    // 완성된 1600샘플 청크마다 Go 로 넘긴다.
    NSUInteger chunkBytes = kLTChunkSamples * sizeof(float);
    while (gPending.length >= chunkBytes) {
        lt_systemtap_on_chunk((float *)gPending.bytes, (int)kLTChunkSamples);
        [gPending replaceBytesInRange:NSMakeRange(0, chunkBytes) withBytes:NULL length:0];
    }
}

int lt_systemtap_start(void) {
    gLastErrorWasPermission = 0;

    if (@available(macOS 14.4, *)) {
        // 이미 실행 중이면(핸들 존재) 거부 — 상위(Go)가 멱등 보장하지만 방어적으로.
        if (gTapID != kAudioObjectUnknown || gAggregateID != kAudioObjectUnknown) {
            return LT_SYSTEMTAP_ERR_ALREADY;
        }

        // 1) setupTap — 전체 시스템 mono tap. 피드백 루프 방지: 자기 프로세스 **제외**.
        //    (번역 음성 재생 시 자기 출력이 전체 탭에 재캡처되어 무한 재번역되는 것을 차단.)
        NSMutableArray<NSNumber *> *excluded = [NSMutableArray array];
        AudioObjectID selfObj = lt_current_process_object();
        if (selfObj != kAudioObjectUnknown) {
            [excluded addObject:@(selfObj)];
        }
        CATapDescription *tapDesc =
            [[CATapDescription alloc] initMonoGlobalTapButExcludeProcesses:excluded];
        tapDesc.name = @"liveTranslate System Tap";
        [tapDesc setPrivate:YES];                  // isPrivate — 이 클라이언트만 보이는 tap
        tapDesc.muteBehavior = CATapUnmuted;       // 캡처만, 시스템 소리는 정상 재생

        AudioObjectID newTapID = kAudioObjectUnknown;
        OSStatus st = AudioHardwareCreateProcessTap(tapDesc, &newTapID);
        if (st != noErr || newTapID == kAudioObjectUnknown) {
            // 권한 거부/대기 시 tap 생성이 실패할 수 있다 → 권한 안내로 표면화.
            gLastErrorWasPermission = 1;
            lt_teardown();
            return (st != noErr) ? (int)st : LT_SYSTEMTAP_ERR_UNAVAILABLE;
        }
        gTapID = newTapID;
        gTapUUID = tapDesc.UUID.UUIDString;

        // 2) setupAggregateDevice — private + tapautostart + taps=[subTap(uid, drift)].
        NSString *aggregateUID = @"com.altimedia.liveTranslate.systemtap.aggregate";
        NSDictionary *subTap = @{
            @(kAudioSubTapUIDKey) : (gTapUUID ?: @""),
            @(kAudioSubTapDriftCompensationKey) : @YES,
        };
        NSDictionary *description = @{
            @(kAudioAggregateDeviceUIDKey) : aggregateUID,
            @(kAudioAggregateDeviceIsPrivateKey) : @YES,
            @(kAudioAggregateDeviceTapAutoStartKey) : @YES,
            @(kAudioAggregateDeviceTapListKey) : @[ subTap ],
        };
        AudioObjectID newAggregateID = kAudioObjectUnknown;
        st = AudioHardwareCreateAggregateDevice((__bridge CFDictionaryRef)description, &newAggregateID);
        if (st != noErr || newAggregateID == kAudioObjectUnknown) {
            lt_teardown();
            return (st != noErr) ? (int)st : LT_SYSTEMTAP_ERR_UNAVAILABLE;
        }
        gAggregateID = newAggregateID;

        // 3) readTapFormat — tap 스트림 ASBD(보통 48kHz float).
        AudioObjectPropertyAddress fmtAddress = {
            kAudioTapPropertyFormat,
            kAudioObjectPropertyScopeGlobal,
            kAudioObjectPropertyElementMain
        };
        AudioStreamBasicDescription asbd;
        memset(&asbd, 0, sizeof(asbd));
        UInt32 fmtSize = (UInt32)sizeof(asbd);
        st = AudioObjectGetPropertyData(gTapID, &fmtAddress, 0, NULL, &fmtSize, &asbd);
        if (st != noErr || asbd.mSampleRate <= 0 || asbd.mChannelsPerFrame == 0) {
            lt_teardown();
            return (st != noErr) ? (int)st : LT_SYSTEMTAP_ERR_UNAVAILABLE;
        }

        // 4) setupConverter — tap ASBD → 16kHz mono Float32.
        gInputFormat = [[AVAudioFormat alloc] initWithStreamDescription:&asbd];
        gTargetFormat = [[AVAudioFormat alloc] initWithCommonFormat:AVAudioPCMFormatFloat32
                                                         sampleRate:kLTTargetSampleRate
                                                           channels:1
                                                        interleaved:NO];
        if (gInputFormat == nil || gTargetFormat == nil) {
            lt_teardown();
            return LT_SYSTEMTAP_ERR_UNAVAILABLE;
        }
        gConverter = [[AVAudioConverter alloc] initFromFormat:gInputFormat toFormat:gTargetFormat];
        if (gConverter == nil) {
            lt_teardown();
            return LT_SYSTEMTAP_ERR_UNAVAILABLE;
        }
        gPending = [NSMutableData dataWithCapacity:kLTChunkSamples * sizeof(float) * 2];

        // 5) setupIOProc — 전용 시리얼 큐(실시간)에서 IO 블록 실행.
        gIOQueue = dispatch_queue_create("com.altimedia.liveTranslate.systemtap.io",
                                         DISPATCH_QUEUE_SERIAL);
        AudioDeviceIOBlock block = ^(const AudioTimeStamp *inNow,
                                     const AudioBufferList *inInputData,
                                     const AudioTimeStamp *inInputTime,
                                     AudioBufferList *outOutputData,
                                     const AudioTimeStamp *inOutputTime) {
            (void)inNow;
            (void)inInputTime;
            (void)outOutputData;
            (void)inOutputTime;
            lt_process(inInputData);
        };
        AudioDeviceIOProcID newProcID = NULL;
        st = AudioDeviceCreateIOProcIDWithBlock(&newProcID, gAggregateID, gIOQueue, block);
        if (st != noErr || newProcID == NULL) {
            lt_teardown();
            return (st != noErr) ? (int)st : LT_SYSTEMTAP_ERR_UNAVAILABLE;
        }
        gIOProcID = newProcID;

        // 6) startIO.
        st = AudioDeviceStart(gAggregateID, gIOProcID);
        if (st != noErr) {
            lt_teardown();
            return (int)st;
        }

        return LT_SYSTEMTAP_OK;
    } else {
        return LT_SYSTEMTAP_ERR_UNAVAILABLE;
    }
}

void lt_systemtap_stop(void) {
    lt_teardown();
}
