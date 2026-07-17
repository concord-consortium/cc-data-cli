#!/usr/bin/env bash
# Render a thin per-platform goreleaser config from .goreleaser.base.yaml.
# Usage: gen-goreleaser.sh <goos> <goarch> <out-file>
set -euo pipefail

GOOS="${1:?goos required}"
GOARCH="${2:?goarch required}"
OUT="${3:?output path required}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

sed \
  -e "s/__GOOS__/${GOOS}/g" \
  -e "s/__GOARCH__/${GOARCH}/g" \
  "${HERE}/.goreleaser.base.yaml" > "${OUT}"

echo "wrote ${OUT} for ${GOOS}/${GOARCH}"
