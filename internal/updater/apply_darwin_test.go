//go:build darwin

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ── 실행 기반 검증 ────────────────────────────────────────────────────────────
//
// 헬퍼는 detached 셸 스크립트라, "문자열이 들어 있나" 식의 정적 검사는 인용 버그를 놓친다
// (실제로 %q 인용은 큰따옴표 안에서 $(...)·백틱이 셸에 해석돼 임의 명령이 실행됐다).
// 그래서 여기서는 **생성된 스크립트를 진짜 sh로 실행**하고, open/ditto/xattr/ps를 스텁으로
// 가로채 인자를 바이트 단위로 회수해 검증한다.

const (
	recSep = "\x1e" // 호출 레코드 구분자
	argSep = "\x00" // 인자 구분자(경로에 탭/공백이 들어가도 안전)
)

// stubTool writes a fake executable that records its argv and optionally runs extra sh.
func stubTool(t *testing.T, dir, name, extra string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\000' %q "$@" >> "$REC"
printf '\036' >> "$REC"
%s
exit 0
`, name, extra)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
}

// helperRun 은 헬퍼 실행 결과다.
type helperRun struct {
	calls map[string][][]string // 도구명 → 호출별 인자 목록
	log   string                // 헬퍼가 남긴 업데이터 로그 내용
}

// runHelper 는 writeHelper가 만든 스크립트를 스텁 PATH에서 실행하고 호출 인자를 회수한다.
func runHelper(t *testing.T, target, source, oldApp string) helperRun {
	t.Helper()

	sandbox := t.TempDir()
	// LogPath()/txlogPath()가 사용자 실제 로그를 건드리지 않도록 HOME/TMPDIR을 격리한다.
	t.Setenv("TMPDIR", sandbox)
	t.Setenv("HOME", sandbox)

	stubs := filepath.Join(sandbox, "stubs")
	if err := os.MkdirAll(stubs, 0o755); err != nil {
		t.Fatalf("mkdir stubs: %v", err)
	}
	rec := filepath.Join(sandbox, "record")

	// ditto 스텁은 실제로 대상 디렉토리를 만들어 이후 단계(quarantine/open)가 진행되게 한다.
	stubTool(t, stubs, "ditto", `mkdir -p "$2"`)
	stubTool(t, stubs, "open", "")
	stubTool(t, stubs, "xattr", "")
	stubTool(t, stubs, "ps", "") // 자식 프로세스 없음(빈 출력)

	// 소스 번들 준비.
	if err := os.MkdirAll(filepath.Join(source, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}

	helper, err := writeHelper(helperParams{
		// 존재하지 않는 PID → 부모 대기 루프가 즉시 빠져나온다.
		ParentPID: 0x7ffffffe,
		Target:    target,
		Source:    source,
		OldApp:    oldApp,
	})
	if err != nil {
		t.Fatalf("writeHelper: %v", err)
	}

	cmd := exec.Command("/bin/sh", helper)
	cmd.Env = append(os.Environ(),
		"PATH="+stubs+":/usr/bin:/bin",
		"REC="+rec,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("헬퍼 실행 실패: %v\n%s", err, out)
	}

	run := helperRun{calls: map[string][][]string{}}
	raw, _ := os.ReadFile(rec)
	for _, r := range strings.Split(string(raw), recSep) {
		fields := strings.Split(strings.TrimSuffix(r, argSep), argSep)
		if len(fields) < 1 || fields[0] == "" {
			continue
		}
		run.calls[fields[0]] = append(run.calls[fields[0]], fields[1:])
	}
	if b, err := os.ReadFile(LogPath()); err == nil {
		run.log = string(b)
	}
	return run
}

// callArgs 는 도구의 n번째 호출 인자를 돌려준다.
func (r helperRun) callArgs(t *testing.T, tool string, n int) []string {
	t.Helper()
	calls := r.calls[tool]
	if len(calls) <= n {
		t.Fatalf("%s 호출이 %d회뿐이다(want > %d): %+v", tool, len(calls), n, r.calls)
	}
	return calls[n]
}

// **핵심 회귀 테스트(blocker)**: 셸 메타문자가 든 경로가 바이트 그대로 전달되고,
// 어떤 명령 치환도 실행되지 않는다.
func TestHelperPathInjectionIsInert(t *testing.T) {
	sandbox := t.TempDir()
	canary := filepath.Join(sandbox, "PWNED")

	// $(...), 백틱, $VAR, 백슬래시, 탭, 공백, 작은/큰따옴표를 모두 담은 악성 경로.
	evil := filepath.Join(sandbox, "ev$(touch "+canary+")il `touch "+canary+"` $HOME back\\slash\ttab 'sq' \"dq\".app")
	source := filepath.Join(sandbox, "staged", "new.app")

	run := runHelper(t, evil, source, evil)

	if _, err := os.Stat(canary); err == nil {
		t.Fatal("경로의 명령 치환이 실행됐다 — 셸 인젝션(회귀!)")
	}

	// ditto/xattr/open 모두 원본 문자열을 **바이트 그대로** 받아야 한다.
	if got := run.callArgs(t, "ditto", 0); len(got) != 2 || got[0] != source || got[1] != evil {
		t.Fatalf("ditto 인자 = %q, want [%q %q]", got, source, evil)
	}
	if got := run.callArgs(t, "xattr", 0); len(got) != 3 ||
		got[0] != "-dr" || got[1] != "com.apple.quarantine" || got[2] != evil {
		t.Fatalf("xattr 인자 = %q, want [-dr com.apple.quarantine %q]", got, evil)
	}
	if got := run.callArgs(t, "open", 0); len(got) != 2 || got[0] != "-n" || got[1] != evil {
		t.Fatalf("open 인자 = %q, want [-n %q]", got, evil)
	}
	// 실제로 그 경로에 설치됐는지(확장된 다른 경로가 아니라).
	if _, err := os.Stat(evil); err != nil {
		t.Fatalf("설치 대상이 생성되지 않았다: %v", err)
	}
}

// 정상 경로: 기존 번들 백업 → ditto → quarantine 제거 → 실행, 그리고 스테이징 정리.
func TestHelperHappyPathReplacesAndCleansUp(t *testing.T) {
	sandbox := t.TempDir()
	target := filepath.Join(sandbox, "Applications", "app.app")
	source := filepath.Join(sandbox, "staged", "app.app")
	if err := os.MkdirAll(filepath.Join(target, "Contents"), 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}

	run := runHelper(t, target, source, target)

	if got := run.callArgs(t, "open", 0); got[1] != target {
		t.Fatalf("open 대상 = %q, want %q", got[1], target)
	}
	// 스테이징(다운로드 임시) 폴더는 성공 후 정리된다.
	if _, err := os.Stat(filepath.Dir(source)); err == nil {
		t.Fatal("스테이징 폴더가 정리되지 않았다")
	}
	// 백업 잔재가 남지 않는다.
	matches, _ := filepath.Glob(target + ".bak.*")
	if len(matches) != 0 {
		t.Fatalf("백업 잔재: %v", matches)
	}
	for _, want := range []string{"ditto 교체 완료", "quarantine 제거 완료", "새 버전 실행 완료"} {
		if !strings.Contains(run.log, want) {
			t.Fatalf("로그에 %q 없음:\n%s", want, run.log)
		}
	}
}

// 이설(TARGET != OLDAPP)이면 로그가 그 사실을 밝힌다(Relocated 플래그 대신 경로 비교).
func TestHelperRelocationLoggedByPathComparison(t *testing.T) {
	sandbox := t.TempDir()
	target := filepath.Join(sandbox, "Applications", "app.app")
	source := filepath.Join(sandbox, "staged", "app.app")
	oldApp := filepath.Join(sandbox, "AppTranslocation", "UUID", "d", "app.app")

	run := runHelper(t, target, source, oldApp)
	if !strings.Contains(run.log, "이설했습니다") {
		t.Fatalf("이설 로그가 없다:\n%s", run.log)
	}
	if !strings.Contains(run.log, oldApp) {
		t.Fatalf("로그에 기존 실행 위치가 없다:\n%s", run.log)
	}
}

// 시작 로그에 BACKUP 경로가 있어야 복원 실패 시 사용자가 자력 복구할 수 있다.
func TestHelperLogsBackupPath(t *testing.T) {
	sandbox := t.TempDir()
	target := filepath.Join(sandbox, "app.app")
	source := filepath.Join(sandbox, "staged", "app.app")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	run := runHelper(t, target, source, target)
	if !strings.Contains(run.log, "BACKUP="+target+".bak.") {
		t.Fatalf("시작 로그에 BACKUP 경로가 없다:\n%s", run.log)
	}
}

// ── 정적 불변식(실행 테스트로 못 보는 폴백 분기용) ──────────────────────────

func helperScriptText(t *testing.T, p helperParams) string {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	path, err := writeHelper(p)
	if err != nil {
		t.Fatalf("writeHelper: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read helper: %v", err)
	}
	if out, err := exec.Command("/bin/sh", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("헬퍼 스크립트 문법 오류: %v\n%s", err, out)
	}
	return string(b)
}

// **핵심 불변식**: $SOURCE(다운로드 임시 폴더)는 "대입 / ditto 복사 / dirname" 세 곳에서만
// 등장한다. 예전 코드는 실패 폴백에서 `open -n "$SOURCE"` 로 임시 폴더 앱을 실행해
// "업데이트 후 임시 폴더에서 실행" 버그를 만들었다.
func TestHelperSourceUsedOnlyForCopy(t *testing.T) {
	script := helperScriptText(t, helperParams{
		ParentPID: 1,
		Target:    "/Applications/app.app",
		Source:    "/private/var/folders/z2/x/T/cross-livetranslate-update-42/app.app",
		OldApp:    "/private/var/folders/z2/x/T/AppTranslocation/UUID/d/app.app",
	})

	allowed := map[string]bool{
		`SOURCE='/private/var/folders/z2/x/T/cross-livetranslate-update-42/app.app'`: true,
		`STAGE_DIR=$(dirname "$SOURCE")`:                                             true,
		`if ditto "$SOURCE" "$TARGET" 2>>"$LOG"; then`:                               true,
	}
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue // 주석에서의 언급은 설명 문구이므로 허용.
		}
		if !strings.Contains(trimmed, "SOURCE") {
			continue
		}
		if !allowed[trimmed] {
			t.Fatalf("SOURCE가 허용되지 않은 위치에서 사용됐다: %q", trimmed)
		}
	}
}

// quarantine 제거는 ditto **이후**여야 한다(ditto가 확장속성을 그대로 복사하므로).
func TestHelperRemovesQuarantineAfterDitto(t *testing.T) {
	script := helperScriptText(t, helperParams{
		ParentPID: 1, Target: "/Applications/a.app", Source: "/tmp/x/a.app", OldApp: "/Applications/a.app",
	})
	di := strings.Index(script, `ditto "$SOURCE"`)
	xi := strings.Index(script, `xattr -dr com.apple.quarantine "$TARGET"`)
	if di < 0 || xi < 0 {
		t.Fatalf("ditto(%d) 또는 quarantine 제거(%d) 단계가 없다", di, xi)
	}
	if di > xi {
		t.Fatal("quarantine 제거가 ditto보다 먼저다 — 복사로 다시 붙는다")
	}
}

// pkill -f(정규식 해석)를 쓰지 않고 바이트 접두사 비교로 자식을 정리해야 한다.
func TestHelperDoesNotUsePkillRegex(t *testing.T) {
	script := helperScriptText(t, helperParams{
		ParentPID: 1, Target: "/Applications/a.app", Source: "/tmp/x/a.app", OldApp: "/tmp/old.app",
	})
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue // 주석에서의 언급(왜 쓰지 않는지 설명)은 허용.
		}
		if strings.Contains(trimmed, "pkill") {
			t.Fatalf("pkill 은 경로를 정규식으로 해석한다 — 접두사 비교로 대체해야 한다: %q", trimmed)
		}
	}
	if !strings.Contains(script, `kill_bundle_procs "$OLDAPP"`) ||
		!strings.Contains(script, `kill_bundle_procs "$TARGET"`) {
		t.Fatal("두 번들(기존 실행/설치 대상)의 자식 정리가 모두 필요하다")
	}
}

// 부모 대기 타임아웃과 정상 종료를 로그로 구분해야 원인 분석이 된다.
func TestHelperDistinguishesParentTimeout(t *testing.T) {
	script := helperScriptText(t, helperParams{
		ParentPID: 1, Target: "/Applications/a.app", Source: "/tmp/x/a.app", OldApp: "/Applications/a.app",
	})
	if !strings.Contains(script, "parent exited") || !strings.Contains(script, "종료 대기 타임아웃") {
		t.Fatal("부모 종료/타임아웃을 구분해 기록해야 한다")
	}
}

// mv는 translocation 마운트 해제 지연(EBUSY)에 대비해 재시도해야 한다.
func TestHelperRetriesMove(t *testing.T) {
	script := helperScriptText(t, helperParams{
		ParentPID: 1, Target: "/Applications/a.app", Source: "/tmp/x/a.app", OldApp: "/tmp/old.app",
	})
	if !strings.Contains(script, "for attempt in 1 2 3 4 5 6 7 8; do") {
		t.Fatal("mv 백오프 재시도가 없다 — 마운트 해제 레이스에서 영구 실패한다")
	}
	if !strings.Contains(script, "백업 복원 실패") {
		t.Fatal("롤백 실패 시 사용자 복구 안내 로그가 없다")
	}
}

// shellQuote 자체의 계약: 어떤 입력도 단일 인용 문자열로 안전하게 감싼다.
func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/Applications/a.app", `'/Applications/a.app'`},
		{"a b", `'a b'`},
		{`$(touch x)`, `'$(touch x)'`},
		{"back`tick`", "'back`tick`'"},
		{`it's`, `'it'\''s'`},
		{"", `''`},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Fatalf("shellQuote(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}
