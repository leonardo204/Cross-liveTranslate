package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
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
