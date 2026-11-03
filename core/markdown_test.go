// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"strings"
	"testing"
)

func TestReportMarkdown(t *testing.T) {
	r := &Report{Product: "RadarModel AI", Results: []SourceResult{
		{Source: "openai", Items: 5, Changes: []Change{
			{Source: "openai", Kind: Removed, ID: "gpt-4o", Severity: Breaking, Watched: true, Note: "confirmed by a second scan"},
			{Source: "openai", Kind: Added, ID: "gpt-5", Severity: Info},
		}},
		{Source: "evil|src", Items: 1, Changes: []Change{
			{Kind: Changed, ID: "x|y`z", Field: "name", Old: "a", New: "@team <img> [l](u) *b*\nnext", Severity: Warning, Watched: true},
		}},
		{Source: "groq", Baseline: true, Items: 3},
		{Source: "bedrock", Error: "GET https://bedrock... returned 403"},
	}}
	var b bytes.Buffer
	if err := r.WriteMarkdown(&b, Warning); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{MarkdownMarker, "### RadarModel AI: 1 breaking, 1 warning", "| openai | **BREAKING** | `gpt-4o` (watched) | gone (confirmed by a second scan) |",
		"evil\\|src", "``", "@​team", "\\<img\\>", "\\[l\\](u)", "1 more change below warning.",
		"**groq**: baseline of 3 items", "**bedrock** couldn't be scanned"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// Every table row has exactly four cells: nothing from a name broke it.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "| ") && !strings.HasPrefix(line, "| Source") {
			if n := strings.Count(strings.ReplaceAll(line, "\\|", ""), "|"); n != 5 {
				t.Errorf("row with %d cell borders: %s", n, line)
			}
		}
	}
	if strings.Contains(out, "gpt-5") {
		t.Errorf("info change shown:\n%s", out)
	}
	// Names can't escape their code span or cell, or mention anyone (0.5 review).
	b.Reset()
	(&Report{Product: "P", Results: []SourceResult{{Source: "db", Items: 1, Changes: []Change{
		{Kind: Removed, ID: "a`` @octocat ``b", Severity: Breaking, Watched: true},
		{Kind: Removed, ID: "x\r| @y", Severity: Breaking, Watched: true},
		{Kind: Changed, ID: "z", Field: "name", Old: "a", New: "&#64;org/team $x$", Severity: Warning, Watched: true},
	}}}}).WriteMarkdown(&b, Warning)
	out = b.String()
	if !strings.Contains(out, "``` a`` @octocat ``b ```") || strings.Contains(out, "\r") || !strings.Contains(out, `\&\#64;org`) || !strings.Contains(out, `\$x\$`) {
		t.Errorf("escaping:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "| db") {
			if n := strings.Count(strings.ReplaceAll(line, "\\|", ""), "|"); n != 5 {
				t.Errorf("row with %d cell borders: %s", n, line)
			}
		}
	}
	if got := oneLine(strings.Repeat("é", 200)); !strings.HasSuffix(got, "é...") {
		t.Errorf("cut inside a character: %q", got[len(got)-6:])
	}
	b.Reset()
	(&Report{Product: "P", Results: []SourceResult{{Source: "a", Items: 2}}}).WriteMarkdown(&b, Warning)
	if !strings.Contains(b.String(), "No changes at warning or above.") {
		t.Errorf("quiet report:\n%s", b.String())
	}
}
