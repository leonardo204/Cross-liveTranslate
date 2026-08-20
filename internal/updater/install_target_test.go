package updater

import (
	"strings"
	"testing"
)

// writableSet 은 지정한 디렉토리들만 쓰기 가능하다고 보고하는 테스트용 프로브다.
func writableSet(dirs ...string) func(string) bool {
	set := map[string]bool{}
	for _, d := range dirs {
		set[d] = true
	}
	return func(dir string) bool { return set[dir] }
}

const (
	tmpRoot   = "/private/var/folders/z2/xxxx/T"
	transApp  = tmpRoot + "/AppTranslocation/2E9A-UUID/d/cross-livetranslate.app"
	stagedApp = tmpRoot + "/cross-livetranslate-update-4237585455/cross-livetranslate.app"
)

// baseEnv 는 매 케이스의 공통 뼈대다(WritableDir만 케이스별로 바꾼다).
func baseEnv() installEnv {
	return installEnv{
		NewAppName:      "cross-livetranslate.app",
		ApplicationsDir: "/Applications",
		TempDir:         tmpRoot,
	}
}

// --- 1) 정상 설치본 → 현재 위치 교체 ---

func TestPlanInstall_InPlace(t *testing.T) {
	env := baseEnv()
	env.CurrentApp = "/Applications/cross-livetranslate.app"
	env.WritableDir = writableSet("/Applications")

	plan, err := planInstall(env)
	if err != nil {
		t.Fatalf("planInstall error = %v", err)
	}
	if plan.Mode != installInPlace {
		t.Fatalf("Mode = %v, want in-place", plan.Mode)
	}
	if plan.Target != env.CurrentApp {
		t.Fatalf("Target = %q, want %q", plan.Target, env.CurrentApp)
	}
}

// 사용자가 이름을 바꾼 번들도 그 자리/그 이름을 유지한다.
func TestPlanInstall_InPlaceKeepsRenamedBundle(t *testing.T) {
	env := baseEnv()
	env.CurrentApp = "/Applications/자막번역기.app"
	env.WritableDir = writableSet("/Applications")

	plan, err := planInstall(env)
	if err != nil {
		t.Fatalf("planInstall error = %v", err)
	}
	if plan.Target != "/Applications/자막번역기.app" {
		t.Fatalf("Target = %q — 사용자가 바꾼 이름을 유지해야 한다", plan.Target)
	}
}

// --- 2) translocated + 원본 해석 성공 + 쓰기 가능 → 원본 교체 ---

func TestPlanInstall_TranslocatedUsesOriginal(t *testing.T) {
	env := baseEnv()
	env.CurrentApp = transApp
	env.Translocated = true
	env.OriginalPath = "/Users/me/Downloads/cross-livetranslate.app"
	env.WritableDir = writableSet("/Users/me/Downloads", "/Applications")

	plan, err := planInstall(env)
	if err != nil {
		t.Fatalf("planInstall error = %v", err)
	}
	if plan.Mode != installOriginal {
		t.Fatalf("Mode = %v, want original", plan.Mode)
	}
	if plan.Target != env.OriginalPath {
		t.Fatalf("Target = %q, want %q", plan.Target, env.OriginalPath)
	}
}

// --- 3) translocated + 원본 해석 실패 → /Applications 이설 ---

func TestPlanInstall_TranslocatedNoOriginalGoesToApplications(t *testing.T) {
	env := baseEnv()
	env.CurrentApp = transApp
	env.Translocated = true
	env.OriginalPath = "" // SPI 해석 실패
	env.WritableDir = writableSet("/Applications")

	plan, err := planInstall(env)
	if err != nil {
		t.Fatalf("planInstall error = %v", err)
	}
	if plan.Mode != installApplications {
		t.Fatalf("Mode = %v, want applications", plan.Mode)
	}
	if plan.Target != "/Applications/cross-livetranslate.app" {
		t.Fatalf("Target = %q", plan.Target)
	}
	if !strings.Contains(plan.Reason, "해석 실패") {
		t.Fatalf("Reason = %q — 원인이 드러나야 한다", plan.Reason)
	}
}

// 원본이 읽기전용(DMG 마운트에서 직접 실행) → /Applications 이설.
func TestPlanInstall_TranslocatedReadOnlyOriginalGoesToApplications(t *testing.T) {
	env := baseEnv()
	env.CurrentApp = transApp
	env.Translocated = true
	env.OriginalPath = "/Volumes/Cross-liveTranslate 1.5.1/cross-livetranslate.app"
	env.WritableDir = writableSet("/Applications") // DMG 볼륨은 쓰기 불가

	plan, err := planInstall(env)
	if err != nil {
		t.Fatalf("planInstall error = %v", err)
	}
	if plan.Mode != installApplications {
		t.Fatalf("Mode = %v, want applications", plan.Mode)
	}
	if plan.Target != "/Applications/cross-livetranslate.app" {
		t.Fatalf("Target = %q", plan.Target)
	}
}

