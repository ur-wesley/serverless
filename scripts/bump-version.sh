#!/bin/sh
# Version bump entry point: lockstep manifests, see bump.config.ts.
# Usage: sh scripts/bump-version.sh patch|minor|major|<version, e.g. 0.1.2>
# Equivalent to: npx bumpp <args>   (commits + tags, does not push)
set -eu
exec npx bumpp "$@"
