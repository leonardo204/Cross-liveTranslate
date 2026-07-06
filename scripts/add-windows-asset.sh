#!/usr/bin/env bash
# 기존 GitHub 릴리스에 Windows(portable .exe) 자산을 추가한다.
# macOS DMG 자산/서명은 건드리지 않고, 해당 버전 릴리스의 latest.json에
# windows-x86_64 키만 병합(merge)한 뒤 exe + latest.json을 재업로드한다.
#
# Wails 2.12는 macOS에서 windows/amd64 크로스 빌드 가능(순수 Go WebView2Loader).
# Windows 코드서명은 하지 않는다 — 무결성은 minisign 서명으로 보장.
#
# 사용법:
#   scripts/add-windows-asset.sh <version>   # 예: 1.0.0
#
# 전제: .env 에 MINISIGN_*/TAURI_CLI_DIR, gh 인증 완료.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
DIST="$ROOT/dist"
BIN="$ROOT/build/bin"
PATH_WITH_GO="$HOME/go/bin:$PATH"
GH_REPO="leonardo204/Cross-liveTranslate"

log()  { printf "\033[1;36m▸\033[0m %s\n" "$*"; }
die()  { printf "\033[1;31m✗\033[0m %s\n" "$*" >&2; exit 1; }

[[ -f .env ]] && { set -a; . ./.env; set +a; }

VERSION="${1:-}"
[[ -n "$VERSION" ]] || die "version 인자 필요 (예: 1.0.0)"
VERSION="${VERSION#v}"
TAG="v${VERSION}"

MINISIGN_SECRET_KEY="${MINISIGN_SECRET_KEY:-$HOME/.tauri/flipmd.key}"
MINISIGN_PASSWORD="${MINISIGN_PASSWORD:-}"
TAURI_CLI_DIR="${TAURI_CLI_DIR:-/Users/zerolive/work/flipbookMaker}"
[[ -d "$TAURI_CLI_DIR/node_modules/@tauri-apps/cli" ]] || die "tauri signer 없음: $TAURI_CLI_DIR"

EXE_NAME="Cross-liveTranslate.exe"   # 버전 없는 이름: in-place 업데이트 후에도 동일 파일명 유지. 'installer' 미포함 → portable 인식
EXE_PATH="$DIST/$EXE_NAME"
EXE_URL="https://github.com/${GH_REPO}/releases/download/${TAG}/${EXE_NAME}"
mkdir -p "$DIST"

# ── 1. wails build windows/amd64 ──────────────────────────────────────────
log "[build] wails build windows/amd64 (appVersion=${VERSION})"
PATH="$PATH_WITH_GO" wails build -platform windows/amd64 \
  -ldflags "-X main.appVersion=${VERSION}"
[[ -f "$BIN/cross-livetranslate.exe" ]] || die "빌드 산출물 없음: $BIN/cross-livetranslate.exe"
cp -f "$BIN/cross-livetranslate.exe" "$EXE_PATH"
log "[build] → $EXE_NAME ($(du -h "$EXE_PATH" | cut -f1))"

# ── 2. minisign 서명 (tauri signer) ───────────────────────────────────────
log "[sign] tauri signer → ${EXE_NAME}.minisig"
( cd "$TAURI_CLI_DIR" && npx tauri signer sign -f "$MINISIGN_SECRET_KEY" -p "${MINISIGN_PASSWORD:-}" "$EXE_PATH" >/dev/null 2>&1 ) \
  || die "tauri signer 서명 실패"
[[ -f "${EXE_PATH}.sig" ]] || die "${EXE_PATH}.sig 생성 실패"
mv -f "${EXE_PATH}.sig" "${EXE_PATH}.minisig"
SIG="$(tr -d '\n' < "${EXE_PATH}.minisig")"
[[ -n "$SIG" ]] || die "서명 내용 비어있음"

# ── 3. 기존 latest.json 다운로드 후 windows-x86_64 키 병합 ────────────────
log "[manifest] 기존 latest.json 가져와 windows-x86_64 병합"
TMP="$(mktemp -d)"
gh release download "$TAG" --repo "$GH_REPO" -p latest.json -D "$TMP" --clobber \
  || die "기존 latest.json 다운로드 실패 (릴리스 $TAG 가 존재하고 latest.json 자산이 있어야 함)"

SIG="$SIG" EXE_URL="$EXE_URL" python3 - "$TMP/latest.json" "$DIST/latest.json" <<'PY'
import json, os, sys
src, dst = sys.argv[1], sys.argv[2]
with open(src, encoding="utf-8") as f:
    m = json.load(f)
m.setdefault("platforms", {})
m["platforms"]["windows-x86_64"] = {
    "signature": os.environ["SIG"],
    "url": os.environ["EXE_URL"],
}
with open(dst, "w", encoding="utf-8") as f:
    json.dump(m, f, ensure_ascii=False, indent=2)
    f.write("\n")
print("platforms:", ", ".join(sorted(m["platforms"])))
PY
rm -rf "$TMP"

log "[manifest] 미리보기:"
cat "$DIST/latest.json"

# ── 4. 업로드 (exe + 병합된 latest.json) ──────────────────────────────────
log "[gh] 업로드: $EXE_NAME + latest.json (--clobber)"
gh release upload "$TAG" --repo "$GH_REPO" "$EXE_PATH" "$DIST/latest.json" --clobber \
  || die "gh release upload 실패"
log "[gh] 완료: https://github.com/${GH_REPO}/releases/tag/${TAG}"
