package updater

// install_target.go — "새 번들을 어디에 설치할 것인가" 결정 로직(순수 Go, 플랫폼 무관).
//
// # 배경(실제 버그)
//
// 자동 업데이트 후 앱이 설치 위치가 아니라 다운로드 임시 폴더
// (/private/var/folders/.../T/cross-livetranslate-update-NNNN/cross-livetranslate.app)에서
// 실행되는 문제가 있었다. 원인은 두 단계다.
//
//  1. 실행 중이던 앱이 **App Translocation** 상태였다. quarantine이 붙은 앱을 Finder로
//     옮기지 않고 실행하면 macOS가 /private/var/folders/.../AppTranslocation/<UUID>/d/ 아래
//     읽기전용 랜덤 마운트에서 실행한다. 이 경로는 덮어쓸 수 없다.
//  2. 그래서 swap 헬퍼가 교체에 실패했고, 폴백으로 **임시 폴더의 새 앱을 직접 실행**했다
//     (`open -n "$SOURCE"`). 임시 폴더는 언제든 청소되므로 다음 실행이 깨진다.
//
// 그래서 설치 대상을 헬퍼 스크립트가 아니라 여기서 **먼저** 결정하고, 임시 폴더는 어떤
// 경우에도 최종 대상이 되지 않도록 막는다.
//
// 이 파일은 cgo/파일시스템에 직접 의존하지 않는다(모든 외부 요소는 installEnv로 주입) —
// 결정 매트릭스 전체를 유닛 테스트로 고정할 수 있다.

import (
	"fmt"
	"path/filepath"
	"strings"
)

// installMode 는 결정된 설치 방식이다(로그/진단용).
type installMode int

const (
	// installInPlace — 현재 실행 중인 번들을 그 자리에서 교체(정상 설치본).
	installInPlace installMode = iota
	// installOriginal — translocation 상태라 SPI로 해석한 **원본** 번들을 교체.
	installOriginal
	// installApplications — 원본을 못 찾거나 쓸 수 없어 /Applications로 이설.
	installApplications
)

func (m installMode) String() string {
	switch m {
	case installOriginal:
		return "original(translocation 원본 경로 교체)"
	case installApplications:
		return "applications(/Applications로 이설)"
	default:
		return "in-place(현재 위치 교체)"
	}
}

// installPlan 은 헬퍼에 넘길 최종 설치 계획이다.
type installPlan struct {
	Mode   installMode
	Target string // 새 번들을 설치할 .app 경로(항상 절대경로, 임시 폴더 밖).
	Reason string // 로그에 남길 결정 근거(한 줄).
}

// installEnv 는 결정에 필요한 외부 상태다. 테스트에서 전부 주입한다.
type installEnv struct {
	// CurrentApp 은 현재 실행 중인 .app 경로(translocated면 그 랜덤 경로).
	CurrentApp string
	// Translocated 는 CurrentApp이 App Translocation 마운트인지.
	Translocated bool
	// OriginalPath 는 translocation SPI가 해석한 원본 .app 경로("" = 해석 실패).
	OriginalPath string
	// NewAppName 은 새로 받은 번들의 이름(예: "cross-livetranslate.app").
	// /Applications로 이설할 때 이 이름을 쓴다(릴리스가 정한 정식 이름).
	NewAppName string
	// ApplicationsDir 는 보통 "/Applications".
	ApplicationsDir string
	// TempDir 는 다운로드/추출에 쓰는 임시 루트(os.TempDir()). 이 아래는 절대 설치 대상이
	// 될 수 없다 — 이번 버그의 재발 방지선이다.
	TempDir string
	// WritableDir 는 해당 디렉토리에 쓰기가 가능한지 보고한다(교체는 부모 디렉토리에서
	// mv/ditto 하므로 번들 자신이 아니라 **부모 디렉토리** 권한이 기준이다).
	WritableDir func(dir string) bool
	// Exists 는 경로가 실제로 존재하는지 보고한다. translocation 원본이 이미 지워진 경우
	// (사용자가 Downloads의 앱을 삭제) 그 자리에 앱을 되살리지 않기 위한 확인이다.
	// nil이면 존재하는 것으로 간주한다.
	Exists func(path string) bool
}

