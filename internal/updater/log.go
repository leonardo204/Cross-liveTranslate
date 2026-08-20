package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cross-livetranslate/internal/txlog"
)

// LogPath is the diagnostic log file used by the self-update flow.
// Same file the Windows swap helper appends to (%TEMP%\cross-livetranslate-update.log).
func LogPath() string {
	return filepath.Join(os.TempDir(), "cross-livetranslate-update.log")
}

// Logf appends a timestamped diagnostic line. Written by the app process
// itself (not a child), so it is produced even if a spawned helper never runs.
// Best-effort: never fails the update on logging errors.
func Logf(format string, a ...any) {
	f, err := os.OpenFile(LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	prefix := time.Now().Format(time.RFC3339) + "  [app] "
	fmt.Fprintf(f, prefix+format+"\n", a...)
}

// logBoth 는 업데이터 진단 로그와 트랜잭션 로그(~/.liveTranslate/transactions.log)에 함께
// 남긴다. 설치 위치 판정처럼 "나중에 사용자가 로그 하나만 보내와도 원인을 알 수 있어야 하는"
// 결정적 사건에 쓴다(자동 업데이트가 어디에 설치했는지가 대표적).
func logBoth(tag, format string, a ...any) {
	Logf(format, a...)
	txlog.Logf(tag, format, a...)
}

// txlogPath 는 트랜잭션 로그 파일 경로를 반환한다(없으면 ""). detached 헬퍼 스크립트가
// 같은 파일에 결과를 덧붙일 수 있도록 경로만 넘겨준다.
func txlogPath() string {
	dir, err := txlog.Dir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, txlog.FileName)
}
