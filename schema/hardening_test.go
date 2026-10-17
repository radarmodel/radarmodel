// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package schema

// Tests for the ways a scan could leak a secret or report drift that isn't
// there: session settings, privileges, same-name constraints and the odd
// tables SQLite allows.

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/radarmodel/radarmodel/core"
)

func mustExec(t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("%v\n%s", err, stmt)
	}
}

func storedItem(t *testing.T, h *harness, id string) *core.Item {
	t.Helper()
	s, err := h.store.Latest("db")
	if err != nil || s == nil {
		t.Fatalf("no stored snapshot: %v", err)
	}
	for i := range s.Items {
		if s.Items[i].ID == id {
			return &s.Items[i]
		}
	}
	return nil
}

// A connection string never shows up in an error, whether the driver can't
// parse it or the server can't be reached.
func TestConnectionStringNeverShowsUpInErrors(t *testing.T) {
	const secret = "Sup3rSecret"
	for _, c := range []struct{ driver, dsn string }{
		{Postgres, "postgres://u:" + secret + "@127.0.0.1:1/x%zz"},
		{Postgres, "postgres://u:" + secret + "@127.0.0.1:1/x?sslmode=disable&connect_timeout=2"},
		{Postgres, "host=127.0.0.1 port=1 user=u password=" + secret + " dbname=x sslmode=disable connect_timeout=2"},
		{Postgres, "host=127.0.0.1 password='" + secret + "' sslmode=bogus"},
		{MySQL, "u:" + secret + "@tcp(127.0.0.1:1)/x?timeout=2s"},
		{MySQL, "u:" + secret + "@tcp(127.0.0.1:1/x"},
	} {
		t.Setenv("RADAR_TEST_BAD_DSN", c.dsn)
		cfg := &Config{Databases: []DatabaseConfig{{Name: "db", Driver: c.driver, DSNEnv: "RADAR_TEST_BAD_DSN"}},
			Timeout: core.Duration(20 * time.Second)}
		if err := cfg.Validate(); err != nil {
			t.Fatal(err)
		}
		res := Run(context.Background(), cfg, &core.Store{Dir: t.TempDir()}, nil, Options{}).Results[0]
		if res.Error == "" || strings.Contains(res.Error, secret) {
			t.Errorf("%s %q: error %q", c.driver, c.dsn, res.Error)
		}
	}
	d := &DatabaseConfig{Driver: Postgres, DSNEnv: "RADAR_TEST_BAD_DSN"}
	t.Setenv("RADAR_TEST_BAD_DSN", "postgres://u:"+secret+"@h/x")
	got := Redact(d, errors.New(`parse "postgres://u:`+secret+`@h/x": invalid URL escape; password `+secret)).Error()
	if strings.Contains(got, secret) {
		t.Fatalf("Redact left the password in: %q", got)
	}
}

// search_path, IntervalStyle, DateStyle and extra_float_digits change how
// pg_catalog prints names and values. Scanning the same schema under other
// settings (from a role, or the connection string) is not drift.
func TestPostgresSessionSettingsAreNotDrift(t *testing.T) {
	admin := os.Getenv("RADARMODEL_TEST_POSTGRES")
	if admin == "" {
		t.Skip("RADARMODEL_TEST_POSTGRES not set")
	}
	h := newHarness(t, postgresDialect(admin))
	mustExec(t, h.admin, `CREATE SCHEMA billing;
CREATE TYPE billing.state AS ENUM ('open', 'paid');
CREATE TABLE billing.inv (id bigserial PRIMARY KEY, st billing.state NOT NULL DEFAULT 'open',
  due interval DEFAULT '1 day 2 hours', rate float8 DEFAULT 0.1, issued date DEFAULT '2026-01-31', tag bytea DEFAULT '\x01');
CREATE TABLE billing.line (inv bigint REFERENCES billing.inv(id), amount numeric CHECK (amount > 0.1));
CREATE INDEX line_amount ON billing.line ((amount * 1.1));
CREATE VIEW billing.open_inv AS SELECT id, due, st FROM billing.inv WHERE st = 'open';`)
	if r := h.run(Options{}); h.result(r).Error != "" {
		t.Fatal(h.result(r).Error)
	}
	u, _ := url.Parse(os.Getenv(h.db.DSNEnv))
	q := u.Query()
	q.Set("options", "-c search_path=billing,public -c IntervalStyle=iso_8601 -c DateStyle=SQL,DMY -c extra_float_digits=3 -c bytea_output=escape")
	u.RawQuery = q.Encode()
	t.Setenv(h.db.DSNEnv, u.String())
	r := h.run(Options{})
	if res := h.result(r); res.Error != "" || len(res.Changes) != 0 {
		t.Fatalf("other session settings showed up as drift: %q\n%s", res.Error, dump(r))
	}
}

