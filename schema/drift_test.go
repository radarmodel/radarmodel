// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/radarmodel/radarmodel/core"
)

func TestBaselineThenNoChanges(t *testing.T) {
	eachDialect(t, func(t *testing.T, d dialect) {
		h := newHarness(t, d)
		h.exec("base")
		r := h.run(Options{})
		res := h.result(r)
		if !res.Baseline || res.Error != "" || res.Items < 10 {
			t.Fatalf("baseline: %+v", res)
		}
		for i := 0; i < 2; i++ {
			r = h.run(Options{})
			if res := h.result(r); res.Error != "" || len(res.Changes) != 0 || res.Baseline {
				t.Fatalf("unchanged schema, run %d:\n%s", i, dump(r))
			}
		}
	})
}

// The main promise, first half: a manual change made outside a migration is
// reported, with a severity that says how likely it is to break the app.
func TestManualChangesAreCaught(t *testing.T) {
	cases := []struct {
		ddl, kind, field, rel string
		change                core.ChangeKind
		sev                   core.Severity
	}{
		{"add column", KindColumn, "", "customers.phone", core.Added, core.Warning},
		{"drop column", KindColumn, "", "customers.name", core.Removed, core.Breaking},
		{"drop index", KindIndex, "", "orders.orders_status", core.Removed, core.Warning},
		{"add index", KindIndex, "", "customers.customers_name", core.Added, core.Warning},
		{"drop table", KindTable, "", "orders", core.Removed, core.Breaking},
		{"add table", KindTable, "", "tmp_import", core.Added, core.Warning},
		{"change view", KindView, AttrDefinition, "big_orders", core.Changed, core.Warning},
		{"type change", KindColumn, AttrType, "customers.email", core.Changed, core.Breaking},
		{"not null", KindColumn, AttrNullable, "customers.name", core.Changed, core.Breaking},
		{"change default", KindColumn, AttrDefault, "orders.status", core.Changed, core.Warning},
	}
	eachDialect(t, func(t *testing.T, d dialect) {
		for _, c := range cases {
			t.Run(c.ddl, func(t *testing.T) {
				h := newHarness(t, d)
				h.exec("base")
				h.run(Options{})
				h.exec(c.ddl)
				r := h.run(Options{})
				res := h.result(r)
				id := c.kind + " " + h.path(c.rel)
				got := find(res, c.change, id, c.field)
				if got == nil || got.Severity != c.sev {
					t.Fatalf("want %s %s %s at %s, got %+v\n%s", c.change, id, c.field, c.sev, got, dump(r))
				}
				if res.Error != "" {
					t.Fatal(res.Error)
				}
				// Reported once: the next scan is quiet.
				if r := h.run(Options{}); len(h.result(r).Changes) != 0 {
					t.Fatalf("reported again:\n%s", dump(r))
				}
			})
		}
	})
}

func TestDroppedTableReportsOnlyTheTableAsBreaking(t *testing.T) {
	eachDialect(t, func(t *testing.T, d dialect) {
		h := newHarness(t, d)
		h.exec("base")
		h.run(Options{})
		h.exec("drop table")
		r := h.run(Options{})
		breaking := 0
		for _, c := range h.result(r).Changes {
			if c.Severity == core.Breaking {
				breaking++
			}
		}
		// The orders table, and the big_orders view that was dropped with it.
		if breaking != 2 {
			t.Fatalf("%d breaking changes, want 2 (the table and its view)\n%s", breaking, dump(r))
		}
	})
}

func TestIgnoreRules(t *testing.T) {
	eachDialect(t, func(t *testing.T, d dialect) {
		h := newHarness(t, d)
		h.exec("base")
		h.exec("add table")
		h.run(Options{})
		// Ignoring a table that's in the stored snapshot is not a drop.
		h.cfg.Databases[0].Ignore = []string{"*.tmp_*"}
		h.db = &h.cfg.Databases[0]
		if r := h.run(Options{}); len(h.result(r).Changes) != 0 {
			t.Fatalf("adding an ignore rule reported changes:\n%s", dump(r))
		}
		// Changes inside an ignored table aren't reported.
		h.exec("tmp change")
		if r := h.run(Options{}); len(h.result(r).Changes) != 0 {
			t.Fatalf("ignored table's change reported:\n%s", dump(r))
		}
		// Everything else still is.
		h.exec("add column")
		r := h.run(Options{})
		if !r.Worth(core.Warning) || len(r.Filtered(core.Warning).Results[0].Changes) != 1 {
			t.Fatalf("%s", dump(r))
		}
	})
}

func TestPinnedBaselineReportsUntilAccepted(t *testing.T) {
	eachDialect(t, func(t *testing.T, d dialect) {
		h := newHarness(t, d)
		h.cfg.Databases[0].Baseline = Pinned
		h.exec("base")
		h.run(Options{})
		h.exec("drop column")
		for i := 0; i < 3; i++ {
			r := h.run(Options{})
			if c := find(h.result(r), core.Removed, KindColumn+" "+h.path("customers.name"), ""); c == nil || r.ExitCode() != core.ExitBreaking {
				t.Fatalf("run %d: drift not reported against the pinned baseline\n%s", i, dump(r))
			}
		}
		if r := h.run(Options{Accept: true}); h.result(r).Error != "" {
			t.Fatal(h.result(r).Error)
		}
		if r := h.run(Options{}); len(h.result(r).Changes) != 0 {
			t.Fatalf("after accept:\n%s", dump(r))
		}
	})
}

func TestTooFewTablesIsAFailedScan(t *testing.T) {
	eachDialect(t, func(t *testing.T, d dialect) {
		h := newHarness(t, d)
		for i := 0; i < 10; i++ {
			h.exec("many tables", i)
		}
		h.run(Options{})
		before := storedBytes(t, h.store, "db")
		for i := 0; i < 8; i++ {
			h.exec("drop many", i)
		}
		r := h.run(Options{})
		if res := h.result(r); res.Error == "" || len(res.Changes) != 0 {
			t.Fatalf("%s", dump(r))
		}
		if string(storedBytes(t, h.store, "db")) != string(before) {
			t.Fatal("the stored snapshot changed")
		}
		zero := 0.0
		h.cfg.MinFraction = &zero
		if r := h.run(Options{}); h.result(r).Error != "" || r.ExitCode() != core.ExitBreaking {
			t.Fatalf("with min_fraction 0:\n%s", dump(r))
		}
	})
}

func TestTwoDatabasesWithDifferentSchemaNamesCompare(t *testing.T) {
	a := &core.Snapshot{Source: "x", Items: []core.Item{
		{ID: "table app.users"}, {ID: "column app.users.id", Attrs: map[string]string{"type": "int"}},
		{ID: "constraint app.orders.fk", Attrs: map[string]string{"definition": "FOREIGN KEY (u) REFERENCES app.users (id)"}},
	}}
	b := &core.Snapshot{Source: "x", Items: []core.Item{
		{ID: "table app_staging.users"}, {ID: "column app_staging.users.id", Attrs: map[string]string{"type": "bigint"}},
		{ID: "constraint app_staging.orders.fk", Attrs: map[string]string{"definition": "FOREIGN KEY (u) REFERENCES app_staging.users (id)"}},
	}}
	a.Normalize()
	b.Normalize()
	AlignSchemas(a, b)
	ch := core.Diff(a, b)
	if len(ch) != 1 || ch[0].ID != "column app.users.id" || ch[0].Field != "type" {
		t.Fatalf("%+v", ch)
	}
}
