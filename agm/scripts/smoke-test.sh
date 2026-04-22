#!/usr/bin/env bash
# End-to-end smoke test for agm v0.0.1.
#
# Verifies:
#   1. `agm init` creates .agm/ with expected layout
#   2. `agm watch` records file changes
#   3. `agm session start/stop` manages sessions
#   4. `agm hook <name>` persists hook payloads (DB + JSONL)
#   5. `agm events --session <id>` can retrieve everything
#
# Usage:
#   AGM=./agm.exe ./scripts/smoke-test.sh       # custom binary
#   ./scripts/smoke-test.sh                     # assumes `agm` on PATH
#
# Exit code 0 on pass, non-zero on any assertion failure.

set -euo pipefail

AGM="${AGM:-agm}"

# Resolve AGM to an absolute path if it's a relative path — the script cds into
# a tempdir, so relative paths would break. `command -v` handles PATH lookups.
if [[ "$AGM" == */* ]]; then
    AGM="$(cd "$(dirname "$AGM")" && pwd)/$(basename "$AGM")"
else
    AGM="$(command -v "$AGM")" || { echo "agm binary '$AGM' not found on PATH" >&2; exit 1; }
fi

# --- pretty output helpers -------------------------------------------------
red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
step()  { printf '\n\033[36m▸ %s\033[0m\n' "$*"; }
pass()  { green "  ✓ $*"; }
fail()  { red   "  ✗ $*"; exit 1; }

# --- isolated workspace + cleanup ------------------------------------------
WORKDIR="$(mktemp -d)"
WATCH_PID=""

cleanup() {
    if [[ -n "$WATCH_PID" ]] && kill -0 "$WATCH_PID" 2>/dev/null; then
        kill "$WATCH_PID" 2>/dev/null || true
        wait "$WATCH_PID" 2>/dev/null || true
    fi
    rm -rf "$WORKDIR"
}
trap cleanup EXIT

echo "agm smoke test"
echo "  binary:   $AGM"
echo "  workdir:  $WORKDIR"
echo "  version:  $("$AGM" --version)"

cd "$WORKDIR"

# --- 1. init ---------------------------------------------------------------
step "agm init"
"$AGM" init > /dev/null
[[ -f .agm/config.json ]] || fail ".agm/config.json missing"
[[ -f .agm/state.db    ]] || fail ".agm/state.db missing"
[[ -f .agm/events.jsonl ]] || fail ".agm/events.jsonl missing"
[[ -d .agm/logs        ]] || fail ".agm/logs missing"
pass ".agm/ layout created"

# --- 2. watcher records a file change --------------------------------------
step "agm watch records FileChange"
"$AGM" watch > "$WORKDIR/watch.log" 2>&1 &
WATCH_PID=$!
sleep 1

echo "hello" > watched.txt
sleep 1

grep -q "watched.txt" "$WORKDIR/watch.log" \
    || fail "watcher did not log watched.txt (see $WORKDIR/watch.log)"
pass "watcher logged watched.txt"

# --- 3. session lifecycle --------------------------------------------------
step "agm session start/stop"
SID="$("$AGM" session start smoke-test | tail -1)"
[[ "$SID" =~ ^sess_ ]] || fail "unexpected session id: '$SID'"
pass "session id: $SID"

# --- 4. hooks --------------------------------------------------------------
step "agm hook SessionStart / PostToolUse"
echo "{\"session_id\":\"$SID\"}"   | "$AGM" hook SessionStart  > /dev/null
echo '{"tool_name":"Edit"}'        | "$AGM" hook PostToolUse  > /dev/null
pass "hooks accepted"

"$AGM" session stop "$SID" > /dev/null
pass "session stopped"

# --- 5. retrieval ----------------------------------------------------------
step "agm events --session $SID"
EVENTS_OUT="$("$AGM" events --session "$SID")"

# CLI-driven lifecycle events (from `agm session start/stop`).
echo "$EVENTS_OUT" | grep -q SessionRegistered \
    || fail "SessionRegistered missing — CLI start did not emit it"
echo "$EVENTS_OUT" | grep -q SessionEnded \
    || fail "SessionEnded missing — CLI stop did not emit it"
pass "CLI admin events present (SessionRegistered, SessionEnded)"

# Hook-driven events (from `agm hook ...`).
echo "$EVENTS_OUT" | grep -q SessionStart \
    || fail "SessionStart missing — hook did not land"
echo "$EVENTS_OUT" | grep -q PostToolUse \
    || fail "PostToolUse missing — hook did not land"
pass "hook events present (SessionStart, PostToolUse)"

# SessionStart must appear exactly once — CLI start should NOT also emit it.
# This is the regression guard for issue #2 (duplicate SessionStart).
SS_COUNT="$(echo "$EVENTS_OUT" | grep -c SessionStart || true)"
[[ "$SS_COUNT" -eq 1 ]] \
    || fail "SessionStart appeared $SS_COUNT times, want exactly 1 (regression of issue #2)"
pass "SessionStart not duplicated"

# JSONL completeness: every event written to SQLite must also be mirrored to
# events.jsonl. Regression guard for issue #1 — before the recorder refactor,
# only hook-driven rows landed in JSONL; FileChange and CLI admin events were
# silently dropped.
for t in FileChange SessionRegistered SessionStart PostToolUse SessionEnded; do
    grep -q "\"type\":\"$t\"" .agm/events.jsonl \
        || fail "events.jsonl missing type=$t (regression of issue #1)"
done
pass "events.jsonl mirrors all 5 event types"

# Every JSONL line must be valid JSON — concurrent writers must not tear lines.
INVALID="$(
    while IFS= read -r line; do
        printf '%s' "$line" | python -c 'import sys,json; json.loads(sys.stdin.read())' 2>/dev/null \
            || echo BAD
    done < .agm/events.jsonl | grep -c BAD || true
)"
[[ "$INVALID" -eq 0 ]] || fail "$INVALID JSONL lines failed to parse"
pass "all events.jsonl lines are valid JSON"

echo
green "✓ smoke test passed"
