#!/usr/bin/env bash
# Sign and notarize a macOS binary as a goreleaser build post-hook, before
# archiving. No-ops on non-Mach-O binaries (Linux/Windows). Degrades to an
# unsigned dev build with a loud log line when the signing credentials are
# absent; the release workflow fails the tag if a macOS artifact was left
# unsigned.
#
# Required env for the signed path:
#   MACOS_SIGN_IDENTITY   Developer ID Application identity (codesign --sign)
#   AC_API_KEY_PATH       App Store Connect API key (.p8) path
#   AC_API_KEY_ID         key id
#   AC_API_ISSUER         issuer id
set -euo pipefail

BIN="${1:?binary path required}"
MARKER_DIR="$(dirname "${BIN}")"

# Only sign Mach-O binaries; a Linux ELF or Windows PE is skipped cleanly.
if ! file "${BIN}" | grep -q "Mach-O"; then
  exit 0
fi

if [[ -z "${MACOS_SIGN_IDENTITY:-}" || -z "${AC_API_KEY_PATH:-}" || -z "${AC_API_KEY_ID:-}" || -z "${AC_API_ISSUER:-}" ]]; then
  echo "notarize: signing credentials absent; shipping an UNSIGNED dev build" >&2
  echo "unsigned" > "${MARKER_DIR}/.notarize-unsigned"
  exit 0
fi

echo "notarize: codesign ${BIN}"
codesign --force --options runtime --timestamp --sign "${MACOS_SIGN_IDENTITY}" "${BIN}"

# notarytool accepts only ZIP/DMG/PKG, never a bare Mach-O or tar.gz.
SUBMIT_ZIP="$(mktemp -d)/cc-data.zip"
/usr/bin/ditto -c -k --keepParent "${BIN}" "${SUBMIT_ZIP}"

echo "notarize: submitting to Apple (this can take minutes)"
xcrun notarytool submit "${SUBMIT_ZIP}" \
  --key "${AC_API_KEY_PATH}" \
  --key-id "${AC_API_KEY_ID}" \
  --issuer "${AC_API_ISSUER}" \
  --wait

rm -f "${SUBMIT_ZIP}"

# No stapling: a ticket cannot be stapled to a standalone Mach-O (stapler
# error 73), so the shipped binary is notarized-but-unstapled, the standard
# state for CLI tools.
echo "signed" > "${MARKER_DIR}/.notarize-signed"
echo "notarize: signed and notarized ${BIN}"
