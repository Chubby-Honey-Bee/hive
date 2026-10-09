#!/usr/bin/env bash
# build-image — build the HIVE container image locally,
# without depending on a published image at ghcr.io.
#
# Builds for the local Docker engine's own architecture and tags
# `chb:local` unless you name another tag.
#
# Usage:
#   ./scripts/build-image.sh                       # chb:local
#   ./scripts/build-image.sh chb:dev               # custom tag
#
# There is no multi-arch mode. The CLI providers the image installs
# (@anthropic-ai/claude-code, @google/gemini-cli) ship per-platform
# native binaries whose postinstall crashes under QEMU (SIGILL), so each
# architecture has to build on a native host. Run this script on each
# host, or let .github/workflows/publish-image.yml build each arch on a
# native runner and publish the multi-arch manifest. `--multi` stops
# with an error that says so, and `--push`, which only served it, stops
# too.
#
# After a build, point the MCP wrapper at the local tag:
#
#   CHB_IMAGE=chb:local ./scripts/chb-mcp-docker.sh

set -euo pipefail

TAG="chb:local"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --multi)
      echo "error: --multi is not supported: the CLI providers' native postinstall crashes under QEMU (SIGILL)," >&2
      echo "so each architecture must build on a native host. Run this script on each host, or use" >&2
      echo ".github/workflows/publish-image.yml, which builds per arch on native runners." >&2
      exit 2
      ;;
    --push)
      echo "error: --push went with --multi. Build here, then run: docker push <tag>" >&2
      exit 2
      ;;
    -h|--help)
      sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      TAG="$1"
      shift
      ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

echo "── single-arch local build: $TAG ──"
# BuildKit gives us cache mounts for the Go module + build cache, which
# makes incremental rebuilds nearly instant. Single-platform so we can
# `--load` into the local Docker engine for immediate `docker run`.
DOCKER_BUILDKIT=1 docker build \
  --tag "$TAG" \
  --file Dockerfile \
  .

echo ""
echo "✓ Built $TAG"
echo ""
echo "Smoke test:"
docker run --rm "$TAG" --version

echo ""
echo "Use locally with the MCP wrapper:"
echo "  CHB_IMAGE=$TAG ./scripts/chb-mcp-docker.sh"
echo ""
echo "Or in your MCP host config (e.g. ~/.cursor/mcp.json):"
echo "  \"env\": { \"CHB_IMAGE\": \"$TAG\" }"
