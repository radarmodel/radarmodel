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
	go test -count=3 -v -run '^(TestFailuresAreNeverReportedAsRemovals|TestTransientFailuresAreRetried|TestUnreadableStoredSnapshotIsAnErrorNotANewBaseline|TestCancelledRunLeavesSnapshotsAlone|TestOpenRouterPagingIsAFailedScan|TestSilentlyShortListOnlyWarnsOnce|TestAModelBackAfterOneMissIsNotARemoval|TestFailedSaveKeepsTheOldSnapshot)$' ./ai/
record schema-failures.txt "Schema failure tests: lost connections, revoked privileges, session settings, missing files and migrations mid-scan must never be reported as drift, and no password may show in an error; three runs" \
	go test -count=3 -v -run '^(TestConnectionLostMidScanIsAFailedScan|TestServerUnreachableIsAFailedScan|TestWrongPasswordIsAFailedScan|TestMissingDSNVariableIsAFailedScan|TestSQLiteFileProblemsAreFailedScans|TestPostgresRevokedPrivilegesAreNotDrift|TestMySQLRevokedPrivilegeIsAFailedScanNotADrop|TestSchemaChangingDuringTheScan|TestTimeoutIsAFailedScan|TestTooFewTablesIsAFailedScan|TestConnectionStringNeverShowsUpInErrors|TestPostgresSessionSettingsAreNotDrift|TestMySQLUserWithoutSelectIsRefusedAtBaseline|TestMySQLRevokedTriggerPrivilegeIsAFailedScan|TestAcceptGetsPastAPrivilegeProbe)$' ./schema/
record schema-large.txt "Large schema: 2,000 tables of 8 columns with an index each, on SQLite, Postgres and MySQL" \
	go test -count=1 -v -run '^TestLargeSchema$' -timeout 30m ./schema/
go build -o /tmp/radarmodel-ai-record ./cmd/radarmodel-ai
go build -o /tmp/radarmodel-schema-record ./cmd/radarmodel-schema
record smoke.txt "Smoke tests of the built binaries, offline" sh -c 'sh scripts/smoke.sh /tmp/radarmodel-ai-record && sh scripts/smoke-schema.sh /tmp/radarmodel-schema-record'

# A live scan of OpenRouter's public model list (it needs no key): proves the
# parser against the real thing. Two scans, so the second compares with the
# first. It depends on a third-party service, so it is recorded but doesn't
# fail the recording.
live=$(mktemp -d)
printf '{"providers":[{"type":"openrouter"}],"watch":["openrouter:openai/*","openrouter:anthropic/*"],"timeout":"30s"}\n' > "$live/radarmodel.json"
{
	header "Live scan of OpenRouter's public model list, twice" "radarmodel-ai check -min info (twice), then show openrouter | head -20"
	/tmp/radarmodel-ai-record check -config "$live/radarmodel.json" -min info 2>&1 || echo "exit $?"
	/tmp/radarmodel-ai-record check -config "$live/radarmodel.json" -min info 2>&1 || echo "exit $?"
	/tmp/radarmodel-ai-record show -config "$live/radarmodel.json" openrouter 2>&1 | head -20
} > "$out/live-openrouter.txt"
rm -rf "$live"

grep -hE '^(ok|FAIL|---)' "$out/tests.txt" "$out/race.txt" "$out/failures.txt" "$out/schema-failures.txt" | grep -v -- '--- PASS' || true
echo "results written to $out/"
