#!/usr/bin/env bash
# Copyright radarmodel.com 2026
# SPDX-License-Identifier: Apache-2.0
#
# Tests scripts/action-comment.sh against a fake gh that records its calls.
# The fake answers a lookup of comments or issues with FAKE_EXISTING and a
# lookup of earlier report bodies with FAKE_LAST; /user fails, as it does
# for GITHUB_TOKEN. The jq filters the script passes are checked by running
# them on sample data.
# Usage: scripts/test-action-comment.sh
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin"
cat > "$work/bin/gh" << 'GH'
#!/usr/bin/env bash
echo "gh $*" >> "$FAKE_LOG"
args=" $* "
if [[ "$args" == *" -X POST "* || "$args" == *" -X PATCH "* ]]; then
	cat > "$FAKE_LOG.body"
	[[ "$args" == *"--jq"* ]] && echo "opened #7"
	exit 0
fi
[[ "$args" == *" api user "* ]] && exit 1
# Keep the filter, to run it against sample data in the test.
prev=
for a in "$@"; do [ "$prev" = --jq ] && printf '%s\n' "$a" >> "$FAKE_LOG.jq"; prev=$a; done
if [[ "$args" == *"| .body | @json"* || "$args" == *".body | @json"* ]]; then
	[ -n "${FAKE_LAST:-}" ] && printf '%s\n' "$FAKE_LAST"
	exit 0
fi
[ -n "${FAKE_EXISTING:-}" ] && printf '%s\n' "$FAKE_EXISTING"
exit 0
GH
chmod +x "$work/bin/gh"
export PATH="$work/bin:$PATH" GITHUB_REPOSITORY=o/r FAKE_LOG="$work/log" GH_TOKEN=x
echo '{"pull_request":{"number":42}}' > "$work/event.json"
export GITHUB_EVENT_PATH="$work/event.json" ISSUE_TITLE="RadarModel: model drift" ISSUE_LABEL="model drift"

drift="$work/drift.md"
printf '%s\n### RadarModel AI: 1 breaking\n\n| Source | Severity | Item | Change |\n|---|---|---|---|\n| openai | **BREAKING** | `gpt-4o` (watched) | gone |\n' '<!-- radarmodel-report -->' > "$drift"
quiet="$work/quiet.md"
printf '%s\n### RadarModel AI: nothing at warning or above\n\nNo changes at warning or above.\n' '<!-- radarmodel-report -->' > "$quiet"

fail() { echo "::error::action-comment test: $1"; cat "$FAKE_LOG" 2>/dev/null; cat "$work/out" 2>/dev/null; exit 1; }
run() { : > "$FAKE_LOG"; : > "$FAKE_LOG.jq"; bash "$here/action-comment.sh" "$@" > "$work/out" 2>&1 || fail "exit $? for $*: $(cat "$work/out")"; }

FAKE_EXISTING= run pr "$drift"
grep -q -- '-X POST repos/o/r/issues/42/comments' "$FAKE_LOG" || fail "new PR comment"
jq -e '.body | startswith("<!-- radarmodel:default -->\n<!-- radarmodel-report -->")' "$FAKE_LOG.body" > /dev/null || fail "comment starts with its key"

# The filter keeps only this key's comments by the posting account.
filter=$(head -n 1 "$FAKE_LOG.jq")
got=$(jq -r "$filter" <<< '[
 {"id":1,"user":{"login":"mallory"},"body":"<!-- radarmodel:default -->\nfake"},
 {"id":2,"user":{"login":"github-actions[bot]"},"body":"<!-- radarmodel:schema -->\nother"},
 {"id":3,"user":{"login":"github-actions[bot]"},"body":"<!-- radarmodel:default -->\nours"}]')
[ "$got" = 3 ] || fail "comment filter picked '$got'"

FAKE_EXISTING=$'99\n100' run pr "$quiet"
grep -q -- '-X PATCH repos/o/r/issues/comments/99' "$FAKE_LOG" || fail "existing PR comment updated even when quiet"

FAKE_EXISTING= run pr "$quiet"
! grep -q -- '-X' "$FAKE_LOG" || fail "quiet report posted a new PR comment"

COMMENT_KEY=schema FAKE_EXISTING= run pr "$drift"
jq -e '.body | startswith("<!-- radarmodel:schema -->")' "$FAKE_LOG.body" > /dev/null || fail "comment key"

FAKE_EXISTING= run issue "$drift"
grep -q -- '-X POST repos/o/r/issues --input' "$FAKE_LOG" || fail "issue opened"
jq -e '.labels == ["model drift"]' "$FAKE_LOG.body" > /dev/null || fail "issue label"
grep -q 'labels=model%20drift' "$FAKE_LOG" || fail "label not encoded in the search"

FAKE_EXISTING=5 run issue "$drift"
grep -q -- '-X POST repos/o/r/issues/5/comments' "$FAKE_LOG" || fail "comment on the open issue"

same=$(jq -n --arg b "<!-- radarmodel:default -->
$(cat "$drift")" '$b | @json' | jq -r .)
FAKE_EXISTING=5 FAKE_LAST="$same" run issue "$drift"
! grep -q -- '-X' "$FAKE_LOG" || fail "the same report was posted twice"

# A failing source's error text differs run to run; that alone isn't new.
err1="$work/err1.md"; err2="$work/err2.md"
printf '%s\n### RadarModel AI: 1 source(s) failed to scan\n\n- **groq** couldn'"'"'t be scanned: dial tcp 1.2.3.4:443: i/o timeout. Its stored snapshot is unchanged.\n' '<!-- radarmodel-report -->' > "$err1"
sed 's/1\.2\.3\.4/5.6.7.8/' "$err1" > "$err2"
prev=$(jq -n --arg b "<!-- radarmodel:default -->
$(cat "$err1")" '$b | @json' | jq -r .)
FAKE_EXISTING=5 FAKE_LAST="$prev" run issue "$err2"
! grep -q -- '-X' "$FAKE_LOG" || fail "the same failure posted again"

# Long reports are cut to fit a comment.
{ head -n 5 "$drift"; for i in $(seq 1 1500); do echo "| openai | **WARNING** | \`m$i\` (watched) | gone, a long line to fill the comment up |"; done; } > "$work/long.md"
FAKE_EXISTING= run pr "$work/long.md"
jq -e '(.body | length) < 65536 and (.body | endswith("the rest is in the job summary."))' "$FAKE_LOG.body" > /dev/null || fail "long report not cut"

: > "$FAKE_LOG"
if COMMENT_KEY='x-->y' bash "$here/action-comment.sh" pr "$drift" > /dev/null 2>&1; then fail "a key that ends the HTML comment was accepted"; fi

FAKE_EXISTING=5 run issue "$quiet"
! grep -q -- '-X' "$FAKE_LOG" || fail "quiet report touched the issue"

: > "$FAKE_LOG"
echo "not a report" > "$work/bad.md"
if bash "$here/action-comment.sh" pr "$work/bad.md" > /dev/null 2>&1; then fail "posted a file that isn't a report"; fi

echo "action-comment tests passed"
