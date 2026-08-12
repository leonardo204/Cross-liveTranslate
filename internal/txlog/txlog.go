// Package txlog — 자막 파이프라인 전 구간의 트랜잭션(진단) 로그.
//
// 왜 별도 로그인가:
//
//	기존 role 로그(~/Library/Logs/Cross-liveTranslate/<role>.log, main.go setupFileLogging)는
//	프로세스 진단(패닉/stderr 포함)이 목적이고 플랫폼별 경로를 쓴다. 반면 이 로그는
//	"오디오 → gemini 수신 → pipeline 이벤트 → 자막 엔진 결정 → 오버레이 push"의 인과
//	사슬을 한 파일에서 시간순으로 따라가기 위한 것이다. 사용자가 바로 찾아 첨부할 수 있도록
//	홈 아래 고정 경로(~/.liveTranslate/transactions.log)에 남긴다(Windows는 %USERPROFILE%).
//
// # 형식
//
//	2026-08-12 14:03:02.123 [controller] [gemini.rx] turnComplete 수신
//	└ 타임스탬프(ms)          └ role      └ 태그      └ 내용
//
// 태그 프리픽스로 grep 한다(gemini.conn / gemini.rx / pipe.event / engine.* / subtitle.push /
// vad.gate / session). 레코드는 항상 한 줄이다(내용의 개행은 "\n" 리터럴로 escape).
//
// # 설계 제약
//
//   - 순수 Go(cgo·플랫폼 분기 없음) — windows 크로스빌드 그대로 컴파일된다.
//   - 단일 mutex writer, fsync 없음, 포맷팅 최소 — 오디오 경로를 블로킹하지 않는다.
//   - 실패(홈 디렉토리 없음/권한 등)는 조용히 no-op으로 강등한다. nil *Logger 도 안전하다.
//   - 순수 상태머신(internal/subtitle, internal/vad)은 이 패키지를 import 하지 않는다.
//     대신 콜백 필드(Logf)를 노출하고 controller가 주입한다.
package txlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// DirName 은 홈 아래 트랜잭션 로그 디렉토리 이름(사용자 지정 경로).
	DirName = ".liveTranslate"
	// FileName 은 트랜잭션 로그 파일 이름.
	FileName = "transactions.log"
	// DefaultMaxBytes 를 넘으면 <FileName>.1 로 밀고 새 파일을 시작한다(1세대만 보관).
	DefaultMaxBytes = 10 << 20 // 10MiB
	// timeLayout 은 ms 단위 타임스탬프.
	timeLayout = "2006-01-02 15:04:05.000"
)

// Logger 는 트랜잭션 로그 writer다. nil 리시버는 모든 메서드가 no-op이므로
// 호출부에서 nil 검사를 하지 않아도 된다(로깅 실패 시 무력화 계약).
type Logger struct {
	mu       sync.Mutex
	f        *os.File // nil이면 무력화 상태.
	path     string
	role     string
	size     int64
	maxBytes int64
}

// Dir 은 트랜잭션 로그 디렉토리 경로(~/.liveTranslate)를 반환한다. 생성은 하지 않는다.
// Windows에서는 %USERPROFILE%\.liveTranslate 가 된다(os.UserHomeDir 계약).
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, DirName), nil
}

// Open 은 ~/.liveTranslate/transactions.log 를 append 모드로 열고 세션 헤더를 남긴다.
// 실패하면 nil을 반환한다(호출부는 그대로 써도 안전 — 모든 메서드가 no-op).
func Open(role, version string) *Logger {
	dir, err := Dir()
	if err != nil {
		return nil
	}
	return openIn(dir, role, version, DefaultMaxBytes)
}

