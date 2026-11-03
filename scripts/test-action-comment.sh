#!/usr/bin/env bash
# Copyright radarmodel.com 2026
# SPDX-License-Identifier: Apache-2.0
#
# Tests scripts/action-comment.sh against a fake gh that records its calls.
# Usage: scripts/test-action-comment.sh
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin"
cat > "$work/bin/gh" << 'GH'
#!/usr/bin/env bash
# Fake gh: logs each call; GET answers come from FAKE_EXISTING (an id or number, or empty).
echo "gh $*" >> "$FAKE_LOG"
if [[ " $* " == *" -X POST "* || " $* " == *" -X PATCH "* ]]; then
	cat > "$FAKE_LOG.body"
	[[ "$*" == *"--jq"* ]] && echo "opened #7"
	exit 0
fi
[ -n "${FAKE_EXISTING:-}" ] && echo "$FAKE_EXISTING"
exit 0
GH
chmod +x "$work/bin/gh"
export PATH="$work/bin:$PATH" GITHUB_REPOSITORY=o/r FAKE_LOG="$work/log" GH_TOKEN=x
echo '{"pull_request":{"number":42}}' > "$work/event.json"
export GITHUB_EVENT_PATH="$work/event.json" ISSUE_TITLE="RadarModel: model drift" ISSUE_LABEL=radarmodel

drift="$work/drift.md"
printf '%s\n### RadarModel AI: 1 breaking\n\n| Source | Severity | Item | Change |\n|---|---|---|---|\n| openai | **BREAKING** | `gpt-4o` (watched) | gone |\n' '<!-- radarmodel-report -->' > "$drift"
quiet="$work/quiet.md"
printf '%s\n### RadarModel AI: nothing at warning or above\n\nNo changes at warning or above.\n' '<!-- radarmodel-report -->' > "$quiet"

fail() { echo "FAIL: $1"; cat "$FAKE_LOG" 2>/dev/null; exit 1; }
run() { : > "$FAKE_LOG"; bash "$here/action-comment.sh" "$@" > "$work/out" 2>&1 || fail "exit $? for $*: $(cat "$work/out")"; }

FAKE_EXISTING= run pr "$drift"
grep -q 'repos/o/r/issues/42/comments' "$FAKE_LOG" && grep -q -- '-X POST' "$FAKE_LOG" || fail "new PR comment"
grep -q 'gpt-4o' "$FAKE_LOG.body" || fail "comment body"

FAKE_EXISTING=99 run pr "$quiet"
grep -q -- '-X PATCH repos/o/r/issues/comments/99' "$FAKE_LOG" || fail "existing PR comment updated even when quiet"

FAKE_EXISTING= run pr "$quiet"
! grep -q -- '-X' "$FAKE_LOG" || fail "quiet report posted a new PR comment"

FAKE_EXISTING= run issue "$drift"
grep -q -- '-X POST repos/o/r/issues --input' "$FAKE_LOG" || fail "issue opened"
grep -q '"labels":\["radarmodel"\]' <(jq -c . "$FAKE_LOG.body") || fail "issue label"

FAKE_EXISTING=5 run issue "$drift"
grep -q -- '-X POST repos/o/r/issues/5/comments' "$FAKE_LOG" || fail "comment on the open issue"

FAKE_EXISTING=5 run issue "$quiet"
! grep -q -- '-X' "$FAKE_LOG" || fail "quiet report touched the issue"

: > "$FAKE_LOG"
echo "not a report" > "$work/bad.md"
if bash "$here/action-comment.sh" pr "$work/bad.md" > /dev/null 2>&1; then fail "posted a file that isn't a report"; fi

echo "action-comment tests passed"
