#!/usr/bin/env bash
# Cross-liveTranslate release pipeline — macOS universal DMG 빌드/서명/notarize/배포
#
# Required env / args:
#   VERSION                  e.g. 1.0.0                  (필수, --version 또는 env)
#   MINISIGN_SECRET_KEY      path to minisign private key (기본: ~/.tauri/flipmd.key)
#   MINISIGN_PASSWORD        key password (빈 string OK)  (기본: "")
#   APPLE_SIGNING_IDENTITY   Developer ID Application: ...  (없으면 서명 스킵)
#   APPLE_NOTARY_PROFILE     notarytool keychain profile    (없으면 notarize 스킵)
#   TAURI_CLI_DIR            tauri signer 위치              (기본: ~/work/flipbookMaker)
#
# Usage:
#   scripts/release.sh --version 1.0.0 [--upload] [--notes "릴리즈 노트"]
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
BIN="$ROOT/build/bin"
PATH_WITH_GO="$HOME/go/bin:$PATH"

# ── helpers ────────────────────────────────────────────────────────────────
log()  { printf "\033[1;36m▸\033[0m %s\n" "$*"; }
warn() { printf "\033[1;33m⚠\033[0m %s\n" "$*" >&2; }
die()  { printf "\033[1;31m✗\033[0m %s\n" "$*" >&2; exit 1; }

# 서명: Cross-liveTranslate 전용 minisign 키(무암호 표준 minisign, ~/.tauri/cross-livetranslate.key).
# 표준 `minisign` CLI로 서명한다(tauri CLI 의존 제거). MINISIGN_SECRET_KEY로 경로 오버라이드.
#
# latest.json의 signature 값은 **base64(minisign 서명파일 내용)** = 1중 wrap 이어야 한다.
# verify.go(updater)가 base64 한 겹을 풀면 minisign 서명 텍스트가 나오기 때문이다.
# 따라서 minisign이 낸 .minisig(raw 텍스트)를 base64로 한 번 감싸 latest.json에 넣는다.
sign_minisign() {
  local file="$1"
  command -v minisign >/dev/null 2>&1 || die "minisign CLI가 없습니다 (brew install minisign)"
  [[ -f "$MINISIGN_SECRET_KEY" ]] || die "minisign 개인키를 못 찾음: $MINISIGN_SECRET_KEY"
  rm -f "${file}.minisig"
  # 무암호 키라 비대화식. -x로 출력 경로 지정. 실패 시 에러 표면화.
  minisign -S -s "$MINISIGN_SECRET_KEY" -m "$file" -x "${file}.minisig" >/dev/null 2>&1 \
    || die "minisign 서명 실패: $file"
  [[ -f "${file}.minisig" ]] || die "${file}.minisig 생성 실패"
}

# ── 인자 파싱 ───────────────────────────────────────────────────────────────
VERSION="${VERSION:-}"
GH_UPLOAD=0
NOTES=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --upload)  GH_UPLOAD=1; shift ;;
    --notes)   NOTES="$2"; shift 2 ;;
    -h|--help) sed -n '2,16p' "$0"; exit 0 ;;
    *) die "알 수 없는 인자: $1" ;;
  esac
done

# ── 전제조건 검증 ──────────────────────────────────────────────────────────
[[ -n "$VERSION" ]] || die "VERSION이 필요합니다 (--version X.Y.Z 또는 환경변수 VERSION=...)"

# v 접두사 정규화: 태그는 vX.Y.Z, manifest/ldflags는 X.Y.Z
VERSION="${VERSION#v}"        # 내부 처리는 항상 X.Y.Z
TAG="v${VERSION}"             # GitHub tag

MINISIGN_SECRET_KEY="${MINISIGN_SECRET_KEY:-$HOME/.tauri/flipmd.key}"
[[ -f "$MINISIGN_SECRET_KEY" ]] || die "MINISIGN_SECRET_KEY 파일 없음: $MINISIGN_SECRET_KEY"

NOTES="${NOTES:-${RELEASE_NOTES:-Cross-liveTranslate ${VERSION}}}"
PUB_DATE="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

GH_REPO="leonardo204/Cross-liveTranslate"
DMG_NAME="Cross-liveTranslate_${VERSION}_universal.dmg"

rm -rf "$DIST"
mkdir -p "$DIST"

# ── 1. wails build darwin/universal ───────────────────────────────────────
log "[build] wails build darwin/universal (ldflags appVersion=${VERSION})"
rm -rf "$BIN"
# -tags netgo: 순수 Go DNS 리졸버 강제. malgo(CoreAudio) 초기화와 cgo DNS 조회가
# 동시에 돌면 macOS에서 SIGSEGV로 급종료되므로 cgo DNS를 제거한다(배포본 필수).
PATH="$PATH_WITH_GO" wails build \
  -platform darwin/universal \
  -tags netgo \
  -ldflags "-X main.appVersion=${VERSION}"

