#!/usr/bin/env bash
#
# build-mac.sh — macOS 배포/개발 빌드 + Developer ID 안정 서명.
#
# 왜 이 스크립트가 필요한가:
#   `wails build`는 기본적으로 ad-hoc 서명을 한다. ad-hoc 서명은 코드 해시(cdhash)로만
#   식별되므로, 재빌드할 때마다 해시가 바뀌어 macOS TCC가 "새로운 앱"으로 인식한다.
#   → 마이크 권한이 매 빌드마다 초기화되어 "번역 안 됨(연결 중…)"으로 이어진다.
#
#   Developer ID 인증서로 서명하면 TCC가 안정적인 designated requirement(Team ID)로
#   앱을 식별하므로, 재빌드해도 마이크 권한이 유지된다. 이것이 근본 해결책이다.
#
# 사용법:  scripts/build-mac.sh
#
set -euo pipefail

cd "$(dirname "$0")/.."

# 안정 서명 identity. 다른 머신/개발자는 자신의 Developer ID로 바꾸거나,
# CODESIGN_IDENTITY 환경변수로 오버라이드한다. 미설정·부재 시 wails 기본(ad-hoc) 유지.
IDENTITY="${CODESIGN_IDENTITY:-Developer ID Application: YONGSUB LEE (XU8HS9JUTS)}"
APP="build/bin/cross-livetranslate.app"
ENTITLEMENTS="build/darwin/entitlements.plist"

export PATH="$HOME/go/bin:$PATH"

echo "▶ wails build (darwin, netgo — cgo DNS 리졸버 비활성)…"
# netgo: 순수 Go DNS 리졸버를 강제한다. macOS에서 malgo(CoreAudio) cgo 오디오 초기화와
# cgo DNS 조회(getaddrinfo)가 동시에 돌면 SIGSEGV로 앱이 급종료되므로 cgo DNS를 제거한다.
wails build -tags netgo

# 서명 identity가 키체인에 존재할 때만 재서명한다(없으면 ad-hoc 그대로 두어 빌드는 성공).
if security find-identity -v -p codesigning | grep -qF "$IDENTITY"; then
	echo "▶ Developer ID로 재서명: $IDENTITY"
	codesign --deep --force \
		--options runtime \
		--entitlements "$ENTITLEMENTS" \
		--sign "$IDENTITY" \
		"$APP"
	echo "▶ 서명 검증:"
	codesign -dvvv "$APP" 2>&1 | grep -E "Authority|TeamIdentifier|Signature" || true
	# 폴더 mtime 갱신(파인더가 최신 빌드를 인식하도록).
	touch "$APP"
	echo "✅ 완료 — 마이크 권한은 최초 1회만 허용하면 이후 재빌드에도 유지됩니다."
else
	echo "⚠ 서명 identity를 찾지 못함('$IDENTITY') — ad-hoc 서명 유지(재빌드 시 권한 재요청 필요)."
fi
