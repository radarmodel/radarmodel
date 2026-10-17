#!/bin/sh
# Copyright radarmodel.com 2026
# SPDX-License-Identifier: Apache-2.0
#
# Smoke test for a built radarmodel-ai binary, run offline: a saved OpenAI
# model list is served from disk through a file:// base URL. The first check
# records the baseline, the second finds nothing, then a watched model is
# taken out of the list: the third check warns (exit 0) and the fourth
# confirms it as breaking (exit 1).
# Used by CI and by the release workflow on every binary it can run.
#
# Usage: scripts/smoke.sh ./radarmodel-ai
set -eu

bin=$1
case $bin in /*) ;; *) bin=$(pwd)/$bin ;; esac
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"

"$bin" version
"$bin" help > /dev/null

mkdir -p api/v1
list() {
	printf '{"object":"list","data":[' > api/v1/models
	sep=
	for m in "$@"; do
		printf '%s{"id":"%s","object":"model","owned_by":"system"}' "$sep" "$m" >> api/v1/models
		sep=,
	done
	printf ']}\n' >> api/v1/models
}
list gpt-4o gpt-4o-mini o3

cat > radarmodel.json << EOF2
{"providers":[{"type":"openai","base_url":"file://$work/api"}],
 "watch":["openai:gpt-4o"]}
EOF2

"$bin" check | grep -q baseline
"$bin" check | grep -q 'no changes (3 items)'

list gpt-4o-mini o3
"$bin" check | grep -q 'WARNING.*gpt-4o \*.*next scan'
set +e
"$bin" check > out.txt
code=$?
set -e
cat out.txt
test "$code" -eq 1
grep -q 'BREAKING' out.txt
grep -q 'gpt-4o \*.*confirmed' out.txt

"$bin" show openai | grep -q '2 models'

echo "smoke test passed"
