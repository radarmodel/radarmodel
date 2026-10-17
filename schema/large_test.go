// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/radarmodel/radarmodel/core"
)

// largeTables is the number of tables in the large-schema test: 2,000 by
// default, each with 8 columns, a primary key and an index.
// RADARMODEL_TEST_LARGE overrides it.
func largeTables() int {
	if v, err := strconv.Atoi(os.Getenv("RADARMODEL_TEST_LARGE")); err == nil && v > 0 {
		return v
	}
	return 2000
}

func TestLargeSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	n := largeTables()
	eachDialect(t, func(t *testing.T, d dialect) {
		h := newHarness(t, d)
		start := time.Now()
		for i := 0; i < n; i++ {
			h.exec("many tables", i)
		}
		t.Logf("%s: created %d tables in %s", d.name, n, time.Since(start).Round(time.Millisecond))

		start = time.Now()
		r := h.run(Options{})
		first := time.Since(start)
		res := h.result(r)
		if res.Error != "" || !res.Baseline {
			t.Fatalf("%+v", res)
		}
		start = time.Now()
		h.exec("drop many", n/2)
		r = h.run(Options{})
		second := time.Since(start)
		res = h.result(r)
		if res.Error != "" {
			t.Fatal(res.Error)
		}
		if c := find(res, core.Removed, KindTable+" "+h.path("t"+strconv.Itoa(n/2)), ""); c == nil || c.Severity != core.Breaking {
			t.Fatalf("the dropped table among %d wasn't reported", n)
		}
		// The table's own removal is breaking; its columns and index are info.
		breaking := 0
		for _, c := range res.Changes {
			if c.Severity == core.Breaking {
				breaking++
			}
		}
		if breaking != 1 {
			t.Fatalf("%d breaking changes for one dropped table", breaking)
		}
		// Each scan reads the schema twice; report the scan as a whole.
		t.Logf("%s: %d tables, %d objects; baseline scan %s, scan with one drop %s",
			d.name, n, res.Items, first.Round(time.Millisecond), second.Round(time.Millisecond))
		if first > time.Minute || second > time.Minute {
			t.Fatalf("too slow: %s, %s", first, second)
		}
	})
}
