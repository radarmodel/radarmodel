// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	// Database drivers: lib/pq registers "postgres", go-sql-driver "mysql",
	// and ncruces' pure-Go SQLite "sqlite3" (with SQLite itself embedded).
	"github.com/go-sql-driver/mysql"
	"github.com/lib/pq"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	"github.com/radarmodel/radarmodel"
	"github.com/radarmodel/radarmodel/core"
)

// readAttempts is how many pairs of reads a scan tries before giving up on a
// schema that keeps changing under it.
const readAttempts = 3

// minGuardTables is the smallest schema the min_fraction guard applies to.
// In a database with a handful of tables, dropping two of three is ordinary
// drift; the guard is for a scan that suddenly finds most of a real schema
// gone, as when a connection string points at the wrong database.
const minGuardTables = 10

// Options control one run.
type Options struct {
	// DryRun compares with the stored snapshots but doesn't replace them.
	DryRun bool
	// Accept stores the current schema as the new baseline even when the
	// database's baseline is pinned.
	Accept bool
	// Now returns the time stamped on snapshots; time.Now when nil.
	Now func() time.Time
	// BetweenReads, when set, runs between the two reads of each pair (tests
	// use it to change the schema in the middle of a scan).
	BetweenReads func(database string)
}

// Run scans the named databases (all when names is empty), compares each with
// its stored snapshot, and stores the new snapshots as their baseline mode
// says. Databases are scanned in parallel; one failing leaves the others, and
// its own stored snapshot, alone.
func Run(ctx context.Context, cfg *Config, store *core.Store, names []string, opt Options) *core.Report {
	now := time.Now
	if opt.Now != nil {
		now = opt.Now
	}
	var dbs []*DatabaseConfig
	for i := range cfg.Databases {
		d := &cfg.Databases[i]
		if len(names) == 0 || containsID(names, d.Name) {
			dbs = append(dbs, d)
		}
	}
	rep := &core.Report{Product: Product, Version: radarmodel.Version, At: now().UTC(),
		Results: make([]core.SourceResult, len(dbs))}
	minFraction := DefaultMinFraction
	if cfg.MinFraction != nil {
		minFraction = *cfg.MinFraction
	}
	var wg sync.WaitGroup
	for i, d := range dbs {
		wg.Add(1)
		go func(i int, d *DatabaseConfig) {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Timeout))
			defer cancel()
			ignore := append(append([]string(nil), cfg.Ignore...), d.Ignore...)
			rep.Results[i] = scanOne(sctx, d, ignore, store, minFraction, now, opt)
		}(i, d)
	}
	wg.Wait()
	return rep
}

func driverName(d *DatabaseConfig) string {
	if d.Driver == SQLite {
		return "sqlite3"
	}
	return d.Driver
}

// Open connects to a configured database. Connection strings come from the
// environment and never appear in errors: one that can't be parsed is
// reported as invalid without being quoted.
func Open(d *DatabaseConfig) (*sql.DB, error) {
	var dsn string
	switch d.Driver {
	case SQLite:
		var err error
		if dsn, err = sqliteDSN(d.Path); err != nil {
			return nil, err
		}
	default:
		dsn = os.Getenv(d.DSNEnv)
		if dsn == "" {
			return nil, fmt.Errorf("%s is not set", d.DSNEnv)
		}
		if !validDSN(d.Driver, dsn) {
			return nil, fmt.Errorf("%s: invalid connection string", d.DSNEnv)
		}
	}
	db, err := sql.Open(driverName(d), dsn)
	if err != nil {
		return nil, Redact(d, fmt.Errorf("opening %s: %v", d.Name, err))
	}
	db.SetMaxOpenConns(2)
	db.SetConnMaxLifetime(time.Minute)
	return db, nil
}

func validDSN(driver, dsn string) bool {
	switch driver {
	case Postgres:
		_, err := pq.NewConfig(dsn)
		return err == nil
	case MySQL:
		_, err := mysql.ParseDSN(dsn)
		return err == nil
	}
	return true
}

var pgPassword = regexp.MustCompile(`password\s*=\s*('(?:[^'\\]|\\.)*'|\S+)`)

