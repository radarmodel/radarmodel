#!/usr/bin/env bash
# Copyright radarmodel.com 2026
# SPDX-License-Identifier: Apache-2.0
#
# Posts a RadarModel Markdown report where people see it, for action.yml:
#   pr     one comment on the pull request, updated on every run
#   issue  an open issue with the given title and label: created on the
#          first report worth reading, then a comment for each later one
# A report is worth posting when it has a change at or above the minimum
# severity, or a source that couldn't be scanned. A pull request comment
# that exists already is updated either way, so it never shows old drift.
#
# Usage: action-comment.sh pr|issue REPORT.md
# Environment: GITHUB_REPOSITORY, GITHUB_EVENT_PATH (pr), ISSUE_TITLE and
# ISSUE_LABEL (issue), GH_TOKEN; gh and jq on PATH.
set -euo pipefail

mode=$1
report=$2
marker='<!-- radarmodel-report -->'
repo=$GITHUB_REPOSITORY

[ -f "$report" ] || { echo "no report at $report" >&2; exit 1; }
head -n 1 "$report" | grep -qxF "$marker" || { echo "$report is not a RadarModel report" >&2; exit 1; }
worth=false
if grep -q '^| Source | Severity' "$report" || grep -q "couldn't be scanned" "$report"; then
	worth=true
fi
body=$(jq -Rs '{body: .}' < "$report")

case "$mode" in
pr)
	number=$(jq -r '.pull_request.number // empty' "$GITHUB_EVENT_PATH")
	[ -n "$number" ] || { echo "not a pull request event" >&2; exit 1; }
	id=$(gh api --paginate "repos/$repo/issues/$number/comments" \
		--jq ".[] | select(.body | startswith(\"$marker\")) | .id" | head -n 1)
	if [ -n "$id" ]; then
		gh api -X PATCH "repos/$repo/issues/comments/$id" --input - <<< "$body" > /dev/null
		echo "updated comment $id on #$number"
	elif [ "$worth" = true ]; then
		gh api -X POST "repos/$repo/issues/$number/comments" --input - <<< "$body" > /dev/null
		echo "commented on #$number"
	else
		echo "nothing to report on #$number"
	fi
	;;
issue)
	[ "$worth" = true ] || { echo "nothing to report"; exit 0; }
	title=${ISSUE_TITLE:?}
	label=${ISSUE_LABEL:-radarmodel}
	number=$(gh api --paginate "repos/$repo/issues?state=open&labels=$label&per_page=100" \
		--jq ".[] | select(.pull_request == null) | select(.title == $(jq -Rn --arg t "$title" '$t')) | .number" | head -n 1)
	if [ -n "$number" ]; then
		gh api -X POST "repos/$repo/issues/$number/comments" --input - <<< "$body" > /dev/null
		echo "commented on #$number"
	else
		jq -n --arg title "$title" --arg label "$label" --rawfile body "$report" \
			'{title: $title, body: $body, labels: [$label]}' |
			gh api -X POST "repos/$repo/issues" --input - --jq '"opened #\(.number)"'
	fi
	;;
*)
	echo "mode must be pr or issue" >&2
	exit 2
	;;
esac
