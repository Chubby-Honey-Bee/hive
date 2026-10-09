# HIVE — developer loop. `make fast` while iterating, `make gate` before a
# push, `make harness` to drive the prompts through the local claude CLI.
.PHONY: build lint fast gate test test-race validate replay smoke preflight harness canary lean ci-status help

CHB_BIN  ?= $(HOME)/bin/chb
MCP_BIN  ?= $(HOME)/bin/chb-mcp
HARNESS_ARGS ?=
PROFILE ?= local-fast

help: ## list targets
	@awk 'BEGIN{FS=":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## build chb + chb-mcp into ~/bin
	go build -o $(CHB_BIN) ./cmd/chb
	go build -o $(MCP_BIN) ./cmd/chb-mcp

lint: ## go vet + gofmt drift (CI's first gate steps)
	go vet ./...
	@drift=$$(gofmt -l cmd internal lean4 assets.go assets_test.go fixtures); if [ -n "$$drift" ]; then echo "gofmt drift:"; echo "$$drift"; exit 1; fi

fast: build lint ## ~30 s: vet, gofmt, unit tests (no race), replay fixtures
	go test ./...
	$(CHB_BIN) replay-behavior --bin $(CHB_BIN) --keep-going

gate: build lint test-race validate replay smoke preflight ## full pre-push gate (what CI runs)

test: ## unit tests, no race detector
	go test ./...

test-race: ## unit tests under the race detector (CI's setting)
	go test -race -count=1 -timeout=900s ./...

validate: build ## in-binary regression harness (its summary prints the counts)
	$(CHB_BIN) validate

replay: build ## byte-equal CLI behaviour fixtures (21)
	$(CHB_BIN) replay-behavior --bin $(CHB_BIN) --keep-going

smoke: build ## MCP server smoke over stdio (20 tools)
	$(CHB_BIN) mcp-smoke --mcp-bin $(MCP_BIN) --chb-bin $(CHB_BIN)

preflight: build ## every shipped workflow passes the launch gate
	@fails=0; for w in workflows/*.yaml; do $(CHB_BIN) preflight --skip-provider-check $$w >/dev/null 2>&1 || { echo "  FAIL $$w"; fails=$$((fails+1)); }; done; \
	  echo "preflight: $$fails failure(s) across $$(ls workflows/*.yaml | wc -l | tr -d ' ') workflows"; test $$fails -eq 0

harness: build ## drive personas, agent templates and the proof workflow through the local claude CLI; prints each case's cost; slow cases need HARNESS_ARGS=--slow; report in workspace/agent-harness/
	$(CHB_BIN) agent-harness $(HARNESS_ARGS)

canary: build ## after a model or Ollama update: the graded twin canary against a routing profile, PROFILE=local-fast by default; report in workspace/agent-harness/
	$(CHB_BIN) agent-harness --profile $(PROFILE) --only profile-canary --slow $(HARNESS_ARGS)

lean: ## compile the Lean proof tree (needs elan/lake; CI runs this too)
	cd lean4 && lake build

ci-status: ## one API call: the latest CI runs for HEAD
	@gh run list --limit 6 --json name,status,conclusion,headSha --jq '.[] | select(.headSha|startswith("'"$$(git rev-parse --short HEAD)"'")) | "\(.status)\t\(.conclusion // "-")\t\(.name)"' | column -t -s'	'
