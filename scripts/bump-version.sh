#!/bin/sh
# Bump every version manifest in lockstep, then commit + tag with:
#   git commit -am "chore: release v$1" && git tag "v$1"
# Usage: sh scripts/bump-version.sh 0.1.2
set -eu

VER="${1:?usage: sh scripts/bump-version.sh <version, e.g. 0.1.2>}"
case "$VER" in
  *[^0-9.]*|""|*..*|.*|*.) echo "refusing suspicious version: $VER" >&2; exit 1;;
esac

# Root package.json is the source of truth.
npm version "$VER" --no-git-tag-version --allow-same-version >/dev/null
# TS SDK (keeps sdks/ts/package-lock.json in sync too).
npm --prefix sdks/ts version "$VER" --no-git-tag-version --allow-same-version >/dev/null
# internal/version embeds a copy (go:embed forbids "..").
cp package.json internal/version/package.json
# npm/ wrapper is synced by the release workflow; keep the tree consistent too.
npm --prefix npm version "$VER" --no-git-tag-version --allow-same-version >/dev/null

echo "root:              $(node -p "require('./package.json').version")"
echo "sdks/ts:           $(node -p "require('./sdks/ts/package.json').version")"
echo "internal/version:  $(node -p "require('./internal/version/package.json').version")"
echo "npm/:               $(node -p "require('./npm/package.json').version")"
echo "Next: git commit -am \"chore: release v$VER\" && git tag \"v$VER\""
