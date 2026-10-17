// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"

	"github.com/radarmodel/radarmodel"
	"github.com/radarmodel/radarmodel/core"
)

func call(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// setup writes a config for two SQLite databases, app and staging, and
// returns their handles for changing them.
func setup(t *testing.T, extra string) (cfg string, app, staging *sql.DB) {
	t.Helper()
	t.Setenv("RADARMODEL_SCHEMA_CONFIG", "")
	dir := t.TempDir()
	open := func(name string) *sql.DB {
		db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(dir, name)))
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { db.Close() })
		if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL, name TEXT);
CREATE INDEX users_email ON users (email);`); err != nil {
			t.Fatal(err)
		}
		return db
	}
	app, staging = open("app.db"), open("staging.db")
	cfg = filepath.Join(dir, "radarmodel-schema.json")
	body := `{"databases":[{"name":"app","driver":"sqlite","path":"app.db"` + extra + `},{"name":"staging","driver":"sqlite","path":"staging.db"}]}`
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfg, app, staging
}

func TestCheckExitCodes(t *testing.T) {
	cfg, app, _ := setup(t, "")
	if code, out, errs := call("check", "-config", cfg); code != core.ExitOK || !strings.Contains(out, "baseline") {
		t.Fatalf("baseline: %d %q %q", code, out, errs)
	}
	if code, _, _ := call("check", "-config", cfg); code != core.ExitOK {
		t.Fatalf("unchanged: exit %d", code)
	}
	app.Exec("ALTER TABLE users DROP COLUMN name")
	code, out, _ := call("check", "-config", cfg, "-dry-run", "app")
	if code != core.ExitBreaking || !strings.Contains(out, "column main.users.name") || strings.Contains(out, "staging") {
		t.Fatalf("dropped column: exit %d\n%s", code, out)
	}
	code, out, _ = call("check", "-config", cfg, "-json")
	var rep core.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || code != core.ExitBreaking || len(rep.Results) != 2 {
		t.Fatalf("json: %v %d\n%s", err, code, out)
	}
	if code, _, _ = call("check", "-config", cfg); code != core.ExitOK {
		t.Fatalf("reported twice in rolling mode: exit %d", code)
	}
}

func TestPinnedAndAccept(t *testing.T) {
	cfg, app, _ := setup(t, `,"baseline":"pinned"`)
	call("check", "-config", cfg)
	app.Exec("DROP INDEX users_email")
	for i := 0; i < 2; i++ {
		if code, out, _ := call("check", "-config", cfg, "app"); code != core.ExitOK || !strings.Contains(out, "users_email") {
			t.Fatalf("pinned run %d: %d %q", i, code, out)
		}
	}
	if code, out, errs := call("accept", "-config", cfg, "app"); code != core.ExitOK || !strings.Contains(out, "accepted") {
		t.Fatalf("accept: %d\n%s%s", code, out, errs)
	}
	if code, out, _ := call("check", "-config", cfg, "app"); code != core.ExitOK || strings.Contains(out, "users_email") {
		t.Fatalf("after accept: %d\n%s", code, out)
	}
}

func TestDiff(t *testing.T) {
	cfg, _, staging := setup(t, "")
	if code, out, _ := call("diff", "-config", cfg, "app", "staging"); code != 0 || !strings.Contains(out, "no changes") {
		t.Fatalf("same schemas: %d %q", code, out)
	}
	staging.Exec("ALTER TABLE users ADD COLUMN phone TEXT")
	code, out, _ := call("diff", "-config", cfg, "app", "staging")
	if code != 1 || !strings.Contains(out, "column main.users.phone") {
		t.Fatalf("different schemas: %d\n%s", code, out)
	}
}

func TestShowInitAndBadInput(t *testing.T) {
	cfg, _, _ := setup(t, "")
	if code, _, errs := call("show", "-config", cfg, "app"); code != core.ExitError || !strings.Contains(errs, "no snapshot") {
		t.Fatalf("%d %s", code, errs)
	}
	call("check", "-config", cfg)
	if code, out, _ := call("show", "-config", cfg, "app"); code != 0 || !strings.Contains(out, "index   main.users.users_email") {
		t.Fatalf("%d\n%s", code, out)
	}
	p := filepath.Join(t.TempDir(), "radarmodel-schema.json")
	if code, _, _ := call("init", "-config", p); code != 0 {
		t.Fatal("init failed")
	}
	if code, _, errs := call("init", "-config", p); code != core.ExitError || !strings.Contains(errs, "exists") {
		t.Fatalf("init overwrote: %d %s", code, errs)
	}
	if _, _, err := load(p); err != nil {
		t.Fatalf("starter config invalid: %v", err)
	}
	for _, args := range [][]string{
		{}, {"frobnicate"},
		{"check", "-config", cfg, "nope"},
		{"check", "-config", cfg, "-min", "loud"},
		{"watch", "-config", cfg, "app"},
		{"accept", "-config", cfg},
		{"diff", "-config", cfg, "app"},
		{"show", "-config", cfg},
	} {
		if code, _, _ := call(args...); code != core.ExitError {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	if code, out, _ := call("version"); code != 0 || out != "radarmodel-schema "+radarmodel.Version+"\n" {
		t.Fatalf("%q", out)
	}
}

func TestDropRepeats(t *testing.T) {
	sent := map[string]string{}
	drift := &core.Report{Results: []core.SourceResult{{Source: "a", Changes: []core.Change{{Kind: core.Removed, ID: "table s.t", Severity: core.Breaking}}}}}
	if len(dropRepeats(drift, sent, core.Warning).Results[0].Changes) != 1 {
		t.Fatal("first time dropped")
	}
	if len(dropRepeats(drift, sent, core.Warning).Results[0].Changes) != 0 {
		t.Fatal("repeat sent again")
	}
	quiet := &core.Report{Results: []core.SourceResult{{Source: "a"}}}
	dropRepeats(quiet, sent, core.Warning)
	if len(dropRepeats(drift, sent, core.Warning).Results[0].Changes) != 1 {
		t.Fatal("drift coming back wasn't sent")
	}
}
