// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/radarmodel/radarmodel/ai"
	"github.com/radarmodel/radarmodel/core"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func cmdScan(args []string, stdout, stderr io.Writer) int {
	fs, cfgPath := newFlags("scan", stderr)
	write := fs.Bool("write", false, "add the models found to the watch list")
	check := fs.Bool("check", false, "exit 1 when the watch list misses a model the code uses")
	asJSON := fs.Bool("json", false, "print JSON")
	var exclude multiFlag
	fs.Var(&exclude, "exclude", "leave out matching paths")
	if err := fs.Parse(args); err != nil {
		return core.ExitError
	}
	roots := fs.Args()
	if len(roots) == 0 {
		roots = []string{"."}
	}

	// Without a config, every provider counts as configured under its own
	// name, so the findings say which providers to add.
	var cfg *ai.Config
	var w ai.WatchList
	var store *core.Store
	providers := []ai.ProviderConfig{{Type: ai.OpenAI}, {Type: ai.Anthropic}, {Type: ai.Google}, {Type: ai.OpenRouter}}
	haveConfig := true
	if _, err := os.Stat(*cfgPath); errors.Is(err, os.ErrNotExist) {
		if _, lerr := os.Lstat(*cfgPath); lerr == nil {
			fmt.Fprintf(stderr, "%s is a link to a file that doesn't exist\n", *cfgPath)
			return core.ExitError
		}
		haveConfig = false
	} else {
		c, watch, st, err := load(*cfgPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return core.ExitError
		}
		cfg, w, store, providers = c, watch, st, c.Providers
	}

	// The config's own watch list and the stored snapshots name models too.
	skip := []string{*cfgPath}
	if store != nil {
		skip = append(skip, store.Dir)
	}
	res, err := ai.Scan(roots, providers, w, store, ai.ScanOptions{Exclude: exclude, SkipPaths: skip})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return core.ExitError
	}

	// -check fails on models with no source or missing from their source's
	// list, and on unwatched ones unless -write has just added them.
	var add []string
	problems := 0
	for _, f := range res.Found {
		if f.Source == "" || f.Unlisted {
			problems++
		}
		if f.Source != "" && !f.Watched {
			add = append(add, f.Entry())
		}
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if res.Found == nil {
			res.Found = []ai.Found{}
		}
		enc.Encode(res)
	} else {
		printScan(stdout, res, haveConfig)
	}

	if *write && len(add) > 0 {
		if err := writeWatch(*cfgPath, cfg, haveConfig, res, add); err != nil {
			fmt.Fprintln(stderr, err)
			return core.ExitError
		}
		verb := "added to the watch list in"
		if !haveConfig {
			verb = "written to a new"
		}
		fmt.Fprintf(stderr, "%d %s %s %s\n", len(add), plural(len(add), "model", "models"), verb, *cfgPath)
	}
	if !*write {
		problems += len(add)
	}
	if *check && problems > 0 {
		return core.ExitBreaking
	}
	return core.ExitOK
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func printScan(out io.Writer, res ai.ScanResult, haveConfig bool) {
	fmt.Fprintf(out, "%d %s read, %d %s found\n", res.Files, plural(res.Files, "file", "files"), len(res.Found), plural(len(res.Found), "model", "models"))
	if len(res.Found) == 0 {
		return
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for _, f := range res.Found {
		status := "new"
		switch {
		case f.Source == "":
			status = "no " + f.Provider + " provider configured"
		case f.Unlisted:
			status = fmt.Sprintf("not in %s's list (%s)", f.Source, f.ListedAt.Format("2006-01-02"))
		case f.Watched:
			status = "watched"
		case !haveConfig:
			status = "found"
		}
		entry := f.Entry()
		if f.Source == "" {
			entry = f.Provider + ":" + f.ID
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", entry, status, where(f.Locations))
	}
	tw.Flush()
}

func where(locs []ai.Location) string {
	var parts []string
	for i, l := range locs {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("+%d more", len(locs)-3))
			break
		}
		parts = append(parts, fmt.Sprintf("%s:%d", l.File, l.Line))
	}
	return strings.Join(parts, ", ")
}

// writeWatch adds entries to the config's watch list, keeping every other
// part of the file and every existing entry. Without a config it writes a
// new one with the providers the findings need.
func writeWatch(path string, cfg *ai.Config, haveConfig bool, res ai.ScanResult, add []string) error {
	if !haveConfig {
		used := map[string]bool{}
		for _, f := range res.Found {
			if f.Source != "" {
				used[f.Provider] = true
			}
		}
		var providers []string
		for _, t := range []string{ai.OpenAI, ai.Anthropic, ai.Google, ai.OpenRouter} {
			if used[t] {
				providers = append(providers, fmt.Sprintf(`{"type": %q}`, t))
			}
		}
		raw := fmt.Sprintf("{\n  \"interval\": \"1h\",\n  \"providers\": [%s],\n  \"watch\": [],\n  \"alerts\": {\"min_severity\": \"warning\"}\n}\n", strings.Join(providers, ", "))
		out, err := setWatch([]byte(raw), add)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		if _, err := f.Write(out); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	// A config that is a link is rewritten where it points, so the link
	// stays and the file it names gets the new list.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	watch := append(append([]string(nil), cfg.Watch...), add...)
	out, err := setWatch(raw, watch)
	if err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	// Check the result still loads before replacing the file.
	var check ai.Config
	if err := json.Unmarshal(out, &check); err != nil {
		return fmt.Errorf("%s: rewritten config doesn't parse: %v", path, err)
	}
	if _, err := check.Validate(); err != nil {
		return fmt.Errorf("%s: rewritten config is invalid: %v", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".radarmodel-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if info, err := os.Stat(path); err == nil {
		tmp.Chmod(info.Mode().Perm())
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// setWatch replaces the top-level "watch" array of a JSON object, keeping
// the other keys in their order, and writes the result with two-space
// indentation and one watch entry per line.
func setWatch(raw []byte, watch []string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, errors.New("config is not a JSON object")
	}
	type kv struct {
		key string
		val json.RawMessage
	}
	var fields []kv
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		fields = append(fields, kv{key, val})
	}
	var w bytes.Buffer
	if len(watch) == 0 {
		w.WriteString("[]")
	} else {
		w.WriteString("[\n")
		for i, e := range watch {
			b, _ := json.Marshal(e)
			w.WriteString("    ")
			w.Write(b)
			if i < len(watch)-1 {
				w.WriteByte(',')
			}
			w.WriteByte('\n')
		}
		w.WriteString("  ]")
	}
	replaced := false
	var out bytes.Buffer
	out.WriteString("{\n")
	for i, f := range fields {
		val := f.val
		if f.key == "watch" {
			val = w.Bytes()
			replaced = true
		} else {
			var ind bytes.Buffer
			if err := json.Indent(&ind, f.val, "  ", "  "); err != nil {
				return nil, err
			}
			val = ind.Bytes()
		}
		k, _ := json.Marshal(f.key)
		fmt.Fprintf(&out, "  %s: %s", k, val)
		if i < len(fields)-1 || !replaced && i == len(fields)-1 {
			out.WriteByte(',')
		}
		out.WriteByte('\n')
	}
	if !replaced {
		fmt.Fprintf(&out, "  \"watch\": %s\n", w.Bytes())
	}
	out.WriteString("}\n")
	return out.Bytes(), nil
}
