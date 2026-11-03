#!/usr/bin/env bash
# Copyright radarmodel.com 2026
# SPDX-License-Identifier: Apache-2.0
#
# Posts a RadarModel Markdown report where people see it, for action.yml:
#   pr     one comment on the pull request, updated on every run
#   issue  an open issue with the given title and label: created on the
#          first report worth reading, then a comment for each later one
#          that differs from the last
# A report is worth posting when it has a change at or above the minimum
# severity, or a source that couldn't be scanned. A pull request comment
# that exists already is updated either way, so it never shows old drift.
#
# Each comment starts with a key line, so two reports in one repository
# (AI and schema, or two configs) keep their own comments, and only comments
# the posting account wrote itself are ever updated.
#
# Usage: action-comment.sh pr|issue REPORT.md
# Environment: GITHUB_REPOSITORY, GITHUB_EVENT_PATH (pr), ISSUE_TITLE and
# ISSUE_LABEL (issue), COMMENT_KEY (default "default"), GH_TOKEN; gh and jq
# on PATH.
set -euo pipefail

mode=$1
report=$2
marker='<!-- radarmodel-report -->'
repo=$GITHUB_REPOSITORY
key="<!-- radarmodel:${COMMENT_KEY:-default} -->"

[ -f "$report" ] || { echo "no report at $report" >&2; exit 1; }
head -n 1 "$report" | grep -qxF "$marker" || { echo "$report is not a RadarModel report" >&2; exit 1; }
case "$key" in *$'\n'*) echo "comment key has a line break" >&2; exit 2 ;; esac
worth=false
if grep -q '^| Source | Severity' "$report" || grep -q "couldn't be scanned" "$report"; then
	worth=true
fi
text="$key
$(cat "$report")"
body=$(jq -n --arg b "$text" '{body: $b}')

# The account the token posts as: a personal token's user, or the Actions
# bot for GITHUB_TOKEN (which can't read /user).
me=$(gh api user --jq .login 2>/dev/null || true)
[ -n "$me" ] || me='github-actions[bot]'
mine=".user.login == $(jq -n --arg m "$me" '$m') and (.body | startswith($(jq -n --arg k "$key" '$k')))"

case "$mode" in
pr)
	number=$(jq -r '.pull_request.number // empty' "$GITHUB_EVENT_PATH")
	[ -n "$number" ] || { echo "not a pull request event" >&2; exit 1; }
	ids=$(gh api --paginate "repos/$repo/issues/$number/comments" --jq ".[] | select($mine) | .id")
	id=${ids%%$'\n'*}
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
	q=$(jq -rn --arg l "$label" '$l | @uri')
	numbers=$(gh api --paginate "repos/$repo/issues?state=open&labels=$q&per_page=100" \
		--jq ".[] | select(.pull_request == null) | select(.title == $(jq -n --arg t "$title" '$t')) | .number")
	number=${numbers%%$'\n'*}
	if [ -n "$number" ]; then
		# A report the same as the last one (a source failing every day, a
		# model still missing) isn't posted again.
		last=$(gh api --paginate "repos/$repo/issues/$number/comments" --jq ".[] | select($mine) | .body | @json")
		last=${last##*$'\n'}
		if [ -z "$last" ]; then
			last=$(gh api "repos/$repo/issues/$number" --jq '.body | @json')
		fi
		if [ -n "$last" ] && [ "$(jq -r . <<< "$last" | tr -d '\r')" = "$(printf '%s' "$text" | tr -d '\r')" ]; then
			echo "same report as the last one on #$number"
			exit 0
		fi
		gh api -X POST "repos/$repo/issues/$number/comments" --input - <<< "$body" > /dev/null
		echo "commented on #$number"
	else
		jq -n --arg title "$title" --arg label "$label" --arg body "$text" \
			'{title: $title, body: $body, labels: [$label]}' |
			gh api -X POST "repos/$repo/issues" --input - --jq '"opened #\(.number)"'
	fi
	;;
*)
	echo "mode must be pr or issue" >&2
	exit 2
	;;
esac
