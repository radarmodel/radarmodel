// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package schema

// Tests run against real databases. SQLite always runs. Postgres and MySQL
// run when a server is given:
//
//	RADARMODEL_TEST_POSTGRES=postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable
//	RADARMODEL_TEST_MYSQL=root:root@tcp(127.0.0.1:3306)/
//
// Each test gets a fresh database on the server and drops it afterwards.

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/radarmodel/radarmodel/core"
)

type dialect struct {
	name string
	// newDB creates an empty database and returns its config (with the DSN
	// environment variable set for the test) and an admin handle to it.
	newDB func(t *testing.T) (*DatabaseConfig, *sql.DB)
	// ddl maps a statement name to this dialect's SQL.
	ddl map[string]string
}

func dialects(t *testing.T) []dialect {
	t.Helper()
	out := []dialect{sqliteDialect()}
	if dsn := os.Getenv("RADARMODEL_TEST_POSTGRES"); dsn != "" {
		out = append(out, postgresDialect(dsn))
	}
	if dsn := os.Getenv("RADARMODEL_TEST_MYSQL"); dsn != "" {
		out = append(out, mysqlDialect(dsn))
	}
	return out
}

func eachDialect(t *testing.T, fn func(t *testing.T, d dialect)) {
	for _, d := range dialects(t) {
		d := d
		t.Run(d.name, func(t *testing.T) { fn(t, d) })
	}
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano()%1e9, rand.Intn(1e6))
}

func sqliteDialect() dialect {
	return dialect{
		name: "sqlite",
		newDB: func(t *testing.T) (*DatabaseConfig, *sql.DB) {
			p := filepath.Join(t.TempDir(), "app.db")
			db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(p))
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { db.Close() })
			if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
				t.Fatal(err)
			}
			return &DatabaseConfig{Name: "db", Driver: SQLite, Path: p, Baseline: Rolling}, db
		},
		ddl: map[string]string{
			"base": `
CREATE TABLE customers (id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE, name TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers(id) ON DELETE CASCADE, total REAL NOT NULL CHECK (total >= 0), status TEXT DEFAULT 'new');
CREATE INDEX orders_status ON orders (status);
CREATE VIEW big_orders AS SELECT id, total FROM orders WHERE total > 1000;
CREATE TRIGGER orders_touch BEFORE UPDATE ON orders BEGIN SELECT 1; END;`,
			"add column":       `ALTER TABLE customers ADD COLUMN phone TEXT`,
			"drop column":      `ALTER TABLE customers DROP COLUMN name`,
			"drop index":       `DROP INDEX orders_status`,
			"add index":        `CREATE INDEX customers_name ON customers (name)`,
			"drop table":       `DROP VIEW big_orders; DROP TABLE orders`,
			"add table":        `CREATE TABLE tmp_import (id INTEGER, payload TEXT)`,
			"change view":      `DROP VIEW big_orders; CREATE VIEW big_orders AS SELECT id, total FROM orders WHERE total > 5000`,
			"tmp change":       `ALTER TABLE tmp_import ADD COLUMN extra TEXT`,
			"type change":      "", // SQLite can't change a column type in place
			"not null":         "",
			"change default":   "",
			"many tables":      "CREATE TABLE t%d (id INTEGER PRIMARY KEY, a TEXT, b INTEGER, c REAL, d TEXT NOT NULL DEFAULT 'x', e BLOB, f TEXT, g INTEGER); CREATE INDEX t%[1]d_a ON t%[1]d (a)",
			"drop many":        "DROP TABLE t%d",
			"other base":       "",
			"restrict":         "",
			"revoke":           "",
			"probe privilege":  "",
			"disable trigger":  "",
			"change check":     "", // only in the CREATE statement, covered by the sql attribute
			"extend index":     `DROP INDEX orders_status; CREATE INDEX orders_status ON orders (status, total)`,
			"index order":      `DROP INDEX orders_status; CREATE INDEX orders_status ON orders (status DESC)`,
			"change trigger":   `DROP TRIGGER orders_touch; CREATE TRIGGER orders_touch BEFORE INSERT ON orders BEGIN SELECT 1; END`,
			"change collation": "",
		},
	}
}

