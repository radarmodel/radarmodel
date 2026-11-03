#!/bin/sh
# Copyright radarmodel.com 2026
# SPDX-License-Identifier: Apache-2.0
#
# Records `radarmodel-ai scan` on three open source AI agents at pinned
# commits, with no config (all four providers count as configured), into
# test/results/scan-agents.txt. The hand check of these runs, line by line,
# is in test/results/scan-agents.md.
#
# Usage: scripts/scan-case-studies.sh ./radarmodel-ai [CACHE]
# CACHE holds the clones (default: $TMPDIR/radarmodel-scan-cache).
set -eu

bin=$1
case $bin in /*) ;; *) bin=$(pwd)/$bin ;; esac
cache=${2:-${TMPDIR:-/tmp}/radarmodel-scan-cache}
out=$(cd "$(dirname "$0")/.." && pwd)/test/results/scan-agents.txt
mkdir -p "$cache"

repos="aider https://github.com/Aider-AI/aider 5dc9490
gpt-researcher https://github.com/assafelovic/gpt-researcher 3582a68
browser-use https://github.com/browser-use/browser-use b49a68e"

{
	echo "radarmodel-ai scan on three AI agents, $("$bin" version)"
	echo "Recorded $(date -u +%Y-%m-%d) by scripts/scan-case-studies.sh. No config: all four providers count as configured."
	echo "$repos" | while read -r name url commit; do
		dir=$cache/$name
		if [ ! -d "$dir/.git" ]; then git clone -q "$url" "$dir"; fi
		git -C "$dir" checkout -q "$commit" 2>/dev/null || { git -C "$dir" fetch -q origin; git -C "$dir" checkout -q "$commit"; }
		echo
		echo "== $name $url at $commit"
		(cd "$dir" && "$bin" scan .) || true
	done
} > "$out"
grep -E '^(==|[0-9]+ files read)' "$out" || true
echo "wrote $out"
