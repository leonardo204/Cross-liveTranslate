#!/usr/bin/env bash
# Cross-liveTranslate release 편의 래퍼 — .env 자동 로드 + notarytool keychain 자동 등록.
# 비대화식 실행 지원: .env에서 모든 비밀을 읽어 Claude Code inline bash / CI에서도 동작.
#
# 사용법:
#   scripts/release-cross-livetranslate.sh --version 1.0.0 --upload [--notes "릴리즈 노트"]
#
# .env 예시 (프로젝트 루트, gitignore됨):
#   APPLE_ID='...'
#   APPLE_APP_PASSWORD='xxxx-xxxx-xxxx-xxxx'
#   APPLE_TEAM_ID='XXXXXXXXXX'
#   APPLE_SIGNING_IDENTITY='Developer ID Application: ... (TEAMID)'
#   MINISIGN_PASSWORD=''     # Tauri 키는 빈 비밀번호
set -euo pipefail

cd "$(cd "$(dirname "$0")/.." && pwd)"

# ── .env 자동 로드 ─────────────────────────────────────────────────────────
# 우선순위: 기존 환경변수 > .env (이미 set된 변수는 덮어쓰지 않음)
if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

# ── MINISIGN_PASSWORD 결정 ─────────────────────────────────────────────────
# Tauri 키(.tauri/flipmd.key)는 **빈 string**이 비밀번호.
# ${VAR+set} 으로 unset 여부 검사 — 빈 string("")도 유효한 값으로 수용.
export MINISIGN_SECRET_KEY="${MINISIGN_SECRET_KEY:-$HOME/.tauri/flipmd.key}"
export MINISIGN_PASSWORD="${MINISIGN_PASSWORD:-}"   # 미설정이면 빈 string 기본값

# ── 기본값 설정 ────────────────────────────────────────────────────────────
export APPLE_NOTARY_PROFILE="${APPLE_NOTARY_PROFILE:-CROSS_LIVETRANSLATE_NOTARY}"
# APPLE_SIGNING_IDENTITY는 .env에서 설정 권장 (하드코딩 금지)
# TAURI_CLI_DIR은 release.sh 기본값(/Users/zerolive/work/flipbookMaker) 사용

# ── Apple Notarization keychain profile 자동 등록 ─────────────────────────
# keychain에 profile이 없고 .env에 자격증명이 있으면 한 번만 자동 등록.
# 이후 실행부터는 keychain만 사용 (.env Apple 항목은 백업용).
if ! xcrun notarytool history --keychain-profile "$APPLE_NOTARY_PROFILE" >/dev/null 2>&1; then
  if [[ -n "${APPLE_ID:-}" && -n "${APPLE_APP_PASSWORD:-}" && -n "${APPLE_TEAM_ID:-}" ]]; then
    printf "\033[1;36m▸\033[0m notarytool store-credentials %s\n" "$APPLE_NOTARY_PROFILE"
    xcrun notarytool store-credentials "$APPLE_NOTARY_PROFILE" \
      --apple-id      "$APPLE_ID" \
      --team-id       "$APPLE_TEAM_ID" \
      --password      "$APPLE_APP_PASSWORD" >/dev/null
    printf "\033[1;32m✔\033[0m keychain profile 등록 완료: %s\n" "$APPLE_NOTARY_PROFILE"
  else
    printf "\033[1;33m⚠\033[0m APPLE_NOTARY_PROFILE(%s) 미등록.\n" "$APPLE_NOTARY_PROFILE" >&2
    printf "  .env에 APPLE_ID / APPLE_APP_PASSWORD / APPLE_TEAM_ID를 채우거나\n" >&2
    printf "  xcrun notarytool store-credentials 로 직접 등록하세요.\n" >&2
    printf "  → notarize/staple 단계 스킵 (Gatekeeper 경고 발생)\n" >&2
    unset APPLE_NOTARY_PROFILE
  fi
fi

exec scripts/release.sh "$@"
