#!/usr/bin/env bash
# Render the Homebrew formula from packaging/cc-data.rb.tmpl with per-platform
# sha256 sums and push it to concord-consortium/homebrew-tap.
# Usage: render-formula.sh <tag>   (tag like v1.2.3)
set -euo pipefail

TAG="${1:?tag required}"
VERSION="${TAG#v}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMPL="${HERE}/packaging/cc-data.rb.tmpl"
DIST="${HERE}/dist"
OUT="${DIST}/cc-data.rb"

mkdir -p "${DIST}"

sha_for() {
  # Resolve artifacts against the repo root so the script works from any CWD.
  local artifact="${DIST}/cc-data_${VERSION}_${1}.tar.gz"
  if [[ ! -f "${artifact}" ]]; then
    echo "missing artifact ${artifact}" >&2
    exit 1
  fi
  shasum -a 256 "${artifact}" | awk '{print $1}'
}

SHA_DARWIN_ARM64="$(sha_for darwin_arm64)"
SHA_DARWIN_AMD64="$(sha_for darwin_amd64)"
SHA_LINUX_AMD64="$(sha_for linux_amd64)"

sed \
  -e "s/__VERSION__/${VERSION}/g" \
  -e "s/__SHA_DARWIN_ARM64__/${SHA_DARWIN_ARM64}/g" \
  -e "s/__SHA_DARWIN_AMD64__/${SHA_DARWIN_AMD64}/g" \
  -e "s/__SHA_LINUX_AMD64__/${SHA_LINUX_AMD64}/g" \
  "${TMPL}" > "${OUT}"

echo "rendered ${OUT}:"
cat "${OUT}"

if [[ -z "${TAP_TOKEN:-}" ]]; then
  echo "TAP_TOKEN not set; skipping push (formula rendered to ${OUT})" >&2
  exit 0
fi

TAP_DIR="$(mktemp -d)"

# Supply the token via GIT_ASKPASS so it never appears in the clone URL, argv, or
# process listings. The askpass helper reads TAP_TOKEN from the environment.
ASKPASS="$(mktemp)"
printf '#!/bin/sh\nprintf %%s "%s"\n' '$TAP_TOKEN' > "${ASKPASS}"
chmod 700 "${ASKPASS}"
cleanup() { rm -f "${ASKPASS}"; }
trap cleanup EXIT

GIT_ASKPASS="${ASKPASS}" GIT_TERMINAL_PROMPT=0 TAP_TOKEN="${TAP_TOKEN}" \
  git clone "https://x-access-token@github.com/concord-consortium/homebrew-tap.git" "${TAP_DIR}"

mkdir -p "${TAP_DIR}/Formula"
cp "${OUT}" "${TAP_DIR}/Formula/cc-data.rb"
cd "${TAP_DIR}"
git add Formula/cc-data.rb
git -c user.name="cc-data release" -c user.email="noreply@concord.org" commit -m "cc-data ${VERSION}"
GIT_ASKPASS="${ASKPASS}" GIT_TERMINAL_PROMPT=0 TAP_TOKEN="${TAP_TOKEN}" git push