func TestPostgresPartitionsRowSecurityAndExtensions(t *testing.T) {
	admin := os.Getenv("RADARMODEL_TEST_POSTGRES")
	if admin == "" {
		t.Skip("RADARMODEL_TEST_POSTGRES not set")
	}
	h := newHarness(t, postgresDialect(admin))
	mustExec(t, h.admin, `CREATE TABLE events (id bigint, at date) PARTITION BY RANGE (at);
CREATE TABLE events_2026 PARTITION OF events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');`)
	h.run(Options{})
	mustExec(t, h.admin, `ALTER TABLE events DETACH PARTITION events_2026; ALTER TABLE events ENABLE ROW LEVEL SECURITY`)
	r := h.run(Options{})
	res := h.result(r)
	if c := find(res, core.Changed, "table public.events_2026", AttrPartitionOf); c == nil || c.Severity != core.Warning {
		t.Fatalf("detached partition not reported:\n%s", dump(r))
	}
	if c := find(res, core.Changed, "table public.events", AttrRowSecurity); c == nil || c.New != "on" {
		t.Fatalf("row security not reported:\n%s", dump(r))
	}

	// Objects an extension creates belong to the extension, not the app.
	var ok bool
	h.admin.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'pg_stat_statements')`).Scan(&ok)
	if !ok {
		t.Skip("pg_stat_statements isn't available on this server")
	}
	mustExec(t, h.admin, `CREATE EXTENSION pg_stat_statements`)
	if r := h.run(Options{}); len(h.result(r).Changes) != 0 || h.result(r).Error != "" {
		t.Fatalf("an extension's objects were reported:\n%s", dump(r))
	}
}

// mysqlUser points the harness at a user holding only the given privileges on
// the test database.
func mysqlUser(t *testing.T, h *harness, privileges string) {
	t.Helper()
	db := h.dbName()
	for _, host := range []string{"%", "localhost"} {
		mustExec(t, h.admin, "DROP USER IF EXISTS 'radar_min'@'"+host+"'")
		mustExec(t, h.admin, "CREATE USER 'radar_min'@'"+host+"' IDENTIFIED BY 'minimal'")
		mustExec(t, h.admin, "GRANT "+privileges+" ON "+db+".* TO 'radar_min'@'"+host+"'")
	}
	t.Cleanup(func() {
		h.admin.Exec("DROP USER IF EXISTS 'radar_min'@'%'")
		h.admin.Exec("DROP USER IF EXISTS 'radar_min'@'localhost'")
	})
	cfg, _ := mysql.ParseDSN(os.Getenv(h.db.DSNEnv))
	cfg.User, cfg.Passwd = "radar_min", "minimal"
	t.Setenv(h.db.DSNEnv, cfg.FormatDSN())
}

func mysqlHarness(t *testing.T) *harness {
	t.Helper()
	admin := os.Getenv("RADARMODEL_TEST_MYSQL")
	if admin == "" {
		t.Skip("RADARMODEL_TEST_MYSQL not set")
	}
	h := newHarness(t, mysqlDialect(admin))
	h.exec("base")
	return h
}

// Without SELECT a later DROP can't be told from a hidden table, so a user
// without it is refused at the baseline, before anything is stored.
func TestMySQLUserWithoutSelectIsRefusedAtBaseline(t *testing.T) {
	h := mysqlHarness(t)
	mysqlUser(t, h, "REFERENCES, SHOW VIEW")
	res := h.result(h.run(Options{}))
	if !strings.Contains(res.Error, "SELECT") {
		t.Fatalf("want an error about SELECT, got %+v", res)
	}
	if s, _ := h.store.Latest("db"); s != nil {
		t.Fatal("a snapshot was stored")
	}
}

// information_schema.TRIGGERS only lists triggers the user holds TRIGGER on:
// losing it is a failed scan, and a real DROP TRIGGER is still reported.
func TestMySQLRevokedTriggerPrivilegeIsAFailedScan(t *testing.T) {
	h := mysqlHarness(t)
	mysqlUser(t, h, "SELECT, SHOW VIEW, TRIGGER")
	h.run(Options{})
	trig := KindTrigger + " " + h.path("orders.orders_touch")
	if storedItem(t, h, trig) == nil {
		t.Fatal("the baseline has no trigger")
	}
	before := storedBytes(t, h.store, "db")
	for _, host := range []string{"%", "localhost"} {
		mustExec(t, h.admin, "REVOKE TRIGGER ON "+h.dbName()+".* FROM 'radar_min'@'"+host+"'")
	}
	h.mustFailQuietly(h.run(Options{}), before, "privileges")
	for _, host := range []string{"%", "localhost"} {
		mustExec(t, h.admin, "GRANT TRIGGER ON "+h.dbName()+".* TO 'radar_min'@'"+host+"'")
	}
	if r := h.run(Options{}); h.result(r).Error != "" || len(h.result(r).Changes) != 0 {
		t.Fatalf("after granting back:\n%s", dump(r))
	}
	mustExec(t, h.admin, "DROP TRIGGER orders_touch")
	r := h.run(Options{})
	if c := find(h.result(r), core.Removed, trig, ""); c == nil || c.Severity != core.Warning {
		t.Fatalf("a real DROP TRIGGER wasn't reported:\n%s", dump(r))
	}
}

// accept is the user saying the change is real; it gets a database out of
// failed scans caused by a lost privilege.
func TestAcceptGetsPastAPrivilegeProbe(t *testing.T) {
	h := mysqlHarness(t)
	h.exec("restrict", h.dbName())
	cfg, _ := mysql.ParseDSN(os.Getenv(h.db.DSNEnv))
	cfg.User, cfg.Passwd = "radar_reader", "reader"
	t.Setenv(h.db.DSNEnv, cfg.FormatDSN())
	h.cfg.Databases[0].Ignore = []string{"*.big_orders", "*.orders_touch"}
	h.db = &h.cfg.Databases[0]
	h.run(Options{})
	before := storedBytes(t, h.store, "db")
	h.exec("revoke", h.dbName())
	t.Cleanup(func() { h.exec("restrict", h.dbName()) })
	h.mustFailQuietly(h.run(Options{}), before, "privileges")
	if res := h.result(h.run(Options{Accept: true})); res.Error != "" {
		t.Fatalf("accept: %s", res.Error)
	}
	if storedItem(t, h, KindTable+" "+h.path("orders")) != nil {
		t.Fatal("accept didn't store the new schema")
	}
	if r := h.run(Options{}); h.result(r).Error != "" || len(h.result(r).Changes) != 0 {
		t.Fatalf("after accept:\n%s", dump(r))
	}
}

// MySQL lets a UNIQUE key and a FOREIGN KEY, or a CHECK and a FOREIGN KEY,
// share a name. Each is kept, with its own definition.
func TestMySQLSameNameConstraints(t *testing.T) {
	h := mysqlHarness(t)
	mustExec(t, h.admin, `CREATE TABLE p (id INT PRIMARY KEY);