// openIn 은 Open의 테스트 가능한 알맹이다(디렉토리/로테이션 임계 주입).
func openIn(dir, role, version string, maxBytes int64) *Logger {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	path := filepath.Join(dir, FileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	var size int64
	if st, serr := f.Stat(); serr == nil {
		size = st.Size()
	}
	l := &Logger{f: f, path: path, role: role, size: size, maxBytes: maxBytes}
	l.Logf("session", "──── 세션 시작 role=%s version=%s pid=%d ────", role, version, os.Getpid())
	return l
}

// Logf 는 태그가 붙은 레코드 한 줄을 기록한다. args가 없으면 format을 그대로 쓴다
// (% 문자 오해석 방지). 내용의 개행은 escape되어 항상 1레코드=1줄이 보장된다.
func (l *Logger) Logf(tag, format string, args ...any) {
	if l == nil {
		return
	}
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	var b strings.Builder
	b.Grow(len(msg) + 64)
	b.WriteString(time.Now().Format(timeLayout))
	b.WriteString(" [")
	b.WriteString(l.role)
	b.WriteString("] [")
	b.WriteString(tag)
	b.WriteString("] ")
	b.WriteString(escapeNewlines(msg))
	b.WriteByte('\n')
	l.write(b.String())
}

// Enabled 는 실제로 파일에 기록되는 상태인지 보고한다(테스트/조건부 비용 회피용).
func (l *Logger) Enabled() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f != nil
}

// Path 는 기록 중인 파일 경로를 반환한다(무력화 상태면 "").
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Close 는 파일을 닫는다. 이후 호출은 no-op이다.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// write 는 한 레코드를 기록한다. 임계 초과 시 먼저 로테이션한다. 쓰기 에러가 나면
// 조용히 무력화한다(앱 동작에 영향 금지).
func (l *Logger) write(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	if l.maxBytes > 0 && l.size+int64(len(s)) > l.maxBytes {
		l.rotateLocked()
		if l.f == nil {
			return
		}
	}
	n, err := l.f.WriteString(s)
	l.size += int64(n)
	if err != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

// rotateLocked 는 현재 파일을 <path>.1 로 밀고 새 파일을 연다(1세대 보관).
// 다른 프로세스가 이미 밀었을 수 있으므로 실제 크기를 다시 확인한다.
func (l *Logger) rotateLocked() {
	if st, err := os.Stat(l.path); err == nil {
		l.size = st.Size()
		if l.size < l.maxBytes {
			return // 다른 프로세스가 이미 로테이션함 — 그대로 이어 쓴다.
		}
	}
	_ = l.f.Close()
	l.f = nil
	_ = os.Remove(l.path + ".1")
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		// 밀지 못했으면 원래 파일에 계속 쓰는 편이 낫다(로그 유실 방지).
		if f, oerr := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); oerr == nil {
			l.f = f
		}
		return
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return // 무력화(f=nil).
	}
	l.f = f
	l.size = 0
}

// escapeNewlines 는 개행/캐리지리턴을 리터럴로 바꿔 레코드가 여러 줄로 쪼개지지 않게 한다.
func escapeNewlines(s string) string {
	if !strings.ContainsAny(s, "\n\r") {
		return s
	}
	r := strings.NewReplacer("\r\n", "\\n", "\n", "\\n", "\r", "\\n")
	return r.Replace(s)
}

// -----------------------------------------------------------------------------
// 프로세스 기본 로거 (하위 시스템이 배선 없이 쓰는 경로)
// -----------------------------------------------------------------------------

var (
	defMu sync.RWMutex
	def   *Logger
)

// Init 은 프로세스 기본 로거를 연다(main에서 role 확정 직후 1회). 반환값은 Close용이며
// 실패 시 nil이다 — 이후 패키지 레벨 Logf는 조용히 no-op이 된다.
func Init(role, version string) *Logger {
	l := Open(role, version)
	defMu.Lock()
	def = l
	defMu.Unlock()
	return l
}

// SetDefault 는 기본 로거를 교체한다(테스트/특수 배선용).
func SetDefault(l *Logger) {
	defMu.Lock()
	def = l
	defMu.Unlock()
}

// Default 는 현재 기본 로거를 반환한다(미초기화면 nil — 호출은 안전).
func Default() *Logger {
	defMu.RLock()
	defer defMu.RUnlock()
	return def
}

// Logf 는 기본 로거에 레코드를 남긴다(미초기화면 no-op).
func Logf(tag, format string, args ...any) { Default().Logf(tag, format, args...) }

// Enabled 는 기본 로거가 실제로 기록 중인지 보고한다.
func Enabled() bool { return Default().Enabled() }

// Close 는 기본 로거를 닫는다.
func Close() error { return Default().Close() }
