#!/usr/bin/env bash
# Runs every Go spec. MODE=human (default) streams full output; MODE=llm prints only
# failure lines, or a single ok line. Log and coverage are written to $TEST_DIR.
set -euo pipefail
MODE="${MODE:-human}"
TEST_DIR="${TEST_DIR:-.test-output}"
mkdir -p "$TEST_DIR"
args=(./... -count=1 -coverprofile="$TEST_DIR/coverage.out")
if [[ "$MODE" == "llm" ]]; then
  if go test "${args[@]}" -args -ginkgo.no-color >"$TEST_DIR/unit.log" 2>&1; then
    echo "ok: all specs passed (log: $TEST_DIR/unit.log)"
  else
    grep -E '^(FAIL|--- FAIL|panic:|# )|\[FAILED\]|\[PANICKED\]|_test\.go:[0-9]+|\.go:[0-9]+:[0-9]+:|^\s+(Expected|to |not to )' \
      "$TEST_DIR/unit.log" | head -200
    exit 1
  fi
else
  go test "${args[@]}" 2>&1 | tee "$TEST_DIR/unit.log"
fi