CREATE TABLE c (a INT, pid INT CHECK (pid > 0), UNIQUE KEY fk_a (a),
  CONSTRAINT fk_a FOREIGN KEY (a) REFERENCES p (id),
  CONSTRAINT pid FOREIGN KEY (pid) REFERENCES p (id))`)
	if res := h.result(h.run(Options{})); res.Error != "" {
		t.Fatal(res.Error)
	}
	for id, want := range map[string]string{
		KindConstraint + " " + h.path("c.fk_a (unique)"):      "UNIQUE (a)",
		KindConstraint + " " + h.path("c.fk_a (foreign key)"): "FOREIGN KEY (a) REFERENCES " + h.path("p (id)"),
	} {
		it := storedItem(t, h, id)
		if it == nil || !strings.HasPrefix(it.Attrs[AttrDefinition], want) {
			t.Errorf("%s: want a definition starting %q, got %+v", id, want, it)
		}
	}
	// MariaDB names a column CHECK after its column, so it shares pid's name
	// with the foreign key; MySQL calls it c_chk_1.
	fk := KindConstraint + " " + h.path("c.pid")
	if storedItem(t, h, fk) == nil {
		fk += " (foreign key)"
	}
	s, _ := h.store.Latest("db")
	checks := 0
	for _, it := range s.Items {
		if strings.HasPrefix(it.ID, KindConstraint+" "+h.path("c.")) && it.Attrs[AttrType] == "check" &&
			it.Attrs[AttrDefinition] != "" && it.Attrs[AttrDefinition] != "CHECK ()" {
			checks++
		}
	}
	if checks != 1 || storedItem(t, h, fk) == nil {
		t.Fatalf("want the check and the foreign key pid, got:\n%+v", s.Items)
	}
	mustExec(t, h.admin, "ALTER TABLE c DROP FOREIGN KEY pid")
	r := h.run(Options{})
	if find(h.result(r), core.Removed, fk, "") == nil {
		t.Fatalf("the dropped foreign key wasn't reported:\n%s", dump(r))
	}
}

// Two foreign keys on one column, and a virtual table whose module this
// SQLite doesn't have, are scanned like anything else.
func TestSQLiteOddTables(t *testing.T) {
	h := newHarness(t, sqliteDialect())
	mustExec(t, h.admin, `CREATE TABLE a (id INTEGER PRIMARY KEY);
