// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"time"

	// Database drivers: lib/pq registers "postgres", go-sql-driver "mysql",
	// and ncruces' pure-Go SQLite "sqlite3" (with SQLite itself embedded).
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
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
// environment and never appear in errors.
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
	}
	db, err := sql.Open(driverName(d), dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %v", d.Name, err)
	}
	db.SetMaxOpenConns(2)
	db.SetConnMaxLifetime(time.Minute)
	return db, nil
}

// ReadSchema reads a database's schema, twice, and returns it only when
// both reads agree, so a migration running during the scan can't produce a
// half-changed snapshot. Ignored objects are left out.
func ReadSchema(ctx context.Context, db *sql.DB, d *DatabaseConfig, ignore []string, between func(string)) ([]core.Item, error) {
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

func scanOne(ctx context.Context, d *DatabaseConfig, ignore []string, store *core.Store,
	minFraction float64, now func() time.Time, opt Options) core.SourceResult {
	res := core.SourceResult{Source: d.Name}
	fail := func(err error) core.SourceResult {
		res.Error = err.Error()
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
	next := &core.Snapshot{Source: d.Name, Kind: "schema/" + d.Driver, TakenAt: now().UTC(), Items: items}
	if err := next.Normalize(); err != nil {
		return fail(err)
	}
	res.Items = len(next.Items)
	if prev == nil {
		res.Baseline = true
	} else {
		prev = &core.Snapshot{Source: prev.Source, Kind: prev.Kind, TakenAt: prev.TakenAt, Items: dropIgnored(prev.Items, ignore)}
		if before, after := countTables(prev.Items), countTables(next.Items); minFraction > 0 && before >= minGuardTables &&
			float64(after) < minFraction*float64(before) {
			return fail(fmt.Errorf("found %d tables and views, down from %d; treated as a failed scan, not as drops (wrong database? min_fraction %.2f). If the drop is real, run once with min_fraction 0 to accept it",
				after, before, minFraction))
		}
		res.Changes = core.Diff(prev, next)
		Classify(res.Changes)
		if d.Driver == MySQL {
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
