#!/usr/bin/env bash
# Fail a build whose binary was compiled by a Go other than the one go.mod's toolchain line pins.
# A package zip's checksum depends on the Go that built cc-data, so every build of a tag has to
# use the same one. The optional second argument replaces go.mod's version, for the tests.
set -euo pipefail

BIN="${1:?binary path required}"
WANT="${2:-$(awk '$1 == "toolchain" { print $2 }' "$(dirname "$0")/../go.mod")}"
if [[ -z "${WANT}" ]]; then
  echo "go.mod has no toolchain line" >&2
  exit 1
fi
GOT="$(go version "${BIN}" | awk '{ print $NF }')"
if [[ "${GOT}" != "${WANT}" ]]; then
  echo "${BIN} was built with ${GOT}, but go.mod pins ${WANT}" >&2
  exit 1
fi
