// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Store keeps snapshots as plain JSON files:
//
//	DIR/latest/SOURCE.json            the snapshot the next scan is compared with
//	DIR/history/SOURCE/STAMP.json     earlier snapshots, newest Keep of them
//
// Every write goes to a temporary file in the same folder and is renamed into
// place, so a crash leaves either the old file or the new one, never half of
// one.
type Store struct {
	Dir  string
	Keep int // history files kept per source; 0 means 50
}

func (st *Store) keep() int {
	if st.Keep <= 0 {
		return 50
	}
	return st.Keep
}

// safeName turns a source name into a file name: letters, digits, '-', '_'
// and '.' stay, everything else becomes '_'.
func safeName(source string) string {
	var b strings.Builder
	for _, r := range source {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	n := b.String()
	if n == "" || n == "." || n == ".." {
		n = "_" + n
	}
	return n
}

func (st *Store) latestPath(source string) string {
	return filepath.Join(st.Dir, "latest", safeName(source)+".json")
}

// Latest returns the last saved snapshot of source, or nil if there is none.
// A file that exists but can't be read is an error, never "no baseline":
// treating it as empty would hide every change since.
func (st *Store) Latest(source string) (*Snapshot, error) {
	b, err := os.ReadFile(st.latestPath(source))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("stored snapshot of %s is unreadable (%v); move %s aside to start a new baseline", source, err, st.latestPath(source))
	}
	if err := s.Normalize(); err != nil {
		return nil, fmt.Errorf("stored snapshot of %s: %v", source, err)
	}
	return &s, nil
}

// Save makes s the latest snapshot of its source and adds it to the history.
func (st *Store) Save(s *Snapshot) error {
	if err := s.Normalize(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	name := safeName(s.Source)
	hist := filepath.Join(st.Dir, "history", name)
	stamp := s.TakenAt.UTC().Format("20060102T150405.000000000Z")
	if err := writeAtomic(filepath.Join(hist, stamp+".json"), b); err != nil {
		return err
	}
	if err := writeAtomic(st.latestPath(s.Source), b); err != nil {
		return err
	}
	return st.prune(hist)
}

func (st *Store) prune(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > st.keep() {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	if werr == nil {
		werr = f.Sync()
	}
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp)
		return werr
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
