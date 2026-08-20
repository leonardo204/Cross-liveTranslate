//go:build darwin

package updater

import (
	"os"
	"path/filepath"
	"testing"
)

// TestQueryTranslocationSPIAvailable 는 Security 프레임워크 SPI 바인딩이 실제로 동작하는지
// (dlopen/dlsym 심볼 해석 + CFURL 변환 + 호출) 런타임에서 확인한다. 일반 경로는 translocation이
// 아니므로 Translocated=false 여야 하고, SPIAvailable=true 여야 한다.
//
// 진짜 translocation 상태는 Gatekeeper가 quarantine된 **공증된** 앱을 실행할 때만 만들어져
// 유닛 테스트로 재현할 수 없다 — 그래서 여기서는 "SPI를 실제로 호출할 수 있는가"까지 실증하고,
// 판정 이후의 결정 로직은 install_target_test.go가 전부 커버한다.
func TestQueryTranslocationSPIAvailable(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "sample.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	info := queryTranslocation(app)
	if !info.SPIAvailable {
		t.Fatalf("SPIAvailable = false — Security 프레임워크 SPI(SecTranslocateIsTranslocatedURL) "+
			"해석에 실패했다(info=%+v). 폴백은 동작하지만 원본 경로 해석을 못 한다.", info)
	}
	if info.Translocated {
		t.Fatalf("일반 임시 디렉토리를 translocated로 판정했다: %+v", info)
	}
	if info.Original != "" {
		t.Fatalf("translocation이 아닌데 원본 경로가 나왔다: %q", info.Original)
	}
}

// 존재하지 않는 경로에서도 크래시 없이 안전하게 판정한다(SPI 에러 경로).
func TestQueryTranslocationMissingPath(t *testing.T) {
	info := queryTranslocation(filepath.Join(t.TempDir(), "없는앱.app"))
	if info.Translocated {
		t.Fatalf("없는 경로를 translocated로 판정했다: %+v", info)
	}
}

// 경로 문자열이 명백한 translocation이면 SPI 응답과 무관하게 보수적으로 true를 준다
// (SPI가 false를 주더라도 그 경로는 읽기전용이라 교체 대상이 될 수 없다).
func TestQueryTranslocationPathFallback(t *testing.T) {
	const p = "/private/var/folders/z2/x/T/AppTranslocation/UUID/d/cross-livetranslate.app"
	if info := queryTranslocation(p); !info.Translocated {
		t.Fatalf("AppTranslocation 경로를 놓쳤다: %+v", info)
	}
}
