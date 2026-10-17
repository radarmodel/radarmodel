// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/radarmodel/radarmodel"
	"github.com/radarmodel/radarmodel/core"
)

// Product is the name RadarModel AI puts on its reports.
const Product = "RadarModel AI"

// Config is the radarmodel-ai configuration file.
type Config struct {
	// StateDir holds the snapshots; ".radarmodel" when empty, relative to
	// the config file.
	StateDir string `json:"state_dir,omitempty"`
	// Interval is the time between scans in watch mode; 1h when empty.
	Interval core.Duration `json:"interval,omitempty"`
	// Timeout is the limit for one HTTP request; 30s when empty.
	Timeout core.Duration `json:"timeout,omitempty"`
	// MinFraction guards against half-empty answers: a list holding fewer
	// than this share of the models in the last one is treated as a failed
	// scan, not as models disappearing. 0.5 when unset; 0 turns it off.
	MinFraction *float64 `json:"min_fraction,omitempty"`
	// ConfirmRemovals holds back a watched model's removal until a second
	// scan agrees: the first scan that misses it reports a warning, the next
	// one that still misses it reports breaking. This keeps a list that came
	// back short without any visible error from paging anyone. True when
	// unset; false reports breaking on the first scan.
	ConfirmRemovals *bool `json:"confirm_removals,omitempty"`

	Providers []ProviderConfig `json:"providers"`
	// Watch lists the models the application uses, as source:model.
	Watch  []string          `json:"watch"`
	Alerts *core.AlertConfig `json:"alerts,omitempty"`
}

// Defaults applied by Validate.
const (
	DefaultInterval    = time.Hour
	DefaultTimeout     = 30 * time.Second
	DefaultMinFraction = 0.5
	DefaultStateDir    = ".radarmodel"
	minInterval        = time.Minute
)

// Validate fills in defaults and checks the configuration, returning the
// parsed watch list.
func (c *Config) Validate() (WatchList, error) {
	if c.StateDir == "" {
		c.StateDir = DefaultStateDir
	}
	if c.Interval == 0 {
		c.Interval = core.Duration(DefaultInterval)
	}
	if time.Duration(c.Interval) < minInterval {
		return WatchList{}, fmt.Errorf("interval must be at least %s", minInterval)
	}
	if c.Timeout == 0 {
		c.Timeout = core.Duration(DefaultTimeout)
	}
	if c.MinFraction == nil {
		v := DefaultMinFraction
		c.MinFraction = &v
	}
	if f := *c.MinFraction; f < 0 || f > 1 {
		return WatchList{}, fmt.Errorf("min_fraction must be between 0 and 1")
	}
	if len(c.Providers) == 0 {
		return WatchList{}, fmt.Errorf("no providers configured")
	}
	if c.ConfirmRemovals == nil {
		v := true
		c.ConfirmRemovals = &v
	}
	names := map[string]bool{}
	keys := map[string]string{}
	for i, p := range c.Providers {
		if _, ok := defaultBase[p.Type]; !ok {
			return WatchList{}, fmt.Errorf("providers[%d]: type %q is not one of openai, anthropic, google, openrouter", i, p.Type)
		}
		n := p.source()
		if names[n] {
			return WatchList{}, fmt.Errorf("providers[%d]: name %q is used twice; give one a different name", i, n)
		}
		// Names that differ only in case or punctuation would share a
		// snapshot file and report each other's models as removed.
		if other, ok := keys[core.FileKey(n)]; ok {
			return WatchList{}, fmt.Errorf("providers[%d]: name %q is too close to %q (they would share a snapshot file); pick a clearly different name", i, n, other)
		}
		names[n] = true
		keys[core.FileKey(n)] = n
	}
	return ParseWatch(c.Watch, names)
}

// Options control one run.
type Options struct {
	// DryRun compares with the stored snapshots but doesn't replace them.
	DryRun bool
	// Now returns the time stamped on snapshots; time.Now when nil.
	Now func() time.Time
	// Fetcher overrides the HTTP fetcher (tests use it to skip waits).
	Fetcher *core.Fetcher
}