CREATE TABLE b (id INTEGER PRIMARY KEY);
CREATE TABLE c (p INTEGER REFERENCES a, q INTEGER, FOREIGN KEY (p) REFERENCES a (id) ON DELETE CASCADE, FOREIGN KEY (q) REFERENCES b (id));
PRAGMA writable_schema = ON;
INSERT INTO sqlite_master (type, name, tbl_name, rootpage, sql) VALUES ('table', 'docs', 'docs', 0, 'CREATE VIRTUAL TABLE docs USING nosuchmodule(body)');
PRAGMA writable_schema = OFF;`)
	h.admin.Close()
	r := h.run(Options{})
	if res := h.result(r); res.Error != "" {
		t.Fatalf("baseline: %s", res.Error)
	}
	for _, id := range []string{
		KindTable + " main.docs",
		KindConstraint + " main.c.fk_p_a_1",
		KindConstraint + " main.c.fk_p_a_2",
		KindConstraint + " main.c.fk_q_b",
	} {
		if storedItem(t, h, id) == nil {
			s, _ := h.store.Latest("db")
			t.Errorf("%s missing from %+v", id, s.Items)
		}
	}
	if r := h.run(Options{}); h.result(r).Error != "" || len(h.result(r).Changes) != 0 {
		t.Fatalf("second scan:\n%s", dump(r))
	}
}