// 현재 위치가 읽기전용이면 translocation이 아니어도 /Applications로 간다.
func TestPlanInstall_ReadOnlyCurrentGoesToApplications(t *testing.T) {
	env := baseEnv()
	env.CurrentApp = "/Volumes/설치디스크/cross-livetranslate.app"
	env.WritableDir = writableSet("/Applications")

	plan, err := planInstall(env)
	if err != nil {
		t.Fatalf("planInstall error = %v", err)
	}
	if plan.Mode != installApplications {
		t.Fatalf("Mode = %v, want applications", plan.Mode)
	}
}

// --- 핵심 회귀: 임시 폴더는 절대 설치/실행 대상이 되지 않는다 ---

func TestPlanInstall_NeverTargetsTempDir(t *testing.T) {
	cases := []struct {
		name string
		env  func() installEnv
	}{
		{
			// 이전 버그로 임시 폴더 앱이 실행 중인 상태 → 스스로 /Applications로 복구.
			name: "임시 폴더에서 실행 중",
			env: func() installEnv {
				e := baseEnv()
				e.CurrentApp = stagedApp
				e.WritableDir = func(dir string) bool { return true } // 임시 폴더도 쓰기 가능
				return e
			},
		},
		{
			// SPI가 임시 폴더 경로를 원본이라고 답해도 받아들이지 않는다.
			name: "원본 해석이 임시 폴더를 가리킴",
			env: func() installEnv {
				e := baseEnv()
				e.CurrentApp = transApp
				e.Translocated = true
				e.OriginalPath = stagedApp
				e.WritableDir = func(dir string) bool { return true }
				return e
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan, err := planInstall(c.env())
			if err != nil {
				t.Fatalf("planInstall error = %v", err)
			}
			if isUnder(plan.Target, tmpRoot) {
				t.Fatalf("Target = %q — 임시 폴더가 설치 대상이 됐다(회귀!)", plan.Target)
			}
			if plan.Target != "/Applications/cross-livetranslate.app" {
				t.Fatalf("Target = %q, want /Applications 이설", plan.Target)
			}
		})
	}
}

// --- 4) 설치할 곳이 없으면 에러(업데이트 중단, 기존 앱 유지) ---

func TestPlanInstall_NoWritableLocationFails(t *testing.T) {
	env := baseEnv()
	env.CurrentApp = transApp
	env.Translocated = true
	env.OriginalPath = "/Volumes/DMG/cross-livetranslate.app"
	env.WritableDir = writableSet() // 어디에도 못 쓴다

	plan, err := planInstall(env)
	if err == nil {
		t.Fatalf("err = nil, want 실패 (plan=%+v)", plan)
	}
	if !strings.Contains(err.Error(), "/Applications") {
		t.Fatalf("에러 메시지에 해결 안내가 없다: %v", err)
	}
	if plan.Target != "" {
		t.Fatalf("실패인데 Target이 설정됐다: %q", plan.Target)
	}
}

// --- 경로 판정 유틸 ---

func TestIsTranslocatedPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{transApp, true},
		{"/var/folders/z2/xxxx/T/AppTranslocation/UUID/d/app.app", true},
		{"/Applications/cross-livetranslate.app", false},
		{"/Users/me/Downloads/cross-livetranslate.app", false},
		{stagedApp, false}, // 임시 폴더지만 translocation은 아니다
		{"", false},
	}
	for _, c := range cases {
		if got := isTranslocatedPath(c.path); got != c.want {
			t.Fatalf("isTranslocatedPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestIsUnder(t *testing.T) {
	cases := []struct {
		path, root string
		want       bool
	}{
		{stagedApp, tmpRoot, true},
		{tmpRoot, tmpRoot, true},
		// **회귀**: TMPDIR 미설정 환경에서 os.TempDir()이 "/tmp"를 준다. 루트가 슬래시 없이
		// 정확히 "/tmp"·"/var"여도 정규화돼야 임시 폴더 차단선이 뚫리지 않는다.
		{"/tmp/x/app.app", "/tmp", true},
		{"/private/tmp/x/app.app", "/tmp", true},
		{"/tmp/x/app.app", "/private/tmp", true},
		{"/var/folders/z2/x/T/app.app", "/var", true},
		{"/private/var/folders/z2/x/T/app.app", "/var", true},
		// 접두사만 같은 다른 최상위 디렉토리는 아래가 아니다.
		{"/tmpfoo/app.app", "/tmp", false},
		{"/variable/app.app", "/var", false},
		// macOS의 /var → /private/var 심볼릭 링크를 정규화해 비교한다.
		{"/var/folders/z2/x/T/app.app", "/private/var/folders/z2/x/T", true},
		{"/private/var/folders/z2/x/T/app.app", "/var/folders/z2/x/T", true},
		{"/Applications/app.app", tmpRoot, false},
		// 접두사만 같은 형제 디렉토리는 아래가 아니다.
		{"/private/var/folders/z2/x/Two/app.app", "/private/var/folders/z2/x/T", false},
		{"/Applications/app.app", "", false},
		{"", tmpRoot, false},
	}
	for _, c := range cases {
		if got := isUnder(c.path, c.root); got != c.want {
			t.Fatalf("isUnder(%q, %q) = %v, want %v", c.path, c.root, got, c.want)
		}
	}
}

func TestInstallModeString(t *testing.T) {
	for _, m := range []installMode{installInPlace, installOriginal, installApplications} {
		if s := m.String(); s == "" {
			t.Fatalf("installMode(%d).String() 이 비었다", m)
		}
	}
}

// **회귀(major)**: TMPDIR 미설정 환경(os.TempDir()=="/tmp")에서도 임시 폴더가 설치 대상이
// 되어선 안 된다. 예전 normalizePath는 "/tmp/"(슬래시 포함)만 치환해 루트가 정확히 "/tmp"면
// 정규화되지 않았고, /private/tmp 아래에서 실행 중인 앱이 그대로 설치 대상이 됐다.
func TestPlanInstall_BareTmpRootStillBlocked(t *testing.T) {
	for _, tempRoot := range []string{"/tmp", "/private/tmp", "/var/folders/z2/x/T"} {
		t.Run(tempRoot, func(t *testing.T) {
			env := baseEnv()
			env.TempDir = tempRoot
			env.CurrentApp = "/private/tmp/cross-livetranslate-update-42/cross-livetranslate.app"
			if tempRoot == "/var/folders/z2/x/T" {
				env.CurrentApp = "/private/var/folders/z2/x/T/update-42/cross-livetranslate.app"
			}
			env.WritableDir = func(string) bool { return true } // 임시 폴더도 쓰기 가능

			plan, err := planInstall(env)
			if err != nil {
				t.Fatalf("planInstall error = %v", err)
			}
			if plan.Target != "/Applications/cross-livetranslate.app" {
				t.Fatalf("Target = %q — 임시 폴더 차단선이 뚫렸다(TempDir=%q)", plan.Target, tempRoot)
			}
		})
	}
}

// translocation 원본이 이미 삭제됐으면(사용자가 Downloads의 앱을 지움) 그 자리에 앱을
// 되살리지 않고 /Applications로 이설한다.
func TestPlanInstall_MissingOriginalGoesToApplications(t *testing.T) {
	env := baseEnv()
	env.CurrentApp = transApp
	env.Translocated = true
	env.OriginalPath = "/Users/me/Downloads/cross-livetranslate.app"
	env.WritableDir = writableSet("/Users/me/Downloads", "/Applications")
	env.Exists = func(string) bool { return false } // 원본이 사라짐

	plan, err := planInstall(env)
	if err != nil {
		t.Fatalf("planInstall error = %v", err)
	}
	if plan.Mode != installApplications {
		t.Fatalf("Mode = %v, want applications (삭제된 원본을 되살리면 안 된다)", plan.Mode)
	}
	if plan.Target != "/Applications/cross-livetranslate.app" {
		t.Fatalf("Target = %q", plan.Target)
	}
}

// 원본이 존재하면(기본 경로) 그대로 원본을 교체한다 — Exists 주입이 정상 경로를 막지 않는다.
func TestPlanInstall_ExistingOriginalStillUsed(t *testing.T) {
	env := baseEnv()
	env.CurrentApp = transApp
	env.Translocated = true
	env.OriginalPath = "/Users/me/Downloads/cross-livetranslate.app"
	env.WritableDir = writableSet("/Users/me/Downloads", "/Applications")
	env.Exists = func(p string) bool { return p == "/Users/me/Downloads/cross-livetranslate.app" }

	plan, err := planInstall(env)
	if err != nil {
		t.Fatalf("planInstall error = %v", err)
	}
	if plan.Mode != installOriginal {
		t.Fatalf("Mode = %v, want original", plan.Mode)
	}
}
