// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// Exit codes shared by every RadarModel command.
const (
	ExitOK       = 0 // scanned, nothing breaking
	ExitBreaking = 1 // at least one breaking change
	ExitError    = 2 // a source couldn't be scanned, or bad configuration
)

// SourceResult is what one scan of one source found.
type SourceResult struct {
	Source string `json:"source"`
	// Baseline is true when there was no earlier snapshot: this scan became
	// the baseline and nothing was compared.
	Baseline bool     `json:"baseline,omitempty"`
	Items    int      `json:"items"`
	Changes  []Change `json:"changes,omitempty"`
	// Error is set when the source couldn't be scanned. Its stored snapshot
	// is left as it was and no changes are reported for it.
	Error string `json:"error,omitempty"`
}

// Report is the result of one run over all configured sources.
type Report struct {
	Product string         `json:"product"`
	Version string         `json:"version"`
	At      time.Time      `json:"at"`
	Results []SourceResult `json:"results"`
}

// Max is the highest severity of any change in the report.
func (r *Report) Max() (Severity, bool) {
	max, any := Info, false
	for _, res := range r.Results {
		for _, c := range res.Changes {
			if !any || c.Severity > max {
				max = c.Severity
			}
			any = true
		}
	}
	return max, any
}

// Errors counts the sources that couldn't be scanned.
func (r *Report) Errors() int {
	n := 0
	for _, res := range r.Results {
		if res.Error != "" {
			n++
		}
	}
	return n
}

// ExitCode is 1 if anything is breaking, else 2 if any source failed, else 0.
func (r *Report) ExitCode() int {
	if m, ok := r.Max(); ok && m == Breaking {
		return ExitBreaking
	}
	if r.Errors() > 0 {
		return ExitError
	}
	return ExitOK
}

// Worth reports whether the report has anything at or above min, or an error:
// the test for whether outputs other than the terminal get it.
func (r *Report) Worth(min Severity) bool {
	if r.Errors() > 0 {
		return true
	}
	for _, res := range r.Results {
		for _, c := range res.Changes {
			if c.Severity >= min {
				return true
			}
		}
	}
	return false
}

// Filtered returns a copy holding only the changes at or above min.
func (r *Report) Filtered(min Severity) *Report {
	out := *r
	out.Results = make([]SourceResult, len(r.Results))
	for i, res := range r.Results {
		res.Changes = nil
		for _, c := range r.Results[i].Changes {
			if c.Severity >= min {
				res.Changes = append(res.Changes, c)
			}
		}
		out.Results[i] = res
	}
	return &out
}

// WriteJSON writes the report as indented JSON.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Describe is one line in plain words for a change.
func (c Change) Describe() string {
	var s string
	switch c.Kind {
	case Added:
		s = "new"
	case Removed:
		s = "gone"
	case Unlisted:
		s = "watched, but not in the list"
	default:
		switch {
		case c.Old == "":
			s = fmt.Sprintf("%s now %s", c.Field, c.New)
		case c.New == "":
			s = fmt.Sprintf("%s no longer listed (was %s)", c.Field, c.Old)
		default:
			s = fmt.Sprintf("%s %s -> %s", c.Field, c.Old, c.New)
		}
	}
	if c.Note != "" {
		s += " (" + c.Note + ")"
	}
	return s
}

// WriteText writes the report for a person at a terminal, showing changes at
// or above min. Sources with nothing to show get one summary line.
func (r *Report) WriteText(w io.Writer, min Severity) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, res := range r.Results {
		switch {
		case res.Error != "":
			fmt.Fprintf(tw, "%s\tERROR\t%s\n", res.Source, oneLine(res.Error))
		case res.Baseline:
			fmt.Fprintf(tw, "%s\tbaseline\t%d items recorded; changes are reported from the next scan\n", res.Source, res.Items)
		default:
			shown, hidden := 0, 0
			for _, c := range res.Changes {
				if c.Severity < min {
					hidden++
					continue
				}
				shown++
				id := c.ID
				if c.Watched {
					id += " *"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", res.Source, strings.ToUpper(c.Severity.String()), id, c.Describe())
			}
			if shown == 0 {
				msg := fmt.Sprintf("no changes (%d items)", res.Items)
				if hidden > 0 {
					msg = fmt.Sprintf("%d minor changes below %s (%d items)", hidden, min, res.Items)
				}
				fmt.Fprintf(tw, "%s\tok\t%s\n", res.Source, msg)
			} else if hidden > 0 {
				fmt.Fprintf(tw, "%s\t\t%d more below %s\n", res.Source, hidden, min)
			}
		}
	}
	return tw.Flush()
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}
