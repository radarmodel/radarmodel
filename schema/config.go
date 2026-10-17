// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

// Package schema is RadarModel Schema: it reads the structure of Postgres,
// MySQL and SQLite databases (tables, columns, indexes, constraints, views),
// compares it with the last snapshot, and reports drift.
package schema

import (
	"fmt"
	"strings"
	"time"

	"github.com/radarmodel/radarmodel/core"
)

// Product is the name RadarModel Schema puts on its reports.
const Product = "RadarModel Schema"

// Database drivers.
const (
	Postgres = "postgres"
	MySQL    = "mysql"
	SQLite   = "sqlite"
)

// Baseline modes.
const (
	// Rolling compares each scan with the one before it, so every change is
	// reported once.
	Rolling = "rolling"
	// Pinned compares every scan with an accepted baseline, so drift is
	// reported on every run until `radarmodel-schema accept` takes the
	// current schema as the new baseline.
	Pinned = "pinned"
)

// Config is the radarmodel-schema configuration file.
type Config struct {
	// StateDir holds the snapshots; ".radarmodel" when empty, relative to
	// the config file.
	StateDir string `json:"state_dir,omitempty"`
	// Interval is the time between scans in watch mode; 1h when empty.
	Interval core.Duration `json:"interval,omitempty"`
	// Timeout bounds one whole scan of one database; 2m when empty.
	Timeout core.Duration `json:"timeout,omitempty"`
	// MinFraction guards against pointing at the wrong or an empty database:
	// for a schema of 10 or more tables and views, a scan finding fewer than
	// this share of them is treated as a failed scan. 0.5 when unset; 0 turns
	// it off.
	MinFraction *float64 `json:"min_fraction,omitempty"`
	// Ignore lists objects never reported, for every database (see
	// DatabaseConfig.Ignore).
	Ignore []string `json:"ignore,omitempty"`

	Databases []DatabaseConfig  `json:"databases"`
	Alerts    *core.AlertConfig `json:"alerts,omitempty"`
}

// DatabaseConfig is one database to watch.
type DatabaseConfig struct {
	// Name is used in reports, file names and commands.
	Name string `json:"name"`
	// Driver is postgres, mysql or sqlite.
	Driver string `json:"driver"`
	// DSNEnv names the environment variable holding the connection string
	// (Postgres and MySQL). Connection strings carry passwords, so they never
	// go in the config file.
	DSNEnv string `json:"dsn_env,omitempty"`
	// Path is the SQLite database file, relative to the config file. It is
	// opened read-only and never created.
	Path string `json:"path,omitempty"`
	// Schemas limits Postgres to these schemas (all but the system ones when
	// empty) and MySQL to these databases (the connection's database when
	// empty). Not used for SQLite.
	Schemas []string `json:"schemas,omitempty"`
	// Ignore lists objects never reported, as globs on their qualified name:
	// "public.tmp_*" covers the tables and everything in them,
	// "*.updated_at" every column with that name. '*' matches anything,
	// dots included.
	Ignore []string `json:"ignore,omitempty"`
	// Baseline is "rolling" (the default) or "pinned".
	Baseline string `json:"baseline,omitempty"`
}

// Defaults applied by Validate.
const (
	DefaultInterval    = time.Hour
	DefaultTimeout     = 2 * time.Minute
	DefaultMinFraction = 0.5
	DefaultStateDir    = ".radarmodel"
	minInterval        = time.Minute
)

// Validate fills in defaults and checks the configuration.
func (c *Config) Validate() error {
	if c.StateDir == "" {
		c.StateDir = DefaultStateDir
	}
	if c.Interval == 0 {
		c.Interval = core.Duration(DefaultInterval)
	}
	if time.Duration(c.Interval) < minInterval {
		return fmt.Errorf("interval must be at least %s", minInterval)
	}
	if c.Timeout == 0 {
		c.Timeout = core.Duration(DefaultTimeout)
	}
	if c.MinFraction == nil {
		v := DefaultMinFraction
		c.MinFraction = &v
	}
	if f := *c.MinFraction; f < 0 || f > 1 {
		return fmt.Errorf("min_fraction must be between 0 and 1")
	}
	if len(c.Databases) == 0 {
		return fmt.Errorf("no databases configured")
	}
	if err := checkGlobs(c.Ignore, "ignore"); err != nil {
		return err
	}
	names := map[string]bool{}
	keys := map[string]string{}
	for i := range c.Databases {
		d := &c.Databases[i]
		where := fmt.Sprintf("databases[%d]", i)
		if d.Name == "" {
			return fmt.Errorf("%s: name is empty", where)
		}
		if names[d.Name] {
			return fmt.Errorf("%s: name %q is used twice", where, d.Name)
		}
		if other, ok := keys[core.FileKey(d.Name)]; ok {
			return fmt.Errorf("%s: name %q is too close to %q (they would share a snapshot file); pick a clearly different name", where, d.Name, other)
		}
		names[d.Name] = true
		keys[core.FileKey(d.Name)] = d.Name
		switch d.Driver {
		case Postgres, MySQL:
			if d.DSNEnv == "" {
				return fmt.Errorf("%s (%s): dsn_env is empty; name the environment variable holding the connection string", where, d.Name)
			}
			if d.Path != "" {
				return fmt.Errorf("%s (%s): path is for sqlite only", where, d.Name)
			}
		case SQLite:
			if d.Path == "" {
				return fmt.Errorf("%s (%s): path is empty", where, d.Name)
			}
			if d.DSNEnv != "" || len(d.Schemas) > 0 {
				return fmt.Errorf("%s (%s): sqlite takes path only, not dsn_env or schemas", where, d.Name)
			}
		default:
			return fmt.Errorf("%s (%s): driver %q is not one of postgres, mysql, sqlite", where, d.Name, d.Driver)
		}
		switch d.Baseline {
		case "":
			d.Baseline = Rolling
		case Rolling, Pinned:
		default:
			return fmt.Errorf("%s (%s): baseline %q is not rolling or pinned", where, d.Name, d.Baseline)
		}
		if err := checkGlobs(d.Ignore, where+".ignore"); err != nil {
			return err
		}
	}
	return nil
}

func checkGlobs(gs []string, field string) error {
	for _, g := range gs {
		if strings.TrimSpace(g) == "" {
			return fmt.Errorf("%s has an empty entry", field)
		}
	}
	return nil
}

// Find returns the database with the given name.
func (c *Config) Find(name string) (*DatabaseConfig, bool) {
	for i := range c.Databases {
		if c.Databases[i].Name == name {
			return &c.Databases[i], true
		}
	}
	return nil, false
}

// Names lists the configured database names.
func (c *Config) Names() []string {
	var out []string
	for _, d := range c.Databases {
		out = append(out, d.Name)
	}
	return out
}
