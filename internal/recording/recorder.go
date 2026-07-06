// Package recording — 확정 자막을 텍스트 파일로 기록하는 녹화기.
//
// 원본 이식: liveTranslate/Sources/Recording/SubtitleRecorder.swift.
// 제어 HUD의 '녹화' 토글이 켜지면 controller가 Start(path, append)로 파일을 열고,
// 자막엔진의 확정 줄 콜백(OnConfirmedLine)이 들어올 때마다 WriteLine으로
// **타임스탬프 + 원문 + 번역문**을 한 줄씩 기록한다. 토글 OFF/세션 정지 시 Stop.
//
// 형식: `[HH:MM:SS] 원문 → 번역`(원문이 공백뿐이면 `[HH:MM:SS] 번역`).
//
// 동시성: WriteLine은 controller runLoop(자막엔진 owner)에서, Start/Stop/IsRecording은
// 바인딩 goroutine에서 호출될 수 있으므로 mutex로 보호한다. 순수 패키지(cgo 없음) →
// windows 크로스빌드 가능. 민감정보(키)는 기록하지 않는다(자막 텍스트만).
package recording

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Recorder appends confirmed subtitle lines to an open text file.
// 원본 SubtitleRecorder 등가. 단일 열림 파일을 mutex로 직렬 write 한다.
type Recorder struct {
	mu   sync.Mutex
	file *os.File
	open bool
}

// New returns a closed recorder.
func New() *Recorder { return &Recorder{} }

// Start opens (or creates) the recording file at path.
//
//   - append=false: 기존 내용을 지우고 새로 쓴다(O_TRUNC).
//   - append=true : 없으면 생성, 있으면 끝에 이어붙인다(O_APPEND).
//
// 성공 시 세션 시작 헤더 1줄을 기록하고 열림 상태로 만든다(멱등: 이미 열려 있으면 닫고 새로 연다).
func (r *Recorder) Start(path string, appendMode bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 멱등: 이미 열려 있으면 정리 후 새로 연다.
	if r.open {
		r.closeLocked()
	}

	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	r.file = f
	r.open = true

	// 세션 시작 헤더(append든 새로든 매 세션 시작 시 1줄).
	r.writeRawLocked("===== liveTranslate 녹화 시작 =====\n")
	return nil
}

// WriteLine records one confirmed subtitle line (타임스탬프 + 원문 + 번역문).
// 닫혀 있으면 조용히 무시한다. source가 공백뿐이면 번역문만 기록한다.
func (r *Recorder) WriteLine(ts time.Time, source, translation string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.open {
		return
	}
	prefix := fmt.Sprintf("[%s] ", ts.Format("15:04:05"))
	var line string
	if strings.TrimSpace(source) != "" {
		line = prefix + source + " → " + translation + "\n"
	} else {
		line = prefix + translation + "\n"
	}
	r.writeRawLocked(line)
}

// Stop writes a footer and closes the file (멱등). 닫혀 있으면 nil.
func (r *Recorder) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.open {
		return nil
	}
	r.writeRawLocked("===== 녹화 종료 =====\n")
	return r.closeLocked()
}

// IsRecording reports whether a file is currently open for writing.
func (r *Recorder) IsRecording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.open
}

// closeLocked closes the handle and clears state. 호출자는 mu를 보유한다.
func (r *Recorder) closeLocked() error {
	var err error
	if r.file != nil {
		err = r.file.Close()
	}
	r.file = nil
	r.open = false
	return err
}

// writeRawLocked writes UTF-8 text; write failures are ignored(best-effort). 호출자는 mu를 보유한다.
func (r *Recorder) writeRawLocked(text string) {
	if r.file == nil {
		return
	}
	_, _ = r.file.WriteString(text)
}
