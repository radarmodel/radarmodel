// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// MarkdownMarker starts every Markdown report, so a pull request or issue
// comment written from one can be found and updated instead of repeated.
const MarkdownMarker = "<!-- radarmodel-report -->"

// WriteMarkdown writes the report as GitHub-flavoured Markdown, for a job
// summary or a pull request comment: a headline, then one table of the
// changes at or above min, then sources that failed or became a baseline.
// Text from providers and databases is escaped, so a model or column name
// can't break the table or ping anyone.
func (r *Report) WriteMarkdown(w io.Writer, min Severity) error {
	var b strings.Builder
	b.WriteString(MarkdownMarker + "\n")
	fmt.Fprintf(&b, "### %s\n\n", mdText(Headline(r, min)))
	shown, hidden := 0, 0
	var rows strings.Builder
	for _, res := range r.Results {
		for _, c := range res.Changes {
			if c.Severity < min {
				hidden++
				continue
			}
			shown++
			id := mdCode(c.ID)
			if c.Watched {
				id += " (watched)"
			}
			fmt.Fprintf(&rows, "| %s | **%s** | %s | %s |\n", mdText(res.Source), strings.ToUpper(c.Severity.String()), id, mdText(c.Describe()))
		}
	}
	if shown > 0 {
		b.WriteString("| Source | Severity | Item | Change |\n|---|---|---|---|\n")
		b.WriteString(rows.String())
		b.WriteString("\n")
	} else {
		fmt.Fprintf(&b, "No changes at %s or above.\n\n", min)
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "%d more %s below %s.\n\n", hidden, plural(hidden, "change", "changes"), min)
	}
	for _, res := range r.Results {
		switch {
		case res.Error != "":
			fmt.Fprintf(&b, "- **%s** couldn't be scanned: %s. Its stored snapshot is unchanged.\n", mdText(res.Source), mdText(oneLine(res.Error)))
		case res.Baseline:
			fmt.Fprintf(&b, "- **%s**: baseline of %d items recorded; changes are reported from the next scan.\n", mdText(res.Source), res.Items)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// mdText escapes what Markdown or GitHub would act on in a table cell:
// pipes, emphasis, links, HTML, mentions and line breaks.
func mdText(s string) string {
	s = oneLine(s)
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '|', '*', '_', '`', '[', ']', '<', '>', '#', '!', '~', '&':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '@':
			b.WriteString("@​") // no mention
		case '\r', '\n', '\t':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mdCode puts s in a code span that its own backticks can't close: the
// fence is one backtick longer than the longest run in s.
func mdCode(s string) string {
	s = strings.ReplaceAll(oneLine(s), "|", "\\|")
	longest, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	if longest == 0 {
		return "`" + s + "`"
	}
	fence := strings.Repeat("`", longest+1)
	return fence + " " + s + " " + fence
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// WriteMarkdownFile writes the Markdown report to path, replacing it.
func WriteMarkdownFile(path string, r *Report, min Severity) error {
	var b strings.Builder
	if err := r.WriteMarkdown(&b, min); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
