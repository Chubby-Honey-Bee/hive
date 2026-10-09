# HIVE — multi-provider container build
# ─────────────────────────────────────────────────────────────────────
#
# Stage 1: golang:1.27-alpine builder. Compiles both binaries
# (chb + chb-mcp) statically (CGO_ENABLED=0; we use
# modernc.org/sqlite, not C-bindings, so no libc needed).
#
# Stage 2: node:22-alpine runtime, running as the unprivileged `node`
# user. Ships every CLI provider HIVE supports so chb can auto-resolve
# to whichever auth the host has live:
#
#   API-key providers     env var       backend
#   ─────────────────     ───────       ───────
#   anthropic             ANTHROPIC_API_KEY  → SDK
#   openai                OPENAI_API_KEY     → SDK
#   gemini                GEMINI_API_KEY     → SDK
#   copilot               OPENAI_API_KEY + OPENAI_BASE_URL → SDK
#                         (an alias for openai; the base URL names the
#                         OpenAI-compatible endpoint)
#
#   CLI providers         binary        backend         auth state mount
#   ─────────────         ──────        ───────         ────────────────
#   claude-cli            claude        Node CLI        ~/.claude, ~/.claude.json
#   gemini-cli            gemini        Node CLI        ~/.gemini
#   (home is /home/node inside the container)
#
# gh is not a provider. It serves `--auto-pr` (`gh pr create`), which
# reads ~/.config/gh or GH_TOKEN.
#
# Resolution chain (docs/specs/runner.md § Backend Selection):
#   1. --provider on the run command
#   2. HIVE_PROVIDER
#   3. ANTHROPIC_API_KEY / GEMINI_API_KEY (or GOOGLE_API_KEY) / OPENAI_API_KEY
#   4. First CLI on PATH (claude → claude-cli, gemini → gemini-cli)
#   5. Fall through to claude-cli (will surface a clear auth error)
#
# Why alpine and not distroless: distroless can't host the CLI
# providers at all (no shell, no Node). node:22-alpine is the smallest
# runtime that supports the full provider matrix — ~180 MB vs ~15 MB
# distroless. The trade is auth flexibility for size; if you only ever
# use API keys, swap the runtime stage to gcr.io/distroless/static-debian12.
#
# Build:
#   docker build --tag chb:local --file Dockerfile .
#
# Run (MCP stdio, all auth surfaces mounted). The entrypoint is chb-mcp;
# chb is beside it at /usr/local/bin/chb for `--entrypoint`:
#   docker run --rm -i \
#     -v chb-workspace:/app/workspace \
#     -v "$HOME/.claude:/home/node/.claude" \
#     -v "$HOME/.claude.json:/home/node/.claude.json" \
#     -v "$HOME/.gemini:/home/node/.gemini" \
#     -v "$HOME/.config/gh:/home/node/.config/gh" \
#     -e ANTHROPIC_API_KEY -e OPENAI_API_KEY -e GEMINI_API_KEY -e GH_TOKEN \
#     chb:local
#
# The image listens on no port: chb-mcp speaks over stdin and stdout, and
# chb is a command. Publish nothing.
# Mounts: /app/workspace (REQUIRED), /app/agents, /app/workflows,
#         /app/foragers.
# ─────────────────────────────────────────────────────────────────────

ARG GO_VERSION=1.27

# ── builder ──────────────────────────────────────────────────────────
FROM golang:${GO_VERSION}-alpine AS build

RUN apk add --no-cache git

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY assets.go  ./
COPY cmd/       ./cmd/
COPY internal/  ./internal/
COPY agents/    ./agents/
COPY workflows/ ./workflows/
COPY foragers/   ./foragers/
COPY fixtures/  ./fixtures/

ARG TARGETOS=linux
ARG TARGETARCH=amd64
# The version the binaries report; the runtime stage repeats it for the
# image label.
ARG VERSION=dev

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -buildid= -X main.version=${VERSION}" \
        -o /out/chb ./cmd/chb && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -buildid= -X main.version=${VERSION}" \
        -o /out/chb-mcp ./cmd/chb-mcp

RUN mkdir -p /out/empty-workspace

# ── runtime ──────────────────────────────────────────────────────────
FROM node:22-alpine

ARG VERSION=dev
ARG REVISION=unknown

LABEL org.opencontainers.image.title="HIVE" \
      org.opencontainers.image.description="HIVE (chb) — multi-agent reasoning with an MSS integrity gate. Multi-provider runtime with CLI auth pass-through. Built on CDE / WASP / MSS." \
      org.opencontainers.image.source="https://github.com/Chubby-Honey-Bee/hive" \
      org.opencontainers.image.url="https://github.com/Chubby-Honey-Bee/hive" \
      org.opencontainers.image.documentation="https://github.com/Chubby-Honey-Bee/hive/blob/main/README.md" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"

# System deps:
#   bash             — the agent runner's shell tool runs commands through it; alpine ships only sh
#   git              — VCS context for swarm tools (commit, blame)
#   ca-certificates  — HTTPS to all SDK endpoints
#   github-cli       — gh, for --auto-pr (`gh pr create`)
#   tini             — proper PID 1 for clean stdio MCP signal handling
RUN apk add --no-cache \
        bash \
        git \
        ca-certificates \
        github-cli \
        tini

# CLI providers — installed globally so they're on PATH for chb's
# exec.LookPath probe. They ship per-platform native binaries whose
# postinstall crashes under QEMU (SIGILL), so build each architecture on
# a native host, as .github/workflows/publish-image.yml does.
RUN npm install -g --no-audit --no-fund \
        @anthropic-ai/claude-code \
        @google/gemini-cli \
    && npm cache clean --force

WORKDIR /app
RUN chown node:node /app

# Everything from here runs as `node`: an agent's shell tool runs arbitrary
# commands, and as root it would hold root over every mounted path.
USER node

COPY --from=build /out/chb      /usr/local/bin/chb
COPY --from=build /out/chb-mcp  /usr/local/bin/chb-mcp

COPY --from=build --chown=node:node /src/agents     /app/agents
COPY --from=build --chown=node:node /src/workflows  /app/workflows
COPY --from=build --chown=node:node /src/foragers   /app/foragers
COPY --from=build --chown=node:node /src/fixtures   /app/fixtures

# MIT requires the notice in every copy; the release archives carry it too.
COPY LICENSE /app/LICENSE

COPY --from=build --chown=node:node /out/empty-workspace /app/workspace

VOLUME ["/app/workspace"]

ENV HIVE_DB_PATH=/app/workspace/hive.db

# tini reaps zombies and forwards signals — important for stdio MCP
# where the parent expects clean SIGTERM behavior on shutdown. The
# default command is the MCP server; `--entrypoint /usr/local/bin/chb`
# runs the command line.
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/chb-mcp"]