// Run scans every provider once, compares each list with its stored
// snapshot, classifies the changes and, unless DryRun, stores the new
// snapshots. Providers are scanned in parallel; one failing doesn't stop the
// others, and its stored snapshot is left untouched.
func Run(ctx context.Context, cfg *Config, w WatchList, store *core.Store, opt Options) *core.Report {
	now := time.Now
	if opt.Now != nil {
		now = opt.Now
	}
	f := opt.Fetcher
	if f == nil {
		f = &core.Fetcher{Client: core.NewHTTPClient(time.Duration(cfg.Timeout))}
	}
	minFraction := DefaultMinFraction
	if cfg.MinFraction != nil {
		minFraction = *cfg.MinFraction
	}
	confirm := cfg.ConfirmRemovals == nil || *cfg.ConfirmRemovals
	rep := &core.Report{Product: Product, Version: radarmodel.Version, At: now().UTC(),
		Results: make([]core.SourceResult, len(cfg.Providers))}
	var wg sync.WaitGroup
	for i, p := range cfg.Providers {
		wg.Add(1)
		go func(i int, p ProviderConfig) {
			defer wg.Done()
			rep.Results[i] = scanOne(ctx, f, p, w, store, minFraction, confirm, now, opt.DryRun)
		}(i, p)
	}
	wg.Wait()
	return rep
}

// pendingAttr marks a watched model kept in the stored snapshot after one
// scan missed it, so the next scan can confirm the removal.
const pendingAttr = "radarmodel_pending_removal"

func scanOne(ctx context.Context, f *core.Fetcher, p ProviderConfig, w WatchList, store *core.Store,
	minFraction float64, confirm bool, now func() time.Time, dryRun bool) core.SourceResult {
	src := p.source()
	res := core.SourceResult{Source: src}
	fail := func(err error) core.SourceResult {
		res.Error = err.Error()
		res.Changes = nil
		return res
	}
	prev, err := store.Latest(src)
	if err != nil {
		return fail(err)
	}
	items, err := fetchItems(ctx, f, p)
	if err != nil {
		return fail(err)
	}
	next := &core.Snapshot{Source: src, Kind: "ai/" + p.Type, TakenAt: now().UTC(), Items: items}
	if err := next.Normalize(); err != nil {
		return fail(err)
	}
	res.Items = len(next.Items)
	if prev != nil && minFraction > 0 && float64(len(next.Items)) < minFraction*float64(len(prev.Items)) {
		return fail(fmt.Errorf("the list has %d models, down from %d; treated as a failed scan, not as removals (min_fraction %.2f). If the drop is real, run with min_fraction 0 once to accept it",
			len(next.Items), len(prev.Items), minFraction))
	}
	stored := &core.Snapshot{Source: next.Source, Kind: next.Kind, TakenAt: next.TakenAt,
		Items: append([]core.Item(nil), next.Items...)}
	removed := map[string]bool{}
	if prev == nil {
		res.Baseline = true
	} else {
		prev, pending := splitPending(prev)
		res.Changes = core.Diff(prev, next)
		Classify(res.Changes, prev, next, w)
		prevIdx := prev.Index()
		for i := range res.Changes {
			c := &res.Changes[i]
			if c.Kind != core.Removed {
				continue
			}
			removed[c.ID] = true
			if !c.Watched || !confirm {
				continue
			}
			if pending[c.ID] {
				c.Note = joinNote("confirmed by a second scan", c.Note)
				continue
			}
			c.Severity = core.Warning
			c.Note = joinNote("missing from this scan; breaking if the next scan misses it too", c.Note)
			held := prevIdx[c.ID]
			attrs := map[string]string{pendingAttr: "1"}
			for k, v := range held.Attrs {
				attrs[k] = v
			}
			stored.Items = append(stored.Items, core.Item{ID: held.ID, Attrs: attrs})
		}
		nextIdx := next.Index()
		for id := range pending {
			if _, ok := nextIdx[id]; ok {
				res.Changes = append(res.Changes, core.Change{Source: src, Kind: core.Added, ID: id,
					Severity: core.Info, Watched: w.Watched(src, id), Note: "back after one scan missed it"})
			}
		}
	}
	for _, c := range Unlisted(src, next, w) {
		if !removed[c.ID] {
			res.Changes = append(res.Changes, c)
		}
	}
	next = stored
	if !dryRun {
		if err := store.Save(next); err != nil {
			return fail(fmt.Errorf("saving the snapshot: %v", err))
		}
	}
	return res
}

// splitPending returns a copy of s without the pending-removal marks, and
// the IDs that carried one.
func splitPending(s *core.Snapshot) (*core.Snapshot, map[string]bool) {
	pending := map[string]bool{}
	out := &core.Snapshot{Source: s.Source, Kind: s.Kind, TakenAt: s.TakenAt}
	for _, it := range s.Items {
		if it.Attrs[pendingAttr] != "" {
			pending[it.ID] = true
			attrs := map[string]string{}
			for k, v := range it.Attrs {
				if k != pendingAttr {
					attrs[k] = v
				}
			}
			it = core.Item{ID: it.ID, Attrs: attrs}
		}
		out.Items = append(out.Items, it)
	}
	return out, pending
}

func joinNote(a, b string) string {
	if b == "" {
		return a
	}
	return a + "; " + b
}
