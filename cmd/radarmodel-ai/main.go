// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

// Command radarmodel-ai watches the model lists of AI providers and reports
// when a model an application uses disappears, gets renamed, or changes its
// limits or prices.
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
	"github.com/radarmodel/radarmodel/ai"
	"github.com/radarmodel/radarmodel/core"
)

const usage = `radarmodel-ai watches the AI models your app depends on.

Usage:
  radarmodel-ai init  [-config FILE]            write a starter config
  radarmodel-ai check [-config FILE] [flags]    scan once, compare, report
  radarmodel-ai watch [-config FILE] [flags]    scan on an interval until stopped
  radarmodel-ai show  [-config FILE] SOURCE     list the stored models of a source
  radarmodel-ai version

The config file is radarmodel.json, or $RADARMODEL_CONFIG when set.

Flags for check and watch:
  -min LEVEL     lowest severity shown at the terminal: info, warning (default), breaking
  -json          print the report as JSON
  -no-alerts     don't send webhook, Slack or email alerts
  -dry-run       compare but keep the stored snapshots (check only)

Exit codes for check: 0 nothing breaking, 1 a breaking change,
2 a provider couldn't be scanned or the config is wrong.
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
		fmt.Fprintf(stdout, "radarmodel-ai %s\n", radarmodel.Version)
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
	case "show":
		return cmdShow(rest, stdout, stderr)
	}
	fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
	return core.ExitError
}

func defaultConfig() string {
	if v := os.Getenv("RADARMODEL_CONFIG"); v != "" {
		return v
	}
	return "radarmodel.json"
}

func newFlags(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	cfg := fs.String("config", defaultConfig(), "config file")
	return fs, cfg
}

// load reads and checks the config. The state folder is resolved against the
// config file's folder, so the command finds the same snapshots wherever it
// is started from.
func load(path string) (*ai.Config, ai.WatchList, *core.Store, error) {
	var cfg ai.Config
	if err := core.LoadJSON(path, &cfg); err != nil {
		return nil, ai.WatchList{}, nil, err
	}
	w, err := cfg.Validate()
	if err != nil {
		return nil, ai.WatchList{}, nil, fmt.Errorf("%s: %v", path, err)
	}
	dir := cfg.StateDir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(path), dir)
	}
	return &cfg, w, &core.Store{Dir: dir}, nil
}

const starter = `{
  "interval": "1h",
  "providers": [
    {"type": "openai"},
    {"type": "anthropic"},
    {"type": "google"},
    {"type": "openrouter"}
  ],
  "watch": [
    "openai:gpt-4o",
    "anthropic:claude-sonnet-4-5",
    "google:gemini-2.5-flash",
    "openrouter:anthropic/*"
  ],
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
	if _, err := f.WriteString(starter); err != nil {
		f.Close()
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	if err := f.Close(); err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	fmt.Fprintf(stdout, "wrote %s\n", *cfg)
	fmt.Fprintln(stdout, "Put the models your app calls under \"watch\", set OPENAI_API_KEY, ANTHROPIC_API_KEY and GEMINI_API_KEY")
	fmt.Fprintln(stdout, "(or remove the providers you don't use), then run: radarmodel-ai check")
	return core.ExitOK
}

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
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "%s takes no arguments, got %q\n", name, fs.Args())
		return core.ExitError
	}
	show, err := core.ParseSeverity(*minFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	cfg, w, store, err := load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	var outs []core.Output
	alertMin := core.Warning
	if !*noAlerts {
		outs, alertMin, err = cfg.Alerts.Outputs(core.NewHTTPClient(time.Duration(cfg.Timeout)))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return core.ExitError
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	once := func(sent map[string]bool) int {
		rep := ai.Run(ctx, cfg, w, store, ai.Options{DryRun: dryRun})
		if *asJSON {
			rep.WriteJSON(stdout)
		} else {
			fmt.Fprintf(stdout, "%s  %s\n", rep.At.Format(time.RFC3339), core.Headline(rep, show))
			rep.WriteText(stdout, show)
		}
		alert := rep
		if sent != nil {
			alert = dropRepeats(rep, sent)
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
	fmt.Fprintf(stderr, "watching %d providers every %s; Ctrl-C to stop\n", len(cfg.Providers), interval)
	sent := map[string]bool{}
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

// dropRepeats keeps the watch loop from alerting about the same missing
// watched model every interval: an "unlisted" warning is sent once per run of
// the command. It stays in the terminal report every time.
func dropRepeats(r *core.Report, sent map[string]bool) *core.Report {
	out := *r
	out.Results = make([]core.SourceResult, len(r.Results))
	for i, res := range r.Results {
		var keep []core.Change
		for _, c := range res.Changes {
			if c.Kind == core.Unlisted {
				key := c.Source + "\x00" + c.ID
				if sent[key] {
					continue
				}
				sent[key] = true
			}
			keep = append(keep, c)
		}
		res.Changes = keep
		out.Results[i] = res
	}
	return &out
}

func cmdShow(args []string, stdout, stderr io.Writer) int {
	fs, cfgPath := newFlags("show", stderr)
	if err := fs.Parse(args); err != nil {
		return core.ExitError
	}
	cfg, w, store, err := load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	if fs.NArg() != 1 {
		var names []string
		for _, p := range cfg.Providers {
			n := p.Name
			if n == "" {
				n = p.Type
			}
			names = append(names, n)
		}
		fmt.Fprintf(stderr, "show needs one source name: %s\n", strings.Join(names, ", "))
		return core.ExitError
	}
	src := fs.Arg(0)
	s, err := store.Latest(src)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}
	if s == nil {
		fmt.Fprintf(stderr, "no snapshot of %s yet; run radarmodel-ai check first\n", src)
		return core.ExitError
	}
	fmt.Fprintf(stdout, "%s: %d models, scanned %s (* = watched)\n", s.Source, len(s.Items), s.TakenAt.Format(time.RFC3339))
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	for _, it := range s.Items {
		mark := ""
		if w.Watched(src, it.ID) {
			mark = "*"
		}
		keys := make([]string, 0, len(it.Attrs))
		for k := range it.Attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var attrs []string
		for _, k := range keys {
			attrs = append(attrs, k+"="+it.Attrs[k])
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", mark, it.ID, strings.Join(attrs, " "))
	}
	tw.Flush()
	return core.ExitOK
}
