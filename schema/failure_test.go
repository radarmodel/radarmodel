// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package schema

// The main promise, second half: nothing is reported as drift because the
// connection, the server or the user's privileges misbehaved. Each test checks
// that the database is reported as an error (not as changes), that its stored
// snapshot is unchanged, and, where it can, that the next good scan is quiet.

import (
	"bytes"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/radarmodel/radarmodel/core"
)

func (h *harness) mustFailQuietly(r *core.Report, before []byte, want string) {
	h.t.Helper()
	res := h.result(r)
	if res.Error == "" || len(res.Changes) != 0 {
		h.t.Fatalf("want a failed scan, got:\n%s", dump(r))
	}
	if want != "" && !strings.Contains(res.Error, want) {
		h.t.Fatalf("error %q should mention %q", res.Error, want)
	}
	if r.ExitCode() != core.ExitError {
		h.t.Fatalf("exit %d", r.ExitCode())
	}
	if !bytes.Equal(before, storedBytes(h.t, h.store, "db")) {
		h.t.Fatal("a failed scan changed the stored snapshot")
	}
}

// proxied points a Postgres or MySQL harness at the server through a cutter.
func (h *harness) proxied() *cutter {
	h.t.Helper()
	dsn := os.Getenv(h.db.DSNEnv)
	switch h.d.name {
	case "postgres":
		u, _ := url.Parse(dsn)
		c := newCutter(h.t, u.Host)
		u.Host = c.addr()
		h.t.Setenv(h.db.DSNEnv, u.String())
		return c
	case "mysql":
		cfg, _ := mysql.ParseDSN(dsn)
		c := newCutter(h.t, cfg.Addr)
		cfg.Addr = c.addr()
		h.t.Setenv(h.db.DSNEnv, cfg.FormatDSN())
		return c
	}
	h.t.Skip("no network connection to cut")
	return nil
}

func TestConnectionLostMidScanIsAFailedScan(t *testing.T) {
	eachDialect(t, func(t *testing.T, d dialect) {
		if d.name == "sqlite" {
			t.Skip("no connection")
		}
		h := newHarness(t, d)
		h.exec("base")
		cut := h.proxied()
		h.run(Options{})
		before := storedBytes(t, h.store, "db")
		r := h.run(Options{BetweenReads: func(string) { cut.dropAll() }})
		h.mustFailQuietly(r, before, "")
	})
}

func TestServerUnreachableIsAFailedScan(t *testing.T) {
	eachDialect(t, func(t *testing.T, d dialect) {
		if d.name == "sqlite" {
			t.Skip("no server")
		}
		h := newHarness(t, d)
		h.exec("base")
		cut := h.proxied()
		h.run(Options{})
		before := storedBytes(t, h.store, "db")
		cut.dropAll() // refuses new connections from now on
		h.mustFailQuietly(h.run(Options{}), before, "")
	})
}

func TestWrongPasswordIsAFailedScan(t *testing.T) {
	eachDialect(t, func(t *testing.T, d dialect) {
		h := newHarness(t, d)
		h.exec("base")
		h.run(Options{})
		before := storedBytes(t, h.store, "db")
		dsn := os.Getenv(h.db.DSNEnv)
		switch d.name {
		case "postgres":
			u, _ := url.Parse(dsn)
			u.User = url.UserPassword("nobody_radar", "wrong")
			t.Setenv(h.db.DSNEnv, u.String())
		case "mysql":
			cfg, _ := mysql.ParseDSN(dsn)
			cfg.User, cfg.Passwd = "nobody_radar", "wrong"
			t.Setenv(h.db.DSNEnv, cfg.FormatDSN())
		case "sqlite":
			t.Skip("no passwords")
		}
		h.mustFailQuietly(h.run(Options{}), before, "")
	})
}

func TestMissingDSNVariableIsAFailedScan(t *testing.T) {
	eachDialect(t, func(t *testing.T, d dialect) {
		if d.name == "sqlite" {
			t.Skip("no DSN")
		}
		h := newHarness(t, d)
		h.exec("base")
		h.run(Options{})
		before := storedBytes(t, h.store, "db")
		t.Setenv(h.db.DSNEnv, "")
		h.mustFailQuietly(h.run(Options{}), before, "is not set")
	})
}

func TestSQLiteFileProblemsAreFailedScans(t *testing.T) {
	h := newHarness(t, sqliteDialect())
	h.exec("base")
	h.run(Options{})
	before := storedBytes(t, h.store, "db")
	path := h.db.Path
	h.admin.Close()

	// Moved away: an error, and no empty database created in its place.
	os.Rename(path, path+".moved")
	os.Rename(path+"-wal", path+".moved-wal")
	os.Remove(path + "-shm")
	h.mustFailQuietly(h.run(Options{}), before, "")
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the scan created an empty database at the old path")
	}

	// Not a database at all.
	os.WriteFile(path, []byte("this is not an SQLite file, just some text that's long enough to have a header"), 0o644)
	h.mustFailQuietly(h.run(Options{}), before, "")

	// A folder where the file was.
	os.Remove(path)
	os.Mkdir(path, 0o755)
	h.mustFailQuietly(h.run(Options{}), before, "not a regular file")

	// Back where it was: quiet again.
	os.Remove(path)
	os.Rename(path+".moved", path)
	os.Rename(path+".moved-wal", path+"-wal")
	if r := h.run(Options{}); h.result(r).Error != "" || len(h.result(r).Changes) != 0 {
		t.Fatalf("after the file came back:\n%s", dump(r))
	}
}

