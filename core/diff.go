// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Severity ranks a change by what it can do to the application.
type Severity int

const (
	// Info is worth knowing and needs nothing done.
	Info Severity = iota
	// Warning may need attention: a price went up, a limit changed shape.
	Warning
	// Breaking can make the application fail: something it uses is gone or
	// shrank.
	Breaking
)

func (s Severity) String() string {
	switch s {
	case Breaking:
		return "breaking"
	case Warning:
		return "warning"
	default:
		return "info"
	}
}

// ParseSeverity reads "info", "warning" or "breaking".
func ParseSeverity(v string) (Severity, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "info":
		return Info, nil
	case "warning":
		return Warning, nil
	case "breaking":
		return Breaking, nil
	}
	return Info, fmt.Errorf("unknown severity %q (want info, warning or breaking)", v)
}

// MarshalJSON writes the severity as its name.
func (s Severity) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// UnmarshalJSON reads the severity from its name.
func (s *Severity) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	p, err := ParseSeverity(v)
	if err != nil {
		return err
	}
	*s = p
	return nil
}

// ChangeKind says what happened to an item.
type ChangeKind string

const (
	Added   ChangeKind = "added"
	Removed ChangeKind = "removed"
	Changed ChangeKind = "changed"
	// Unlisted is not a difference between snapshots: it marks something the
	// configuration names as watched that the latest scan didn't find.
	Unlisted ChangeKind = "unlisted"
)

// Change is one difference between two snapshots of a source. For Changed,
// Field names the attribute and Old and New hold its values; an attribute
// that appeared or vanished has an empty Old or New.
type Change struct {
	Source   string     `json:"source"`
	Kind     ChangeKind `json:"kind"`
	ID       string     `json:"id"`
	Field    string     `json:"field,omitempty"`
	Old      string     `json:"old,omitempty"`
	New      string     `json:"new,omitempty"`
	Severity Severity   `json:"severity"`
	Watched  bool       `json:"watched"`
	Note     string     `json:"note,omitempty"`
}

// Diff lists every difference between old and new, ordered by item ID and
// then field. Every change starts as Info; the product's classifier raises
// the ones that matter. Both snapshots must be normalized.
func Diff(old, new *Snapshot) []Change {
	oi, ni := old.Index(), new.Index()
	var out []Change
	for _, it := range old.Items {
		if _, ok := ni[it.ID]; !ok {
			out = append(out, Change{Source: new.Source, Kind: Removed, ID: it.ID})
		}
	}
	for _, it := range new.Items {
		prev, ok := oi[it.ID]
		if !ok {
			out = append(out, Change{Source: new.Source, Kind: Added, ID: it.ID})
			continue
		}
		fields := map[string]bool{}
		for k := range prev.Attrs {
			fields[k] = true
		}
		for k := range it.Attrs {
			fields[k] = true
		}
		names := make([]string, 0, len(fields))
		for k := range fields {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, f := range names {
			if prev.Attrs[f] != it.Attrs[f] {
				out = append(out, Change{Source: new.Source, Kind: Changed, ID: it.ID,
					Field: f, Old: prev.Attrs[f], New: it.Attrs[f]})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Field < out[j].Field
	})
	return out
}