# build/bin/*.app 자동 탐지
APP_PATH=$(ls -d "$BIN"/*.app 2>/dev/null | head -1) || true
[[ -n "$APP_PATH" && -d "$APP_PATH" ]] || \
  die "빌드 산출물 .app을 찾을 수 없습니다 ($BIN)"
APP_NAME="$(basename "$APP_PATH")"
log "[build] 산출물 탐지: $APP_NAME"

# ── 2. codesign ────────────────────────────────────────────────────────────
if [[ -n "${APPLE_SIGNING_IDENTITY:-}" ]]; then
  log "[codesign] $APPLE_SIGNING_IDENTITY"
  # entitlements: 마이크/시스템오디오 입력(audio-input) + WKWebView JIT(allow-jit).
  # 하드닝 런타임(--options runtime)에서 이 권한이 없으면 웹뷰 크래시/캡처 실패가 난다.
  ENTITLEMENTS="build/darwin/entitlements.plist"
  codesign --deep --force --options runtime --timestamp \
    --entitlements "$ENTITLEMENTS" \
    --sign "$APPLE_SIGNING_IDENTITY" "$APP_PATH" >/dev/null
  log "[codesign] 완료"
else
  warn "APPLE_SIGNING_IDENTITY 미설정 — codesign 스킵 (개발용 빌드, Gatekeeper 경고 발생)"
fi

# ── 3. DMG 생성 ────────────────────────────────────────────────────────────
DMG_PATH="$DIST/$DMG_NAME"
log "[dmg] hdiutil create → $DMG_NAME"
hdiutil create \
  -volname "Cross-liveTranslate" \
  -srcfolder "$APP_PATH" \
  -ov \
  -format UDZO \
  "$DMG_PATH"
log "[dmg] 생성 완료: $DMG_PATH"

# ── 4. notarize + staple ────────────────────────────────────────────────────
if [[ -n "${APPLE_NOTARY_PROFILE:-}" ]]; then
  log "[notarize] xcrun notarytool submit (수 분 소요)"
  if xcrun notarytool submit "$DMG_PATH" \
       --keychain-profile "$APPLE_NOTARY_PROFILE" --wait; then
    log "[notarize] 완료"
    log "[staple] xcrun stapler staple"
    xcrun stapler staple "$DMG_PATH" >/dev/null || die "stapler staple 실패"
    log "[staple] 완료"
    # staple 티켓 유효성 확인(DMG에 spctl --type install은 오검출이므로 stapler validate 사용).
    xcrun stapler validate "$DMG_PATH" 2>&1 | tail -1 || true
  else
    die "notarization 실패. xcrun notarytool log <submission-id> --keychain-profile $APPLE_NOTARY_PROFILE 로 사유 확인"
  fi
else
  warn "APPLE_NOTARY_PROFILE 미설정 — notarize/staple 스킵 (Gatekeeper 경고 발생 가능)"
fi

# ── 5. minisign 서명 (표준 minisign) ────────────────────────────────────────
log "[sign] minisign → ${DMG_NAME}.minisig"
sign_minisign "$DMG_PATH"

# latest.json signature = base64(minisign 서명파일 내용). verify.go가 base64 한 겹을 풀어
# minisign 텍스트를 얻는다. minisig(raw 텍스트)를 base64로 한 번 감싼다.
SIG=$(base64 < "${DMG_PATH}.minisig" | tr -d '\n')
[[ -n "$SIG" ]] || die ".minisig 내용이 비어있습니다"
log "[sign] 서명 완료"

# ── 6. latest.json 생성 ────────────────────────────────────────────────────
LATEST="$DIST/latest.json"
DMG_URL="https://github.com/${GH_REPO}/releases/download/${TAG}/${DMG_NAME}"

log "[manifest] $LATEST"
{
  printf '{\n'
  printf '  "version": "%s",\n' "$VERSION"
  printf '  "notes": %s,\n' "$(printf '%s' "$NOTES" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')"
  printf '  "pub_date": "%s",\n' "$PUB_DATE"
  printf '  "platforms": {\n'
  printf '    "darwin-aarch64": {\n'
  printf '      "signature": "%s",\n' "$SIG"
  printf '      "url": "%s"\n'        "$DMG_URL"
  printf '    },\n'
  printf '    "darwin-x86_64": {\n'
  printf '      "signature": "%s",\n' "$SIG"
  printf '      "url": "%s"\n'        "$DMG_URL"
  printf '    }\n'
  printf '  }\n'
  printf '}\n'
} > "$LATEST"

log "[manifest] 내용 미리보기:"
cat "$LATEST"

# ── 7. GitHub Release 생성 + 자산 업로드 ──────────────────────────────────
if [[ "$GH_UPLOAD" -eq 1 ]]; then
  command -v gh >/dev/null || die "gh CLI가 설치되지 않았습니다 (brew install gh)"
  gh auth status >/dev/null 2>&1 || die "gh CLI 미인증 — gh auth login 먼저 실행"

  log "[gh] release 확인/생성: $TAG (repo: $GH_REPO)"
  if ! gh release view "$TAG" --repo "$GH_REPO" >/dev/null 2>&1; then
    gh release create "$TAG" \
      --repo "$GH_REPO" \
      --title "$TAG" \
      --notes "$NOTES" \
      || die "gh release create 실패"
    log "[gh] release 생성됨: $TAG"
  else
    log "[gh] release 이미 존재: $TAG — 자산만 업로드"
  fi

  log "[gh] 자산 업로드: $DMG_NAME + latest.json"
  gh release upload "$TAG" \
    --repo "$GH_REPO" \
    "$DMG_PATH" \
    "$LATEST" \
    --clobber
  log "[gh] 업로드 완료"
  log "[gh] 릴리즈 URL: https://github.com/${GH_REPO}/releases/tag/${TAG}"
fi

log "Done. 산출물: $DIST"
ls -lh "$DIST"