// Postgres is read from pg_catalog, so taking a user's privileges away hides
// nothing: no drift, no error.
func TestPostgresRevokedPrivilegesAreNotDrift(t *testing.T) {
	admin := os.Getenv("RADARMODEL_TEST_POSTGRES")
	if admin == "" {
		t.Skip("RADARMODEL_TEST_POSTGRES not set")
	}
	d := postgresDialect(admin)
	h := newHarness(t, d)
	h.exec("base")
	h.exec("restrict", h.dbName())
	u, _ := url.Parse(os.Getenv(h.db.DSNEnv))
	u.User = url.User("radar_reader")
	t.Setenv(h.db.DSNEnv, u.String())
	h.run(Options{})
	h.exec("revoke")
	r := h.run(Options{})
	if res := h.result(r); res.Error != "" || len(res.Changes) != 0 {
		t.Fatalf("revoking privileges showed up as drift:\n%s", dump(r))
	}
	// Real drift is still seen by the restricted user.
	h.exec("drop column")
	if c := find(h.result(h.run(Options{})), core.Removed, KindColumn+" "+h.path("customers.name"), ""); c == nil {
		t.Fatal("a dropped column wasn't seen by the restricted user")
	}
}

// MySQL's information_schema hides tables the user can't access. A revoked
// privilege must be a failed scan, never a dropped table, and a real DROP
// must still be reported.
func TestMySQLRevokedPrivilegeIsAFailedScanNotADrop(t *testing.T) {
	admin := os.Getenv("RADARMODEL_TEST_MYSQL")
	if admin == "" {
		t.Skip("RADARMODEL_TEST_MYSQL not set")
	}
	d := mysqlDialect(admin)
	h := newHarness(t, d)
	h.exec("base")
	h.exec("restrict", h.dbName())
	cfg, _ := mysql.ParseDSN(os.Getenv(h.db.DSNEnv))
	cfg.User, cfg.Passwd = "radar_reader", "reader"
	t.Setenv(h.db.DSNEnv, cfg.FormatDSN())
	// Only the tables are granted: the view and trigger aren't visible to
	// this user, so ignore them to keep the baseline about the two tables.
	h.cfg.Databases[0].Ignore = []string{"*.big_orders", "*.orders_touch"}
	h.db = &h.cfg.Databases[0]
	h.run(Options{})
	before := storedBytes(t, h.store, "db")
	h.exec("revoke", h.dbName())
	h.mustFailQuietly(h.run(Options{}), before, "privileges")

	// Granting it back: quiet.
	h.exec("restrict", h.dbName())
	if r := h.run(Options{}); h.result(r).Error != "" || len(h.result(r).Changes) != 0 {
		t.Fatalf("after granting back:\n%s", dump(r))
	}
	// A real drop is still a drop.
	h.admin.Exec("SET FOREIGN_KEY_CHECKS=0")
	if _, err := h.admin.Exec("DROP TABLE orders"); err != nil {
		t.Fatal(err)
	}
	r := h.run(Options{})
	if c := find(h.result(r), core.Removed, KindTable+" "+h.path("orders"), ""); c == nil || c.Severity != core.Breaking {
		t.Fatalf("a real drop wasn't reported:\n%s", dump(r))
	}
}

// A migration running during the scan: the two reads differ, the scan reads
// again, and only a consistent schema is accepted.
func TestSchemaChangingDuringTheScan(t *testing.T) {
	eachDialect(t, func(t *testing.T, d dialect) {
		h := newHarness(t, d)
		h.exec("base")
		h.run(Options{})
		before := storedBytes(t, h.store, "db")

		// Changed once, between the first two reads: the second pair agrees.
		done := false
		r := h.run(Options{BetweenReads: func(string) {
			if !done {
				done = true
				h.exec("add column")
			}
		}})
		if res := h.result(r); res.Error != "" || find(res, core.Added, KindColumn+" "+h.path("customers.phone"), "") == nil {
			t.Fatalf("%s", dump(r))
		}

		// Changing between every pair of reads: the scan gives up cleanly.
		before = storedBytes(t, h.store, "db")
		n := 0
		r = h.run(Options{BetweenReads: func(string) {
			n++
			h.exec("many tables", 1000+n)
		}})
		h.mustFailQuietly(r, before, "kept changing")
	})
}

func TestUnreadableStoredSnapshotIsAnError(t *testing.T) {
	h := newHarness(t, sqliteDialect())
	h.exec("base")
	h.run(Options{})
	p := filepath.Join(h.store.Dir, "latest", "db.json")
	os.WriteFile(p, []byte(`{"source":"db","items":[{"id":"tab`), 0o644)
	r := h.run(Options{})
	if res := h.result(r); !strings.Contains(res.Error, "unreadable") || res.Baseline {
		t.Fatalf("%+v", res)
	}
}

func TestTimeoutIsAFailedScan(t *testing.T) {
	eachDialect(t, func(t *testing.T, d dialect) {
		h := newHarness(t, d)
		h.exec("base")
		h.run(Options{})
		before := storedBytes(t, h.store, "db")
		h.cfg.Timeout = core.Duration(200 * time.Millisecond)
		r := h.run(Options{BetweenReads: func(string) { time.Sleep(400 * time.Millisecond) }})
		h.mustFailQuietly(r, before, "")
	})
}

var _ = sql.ErrNoRows
