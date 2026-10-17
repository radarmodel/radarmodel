// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

// Command radarmodel-schema watches Postgres, MySQL and SQLite schemas and
// reports drift: tables, columns, indexes, constraints, views and triggers
// that changed since the last scan or the accepted baseline.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/radarmodel/radarmodel"
	"github.com/radarmodel/radarmodel/core"
	"github.com/radarmodel/radarmodel/schema"
)

const usage = `radarmodel-schema watches your database schemas for drift.

Usage:
  radarmodel-schema init   [-config FILE]              write a starter config
  radarmodel-schema check  [-config FILE] [flags] [DB...]  scan, compare, report
  radarmodel-schema watch  [-config FILE] [flags]      scan on an interval until stopped
  radarmodel-schema accept [-config FILE] DB...        take the current schema as the baseline
  radarmodel-schema diff   [-config FILE] DB1 DB2      compare two databases now
  radarmodel-schema show   [-config FILE] DB           list the stored objects of a database
  radarmodel-schema version

The config file is radarmodel-schema.json, or $RADARMODEL_SCHEMA_CONFIG when set.

Flags for check and watch:
  -min LEVEL     lowest severity shown at the terminal: info, warning (default), breaking
  -json          print the report as JSON
  -no-alerts     don't send webhook, Slack or email alerts
  -dry-run       compare but keep the stored snapshots (check only)

Exit codes for check: 0 nothing breaking, 1 a breaking change, 2 a database
couldn't be scanned or the config is wrong. For diff: 0 the same, 1 different,
2 an error.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return core.ExitError
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "-version", "--version":
		fmt.Fprintf(stdout, "radarmodel-schema %s\n", radarmodel.Version)
		return core.ExitOK
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return core.ExitOK
	case "init":
		return cmdInit(rest, stdout, stderr)
	case "check":
		return cmdCheck(rest, stdout, stderr, false)
	case "watch":
		return cmdCheck(rest, stdout, stderr, true)
	case "accept":
		return cmdAccept(rest, stdout, stderr)
	case "diff":
		return cmdDiff(rest, stdout, stderr)
	case "show":
		return cmdShow(rest, stdout, stderr)
	}
	fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
	return core.ExitError
}

func defaultConfig() string {
	if v := os.Getenv("RADARMODEL_SCHEMA_CONFIG"); v != "" {
		return v
	}
	return "radarmodel-schema.json"
}

func newFlags(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	return fs, fs.String("config", defaultConfig(), "config file")
}

// load reads and checks the config. The state folder and SQLite paths are
// resolved against the config file's folder.
func load(path string) (*schema.Config, *core.Store, error) {
	var cfg schema.Config
	if err := core.LoadJSON(path, &cfg); err != nil {
		return nil, nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("%s: %v", path, err)
	}
	base := filepath.Dir(path)
	for i := range cfg.Databases {
		if p := cfg.Databases[i].Path; p != "" && !filepath.IsAbs(p) {
			cfg.Databases[i].Path = filepath.Join(base, p)
		}
	}
	dir := cfg.StateDir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	return &cfg, &core.Store{Dir: dir}, nil
}

func checkNames(cfg *schema.Config, names []string) error {
	for _, n := range names {
		if _, ok := cfg.Find(n); !ok {
			return fmt.Errorf("no database named %q; configured: %s", n, strings.Join(cfg.Names(), ", "))
		}
	}
	return nil
}

const starter = `{
  "interval": "1h",
  "databases": [
    {"name": "prod", "driver": "postgres", "dsn_env": "PROD_DATABASE_URL"},
    {"name": "app", "driver": "mysql", "dsn_env": "APP_MYSQL_DSN"},
    {"name": "local", "driver": "sqlite", "path": "app.db"}
  ],
  "ignore": ["*.tmp_*"],
  "alerts": {
    "min_severity": "warning"
  }
}
`

func cmdInit(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newFlags("init", stderr)
	if err := fs.Parse(args); err != nil {
		return core.ExitError
	}
	f, err := os.OpenFile(*cfg, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			fmt.Fprintf(stderr, "%s exists already; not overwriting it\n", *cfg)
		} else {
			fmt.Fprintln(stderr, err)
		}
		return core.ExitError
	}
	_, werr := f.WriteString(starter)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		fmt.Fprintln(stderr, werr)
		return core.ExitError
	}
	fmt.Fprintf(stdout, "wrote %s\n", *cfg)
	fmt.Fprintln(stdout, "Keep the databases you want to watch, set the environment variables named in dsn_env,")
	fmt.Fprintln(stdout, "then run: radarmodel-schema check")
	return core.ExitOK
}

// lockStale is how old a lock file must be before it counts as left over
// from a crashed run.
const lockStale = 30 * time.Minute

func cmdCheck(args []string, stdout, stderr io.Writer, loop bool) int {
	name := "check"
	if loop {
		name = "watch"
	}
	fs, cfgPath := newFlags(name, stderr)
	minFlag := fs.String("min", "warning", "lowest severity shown")
	asJSON := fs.Bool("json", false, "print JSON")
	noAlerts := fs.Bool("no-alerts", false, "don't send alerts")
	dryRun := false
	if !loop {
		fs.BoolVar(&dryRun, "dry-run", false, "keep the stored snapshots")
	}
	if err := fs.Parse(args); err != nil {
		return core.ExitError
	}
	show, err := core.ParseSeverity(*minFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	cfg, store, err := load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	names := fs.Args()
	if loop && len(names) > 0 {
		fmt.Fprintln(stderr, "watch scans every configured database; it takes no names")
		return core.ExitError
	}
	if err := checkNames(cfg, names); err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	var outs []core.Output
	alertMin := core.Warning
	if !*noAlerts {
		outs, alertMin, err = cfg.Alerts.Outputs(core.NewAlertClient(30 * time.Second))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return core.ExitError
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	once := func(sent map[string]string) int {
		unlock, err := store.Lock(lockStale)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return core.ExitError
		}
		rep := schema.Run(ctx, cfg, store, names, schema.Options{DryRun: dryRun})
		unlock()
		if *asJSON {
			rep.WriteJSON(stdout)
		} else {
			fmt.Fprintf(stdout, "%s  %s\n", rep.At.Format(time.RFC3339), core.Headline(rep, show))
			rep.WriteText(stdout, show)
		}
		alert := rep
		if sent != nil {
			alert = dropRepeats(rep, sent, alertMin)
		}
		for _, err := range core.Deliver(ctx, outs, alert, alertMin) {
			fmt.Fprintln(stderr, err)
		}
		return rep.ExitCode()
	}
	if !loop {
		return once(nil)
	}
	interval := time.Duration(cfg.Interval)
	fmt.Fprintf(stderr, "watching %d databases every %s; Ctrl-C to stop\n", len(cfg.Databases), interval)
	sent := map[string]string{}
	for {
		once(sent)
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return core.ExitOK
		case <-t.C:
		}
	}
}

// dropRepeats keeps watch from alerting about the same drift every interval:
// with a pinned baseline, drift stays in every report until it is accepted.
// A database's changes are alerted when they differ from the last ones sent.
// The terminal report always shows everything.
func dropRepeats(r *core.Report, sent map[string]string, min core.Severity) *core.Report {
	out := *r
	out.Results = make([]core.SourceResult, len(r.Results))
	for i, res := range r.Results {
		if res.Error == "" {
			var sig []string
			for _, c := range res.Changes {
				if c.Severity >= min {
					sig = append(sig, fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", c.Kind, c.ID, c.Field, c.Old, c.New))
				}
			}
			s := strings.Join(sig, "\x01")
			if s != "" && sent[res.Source] == s {
				res.Changes = nil
			}
			sent[res.Source] = s
		}
		out.Results[i] = res
	}
	return &out
}

func cmdAccept(args []string, stdout, stderr io.Writer) int {
	fs, cfgPath := newFlags("accept", stderr)
	if err := fs.Parse(args); err != nil {
		return core.ExitError
	}
	cfg, store, err := load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	if fs.NArg() == 0 {
		fmt.Fprintf(stderr, "accept needs the databases to accept: %s\n", strings.Join(cfg.Names(), ", "))
		return core.ExitError
	}
	if err := checkNames(cfg, fs.Args()); err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	unlock, err := store.Lock(lockStale)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	defer unlock()
	rep := schema.Run(context.Background(), cfg, store, fs.Args(), schema.Options{Accept: true})
	rep.WriteText(stdout, core.Warning)
	if rep.Errors() > 0 {
		return core.ExitError
	}
	fmt.Fprintf(stdout, "accepted the current schema of %s as the baseline\n", strings.Join(fs.Args(), ", "))
	return core.ExitOK
}

func cmdDiff(args []string, stdout, stderr io.Writer) int {
	fs, cfgPath := newFlags("diff", stderr)
	minFlag := fs.String("min", "info", "lowest severity shown")
	if err := fs.Parse(args); err != nil {
		return core.ExitError
	}
	show, err := core.ParseSeverity(*minFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	cfg, _, err := load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, "diff needs two database names: the reference first, then the one to check")
		return core.ExitError
	}
	if err := checkNames(cfg, fs.Args()); err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Duration(cfg.Timeout))
	defer cancel()
	snaps := make([]*core.Snapshot, 2)
	for i, n := range fs.Args() {
		d, _ := cfg.Find(n)
		db, err := schema.Open(d)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", n, err)
			return core.ExitError
		}
		items, err := schema.ReadSchema(ctx, db, d, append(append([]string(nil), cfg.Ignore...), d.Ignore...), nil)
		db.Close()
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", n, err)
			return core.ExitError
		}
		snaps[i] = &core.Snapshot{Source: fs.Arg(0) + " vs " + fs.Arg(1), Items: items}
	}
	schema.AlignSchemas(snaps[0], snaps[1])
	changes := core.Diff(snaps[0], snaps[1])
	schema.Classify(changes)
	rep := &core.Report{Product: schema.Product, Version: radarmodel.Version, At: time.Now().UTC(),
		Results: []core.SourceResult{{Source: snaps[0].Source, Items: len(snaps[1].Items), Changes: changes}}}
	fmt.Fprintf(stdout, "%s: what %s has that differs from %s\n", core.Headline(rep, show), fs.Arg(1), fs.Arg(0))
	rep.WriteText(stdout, show)
	if len(changes) > 0 {
		return 1
	}
	return core.ExitOK
}

func cmdShow(args []string, stdout, stderr io.Writer) int {
	fs, cfgPath := newFlags("show", stderr)
	if err := fs.Parse(args); err != nil {
		return core.ExitError
	}
	cfg, store, err := load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "show needs one database name: %s\n", strings.Join(cfg.Names(), ", "))
		return core.ExitError
	}
	if err := checkNames(cfg, fs.Args()); err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	s, err := store.Latest(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	if s == nil {
		fmt.Fprintf(stderr, "no snapshot of %s yet; run radarmodel-schema check first\n", fs.Arg(0))
		return core.ExitError
	}
	fmt.Fprintf(stdout, "%s: %d objects, scanned %s\n", s.Source, len(s.Items), s.TakenAt.Format(time.RFC3339))
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	for _, it := range s.Items {
		kind, path := schema.Split(it.ID)
		keys := make([]string, 0, len(it.Attrs))
		for k := range it.Attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var attrs []string
		for _, k := range keys {
			attrs = append(attrs, k+"="+it.Attrs[k])
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", kind, path, strings.Join(attrs, " "))
	}
	tw.Flush()
	return core.ExitOK
}
