// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"strings"
	"testing"

	"github.com/radarmodel/radarmodel/core"
)

func TestClassify(t *testing.T) {
	ch := []core.Change{
		{Kind: core.Removed, ID: "table s.gone"},
		{Kind: core.Removed, ID: "column s.gone.a"},
		{Kind: core.Removed, ID: "index s.gone.i"},
		{Kind: core.Added, ID: "table s.new"},
		{Kind: core.Added, ID: "column s.new.a"},
		{Kind: core.Removed, ID: "column s.t.b"},
		{Kind: core.Changed, ID: "column s.t.c", Field: AttrType, Old: "int", New: "bigint"},
		{Kind: core.Changed, ID: "column s.t.d", Field: AttrNullable, Old: "yes", New: "no"},
		{Kind: core.Changed, ID: "column s.t.e", Field: AttrNullable, Old: "no", New: "yes"},
		{Kind: core.Changed, ID: "column s.t.f", Field: AttrPosition, Old: "3", New: "4"},
		{Kind: core.Changed, ID: "column s.t.g", Field: AttrDefault, Old: "1", New: "2"},
		{Kind: core.Removed, ID: "index s.t.i"},
		{Kind: core.Changed, ID: "constraint s.t.k", Field: AttrDefinition, Old: "CHECK (a > 0)", New: "CHECK (a > 1)"},
		{Kind: core.Changed, ID: "view s.v", Field: AttrDefinition, Old: "SELECT 1"},
		{Kind: core.Changed, ID: "view s.w", Field: AttrDefinition, Old: "SELECT 1", New: "SELECT 2"},
		{Kind: core.Changed, ID: "table s.t", Field: AttrSQL, Old: "x", New: "y"},
		{Kind: core.Changed, ID: "table s.u", Field: AttrSQL, Old: "x", New: "y"},
		{Kind: core.Changed, ID: "table s.u", Field: AttrEngine, Old: "InnoDB", New: "MyISAM"},
	}
	Classify(ch)
	want := []core.Severity{core.Breaking, core.Info, core.Info, core.Warning, core.Info,
		core.Breaking, core.Breaking, core.Breaking, core.Warning, core.Info, core.Warning,
		core.Warning, core.Warning, core.Info, core.Warning, core.Info, core.Warning, core.Warning}
	for i, c := range ch {
		if c.Severity != want[i] {
			t.Errorf("%s %s %s: %s, want %s", c.Kind, c.ID, c.Field, c.Severity, want[i])
		}
	}
}

func TestIgnored(t *testing.T) {
	for _, c := range []struct {
		id   string
		glob []string
		ok   bool
	}{
		{"table public.tmp_x", []string{"public.tmp_*"}, true},
		{"column public.tmp_x.a", []string{"public.tmp_*"}, true},
		{"table public.users", []string{"public.tmp_*"}, false},
		{"column public.users.updated_at", []string{"*.updated_at"}, true},
		{"index public.users.users_email", []string{"public.users"}, true},
		{"table public.users2", []string{"public.users"}, false},
	} {
		if got := Ignored(c.id, c.glob); got != c.ok {
			t.Errorf("Ignored(%q, %v) = %v", c.id, c.glob, got)
		}
	}
}

func TestConfigRejectsMistakes(t *testing.T) {
	for _, c := range []struct {
		cfg  Config
		want string
	}{
		{Config{}, "no databases"},
		{Config{Databases: []DatabaseConfig{{Name: "a", Driver: "oracle"}}}, "not one of"},
		{Config{Databases: []DatabaseConfig{{Name: "a", Driver: Postgres}}}, "dsn_env is empty"},
		{Config{Databases: []DatabaseConfig{{Name: "a", Driver: SQLite}}}, "path is empty"},
		{Config{Databases: []DatabaseConfig{{Name: "a", Driver: SQLite, Path: "x", DSNEnv: "Y"}}}, "path only"},
		{Config{Databases: []DatabaseConfig{{Name: "a", Driver: MySQL, DSNEnv: "X", Path: "p"}}}, "sqlite only"},
		{Config{Databases: []DatabaseConfig{{Name: "a", Driver: SQLite, Path: "x", Baseline: "frozen"}}}, "rolling or pinned"},
		{Config{Databases: []DatabaseConfig{{Name: "Prod", Driver: SQLite, Path: "x"}, {Name: "prod", Driver: SQLite, Path: "y"}}}, "too close"},
		{Config{Databases: []DatabaseConfig{{Name: "", Driver: SQLite, Path: "x"}}}, "name is empty"},
		{Config{Databases: []DatabaseConfig{{Name: "a", Driver: SQLite, Path: "x", Ignore: []string{" "}}}}, "empty entry"},
	} {
		if err := c.cfg.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: got %v, want %q", c.cfg, err, c.want)
		}
	}
}

func TestSQLiteDSNIsReadOnlyAndNeverCreates(t *testing.T) {
	if _, err := sqliteDSN("/no/such/file.db"); err == nil {
		t.Fatal("a missing file was accepted")
	}
}
