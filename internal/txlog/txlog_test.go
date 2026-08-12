package txlog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// readLog 는 테스트 디렉토리의 로그 본문을 읽는다.
func readLog(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// --- 파일 생성 + 세션 헤더 ---

func TestOpenCreatesFileWithSessionHeader(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested") // MkdirAll 경로도 함께 검증.
	l := openIn(dir, "controller", "1.4.5", DefaultMaxBytes)
	if l == nil {
		t.Fatal("openIn = nil, want logger")
	}
	defer l.Close()
	if !l.Enabled() {
		t.Fatal("Enabled = false, want true")
	}
	if got, want := l.Path(), filepath.Join(dir, FileName); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}

	body := readLog(t, dir, FileName)
	line := strings.TrimRight(body, "\n")
	if strings.Count(line, "\n") != 0 {
		t.Fatalf("헤더는 1줄이어야 한다: %q", body)
	}
	for _, want := range []string{"[controller]", "[session]", "role=controller", "version=1.4.5", "pid="} {
		if !strings.Contains(line, want) {
			t.Fatalf("헤더에 %q 없음: %q", want, line)
		}
	}
	// 타임스탬프(ms)가 앞에 붙는다: "2006-01-02 15:04:05.000 ".
	if len(line) < 23 || line[4] != '-' || line[13] != ':' || line[19] != '.' {
		t.Fatalf("타임스탬프 형식 불일치: %q", line)
	}
}

// --- append(재오픈 시 기존 내용 보존) + 레코드 포맷 ---

func TestLogfAppendsAndFormats(t *testing.T) {
	dir := t.TempDir()
	l := openIn(dir, "controller", "dev", DefaultMaxBytes)
	l.Logf("gemini.rx", "turnComplete 수신 buffered=%d", 3)
	l.Logf("engine.turn", "여러 줄\n포함 텍스트")
	l.Logf("plain", "포맷 인자 없음 100%% 안전")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 재오픈은 append여야 한다(기존 레코드 보존 + 새 세션 헤더 추가).
	l2 := openIn(dir, "overlay", "dev", DefaultMaxBytes)
	l2.Logf("session", "두 번째 세션 기록")
	defer l2.Close()

	body := readLog(t, dir, FileName)
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) != 6 { // 헤더 + 3 + 헤더 + 1
		t.Fatalf("레코드 수 = %d, want 6\n%s", len(lines), body)
	}
	if !strings.Contains(lines[1], "[controller] [gemini.rx] turnComplete 수신 buffered=3") {
		t.Fatalf("레코드 포맷 불일치: %q", lines[1])
	}
	// 개행은 escape되어 한 줄로 남는다(grep 가능성 보장).
	if !strings.Contains(lines[2], `여러 줄\n포함 텍스트`) {
		t.Fatalf("개행 escape 실패: %q", lines[2])
	}
	// args 없이 호출하면 format을 그대로 쓴다(%% 그대로 유지).
	if !strings.Contains(lines[3], "포맷 인자 없음 100%% 안전") {
		t.Fatalf("무인자 포맷 처리 불일치: %q", lines[3])
	}
	if !strings.Contains(lines[4], "[overlay] [session]") {
		t.Fatalf("두 번째 세션 헤더 role 불일치: %q", lines[4])
	}
}

// --- 로테이션: 임계 초과 시 .1로 밀고 새 파일 시작(1세대 보관) ---

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	l := openIn(dir, "controller", "dev", 512) // 작은 임계로 로테이션 강제.
	defer l.Close()

	payload := strings.Repeat("A", 100)
	for i := 0; i < 20; i++ {
		l.Logf("fill", "%02d %s", i, payload)
	}

	cur := readLog(t, dir, FileName)
	if int64(len(cur)) > 512 {
		t.Fatalf("현재 파일이 임계를 넘었다: %d bytes", len(cur))
	}
	old := readLog(t, dir, FileName+".1")
	if len(old) == 0 {
		t.Fatal(".1 백업이 비어 있다")
	}
	// 마지막 레코드는 반드시 현재 파일에 있어야 한다.
	if !strings.Contains(cur, "fill] 19 ") {
		t.Fatalf("마지막 레코드가 현재 파일에 없다:\n%s", cur)
	}
	// 1세대만 보관 — .2는 생기지 않는다.
	if _, err := os.Stat(filepath.Join(dir, FileName+".2")); !os.IsNotExist(err) {
		t.Fatalf(".2 백업이 존재하면 안 된다 (err=%v)", err)
	}
}

// --- nil 로거/무력화 상태: 패닉 없이 no-op ---

func TestNilLoggerIsNoop(t *testing.T) {
	var l *Logger
	l.Logf("tag", "패닉 없이 무시되어야 한다 %d", 1)
	if l.Enabled() {
		t.Fatal("nil Logger.Enabled = true, want false")
	}
	if l.Path() != "" {
		t.Fatal("nil Logger.Path != \"\"")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("nil Logger.Close = %v, want nil", err)
	}

	// Close 이후에도 기록은 조용히 무시된다.
	dir := t.TempDir()
	l2 := openIn(dir, "controller", "dev", DefaultMaxBytes)
	before := readLog(t, dir, FileName)
	_ = l2.Close()
	l2.Logf("after", "무시되어야 한다")
	if got := readLog(t, dir, FileName); got != before {
		t.Fatalf("Close 후 기록됨:\n%s", got)
	}
}

// --- 패키지 기본 로거: 미초기화 no-op → Init 후 기록 ---

func TestDefaultLogger(t *testing.T) {
	old := Default()
	t.Cleanup(func() { SetDefault(old) })

	SetDefault(nil)
	Logf("tag", "미초기화 상태 — 패닉 없이 무시")
	if Enabled() {
		t.Fatal("미초기화 Enabled = true, want false")
	}

	dir := t.TempDir()
	SetDefault(openIn(dir, "controller", "dev", DefaultMaxBytes))
	defer Close()
	Logf("pipe.event", "TranslatedDelta text=%q", "안녕")
	if !Enabled() {
		t.Fatal("Init 후 Enabled = false")
	}
	if body := readLog(t, dir, FileName); !strings.Contains(body, `[pipe.event] TranslatedDelta text="안녕"`) {
		t.Fatalf("기본 로거 기록 실패:\n%s", body)
	}
}

// --- 동시 기록: 레코드가 섞이거나 유실되지 않는다(-race로 검증) ---

func TestConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	l := openIn(dir, "controller", "dev", DefaultMaxBytes)
	defer l.Close()

	const writers, perWriter = 8, 50
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				l.Logf("concurrent", "writer=%d seq=%d", w, i)
			}
		}(w)
	}
	wg.Wait()

	body := readLog(t, dir, FileName)
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if want := writers*perWriter + 1; len(lines) != want { // +1 세션 헤더
		t.Fatalf("레코드 수 = %d, want %d", len(lines), want)
	}
	for _, ln := range lines {
		if !strings.HasPrefix(ln, "20") { // 모든 줄이 타임스탬프로 시작 = 섞임 없음.
			t.Fatalf("깨진 레코드: %q", ln)
		}
	}
}

// --- Dir(): 홈 아래 고정 경로 ---

func TestDirUnderHome(t *testing.T) {
	dir, err := Dir()
	if err != nil {
		t.Skipf("홈 디렉토리 확인 불가: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("홈 디렉토리 확인 불가: %v", err)
	}
	if want := filepath.Join(home, DirName); dir != want {
		t.Fatalf("Dir = %q, want %q", dir, want)
	}
}
