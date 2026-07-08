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

echo "▶ wails build windows/amd64 (netgo, cgo via mingw)…"
# -tags netgo: 순수 Go DNS 리졸버(cgo DNS 경합 회피, macOS와 동일 정책).
CGO_ENABLED=1 CC="$CC_BIN" CXX="${CXX:-x86_64-w64-mingw32-g++}" \
  wails build -platform windows/amd64 -tags netgo

echo "✅ 완료: build/bin/cross-livetranslate.exe"
echo "   Windows PC로 옮겨 실행(WebView2 런타임 필요)."
