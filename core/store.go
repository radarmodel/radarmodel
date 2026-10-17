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
	"time"
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
	if n == "" || n == "." || n == ".." || reservedOnWindows(n) {
		n = "_" + n
	}
	return n
}

func reservedOnWindows(n string) bool {
	base := strings.ToLower(n)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "con", "prn", "aux", "nul":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '1' && base[3] <= '9' {
		return true
	}
	return false
}

// FileKey is the name a source's files are stored under, folded to lower
// case. Two sources with the same FileKey would share files on some systems
// and overwrite each other's snapshots, so configurations must keep them
// distinct.
func FileKey(source string) string { return strings.ToLower(safeName(source)) }

// Lock takes the store for one run, so overlapping runs (cron next to a
// watch, or a slow cron run) can't write an older snapshot over a newer one.
// A lock older than stale is taken to be left over from a crashed run.
// Call the returned function to release it.
func (st *Store) Lock(stale time.Duration) (func(), error) {
	if err := os.MkdirAll(st.Dir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(st.Dir, "lock")
	for try := 0; ; try++ {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			fmt.Fprintf(f, "pid %d at %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, serr := os.Stat(p)
		if serr == nil && try == 0 && time.Since(info.ModTime()) > stale {
			os.Remove(p)
			continue
		}
		since := ""
		if serr == nil {
			since = " since " + info.ModTime().UTC().Format(time.RFC3339)
		}
		return nil, fmt.Errorf("another run is using %s (lock file %s%s); if none is running, delete the lock file", st.Dir, p, since)
	}
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
	cleanTemp(dir)
	cleanTemp(filepath.Join(st.Dir, "latest"))
	sort.Strings(names)
	for len(names) > st.keep() {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

// cleanTemp removes temporary files left by a write that crashed: any older
// than an hour can't belong to a running write.
func cleanTemp(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".tmp-") {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
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
