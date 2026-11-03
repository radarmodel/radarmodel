// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/radarmodel/radarmodel/core"
)

// The scan reads string literals in code and values in config files, never
// comments or prose: a model named in a comment or a README isn't one the
// application calls, and watching it would report its retirement as
// breaking.

// ScanOptions control a code scan.
type ScanOptions struct {
	// Exclude holds glob patterns of paths (relative to the scanned folder,
	// with forward slashes) to leave out, besides the folders always skipped.
	Exclude []string
	// MaxFileBytes skips larger files; 0 means 2 MiB.
	MaxFileBytes int64
}

// Location is where a model name was found.
type Location struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Found is one model the code uses.
type Found struct {
	// Source is the configured source it belongs to; empty when no source of
	// its provider is configured.
	Source string `json:"source,omitempty"`
	// Provider is the provider type that serves it.
	Provider string `json:"provider"`
	// ID is the model ID as the source's list names it.
	ID string `json:"id"`
	// Watched is true when the watch list already covers it.
	Watched bool `json:"watched"`
	// Unlisted is true when the source's last stored snapshot doesn't list
	// it: a retired model, a typo, or a name the provider never had.
	Unlisted bool `json:"unlisted,omitempty"`
	// ListedAt is when that snapshot was taken.
	ListedAt  *time.Time `json:"listed_at,omitempty"`
	Locations []Location `json:"locations"`
}

// Entry is the watch entry that would cover it.
func (f Found) Entry() string { return f.Source + ":" + f.ID }

// ScanResult is what a code scan found.
type ScanResult struct {
	Files int     `json:"files_read"`
	Found []Found `json:"models"`
	// Skipped counts files left out for their size or binary content.
	Skipped int `json:"files_skipped"`
}

// skipDirs are folders that hold dependencies, builds or tools' state, not
// the application's own code.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, "out": true,
	"__pycache__": true, "venv": true, "env": true, "site-packages": true, "bower_components": true,
	"coverage": true, "third_party": true, "Pods": true,
}

// lockFiles name dependencies, which can look like model names.
var lockFiles = map[string]bool{
	"package-lock.json": true, "npm-shrinkwrap.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"poetry.lock": true, "uv.lock": true, "Pipfile.lock": true, "Cargo.lock": true, "go.sum": true,
	"composer.lock": true, "Gemfile.lock": true, "bun.lockb": true,
}

var codeExt = map[string]bool{
	".py": true, ".js": true, ".mjs": true, ".cjs": true, ".jsx": true, ".ts": true, ".tsx": true, ".mts": true, ".cts": true,
	".go": true, ".rb": true, ".java": true, ".kt": true, ".kts": true, ".scala": true, ".cs": true, ".rs": true,
	".php": true, ".swift": true, ".dart": true, ".ex": true, ".exs": true, ".clj": true, ".lua": true,
	".sh": true, ".bash": true, ".vue": true, ".svelte": true, ".json": true, ".jsonc": true,
}

var configExt = map[string]bool{
	".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".cfg": true, ".conf": true, ".properties": true, ".env": true,
}

// fileKind says how a file is read: "code" (string literals), "config"
// (string literals and key: value / key=value values), or "" (not read).
func fileKind(name string) string {
	base := path.Base(name)
	if lockFiles[base] {
		return ""
	}
	if base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".env") {
		return "config"
	}
	ext := strings.ToLower(path.Ext(base))
	switch {
	case configExt[ext]:
		return "config"
	case codeExt[ext]:
		return "code"
	}
	return ""
}

