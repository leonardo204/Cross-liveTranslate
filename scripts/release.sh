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

# Tauri 시절 생성한 키는 base64로 한 번 더 wrap된 rsign 형식이라 표준
# minisign / rsign2 CLI에서 못 읽는다. Tauri CLI signer만 이 형식을 처리.
#
# 산출물 `<file>.sig`는 base64(minisign텍스트) = 1중 wrap.
# latest.json signature에 **그대로** 넣어야 한다 (추가 base64 인코딩 금지).
# verify.go가 base64 한 단계를 풀면 minisign 텍스트가 나오기 때문.
# (이중 wrap → "signature 본문이 너무 짧습니다" 검증 실패 — flipMd-Go 함정 6 참고)
TAURI_CLI_DIR="${TAURI_CLI_DIR:-/Users/zerolive/work/flipbookMaker}"

sign_minisign() {
  local file="$1"
  [[ -d "$TAURI_CLI_DIR/node_modules/@tauri-apps/cli" ]] || \
    die "tauri CLI signer를 못 찾음: $TAURI_CLI_DIR (TAURI_CLI_DIR로 경로 지정)"
  local out
  out=$(
    cd "$TAURI_CLI_DIR" && \
    npx tauri signer sign \
      -f "$MINISIGN_SECRET_KEY" \
      -p "${MINISIGN_PASSWORD:-}" \
      "$file" 2>&1
  ) || die "tauri signer 서명 실패: $file
$out"
  [[ -f "${file}.sig" ]] || die "${file}.sig 생성 실패"
  mv "${file}.sig" "${file}.minisig"
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
PATH="$PATH_WITH_GO" wails build \
  -platform darwin/universal \
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
  codesign --deep --force --options runtime --timestamp \
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
    /usr/sbin/spctl -a -vv --type install "$DMG_PATH" 2>&1 | head -3 || true
  else
    die "notarization 실패. xcrun notarytool log <submission-id> --keychain-profile $APPLE_NOTARY_PROFILE 로 사유 확인"
  fi
else
  warn "APPLE_NOTARY_PROFILE 미설정 — notarize/staple 스킵 (Gatekeeper 경고 발생 가능)"
fi

# ── 5. minisign 서명 (tauri signer) ────────────────────────────────────────
log "[sign] tauri signer → ${DMG_NAME}.minisig"
sign_minisign "$DMG_PATH"

# .minisig 내용 그대로 읽음 — 추가 base64 인코딩 금지 (이중 wrap → 검증 실패)
SIG=$(tr -d '\n' < "${DMG_PATH}.minisig")
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
