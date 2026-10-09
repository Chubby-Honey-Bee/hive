#!/usr/bin/env bash
# build-binaries — cross-compile chb + chb-mcp for every platform via
# goreleaser inside a Docker container. No host Go install required.
#
# Output (arm64 targets carry `_v8.0`, amd64 `_v1`):
#   dist/chb_<os>_<arch>_<variant>/chb[.exe]
#   dist/chb-mcp_<os>_<arch>_<variant>/chb-mcp[.exe]
#
# Platforms produced (per .goreleaser.yaml):
#   linux/amd64   linux/arm64
#   darwin/amd64  darwin/arm64
#   windows/amd64 windows/arm64
#
# Usage:
#   ./scripts/build-binaries.sh           # snapshot build, all platforms
#   ./scripts/build-binaries.sh --single  # just this machine's OS and architecture
#
# Why goreleaser-in-docker: HIVE's release pipeline already uses
# goreleaser. Running the same tool locally produces the binaries CI would
# publish, without forcing every contributor to install the Go toolchain.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$REPO_ROOT"

GORELEASER_IMAGE="${GORELEASER_IMAGE:-goreleaser/goreleaser:latest}"

# --single-target builds for GOOS/GOARCH, which inside the container would
# be the container's linux — so pass the host's.
TARGET_ENV=()
EXTRA_FLAGS=()
if [[ "${1:-}" == "--single" ]]; then
  case "$(uname -s)" in
    Darwin) goos=darwin ;;
    Linux) goos=linux ;;
    MINGW*|MSYS*|CYGWIN*) goos=windows ;;
    *) echo "unsupported OS: $(uname -s)" >&2; exit 2 ;;
  esac
  case "$(uname -m)" in
    arm64|aarch64) goarch=arm64 ;;
    x86_64|amd64) goarch=amd64 ;;
    *) echo "unsupported architecture: $(uname -m)" >&2; exit 2 ;;
  esac
  TARGET_ENV+=(-e "GOOS=$goos" -e "GOARCH=$goarch")
  EXTRA_FLAGS+=(--single-target)
fi

echo "── Building chb + chb-mcp via $GORELEASER_IMAGE ──"

# build           : raw binaries only — no archives, checksums or release
# --snapshot      : version stamps as `0.0.0-SNAPSHOT-<sha>`, no tag required
# --clean         : wipe prior dist/ before building
# --skip=before   : skip the `go mod tidy -diff` + `go test ./...` hooks
# The ${arr[@]+...} form expands an empty array to nothing under `set -u`,
# which bash 3.2 (macOS) otherwise rejects.
docker run --rm \
  -v "$REPO_ROOT:/src" \
  -w /src \
  -e GOFLAGS=-buildvcs=false \
  ${TARGET_ENV[@]+"${TARGET_ENV[@]}"} \
  "$GORELEASER_IMAGE" \
  build \
    --snapshot \
    --clean \
    --skip=before \
    ${EXTRA_FLAGS[@]+"${EXTRA_FLAGS[@]}"}

echo ""
echo "✓ Build complete. Artifacts:"
find dist -maxdepth 2 -type f \( -name 'chb' -o -name 'chb.exe' -o -name 'chb-mcp' -o -name 'chb-mcp.exe' \) \
  | sort
