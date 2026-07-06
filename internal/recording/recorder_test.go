package recording

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

// 원문 있음/없음 형식 + 타임스탬프.
func TestWriteLineFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.txt")
	r := New()
	if err := r.Start(path, false); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !r.IsRecording() {
		t.Fatal("IsRecording should be true after Start")
	}
	ts := time.Date(2026, 7, 6, 13, 5, 9, 0, time.UTC)
	r.WriteLine(ts, "hello", "안녕")
	r.WriteLine(ts, "   ", "번역만") // 공백 원문 → 번역만
	if err := r.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if r.IsRecording() {
		t.Fatal("IsRecording should be false after Stop")
	}
	got := read(t, path)
	if !strings.Contains(got, "[13:05:09] hello → 안녕\n") {
		t.Errorf("missing 원문→번역 line:\n%s", got)
	}
	if !strings.Contains(got, "[13:05:09] 번역만\n") {
		t.Errorf("missing 번역-only line:\n%s", got)
	}
	if !strings.Contains(got, "녹화 시작") || !strings.Contains(got, "녹화 종료") {
		t.Errorf("missing header/footer:\n%s", got)
	}
}

// 닫힌 상태 WriteLine은 무시된다.
func TestWriteLineWhenClosed(t *testing.T) {
	r := New()
	r.WriteLine(time.Now(), "x", "y") // no panic, no file
	if r.IsRecording() {
		t.Fatal("closed recorder must not be recording")
	}
}

// append=false는 새로쓰기(기존 내용 제거), append=true는 이어붙이기.
func TestAppendVsTruncate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.txt")
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	r := New()
	_ = r.Start(path, false)
	r.WriteLine(ts, "", "첫세션")
	_ = r.Stop()

	// append=true: 첫세션 내용이 남아있어야 한다.
	_ = r.Start(path, true)
	r.WriteLine(ts, "", "둘째세션")
	_ = r.Stop()
	got := read(t, path)
	if !strings.Contains(got, "첫세션") || !strings.Contains(got, "둘째세션") {
		t.Errorf("append should keep prior content:\n%s", got)
	}

	// append=false: 기존 내용이 사라지고 새 내용만.
	_ = r.Start(path, false)
	r.WriteLine(ts, "", "새로쓰기")
	_ = r.Stop()
	got = read(t, path)
	if strings.Contains(got, "첫세션") {
		t.Errorf("truncate should drop prior content:\n%s", got)
	}
	if !strings.Contains(got, "새로쓰기") {
		t.Errorf("truncate should keep new content:\n%s", got)
	}
}

// Start 재호출(멱등): 이전 핸들을 닫고 새로 연다.
func TestStartIdempotent(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "a.txt")
	p2 := filepath.Join(dir, "b.txt")
	r := New()
	_ = r.Start(p1, false)
	if err := r.Start(p2, false); err != nil {
		t.Fatalf("re-start: %v", err)
	}
	r.WriteLine(time.Now(), "", "second")
	_ = r.Stop()
	if !strings.Contains(read(t, p2), "second") {
		t.Error("second file should have content")
	}
}
