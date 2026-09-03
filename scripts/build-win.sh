#!/usr/bin/env bash
#
# build-win.sh — macOS에서 Windows(amd64) 크로스컴파일.
#
# 전제: mingw-w64 설치 (cgo=malgo 오디오에 Windows C 컴파일러 필요).
#   brew install mingw-w64
#
# 산출물: build/bin/cross-livetranslate.exe (PE32+ GUI, x86-64).
# 이 exe를 Windows PC로 옮겨 실행한다(Windows .exe는 mac에서 실행 불가).
# Windows에는 Microsoft Edge WebView2 런타임이 필요하다(Win11 기본 탑재 / Win10 설치 필요).
#
# 사용법:  scripts/build-win.sh
set -euo pipefail

cd "$(dirname "$0")/.."
export PATH="$HOME/go/bin:/opt/homebrew/bin:$PATH"

CC_BIN="${CC:-x86_64-w64-mingw32-gcc}"
if ! command -v "$CC_BIN" >/dev/null 2>&1; then
  echo "✗ mingw-w64 크로스컴파일러가 없습니다($CC_BIN). 'brew install mingw-w64' 후 다시 실행하세요." >&2
  exit 1
fi

# 새 앱 아이콘(build/appicon.png) 반영: 옛 icon.ico가 있으면 지워 wails가 재생성하게 한다.
if [[ -f build/windows/icon.ico && build/appicon.png -nt build/windows/icon.ico ]]; then
  rm -f build/windows/icon.ico
  echo "▸ 옛 icon.ico 제거(새 아이콘으로 재생성)"
fi

# -nsis: 포터블 exe + NSIS 설치형 인스톨러를 함께 생성(makensis 필요).
NSIS_FLAG=""
if command -v makensis >/dev/null 2>&1; then
  NSIS_FLAG="-nsis"
  # makensis는 UTF-8 로케일이 필요하다(LANG이 비면 `Unicode true` 인스톨러 생성이
  # "Generating language tables"에서 std::bad_alloc으로 죽는다). 값이 있으면 건드리지 않는다.
  if [[ -z "${LC_ALL:-}" && -z "${LANG:-}" ]]; then
    export LANG="en_US.UTF-8" LC_ALL="en_US.UTF-8"
    echo "▸ LANG 미설정 — makensis용 UTF-8 로케일 적용(en_US.UTF-8)"
  fi
else
  echo "⚠ makensis 없음 — 인스톨러 스킵(포터블 exe만). 'brew install makensis'로 설치 가능."
fi

echo "▶ wails build windows/amd64 (netgo, cgo via mingw${NSIS_FLAG:+, +NSIS 인스톨러})…"
# -tags netgo: 순수 Go DNS 리졸버(cgo DNS 경합 회피, macOS와 동일 정책).
CGO_ENABLED=1 CC="$CC_BIN" CXX="${CXX:-x86_64-w64-mingw32-g++}" \
  wails build -platform windows/amd64 -tags netgo $NSIS_FLAG

echo "✅ 완료:"
echo "   • 포터블:   build/bin/cross-livetranslate.exe"
[[ -n "$NSIS_FLAG" ]] && echo "   • 인스톨러: build/bin/cross-livetranslate-amd64-installer.exe"
echo "   Windows PC로 옮겨 실행(WebView2 런타임 필요)."
