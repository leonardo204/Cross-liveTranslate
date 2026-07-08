//go:build darwin && cgo

// systemtap_darwin.go — Core Audio Process Tap 시스템 오디오 캡처의 Go `Source` 래퍼.
//
// 실제 CoreAudio/ObjC 구현은 systemtap_darwin.{h,m} 에 있다(원본 SystemTapAudioSource.swift
// 이식). 이 파일은 Source 인터페이스를 충족하고, .m 의 IO 블록이 넘기는 완성 청크
// (16kHz mono Float32 1600샘플)를 논블로킹 채널 → dispatch goroutine → onChunk 로 포워딩한다.
//
// 프로세스당 tap 은 1개이므로 현재 활성 소스를 전역(systemTapCurrent)으로 등록하고,
// export 콜백 lt_systemtap_on_chunk 가 그 소스의 채널로 청크를 전달한다.
package audio

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework CoreAudio -framework AudioToolbox -framework AVFoundation -framework Foundation
#include "systemtap_darwin.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"
)

// ErrSystemTapUnavailable is returned by SystemTapSource.Start when the OS does
// not support Core Audio Process Tap (macOS 14.4 미만). 호출부(newLoopbackSource)는
// 이 경우 가상 루프백 장치로 폴백한다.
var ErrSystemTapUnavailable = errors.New("audio: system tap requires macOS 14.4+")

// 프로세스당 tap 1개 — 현재 활성 소스와 그 전달 채널을 전역으로 등록한다.
// export 콜백(lt_systemtap_on_chunk)이 이 채널로 청크를 넘긴다.
var (
	systemTapMu      sync.Mutex
	systemTapCurrent *SystemTapSource
)

// SystemTapSource captures system output audio directly via the macOS 14.4+
// Core Audio Process Tap API (원본 SystemTapAudioSource.swift 이식). It implements
// Source and emits ChunkSamples(1600)-sized 16kHz mono Float32 Chunks.
//
// 피드백 루프 방지(필수 불변식): tap 은 자기 프로세스를 제외해 생성한다(.m 의
// initMonoGlobalTapButExcludeProcesses + kAudioHardwarePropertyTranslatePIDToProcessObject).
type SystemTapSource struct {
	mu      sync.Mutex
	ch      chan Chunk
	done    chan struct{}
	started bool
	dropped atomic.Uint64
}

// NewSystemTapSource returns an unstarted system-tap Source. 실제 tap 생성/권한은
// Start 시점에 일어난다(생성은 값싸다).
func NewSystemTapSource() *SystemTapSource { return &SystemTapSource{} }

// SystemTapAvailable reports whether the OS supports Core Audio Process Tap
// (macOS 14.4+). newLoopbackSource 가 SystemTap vs BlackHole 폴백을 값싸게 결정하는 데 쓴다.
func SystemTapAvailable() bool { return C.lt_systemtap_available() != 0 }

// SystemTapStatus probes 시스템 오디오 캡처(Core Audio Process Tap) 권한/가용성을 조회해
// 권한 카테고리 표시용 상태 문자열을 반환한다. Process Tap은 사전 조회용 authorizationStatus
// 공개 API가 없어, tap 하나를 만들었다 즉시 파괴하는 경량 프로브로 판정한다.
//
//	"authorized"    — 사용 가능(tap 생성 성공)
//	"denied"        — 생성 실패(권한 미부여/대기 추정 — 시스템 설정에서 허용 필요)
//	"restricted"    — macOS 14.4 미만(미지원)
func SystemTapStatus() string {
	switch C.lt_systemtap_probe() {
	case 1:
		return "authorized"
	case 0:
		return "restricted"
	default:
		return "denied"
	}
}

// Start creates and starts the process tap, then wires the .m IO callback to
// onChunk via a bounded channel + dispatch goroutine (onChunk 은 실시간 오디오 스레드가
// 아니라 이 goroutine 에서 실행). 실패 시 .m 이 부분 자원을 역순 정리하고 에러를 반환한다.
func (s *SystemTapSource) Start(ctx context.Context, onChunk func(Chunk)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return fmt.Errorf("audio: system tap already started")
	}
	if C.lt_systemtap_available() == 0 {
		return ErrSystemTapUnavailable
	}

	s.ch = make(chan Chunk, 32)
	s.done = make(chan struct{})

	// 콜백 라우팅을 위해 tap 시작 전에 현재 소스로 등록한다.
	systemTapMu.Lock()
	if systemTapCurrent != nil {
		systemTapMu.Unlock()
		return fmt.Errorf("audio: another system tap source is active")
	}
	systemTapCurrent = s
	systemTapMu.Unlock()

	rc := C.lt_systemtap_start()
	if rc != C.LT_SYSTEMTAP_OK {
		systemTapMu.Lock()
		if systemTapCurrent == s {
			systemTapCurrent = nil
		}
		systemTapMu.Unlock()
		if rc == C.LT_SYSTEMTAP_ERR_UNAVAILABLE {
			return ErrSystemTapUnavailable
		}
		if C.lt_systemtap_last_error_was_permission() != 0 {
			// 권한 실패는 sentinel로 감싸 controller가 HUD에 명확히 안내하게 한다.
			return fmt.Errorf("%w (code %d)", ErrSystemTapPermission, int(rc))
		}
		return fmt.Errorf("audio: system tap start failed: code %d", int(rc))
	}

	// dispatch goroutine: 채널 → onChunk. ctx/Stop 로 종료.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.done:
				return
			case c := <-s.ch:
				onChunk(c)
			}
		}
	}()

	s.started = true

	// ctx 취소 시 자동 정리.
	go func() {
		<-ctx.Done()
		_ = s.Stop()
	}()

	return nil
}

// Stop tears down the tap and dispatch goroutine. Idempotent (.m teardown 도 멱등).
func (s *SystemTapSource) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return nil
	}
	s.started = false

	C.lt_systemtap_stop()

	systemTapMu.Lock()
	if systemTapCurrent == s {
		systemTapCurrent = nil
	}
	systemTapMu.Unlock()

	close(s.done)
	return nil
}

// Dropped returns the number of Chunks dropped due to backpressure (채널 포화).
func (s *SystemTapSource) Dropped() uint64 { return s.dropped.Load() }

// lt_systemtap_on_chunk is called from the .m IO dispatch queue with a completed
// 16kHz mono Float32 chunk (n == ChunkSamples). C 메모리를 Go 슬라이스로 안전 복사한 뒤
// 현재 소스의 채널로 논블로킹 전달한다(포화 시 드롭 + 카운터).
//
//export lt_systemtap_on_chunk
func lt_systemtap_on_chunk(samples *C.float, n C.int) {
	if n <= 0 || samples == nil {
		return
	}
	systemTapMu.Lock()
	src := systemTapCurrent
	var ch chan Chunk
	if src != nil {
		ch = src.ch
	}
	systemTapMu.Unlock()
	if src == nil || ch == nil {
		return
	}

	count := int(n)
	// C float 배열 → Go 슬라이스 뷰 → 새 Chunk 로 복사(C 메모리 소유권과 분리).
	cView := unsafe.Slice((*float32)(unsafe.Pointer(samples)), count)
	frame := make(Chunk, count)
	copy(frame, cView)

	select {
	case ch <- frame:
	default:
		src.dropped.Add(1)
	}
}
