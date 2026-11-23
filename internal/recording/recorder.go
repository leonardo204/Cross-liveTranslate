// Package recording — 확정 자막을 텍스트 파일로 기록하는 녹화기.
//
// 원본 이식: liveTranslate/Sources/Recording/SubtitleRecorder.swift.
// 제어 HUD의 '녹화' 토글이 켜지면 controller가 Start(path, append)로 기록을 예약하고,
// **첫 확정 줄이 들어올 때 파일을 만든다**(켜 두기만 하고 말이 없으면 빈 파일을 남기지 않는다).
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
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

	// armed/path/appendMode — 녹화가 켜졌지만 아직 쓸 자막이 없어 파일을 만들지 않은 상태다.
	// 첫 확정 자막(WriteLine)에서 실제로 파일을 연다. 이렇게 늦춰 여는 이유: 녹화를 켜 둔 채
	// 앱을 껐다 켜면 매번 빈 파일이 하나씩 쌓이기 때문이다(녹화 상태 복원을 넣으면서 드러남).
	armed      bool
	path       string
	appendMode bool

	// openErr 는 늦춰 연 파일 열기가 실패한 이유다. 호출자가 한 번만 로그로 확인한다.
	openErr error

	// OnOpenError 는 늦춰 연 파일 열기가 실패했을 때 한 번 불린다(녹화는 이미 꺼진 상태).
	// 녹화기 잠금을 푼 뒤에 부르므로 콜백 안에서 Recorder 메서드를 불러도 된다.
	// 실패를 화면에 알리지 않으면 HUD는 계속 '녹화 중'으로 보이고 파일은 끝내 생기지 않는다.
	OnOpenError func(path string, err error)
}

// New returns a closed recorder.
func New() *Recorder { return &Recorder{} }

// Start arms recording at path. 파일은 **첫 확정 자막이 들어올 때** 만들어진다.
//
//   - append=false: 기존 내용을 지우고 새로 쓴다(O_TRUNC).
//   - append=true : 없으면 생성, 있으면 끝에 이어붙인다(O_APPEND).
//
// 멱등: 이미 켜져 있으면 정리하고 새 경로로 다시 켠다. 이 시점에는 디스크를 건드리지 않으므로
// 경로 오류는 첫 기록에서 드러난다(OpenError로 확인).
func (r *Recorder) Start(path string, appendMode bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 멱등: 이미 열려 있으면 정리 후 새로 연다.
	if r.open {
		r.closeLocked()
	}

	r.path = path
	r.appendMode = appendMode
	r.armed = true
	r.openErr = nil
	return nil
}

// openLocked 는 예약해 둔 경로에 실제로 파일을 만들고 세션 헤더를 기록한다.
// 이미 열려 있거나 켜져 있지 않으면 아무것도 하지 않는다. 호출자는 mu를 보유한다.
func (r *Recorder) openLocked() bool {
	if r.open {
		return true
	}
	if !r.armed || r.path == "" {
		return false
	}
	var (
		f   *os.File
		err error
	)
	if r.appendMode {
		f, err = os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	} else {
		// 새로 쓰기여도 기존 파일을 지우지 않는다. 같은 이름이 있으면(1초 안에 껐다 켠 경우 등)
		// 이름 뒤에 -2, -3 … 을 붙여 새 파일을 만든다.
		f, err = createUnique(r.path)
		if err == nil {
			r.path = f.Name()
		}
	}
	if err != nil {
		// 같은 오류로 매 줄마다 재시도하지 않도록 켜짐 상태를 내린다.
		r.openErr = err
		r.armed = false
		return false
	}
	r.file = f
	r.open = true
	r.writeRawLocked("===== liveTranslate 녹화 시작 " + time.Now().Format("2006-01-02 15:04:05") + " =====\n")
	return true
}

// Open creates the file now(녹화를 켜는 순간). 늦춰 열기를 기다리지 않고 바로 파일을 만들어
// 사용자가 녹화가 동작하는지 폴더에서 바로 확인할 수 있게 한다. 실패하면 녹화는 꺼지고 이유를 돌려준다.
func (r *Recorder) Open() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.openLocked() {
		if r.openErr != nil {
			return "", r.openErr
		}
		return "", errors.New("recording: 녹화가 켜져 있지 않습니다")
	}
	return r.path, nil
}

// createUnique creates path, or base-2.ext, base-3.ext … if it already exists (O_EXCL).
func createUnique(path string) (*os.File, error) {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 1; i <= 999; i++ {
		p := path
		if i > 1 {
			p = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("recording: 같은 이름의 파일이 너무 많습니다: %s", path)
}

// Path returns the file path in use (열린 뒤에는 실제 만든 파일 경로). 꺼져 있으면 "".
func (r *Recorder) Path() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.path
}

// Waiting reports whether recording is on but the file is not created yet(첫 확정 자막 대기).
func (r *Recorder) Waiting() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.armed && !r.open
}

// OpenError returns the reason the deferred file open failed (없으면 nil).
func (r *Recorder) OpenError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.openErr
}

// WriteLine records one confirmed subtitle line (타임스탬프 + 원문 + 번역문).
// 닫혀 있으면 조용히 무시한다. source가 공백뿐이면 번역문만 기록한다.
func (r *Recorder) WriteLine(ts time.Time, source, translation string) {
	r.mu.Lock()
	// 켜져 있는데 아직 파일이 없으면 지금 만든다(늦춰 열기).
	wasArmed := r.armed && !r.open
	failPath := r.path
	if !r.openLocked() {
		// 이번 호출에서 열기가 실패했다면(켜져 있었는데 지금 꺼졌다) 잠금을 푼 뒤 알린다.
		err, cb := r.openErr, r.OnOpenError
		failed := wasArmed && !r.armed && err != nil
		r.mu.Unlock()
		if failed && cb != nil {
			cb(failPath, err)
		}
		return
	}
	defer r.mu.Unlock()
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
	// 켜져만 있고 한 줄도 안 쓴 상태면 만들 파일도 없다 — 예약만 취소한다.
	r.armed = false
	r.path = ""
	if !r.open {
		return nil
	}
	r.writeRawLocked("===== 녹화 종료 " + time.Now().Format("2006-01-02 15:04:05") + " =====\n")
	return r.closeLocked()
}

// IsRecording reports whether recording is on — 파일이 열려 있거나, 켜진 채로 첫 자막을
// 기다리는 중이면 true다(사용자에게는 둘 다 '녹화 중'이다).
func (r *Recorder) IsRecording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.open || r.armed
}

// closeLocked closes the handle and clears state. 호출자는 mu를 보유한다.
func (r *Recorder) closeLocked() error {
	var err error
	if r.file != nil {
		err = r.file.Close()
	}
	r.file = nil
	r.open = false
	r.armed = false
	r.path = ""
	return err
}

// writeRawLocked writes UTF-8 text; write failures are ignored(best-effort). 호출자는 mu를 보유한다.
func (r *Recorder) writeRawLocked(text string) {
	if r.file == nil {
		return
	}
	_, _ = r.file.WriteString(text)
}