// Redact takes a database's connection string and password out of an error
// message. Drivers sometimes quote the connection string when it is wrong,
// and the message goes to the terminal, to JSON output and to alerts.
func Redact(d *DatabaseConfig, err error) error {
	if err == nil || d.DSNEnv == "" {
		return err
	}
	dsn := os.Getenv(d.DSNEnv)
	if dsn == "" {
		return err
	}
	secrets := []string{dsn}
	switch {
	case strings.Contains(dsn, "://"):
		if u, e := url.Parse(dsn); e == nil {
			if p, ok := u.User.Password(); ok {
				secrets = append(secrets, p, url.QueryEscape(p), url.PathEscape(p))
			}
		}
		if i := strings.LastIndex(dsn, "@"); i > 0 {
			if j := strings.Index(dsn, "://"); j >= 0 && j+3 < i {
				if _, p, ok := strings.Cut(dsn[j+3:i], ":"); ok {
					secrets = append(secrets, p)
				}
			}
		}
	case d.Driver == MySQL:
		if cfg, e := mysql.ParseDSN(dsn); e == nil {
			secrets = append(secrets, cfg.Passwd)
		}
	default:
		for _, m := range pgPassword.FindAllStringSubmatch(dsn, -1) {
			secrets = append(secrets, m[1], strings.Trim(m[1], "'"))
		}
	}
	msg := err.Error()
	for _, s := range secrets {
		// Very short passwords would turn the message into noise; the
		// connection string itself always goes.
		if s == dsn || len(s) >= 4 {
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
}

// ReadSchema reads a database's schema, twice, and returns it only when
// both reads agree, so a migration running during the scan can't produce a
// half-changed snapshot. Ignored objects are left out.
func ReadSchema(ctx context.Context, db *sql.DB, d *DatabaseConfig, ignore []string, between func(string)) (items []core.Item, err error) {
	defer func() { err = Redact(d, err) }()
	var read func(context.Context, querier, *DatabaseConfig) ([]core.Item, error)
	opts := &sql.TxOptions{ReadOnly: true}
	switch d.Driver {
	case Postgres:
		read, opts.Isolation = readPostgres, sql.LevelRepeatableRead
	case MySQL:
		read = readMySQL
	case SQLite:
		read = readSQLite
	default:
		return nil, fmt.Errorf("unknown driver %q", d.Driver)
	}
	once := func() ([]core.Item, error) {
		tx, err := db.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if d.Driver == Postgres {
			// Catalog functions print names and values according to session
			// settings: a schema on the search_path loses its prefix, and
			// IntervalStyle changes how defaults read. A role or connection
			// string setting them differently must not look like drift.
			if _, err := tx.ExecContext(ctx, pgSession); err != nil {
				return nil, err
			}
		}
		items, err := read(ctx, tx, d)
		if err != nil {
			return nil, err
		}
		s := &core.Snapshot{Source: d.Name, Items: dropIgnored(items, ignore)}
		if err := s.Normalize(); err != nil {
			return nil, err
		}
		return s.Items, nil
	}
	for attempt := 0; attempt < readAttempts; attempt++ {
		first, err := once()
		if err != nil {
			return nil, err
		}
		if between != nil {
			between(d.Name)
		}
		second, err := once()
		if err != nil {
			return nil, err
		}
		if reflect.DeepEqual(first, second) {
			return second, nil
		}
	}
	return nil, errors.New("the schema kept changing while it was read (a migration running?); try again when it's done")
}

// pgSession fixes, for the read transaction only, the settings that change
// how pg_catalog functions print definitions.
const pgSession = `SET LOCAL search_path = pg_catalog, pg_temp; SET LOCAL IntervalStyle = postgres; ` +
	`SET LOCAL DateStyle = 'ISO, MDY'; SET LOCAL extra_float_digits = 1; SET LOCAL bytea_output = hex`

// ServerVersion returns the database server's version ("16.4", "8.4.2",
// "11.4.3-MariaDB"), or "" for SQLite, whose library is built in.
func ServerVersion(ctx context.Context, db *sql.DB, d *DatabaseConfig) (string, error) {
	var q string
	switch d.Driver {
	case Postgres:
		q = "SHOW server_version"
	case MySQL:
		q = "SELECT VERSION()"
	default:
		return "", nil
	}
	var v string
	if err := db.QueryRowContext(ctx, q).Scan(&v); err != nil {
		return "", Redact(d, err)
	}
	if f := strings.Fields(v); len(f) > 0 {
		v = f[0]
	}
	return v, nil
}

func scanOne(ctx context.Context, d *DatabaseConfig, ignore []string, store *core.Store,
	minFraction float64, now func() time.Time, opt Options) core.SourceResult {
	res := core.SourceResult{Source: d.Name}
	fail := func(err error) core.SourceResult {
		res.Error = Redact(d, err).Error()
		res.Changes = nil
		return res
	}
	prev, err := store.Latest(d.Name)
	if err != nil {
		return fail(err)
	}
	db, err := Open(d)
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	items, err := ReadSchema(ctx, db, d, ignore, opt.BetweenReads)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fail(fmt.Errorf("the scan took longer than the timeout: %v", err))
		}
		return fail(err)
	}
	server, err := ServerVersion(ctx, db, d)
	if err != nil {
		return fail(err)
	}
	next := &core.Snapshot{Source: d.Name, Kind: "schema/" + d.Driver, TakenAt: now().UTC(), Items: items}
	if server != "" {
		next.Meta = map[string]string{"server": server}
	}
	if err := next.Normalize(); err != nil {
		return fail(err)
	}
	if d.Driver == MySQL && (prev == nil || opt.Accept) {
		// Without SELECT, a table that is later dropped can't be told apart
		// from one hidden by privileges, and every scan after it would fail.
		if err := checkMySQLSelect(ctx, db, next.Items); err != nil {
			return fail(err)
		}
	}
	res.Items = len(next.Items)
	if prev == nil {
		res.Baseline = true
	} else {
		prev = &core.Snapshot{Source: prev.Source, Kind: prev.Kind, TakenAt: prev.TakenAt, Meta: prev.Meta, Items: dropIgnored(prev.Items, ignore)}
		if before, after := countTables(prev.Items), countTables(next.Items); minFraction > 0 && before >= minGuardTables &&
			float64(after) < minFraction*float64(before) {
			return fail(fmt.Errorf("found %d tables and views, down from %d; treated as a failed scan, not as drops (wrong database? min_fraction %.2f). If the drop is real, run once with min_fraction 0 to accept it",
				after, before, minFraction))
		}
		res.Changes = core.Diff(prev, next)
		Classify(res.Changes)
		// Accepting is the user saying the changes are real, so the probe that
		// asks whether a removal is real is skipped; it can't help a user that
		// lost a privilege get unstuck otherwise.
		if d.Driver == MySQL && !opt.Accept {
			var removed []string
			for _, c := range res.Changes {
				if c.Kind == core.Removed {
					removed = append(removed, c.ID)
				}
			}
			prevIDs := map[string]bool{}
			for _, it := range prev.Items {
				prevIDs[it.ID] = true
			}
			if err := probeMySQL(ctx, db, removed, prevIDs); err != nil {
				return fail(err)
			}
		}
		noteUpgrade(&res, prev.Meta["server"], server)
	}
	save := !opt.DryRun && (prev == nil || d.Baseline != Pinned || opt.Accept)
	if save {
		if err := store.Save(next); err != nil {
			return fail(fmt.Errorf("saving the snapshot: %v", err))
		}
	}
	return res
}

func countTables(items []core.Item) int {
	n := 0
	for _, it := range items {
		if k, _ := Split(it.ID); k == KindTable || k == KindView {
			n++
		}
	}
	return n
}

// noteUpgrade reports a server version change. Formatting of types, defaults
// and definitions can change between versions, so the other changes found in
// the same scan get a note saying so.
func noteUpgrade(res *core.SourceResult, old, new string) {
	if old == "" || new == "" || old == new {
		return
	}
	for i := range res.Changes {
		if res.Changes[i].Note == "" {
			res.Changes[i].Note = "the server was upgraded since the last scan; this may be how the new version prints it"
		}
	}
	res.Changes = append([]core.Change{{Source: res.Source, Kind: core.Changed, ID: "server", Field: "version",
		Old: old, New: new, Severity: core.Info}}, res.Changes...)
}