// Scan reads the files under roots and returns the model names they use,
// assigned to the configured sources. The watch list marks models already
// covered; store, when not nil, supplies each source's last snapshot to mark
// models its list doesn't have.
func Scan(roots []string, providers []ProviderConfig, w WatchList, store *core.Store, opt ScanOptions) (ScanResult, error) {
	max := opt.MaxFileBytes
	if max <= 0 {
		max = 2 << 20
	}
	for _, p := range opt.Exclude {
		if _, err := path.Match(p, ""); err != nil {
			return ScanResult{}, fmt.Errorf("exclude pattern %q: %v", p, err)
		}
	}
	var res ScanResult
	found := map[string]*Found{}
	add := func(c Candidate, loc Location) {
		source, provider, id := assign(c, providers)
		key := source + "\x00" + provider + "\x00" + id
		f := found[key]
		if f == nil {
			f = &Found{Source: source, Provider: provider, ID: id}
			found[key] = f
		}
		f.Locations = append(f.Locations, loc)
	}
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return res, err
		}
		if !info.IsDir() {
			return res, fmt.Errorf("%s is not a folder", root)
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if excluded(rel, opt.Exclude) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if p != root && (skipDirs[name] || (strings.HasPrefix(name, ".") && name != ".github")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() { // links are not followed
				return nil
			}
			kind := fileKind(rel)
			if kind == "" {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			if fi.Size() > max {
				res.Skipped++
				return nil
			}
			display := rel
			if len(roots) > 1 {
				display = filepath.ToSlash(filepath.Join(root, rel))
			}
			read, err := scanFile(p, kind, func(s string, line int) {
				if c, ok := RecognizeModel(s); ok {
					add(c, Location{File: display, Line: line})
				}
			})
			if err != nil {
				return err
			}
			if read {
				res.Files++
			} else {
				res.Skipped++
			}
			return nil
		})
		if err != nil {
			return res, err
		}
	}
	snapshots := map[string]*core.Snapshot{}
	for _, f := range found {
		if f.Source != "" {
			f.Watched = w.Watched(f.Source, f.ID)
			if store != nil {
				s, ok := snapshots[f.Source]
				if !ok {
					s, _ = store.Latest(f.Source) // no or unreadable snapshot: nothing to compare with
					snapshots[f.Source] = s
				}
				if s != nil && !listed(s, f.ID) {
					f.Unlisted = true
					at := s.TakenAt
					f.ListedAt = &at
				}
			}
		}
		sort.Slice(f.Locations, func(i, j int) bool {
			a, b := f.Locations[i], f.Locations[j]
			return a.File < b.File || (a.File == b.File && a.Line < b.Line)
		})
		res.Found = append(res.Found, *f)
	}
	sort.Slice(res.Found, func(i, j int) bool {
		a, b := res.Found[i], res.Found[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.ID < b.ID
	})
	return res, nil
}

func listed(s *core.Snapshot, id string) bool {
	for _, it := range s.Items {
		if it.ID == id && it.Attrs[pendingAttr] == "" {
			return true
		}
	}
	return false
}

func excluded(rel string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
		if ok, _ := path.Match(p, path.Base(rel)); ok {
			return true
		}
	}
	return false
}

// assign picks the source a candidate belongs to. An ID of one provider goes
// to the first source of that type; a vendor/model ID goes to the first
// OpenRouter source, else to the vendor's own API when that is configured.
// With no fitting source, source is empty and provider says what to add.
func assign(c Candidate, providers []ProviderConfig) (source, provider, id string) {
	first := func(typ string) string {
		for _, p := range providers {
			if p.Type == typ {
				return p.source()
			}
		}
		return ""
	}
	if c.Provider == OpenRouter {
		if c.Vendor == "gemini" { // LiteLLM's gemini/ prefix: Google's own API
			if c.VendorProvider == "" {
				return "", Google, strings.TrimPrefix(c.ID, "gemini/")
			}
			return first(Google), Google, c.VendorID
		}
		if s := first(OpenRouter); s != "" {
			return s, OpenRouter, c.ID
		}
		if c.VendorProvider != "" {
			if s := first(c.VendorProvider); s != "" {
				return s, c.VendorProvider, c.VendorID
			}
		}
		return "", OpenRouter, c.ID
	}
	return first(c.Provider), c.Provider, c.ID
}

