#!/usr/bin/env bash
# chb-mcp-docker — stdio shim that lets MCP hosts (Cursor, Claude
# Desktop, Claude Code, etc.) drive HIVE (chb)
# container as if it were a local binary.
#
# What this does:
#   1. Uses a local chb:local image (build it with ./scripts/build-image.sh),
#      or whatever CHB_IMAGE points at; --pull=missing fetches if absent.
#   2. Forwards stdin/stdout to `chb-mcp` running inside the container
#   3. Mounts a host workspace dir so runs persist across calls
#   4. Forwards provider API keys from the host environment
#
# Why this exists:
#   MCP servers communicate over stdio. Hosts launch the configured
#   `command:` and pipe JSON-RPC frames through it. A wrapper script
#   bridges that contract to a docker container with no plumbing on the
#   host beyond `docker` itself. (Most setups can skip this script and
#   use the inline `docker run` MCP config in the README instead — the
#   wrapper is the fallback for hosts without ${VAR} interpolation.)
#
# Usage in your MCP config (e.g., ~/.cursor/mcp.json):
#
#   {
#     "mcpServers": {
#       "chb": {
#         "command": "/absolute/path/to/chb-mcp-docker.sh",
#         "env": {
#           "ANTHROPIC_API_KEY": "sk-ant-..."
#         }
#       }
#     }
#   }
#
# Override the image tag, workspace location, or container args via env:
#   CHB_IMAGE      chb:local                                   (default)
#                  Build it with `./scripts/build-image.sh`, or point at
#                  a published image (e.g. ghcr.io/<owner>/...).
#   CHB_WORKSPACE  $HOME/.chb/workspace                         (default)
#   CHB_DOCKER     docker                                       (default)

set -euo pipefail

IMAGE="${CHB_IMAGE:-chb:local}"
WORKSPACE="${CHB_WORKSPACE:-$HOME/.chb/workspace}"
DOCKER="${CHB_DOCKER:-docker}"

mkdir -p "$WORKSPACE"

# Forward the provider env vars the MCP host exports. Empty values are
# fine — the runner's backend resolver tries each one in order and
# falls back through the precedence chain.
ENV_FLAGS=()
for var in \
    ANTHROPIC_API_KEY \
    GEMINI_API_KEY \
    GOOGLE_API_KEY \
    OPENAI_API_KEY \
    OPENAI_BASE_URL \
    OPENAI_ORG \
    HIVE_PROVIDER; do
  if [ -n "${!var:-}" ]; then
    ENV_FLAGS+=(-e "$var=${!var}")
  fi
done

# `docker run -i` (no -t) keeps stdin open with no terminal allocation,
# which is what the JSON-RPC stdio contract requires. --rm cleans up.
# --pull=missing avoids an unconditional network call on every launch
# but still ensures the image is present.
# bash 3.2 (the system bash on macOS) errors out under `set -u` when
# expanding an empty array as `"${arr[@]}"`. The `${arr[@]+...}`
# pattern only expands when the array has at least one element,
# yielding nothing when ENV_FLAGS is empty. This pattern is portable
# back to bash 3.2 and forward to bash 5+.
exec "$DOCKER" run --rm -i \
    --pull=missing \
    -v "$WORKSPACE:/app/workspace" \
    -e HIVE_DB_PATH=/app/workspace/hive.db \
    ${ENV_FLAGS[@]+"${ENV_FLAGS[@]}"} \
    "$IMAGE" "$@"