func postgresDialect(admin string) dialect {
	return dialect{
		name: "postgres",
		newDB: func(t *testing.T) (*DatabaseConfig, *sql.DB) {
			adm, err := sql.Open("postgres", admin)
			if err != nil {
				t.Fatal(err)
			}
			defer adm.Close()
			name := uniqueName("radar")
			if _, err := adm.Exec("CREATE DATABASE " + name); err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(admin)
			if err != nil {
				t.Fatal(err)
			}
			u.Path = "/" + name
			db, err := sql.Open("postgres", u.String())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				db.Close()
				a, _ := sql.Open("postgres", admin)
				if _, err := a.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
					// Postgres 12 has no FORCE.
					a.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", name)
					a.Exec("DROP DATABASE IF EXISTS " + name)
				}
				a.Close()
			})
			env := "RADAR_TEST_DSN_" + strings.ToUpper(name)
			t.Setenv(env, u.String())
			return &DatabaseConfig{Name: "db", Driver: Postgres, DSNEnv: env, Baseline: Rolling}, db
		},
		ddl: map[string]string{
			"base": `
CREATE TABLE customers (id bigserial PRIMARY KEY, email text NOT NULL UNIQUE, name text, created_at timestamptz DEFAULT now());
CREATE TABLE orders (id bigserial PRIMARY KEY, customer_id bigint REFERENCES customers(id) ON DELETE CASCADE, total numeric(10,2) NOT NULL CONSTRAINT total_positive CHECK (total >= 0), status text DEFAULT 'new');
CREATE INDEX orders_status ON orders (status);
CREATE VIEW big_orders AS SELECT id, total FROM orders WHERE total > 1000;
CREATE FUNCTION touch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$;
CREATE TRIGGER orders_touch BEFORE UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION touch();`,
			"add column":     `ALTER TABLE customers ADD COLUMN phone text`,
			"drop column":    `ALTER TABLE customers DROP COLUMN name`,
			"drop index":     `DROP INDEX orders_status`,
			"add index":      `CREATE INDEX customers_name ON customers (name)`,
			"drop table":     `DROP TABLE orders CASCADE`,
			"add table":      `CREATE TABLE tmp_import (id int, payload text)`,
			"change view":    `CREATE OR REPLACE VIEW big_orders AS SELECT id, total FROM orders WHERE total > 5000`,
			"tmp change":     `ALTER TABLE tmp_import ADD COLUMN extra text`,
			"type change":    `ALTER TABLE customers ALTER COLUMN email TYPE varchar(320)`,
			"not null":       `ALTER TABLE customers ALTER COLUMN name SET NOT NULL`,
			"change default": `ALTER TABLE orders ALTER COLUMN status SET DEFAULT 'pending'`,
			"many tables":    "CREATE TABLE t%d (id bigint PRIMARY KEY, a text, b int, c float8, d text NOT NULL DEFAULT 'x', e bytea, f text, g int); CREATE INDEX t%[1]d_a ON t%[1]d (a)",
			"drop many":      "DROP TABLE t%d",
			// A role with no privileges on anything in the database.
			"restrict": `DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'radar_reader') THEN CREATE ROLE radar_reader LOGIN PASSWORD 'reader'; END IF; END $$;
GRANT CONNECT ON DATABASE %s TO radar_reader; GRANT USAGE ON SCHEMA public TO radar_reader; GRANT SELECT ON ALL TABLES IN SCHEMA public TO radar_reader;`,
			"revoke":           `REVOKE ALL ON ALL TABLES IN SCHEMA public FROM radar_reader; REVOKE USAGE ON SCHEMA public FROM radar_reader;`,
			"change check":     `ALTER TABLE orders DROP CONSTRAINT total_positive, ADD CONSTRAINT total_positive CHECK (total > 0)`,
			"extend index":     `DROP INDEX orders_status; CREATE INDEX orders_status ON orders (status, total)`,
			"index order":      `DROP INDEX orders_status; CREATE INDEX orders_status ON orders (status DESC)`,
			"change trigger":   `DROP TRIGGER orders_touch ON orders; CREATE TRIGGER orders_touch BEFORE INSERT OR UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION touch()`,
			"disable trigger":  `ALTER TABLE orders DISABLE TRIGGER orders_touch`,
			"change collation": `ALTER TABLE customers ALTER COLUMN name TYPE text COLLATE "C"`,
		},
	}
}

