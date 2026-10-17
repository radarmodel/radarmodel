// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/radarmodel/radarmodel/core"
)

// WatchList holds the models an application uses, as "source:model" entries.
// The model part may use '*' to match any run of characters, '/' included,
// so "openrouter:anthropic/*" covers every Anthropic model on OpenRouter.
type WatchList struct {
	entries []watchEntry
}

type watchEntry struct {
	source, pattern string
	raw             string
}

// ParseWatch checks each entry against the configured source names, so a
// misspelt provider can't leave a model silently unwatched.
func ParseWatch(entries []string, sources map[string]bool) (WatchList, error) {
	var w WatchList
	for _, e := range entries {
		src, model, ok := strings.Cut(strings.TrimSpace(e), ":")
		if !ok || src == "" || model == "" {
			return w, fmt.Errorf("watch entry %q should be source:model, such as openai:gpt-4o", e)
		}
		if src != "*" && !sources[src] {
			return w, fmt.Errorf("watch entry %q names source %q, which isn't configured", e, src)
		}
		w.entries = append(w.entries, watchEntry{source: src, pattern: model, raw: e})
	}
	return w, nil
}

// Watched reports whether model id from source is watched.
func (w WatchList) Watched(source, id string) bool {
	for _, e := range w.entries {
		if (e.source == "*" || e.source == source) && glob(e.pattern, id) {
			return true
		}
	}
	return false
}

// exact returns the entries for source that name one model, no wildcards:
// the ones that can be checked for being missing from the list.
func (w WatchList) exact(source string) []string {
	var out []string
	for _, e := range w.entries {
		if e.source == source && !strings.Contains(e.pattern, "*") {
			out = append(out, e.pattern)
		}
	}
	return out
}

// glob matches s against a pattern in which '*' stands for any run of
// characters. Everything else matches itself.
func glob(pattern, s string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == s
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, last) && len(s) >= len(last)
}

// limitAttrs shrinking can break requests that used to fit.
var limitAttrs = map[string]bool{AttrContext: true, AttrMaxOutput: true, AttrInputLimit: true, AttrOutputLimit: true}

// priceAttrs going up costs money.
var priceAttrs = map[string]bool{AttrInputPrice: true, AttrOutputPrice: true}

// Classify sets the severity of each change. Only watched models can be
// breaking or warnings; changes to the rest of the list are info.
//
//   - a watched model gone from the list: breaking
//   - a watched model's context or output limit shrinking: breaking
//   - a watched model's limit no longer published: warning
//   - a watched model's price going up, or a price appearing or vanishing: warning
//   - a watched model getting or changing a retirement date: warning
//   - a watched model's lifecycle leaving "active": warning, "retired": breaking
//   - a watched model losing a supported method: warning
//   - anything else: info
func Classify(changes []core.Change, prev, next *core.Snapshot, w WatchList) {
	for i := range changes {
		c := &changes[i]
		c.Watched = w.Watched(c.Source, c.ID)
		c.Severity = core.Info
		if !c.Watched {
			continue
		}
		switch c.Kind {
		case core.Removed:
			c.Severity = core.Breaking
		case core.Changed:
			c.Severity = changeSeverity(c)
		}
	}
	// A removed model and a new one with the same display name usually mean
	// a rename; say so on the removal.
	added := map[string][]string{}
	nextIdx := next.Index()
	for _, c := range changes {
		if c.Kind == core.Added {
			if n := nextIdx[c.ID].Attrs[AttrName]; n != "" {
				added[n] = append(added[n], c.ID)
			}
		}
	}
	prevIdx := prev.Index()
	for i := range changes {
		c := &changes[i]
		if c.Kind != core.Removed {
			continue
		}
		if ids := added[prevIdx[c.ID].Attrs[AttrName]]; len(ids) > 0 && prevIdx[c.ID].Attrs[AttrName] != "" {
			c.Note = "possibly renamed to " + strings.Join(ids, " or ")
		}
	}
}

// Unlisted reports the exactly named watched models of a source that the
// latest list doesn't hold. They are warnings on every run until the model
// comes back or the watch entry is fixed: either it was retired, or the
// entry has a typo and was never watching anything.
func Unlisted(source string, next *core.Snapshot, w WatchList) []core.Change {
	idx := next.Index()
	var out []core.Change
	for _, id := range w.exact(source) {
		if _, ok := idx[id]; !ok {
			out = append(out, core.Change{Source: source, Kind: core.Unlisted, ID: id,
				Severity: core.Warning, Watched: true})
		}
	}
	return out
}

func changeSeverity(c *core.Change) core.Severity {
	switch {
	case limitAttrs[c.Field]:
		if c.New == "" {
			return core.Warning
		}
		if c.Old == "" {
			return core.Info
		}
		o, e1 := strconv.ParseFloat(c.Old, 64)
		n, e2 := strconv.ParseFloat(c.New, 64)
		if e1 != nil || e2 != nil {
			return core.Warning
		}
		if n < o {
			return core.Breaking
		}
		return core.Info
	case priceAttrs[c.Field]:
		if c.Old == "" || c.New == "" {
			return core.Warning
		}
		o, e1 := strconv.ParseFloat(c.Old, 64)
		n, e2 := strconv.ParseFloat(c.New, 64)
		if e1 != nil || e2 != nil || n > o {
			return core.Warning
		}
		return core.Info
	case c.Field == AttrLifecycle:
		switch c.New {
		case "", "active":
			return core.Info
		case "retired":
			c.Note = "retired"
			return core.Breaking
		default:
			c.Note = "no longer active"
			return core.Warning
		}
	case c.Field == AttrExpires:
		if c.New != "" {
			c.Note = "scheduled to be retired"
			return core.Warning
		}
		return core.Info
	case c.Field == AttrMethods:
		if lost := setMinus(c.Old, c.New); len(lost) > 0 {
			c.Note = "lost " + strings.Join(lost, ", ")
			return core.Warning
		}
		return core.Info
	}
	return core.Info
}

func setMinus(a, b string) []string {
	have := map[string]bool{}
	for _, s := range strings.Split(b, ",") {
		have[s] = true
	}
	var out []string
	for _, s := range strings.Split(a, ",") {
		if s != "" && !have[s] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
