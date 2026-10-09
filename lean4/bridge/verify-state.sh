#!/usr/bin/env bash
# Verify MSS invariants for a workspace database (hive.db) via Lean 4 typechecking.
#
# Usage:
#   ./lean4/bridge/verify-state.sh workspace/hive.db [wave_number]
#
# Prerequisites:
#   - chb on PATH or built at the repo root (it runs `chb lean4-extract`)
#   - Lean 4 toolchain installed (via elan)
#   - lean4/lakefile.lean configured
#
# Exit codes:
#   0 = all invariants verified
#   1 = verification failed (invariant violation found)
#   2 = extraction or setup error (including no chb found)
#
# © 2026 Chubby Honey Bee Inc. — chronomancy.io WASP/CDE/MSS framework

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LEAN4_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

DB_PATH="${1:?Usage: verify-state.sh <db_path> [wave_number]}"
WAVE="${2:-}"

# `chb lean4-extract` is the one extractor. It refuses a database whose Go
# MSS audit fails, and it states only obligations that `by decide` can
# elaborate through MSS.Decidability.
EXTRACT_CMD=""
if command -v chb &>/dev/null; then
    EXTRACT_CMD="chb lean4-extract"
elif [ -x "$LEAN4_DIR/../chb" ]; then
    EXTRACT_CMD="$LEAN4_DIR/../chb lean4-extract"
else
    echo "[verify] ERROR: chb not found on PATH or at the repo root." >&2
    echo "[verify]   The extractor is 'chb lean4-extract'. Build it with: go build -o chb ./cmd/chb" >&2
    exit 2
fi

# Create instance directory
INSTANCE_DIR="$LEAN4_DIR/instance"
mkdir -p "$INSTANCE_DIR"

if [ -n "$WAVE" ]; then
    LEAN_FILE="$INSTANCE_DIR/Wave${WAVE}.lean"
    echo "[verify] Extracting wave $WAVE from $DB_PATH..."
    $EXTRACT_CMD --db "$DB_PATH" --wave "$WAVE" > "$LEAN_FILE"
else
    LEAN_FILE="$INSTANCE_DIR/All.lean"
    echo "[verify] Extracting all waves from $DB_PATH..."
    $EXTRACT_CMD --db "$DB_PATH" > "$LEAN_FILE"
fi

if [ ! -s "$LEAN_FILE" ]; then
    echo "[verify] ERROR: extraction produced empty file" >&2
    exit 2
fi

echo "[verify] Extracted to $LEAN_FILE"
echo "[verify] Running Lean 4 typecheck..."

# Build the MSS library first, then typecheck the instance
cd "$LEAN4_DIR"

if lake build MSS 2>&1; then
    echo "[verify] MSS library built successfully"
else
    echo "[verify] ERROR: MSS library build failed" >&2
    exit 2
fi

# `lake env` puts the built MSS library on Lean's search path. A bare `lean`
# cannot resolve the instance's imports, and its toolchain error would be
# reported below as an invariant violation.
if lake env lean "$LEAN_FILE" 2>&1; then
    echo ""
    echo "[verify] ✓ DECIDABLE MSS INVARIANTS VERIFIED"
    echo "[verify]   - NoDirectLaundering"
    echo "[verify]   - NoUntraceableGuarantees"
    echo "[verify]   - DepsValid"
    echo "[verify]   - Acyclic"
    echo "[verify]   (transitive no-laundering: Go audit, passed at extraction)"

    # Compute proof certificate hash
    if command -v sha256sum >/dev/null 2>&1; then
        CERT_HASH=$(sha256sum "$LEAN_FILE" | cut -d' ' -f1)
    else
        CERT_HASH=$(shasum -a 256 "$LEAN_FILE" | cut -d' ' -f1)  # macOS
    fi
    echo "[verify]   Proof certificate hash: sha256:$CERT_HASH"
    exit 0
else
    echo ""
    echo "[verify] ✗ MSS INVARIANT VERIFICATION FAILED"
    echo "[verify]   Check $LEAN_FILE for details"
    exit 1
fi