// scanFile calls found for every string literal (and, in config files,
// every unquoted value) on each line that isn't a comment. read is false for
// a binary file.
func scanFile(name, kind string, found func(s string, line int)) (read bool, err error) {
	f, err := os.Open(name)
	if err != nil {
		return false, err
	}
	defer f.Close()
	head := make([]byte, 8192)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, err
	}
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return false, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		trimmed := strings.TrimSpace(text)
		if commentLine(trimmed, kind) {
			continue
		}
		for _, s := range quoted(text) {
			found(s, line)
		}
		switch kind {
		case "config":
			if v, ok := configValue(trimmed); ok {
				found(v, line)
			}
		case "code":
			// A line that is nothing but a model ID: one entry of a list
			// written as a multi-line string.
			if !strings.ContainsAny(trimmed, " \t\"'`") {
				found(trimmed, line)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return true, fmt.Errorf("%s: %v", name, err)
	}
	return true, nil
}

func commentLine(t, kind string) bool {
	for _, p := range []string{"//", "/*", "* ", "#", "--", "<!--"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	if t == "*" || (kind == "config" && strings.HasPrefix(t, ";")) {
		return true
	}
	return false
}

// quoted returns the contents of the quoted strings on a line: "...",
// '...' and `...`, with backslash escapes skipped. A quote left open runs to
// the end of the line and is dropped.
func quoted(line string) []string {
	var out []string
	prefixCheck := false
	for i := 0; i < len(line); i++ {
		q := line[i]
		if q != '"' && q != '\'' && q != '`' {
			continue
		}
		// An apostrophe inside a word (don't) doesn't start a string.
		if q == '\'' && i > 0 && isWordByte(line[i-1]) {
			continue
		}
		j := i + 1
		for j < len(line) && line[j] != q {
			if line[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(line) {
			return out
		}
		// Strings tested as prefixes or substrings of a model name
		// (startswith("gemini-2.5"), "o4" in name) are families, not models.
		if !prefixCheck && prefixTest.MatchString(line[:i]) {
			prefixCheck = true
		}
		// The text replaced in a name (s.replace("gpt_4o", "gpt-4o")) is a
		// fragment; what it becomes can be a model, so only the first
		// argument is skipped.
		replaced := replaceArg.MatchString(line[:i])
		if !prefixCheck && !replaced && !substringTest.MatchString(line[j+1:]) {
			out = append(out, line[i+1:j])
		}
		i = j
	}
	return out
}

// prefixTest matches the text before a string that is used as a prefix or
// substring test; every later string on the line is part of the same test.
var prefixTest = regexp.MustCompile(`(?i)(starts_?with|ends_?with|hasprefix|hassuffix|has_prefix|includes|contains|index_?of|search|match)\s*\(\s*\(?\s*$`)

var replaceArg = regexp.MustCompile(`(?i)replace(all)?\s*\(\s*$`)

// substringTest matches what follows a string tested as part of a name
// ("gemini-2.5" in model). Membership in a list of whole names
// (name in ("gpt-5", "gpt-5-mini")) is an exact comparison and is kept.
var substringTest = regexp.MustCompile(`^\s*(not\s+)?in\s+[A-Za-z_]`)

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// configValue reads the value of a "key: value", "key = value" or
// "- value" line, without quotes or a trailing comment.
func configValue(t string) (string, bool) {
	var v string
	if strings.HasPrefix(t, "- ") {
		t = strings.TrimSpace(t[2:])
		if !strings.ContainsAny(t, ":=") || strings.Contains(t, "://") {
			v = t
		}
	}
	switch {
	case v != "":
	default:
		i := strings.IndexAny(t, ":=")
		if i <= 0 {
			return "", false
		}
		v = t[i+1:]
		if strings.Contains(v, "://") { // a URL, not a model
			return "", false
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	v = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(v), ","))
	v = strings.Trim(v, `"'`)
	return v, v != ""
}
