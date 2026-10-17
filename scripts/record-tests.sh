#!/bin/sh
# Copyright radarmodel.com 2026
# SPDX-License-Identifier: Apache-2.0
#
# Runs every test and the race detector and writes the output to
# test/results/ with a note of the machine they ran on. Every number on
# radarmodel.com comes from these files.
#
#   sh scripts/record-tests.sh
#
# The record workflow (.github/workflows/record.yml) runs this on GitHub and
# commits the results.
set -eu
cd "$(dirname "$0")/.."
out=test/results
mkdir -p "$out"
export CGO_ENABLED=0

header() {
	echo "# $1"
	echo "# recorded $(date -u '+%Y-%m-%d %H:%M UTC') with $(go version | cut -d' ' -f3-)"
	echo "# $(uname -sr), $(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2- | sed 's/^ //'), $(nproc 2>/dev/null || echo '?') CPUs"
	echo "# command: $2"
	echo
}

record() {
	name=$1 title=$2
	shift 2
	{ header "$title" "$*"; "$@" 2>&1; } > "$out/$name"
}

go vet ./...

record tests.txt "Every test, verbose" go test -count=1 -v ./...
record race.txt "Every test with the race detector" env CGO_ENABLED=1 go test -race -count=1 ./...
record failures.txt "Failure tests: provider and network faults must never be reported as removed models, three runs" \
	go test -count=3 -v -run '^(TestFailuresAreNeverReportedAsRemovals|TestTransientFailuresAreRetried|TestUnreadableStoredSnapshotIsAnErrorNotANewBaseline|TestCancelledRunLeavesSnapshotsAlone)$' ./ai/
go build -o /tmp/radarmodel-ai-record ./cmd/radarmodel-ai
record smoke.txt "Smoke test of the built binary, offline" sh scripts/smoke.sh /tmp/radarmodel-ai-record

grep -hE '^(ok|FAIL|---)' "$out/tests.txt" "$out/race.txt" "$out/failures.txt" | grep -v -- '--- PASS' || true
echo "results written to $out/"
