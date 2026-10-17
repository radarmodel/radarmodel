#!/bin/sh
# Copyright radarmodel.com 2026
# SPDX-License-Identifier: Apache-2.0
#
# Smoke test for a built radarmodel-schema binary, run offline on SQLite: the
# first check records the baseline from testdata/smoke-before.db, the second
# finds nothing, then the file is replaced by smoke-after.db (an index and a
# column dropped, a column added) and the third check must exit 1 and say so.
# Used by CI and by the release workflow on every binary it can run.
#
# Usage: scripts/smoke-schema.sh ./radarmodel-schema
set -eu

bin=$1
case $bin in /*) ;; *) bin=$(pwd)/$bin ;; esac
data=$(cd "$(dirname "$0")/../testdata" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"

"$bin" version
"$bin" help > /dev/null

cp "$data/smoke-before.db" app.db
cat > radarmodel-schema.json << 'EOF2'
{"databases":[{"name":"app","driver":"sqlite","path":"app.db"}]}
EOF2

"$bin" check | grep -q baseline
"$bin" check | grep -q 'no changes'

cp "$data/smoke-after.db" app.db
set +e
"$bin" check > out.txt
code=$?
set -e
cat out.txt
test "$code" -eq 1
grep -q 'BREAKING.*column main.customers.name.*gone' out.txt
grep -q 'WARNING.*index main.orders.orders_status.*gone' out.txt
grep -q 'WARNING.*column main.customers.phone.*new' out.txt

"$bin" show app | grep -q 'column  *main.customers.phone'

# A missing file is a failed scan (exit 2), and nothing is created.
rm app.db
set +e
"$bin" check > /dev/null 2>&1
code=$?
set -e
test "$code" -eq 2
test ! -e app.db

echo "schema smoke test passed"
