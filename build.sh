#!/usr/bin/env bash
# build.sh — compile the updateweb server into a single self-contained binary.
#
# Steps:
#   1. go build the server binary (HTML templates are embedded via //go:embed,
#      so no runtime assets are needed alongside it).
#
# Target arch: linux/amd64 by default; pass `--arm64` to cross-compile for ARM.
#
# Requires: go.

set -euo pipefail

cd "$(dirname "$0")"

print_help() {
    cat <<'EOF'
build.sh — compile the updateweb server and bundle release artifacts.

Usage:
  ./build.sh [options]

Options:
  --amd64       Cross-compile for linux/amd64 (default).
  --arm64       Cross-compile for linux/arm64.
  --dev         Stamp the full UTC datetime (YYMMDDTHHMMSSZ) into the release
                name instead of the short YYMMDD form. Useful when producing
                multiple builds in the same day.
  -h, --help    Print this help and exit.

Outputs:
  dist/updateweb       Self-contained server binary

Requires: go.
EOF
}

GOARCH=amd64
DEV_DATE=0
for arg in "$@"; do
    case "${arg}" in
        --arm64) GOARCH=arm64 ;;
        --amd64) GOARCH=amd64 ;;
        --dev)   DEV_DATE=1 ;;
        -h|--help) print_help; exit 0 ;;
        *) echo "unknown arg: ${arg}" >&2; echo "(run with --help for usage)" >&2; exit 2 ;;
    esac
done

VERSION="$(git describe --tags --abbrev=0 2>/dev/null || echo dev)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"

case "${VERSION}" in
    dev*) DEV_DATE=1 ;;
esac
if [ "${DEV_DATE}" = "1" ]; then
    DATE="$(date -u +%y%m%dT%H%M%SZ)"
else
    DATE="$(date -u +%y%m%d)"
fi

BUILD_DIR="dist"
BINARY="${BUILD_DIR}/updateweb"

VERSION_PKG="updateweb/pkg/version"
LDFLAGS="-s -w \
    -X ${VERSION_PKG}.Version=${VERSION} \
    -X ${VERSION_PKG}.Commit=${COMMIT} \
    -X ${VERSION_PKG}.Date=${DATE}"

echo "[1/1] building updateweb binary (linux/${GOARCH}) version=${VERSION} commit=${COMMIT} date=${DATE}"
mkdir -p "${BUILD_DIR}"
rm -rf "${BINARY}" # clear any stale path (older builds staged a dir here)
CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH}" go build -trimpath -ldflags="${LDFLAGS}" -o "${BINARY}" .

echo "done."
echo "  binary:   ${BINARY}"