func mysqlDialect(admin string) dialect {
	return dialect{
		name: "mysql",
		newDB: func(t *testing.T) (*DatabaseConfig, *sql.DB) {
			cfg, err := mysql.ParseDSN(admin)
			if err != nil {
				t.Fatal(err)
			}
			cfg.MultiStatements = true
			adm, err := sql.Open("mysql", cfg.FormatDSN())
			if err != nil {
				t.Fatal(err)
			}
			defer adm.Close()
			name := uniqueName("radar")
			if _, err := adm.Exec("CREATE DATABASE " + name); err != nil {
				t.Fatal(err)
			}
			cfg.DBName = name
			db, err := sql.Open("mysql", cfg.FormatDSN())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				db.Exec("DROP DATABASE IF EXISTS " + name)
				db.Close()
			})
			env := "RADAR_TEST_DSN_" + strings.ToUpper(name)
			t.Setenv(env, cfg.FormatDSN())
			return &DatabaseConfig{Name: "db", Driver: MySQL, DSNEnv: env, Baseline: Rolling}, db
		},
		ddl: map[string]string{
			"base": `
CREATE TABLE customers (id BIGINT AUTO_INCREMENT PRIMARY KEY, email VARCHAR(200) NOT NULL UNIQUE, name VARCHAR(100), created_at TIMESTAMP NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE orders (id BIGINT AUTO_INCREMENT PRIMARY KEY, customer_id BIGINT, total DECIMAL(10,2) NOT NULL, status VARCHAR(20) DEFAULT 'new',
  CONSTRAINT total_positive CHECK (total >= 0), CONSTRAINT orders_customer FOREIGN KEY (customer_id) REFERENCES customers(id) ON DELETE CASCADE, INDEX orders_status (status));
CREATE VIEW big_orders AS SELECT id, total FROM orders WHERE total > 1000;
CREATE TRIGGER orders_touch BEFORE UPDATE ON orders FOR EACH ROW SET NEW.status = NEW.status;`,
			"add column":     `ALTER TABLE customers ADD COLUMN phone VARCHAR(30)`,
			"drop column":    `ALTER TABLE customers DROP COLUMN name`,
			"drop index":     `DROP INDEX orders_status ON orders`,
			"add index":      `CREATE INDEX customers_name ON customers (name)`,
			"drop table":     `DROP VIEW big_orders; DROP TABLE orders`,
			"add table":      `CREATE TABLE tmp_import (id INT, payload TEXT)`,
			"change view":    `CREATE OR REPLACE VIEW big_orders AS SELECT id, total FROM orders WHERE total > 5000`,
			"tmp change":     `ALTER TABLE tmp_import ADD COLUMN extra TEXT`,
			"type change":    `ALTER TABLE customers MODIFY email VARCHAR(320) NOT NULL`,
			"not null":       `ALTER TABLE customers MODIFY name VARCHAR(100) NOT NULL`,
			"change default": `ALTER TABLE orders ALTER COLUMN status SET DEFAULT 'pending'`,
			"many tables":    "CREATE TABLE t%d (id BIGINT PRIMARY KEY, a VARCHAR(50), b INT, c DOUBLE, d VARCHAR(10) NOT NULL DEFAULT 'x', e BLOB, f TEXT, g INT, INDEX t%[1]d_a (a))",
			"drop many":      "DROP TABLE t%d",
			// A user with SELECT on each table, granted table by table, so one
			// table's privilege can be revoked on its own.
			"restrict": `CREATE USER IF NOT EXISTS 'radar_reader'@'%%' IDENTIFIED BY 'reader'; CREATE USER IF NOT EXISTS 'radar_reader'@'localhost' IDENTIFIED BY 'reader';
GRANT SELECT ON %[1]s.customers TO 'radar_reader'@'%%'; GRANT SELECT ON %[1]s.orders TO 'radar_reader'@'%%';
GRANT SELECT ON %[1]s.customers TO 'radar_reader'@'localhost'; GRANT SELECT ON %[1]s.orders TO 'radar_reader'@'localhost';`,
			"revoke":           `REVOKE SELECT ON %[1]s.orders FROM 'radar_reader'@'%%'; REVOKE SELECT ON %[1]s.orders FROM 'radar_reader'@'localhost';`,
			"change check":     `ALTER TABLE orders DROP CONSTRAINT total_positive; ALTER TABLE orders ADD CONSTRAINT total_positive CHECK (total > 0)`,
			"extend index":     `DROP INDEX orders_status ON orders; CREATE INDEX orders_status ON orders (status, total)`,
			"index order":      `DROP INDEX orders_status ON orders; CREATE INDEX orders_status ON orders (status DESC)`,
			"change trigger":   `DROP TRIGGER orders_touch; CREATE TRIGGER orders_touch BEFORE INSERT ON orders FOR EACH ROW SET NEW.status = NEW.status`,
			"disable trigger":  "",
			"change collation": `ALTER TABLE customers MODIFY name VARCHAR(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
		},
	}
}

// exec runs a dialect's statement, which may hold several separated by ';'.
// It skips the test when the dialect has no such statement.
func (d dialect) exec(t *testing.T, db *sql.DB, name string, args ...any) {
	t.Helper()
	stmt, ok := d.ddl[name]
	if !ok {
		t.Fatalf("no statement %q for %s", name, d.name)
	}
	if stmt == "" {
		t.Skipf("%s can't do %q", d.name, name)
	}
	if len(args) > 0 {
		stmt = fmt.Sprintf(stmt, args...)
	}
	if d.name == "sqlite" || d.name == "mysql" {
		// SQLite runs a whole script in one Exec; MySQL does with multiStatements.
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, stmt)
		}
		return
	}
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, stmt)
	}
}

// harness runs scans of one database with a store and a fake clock.
type harness struct {
	t     *testing.T
	d     dialect
	cfg   *Config
	db    *DatabaseConfig
	admin *sql.DB
	store *core.Store
	clock time.Time
}

func newHarness(t *testing.T, d dialect) *harness {
	t.Helper()
	dbc, admin := d.newDB(t)
	cfg := &Config{Databases: []DatabaseConfig{*dbc}, Timeout: core.Duration(2 * time.Minute)}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, d: d, cfg: cfg, db: &cfg.Databases[0], admin: admin,
		store: &core.Store{Dir: t.TempDir()}, clock: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
}

func (h *harness) exec(name string, args ...any) { h.t.Helper(); h.d.exec(h.t, h.admin, name, args...) }

func (h *harness) run(opt Options) *core.Report {
	h.t.Helper()
	h.clock = h.clock.Add(time.Hour)
	now := h.clock
	opt.Now = func() time.Time { return now }
	return Run(context.Background(), h.cfg, h.store, nil, opt)
}

func (h *harness) result(r *core.Report) core.SourceResult {
	h.t.Helper()
	if len(r.Results) != 1 {
		h.t.Fatalf("%d results", len(r.Results))
	}
	return r.Results[0]
}

// path qualifies a table-relative name with the dialect's schema.
func (h *harness) path(rel string) string {
	switch h.d.name {
	case "sqlite":
		return "main." + rel
	case "postgres":
		return "public." + rel
	}
	dsn, _ := mysql.ParseDSN(os.Getenv(h.db.DSNEnv))
	return dsn.DBName + "." + rel
}

func (h *harness) dbName() string {
	switch h.d.name {
	case "postgres":
		u, _ := url.Parse(os.Getenv(h.db.DSNEnv))
		return strings.TrimPrefix(u.Path, "/")
	case "mysql":
		dsn, _ := mysql.ParseDSN(os.Getenv(h.db.DSNEnv))
		return dsn.DBName
	}
	return ""
}

func find(res core.SourceResult, kind core.ChangeKind, id, field string) *core.Change {
	for i, c := range res.Changes {
		if c.Kind == kind && c.ID == id && c.Field == field {
			return &res.Changes[i]
		}
	}
	return nil
}

func dump(r *core.Report) string {
	var b strings.Builder
	r.WriteText(&b, core.Info)
	return b.String()
}

func storedBytes(t *testing.T, st *core.Store, source string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(st.Dir, "latest", source+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// cutter is a TCP proxy in front of a database server that can drop every
// connection on demand, to simulate a connection lost in the middle of a scan.
type cutter struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
	cut   bool
}

func newCutter(t *testing.T, target string) *cutter {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := &cutter{ln: ln}
	t.Cleanup(func() { ln.Close(); c.dropAll() })
	go func() {
		for {
			in, err := ln.Accept()
			if err != nil {
				return
			}
			c.mu.Lock()
			if c.cut {
				c.mu.Unlock()
				in.Close()
				continue
			}
			out, err := net.Dial("tcp", target)
			if err != nil {
				c.mu.Unlock()
				in.Close()
				continue
			}
			c.conns = append(c.conns, in, out)
			c.mu.Unlock()
			go func() { io.Copy(out, in); out.Close() }()
			go func() { io.Copy(in, out); in.Close() }()
		}
	}()
	return c
}

func (c *cutter) addr() string { return c.ln.Addr().String() }

func (c *cutter) dropAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cut = true
	for _, x := range c.conns {
		x.Close()
	}
	c.conns = nil
}