// planInstall 은 결정 매트릭스를 적용한다.
//
//  1. translocation 아님 + 현재 위치 쓰기 가능        → 현재 위치 교체
//  2. translocated + 원본 해석 성공 + 원본 쓰기 가능  → 원본 경로 교체(+quarantine 제거)
//  3. 그 외(해석 실패/읽기전용/임시폴더 실행 중)      → /Applications로 이설(+quarantine 제거)
//  4. /Applications도 쓸 수 없음                      → 에러(업데이트 중단, 기존 앱 유지)
//
// 어떤 분기에서도 TempDir 아래를 대상으로 삼지 않는다.
func planInstall(env installEnv) (installPlan, error) {
	writable := env.WritableDir
	if writable == nil {
		writable = func(string) bool { return false }
	}

	// 1) 정상 설치본: 현재 위치에서 교체. 단 현재 위치가 임시 폴더면(이전 버그로 임시
	//    폴더 앱이 실행 중인 상태) 그 자리를 유지하면 안 되므로 아래 3)으로 흘린다.
	if !env.Translocated && env.CurrentApp != "" &&
		!isUnder(env.CurrentApp, env.TempDir) && writable(filepath.Dir(env.CurrentApp)) {
		return installPlan{
			Mode:   installInPlace,
			Target: env.CurrentApp,
			Reason: "정상 설치본 — 현재 위치를 그대로 교체",
		}, nil
	}

	// 2) translocation 원본이 해석되고 쓸 수 있으면 그곳을 교체한다(예: ~/Downloads의 앱).
	//    교체 후 quarantine을 지워야 다음 실행에서 translocation이 재발하지 않는다.
	exists := env.Exists
	if exists == nil {
		exists = func(string) bool { return true }
	}
	if env.OriginalPath != "" && !isUnder(env.OriginalPath, env.TempDir) &&
		exists(env.OriginalPath) && writable(filepath.Dir(env.OriginalPath)) {
		return installPlan{
			Mode:   installOriginal,
			Target: env.OriginalPath,
			Reason: "translocation 상태 — SPI로 해석한 원본 경로를 교체",
		}, nil
	}

	// 3) 원본을 못 찾거나 읽기전용(DMG에서 직접 실행 등) → /Applications로 이설.
	appsDir := env.ApplicationsDir
	if appsDir == "" {
		appsDir = "/Applications"
	}
	name := env.NewAppName
	if name == "" {
		name = filepath.Base(env.CurrentApp)
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		return installPlan{}, fmt.Errorf("설치할 번들 이름을 결정할 수 없습니다")
	}
	if writable(appsDir) {
		return installPlan{
			Mode:   installApplications,
			Target: filepath.Join(appsDir, name),
			Reason: reasonForApplications(env),
		}, nil
	}

	// 4) 여기까지 오면 안전하게 설치할 곳이 없다. 임시 폴더 실행은 절대 하지 않고
	//    업데이트를 중단한다(기존 앱은 그대로 살아 있다).
	return installPlan{}, fmt.Errorf(
		"업데이트를 설치할 수 있는 위치가 없습니다 — 앱을 /Applications 폴더로 옮긴 뒤 다시 시도하세요 "+
			"(현재 실행 경로: %s, translocated=%v, %s 쓰기 불가)",
		env.CurrentApp, env.Translocated, appsDir)
}

// reasonForApplications 는 3)번 분기로 온 이유를 사람이 읽을 문장으로 만든다.
func reasonForApplications(env installEnv) string {
	// translocation 마운트 자체가 임시 폴더(/var/folders/.../T/AppTranslocation) 아래라,
	// translocation 판정을 임시 폴더 판정보다 **먼저** 봐야 원인이 정확히 드러난다.
	switch {
	case env.Translocated && env.OriginalPath == "":
		return "translocation 상태 + 원본 경로 해석 실패 — /Applications로 이설"
	case env.Translocated:
		return "translocation 상태 + 원본이 읽기전용이거나 이미 삭제됨 — /Applications로 이설"
	case isUnder(env.CurrentApp, env.TempDir):
		return "임시 폴더에서 실행 중 — /Applications로 이설(임시 폴더는 설치 대상이 될 수 없음)"
	default:
		return "현재 위치가 읽기전용 — /Applications로 이설"
	}
}

// isUnder 는 path가 root 아래(또는 동일)인지 보고한다. root가 비면 false.
// 경로 문자열만으로 판단하므로 심볼릭 링크는 호출자가 미리 해석해 넘긴다
// (macOS의 /var → /private/var 대비: 양쪽 다 정규화해 비교한다).
func isUnder(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	p := normalizePath(path)
	r := normalizePath(root)
	if p == r {
		return true
	}
	return strings.HasPrefix(p, strings.TrimSuffix(r, string(filepath.Separator))+string(filepath.Separator))
}

// normalizePath 는 비교용 정규화다: Clean + macOS의 /var·/tmp 심볼릭 링크 접두사 통일.
//
// **주의**: 루트가 정확히 "/tmp" 또는 "/var"인 경우까지 반드시 정규화해야 한다. TMPDIR이
// 설정되지 않은 환경에서는 os.TempDir()이 "/tmp"를 돌려주는데, 이때 접두사만 비교하면
// "/private/tmp/..."(실제 실행 경로) 아래인지 판정하지 못해 **임시 폴더 차단선이 뚫린다**.
func normalizePath(p string) string {
	p = filepath.Clean(p)
	for _, pair := range [][2]string{
		{"/var", "/private/var"},
		{"/tmp", "/private/tmp"},
	} {
		if p == pair[0] || strings.HasPrefix(p, pair[0]+string(filepath.Separator)) {
			p = pair[1] + strings.TrimPrefix(p, pair[0])
		}
	}
	return p
}

// isTranslocatedPath 는 경로 형태만으로 App Translocation 여부를 판정한다.
// macOS는 quarantine 앱을 /private/var/folders/<..>/T/AppTranslocation/<UUID>/d/<App>.app
// 형태의 읽기전용 마운트에서 실행한다. SPI(SecTranslocateIsTranslocatedURL)가 1차이고,
// 이 문자열 판정은 SPI를 못 쓸 때의 보조 수단이다.
func isTranslocatedPath(path string) bool {
	return strings.Contains(normalizePath(path), "/AppTranslocation/")
}
