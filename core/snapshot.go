// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

// Package core is the engine shared by the RadarModel products: snapshots of
// a source, the diff between two snapshots, the store that keeps them, and
// the outputs that report changes.
package core

import (
	"fmt"
	"maps"
	"sort"
	"time"
)

// Item is one thing a source holds: a model in a provider's list, a column in
// a database schema. ID identifies it across snapshots; Attrs are the
// properties whose changes matter.
type Item struct {
	ID    string            `json:"id"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// Snapshot is what a source held at one moment.
type Snapshot struct {
	Source  string    `json:"source"`
	Kind    string    `json:"kind"`
	TakenAt time.Time `json:"taken_at"`
	// Meta holds facts about the source itself rather than its items, such
	// as a database server's version. It is never diffed item by item.
	Meta  map[string]string `json:"meta,omitempty"`
	Items []Item            `json:"items"`
}

// Normalize sorts the items by ID and checks that every ID is present and
// unique. A snapshot that fails it is never stored or compared.
func (s *Snapshot) Normalize() error {
	if s.Source == "" {
		return fmt.Errorf("snapshot has no source name")
	}
	sort.SliceStable(s.Items, func(i, j int) bool { return s.Items[i].ID < s.Items[j].ID })
	kept := s.Items[:0]
	for _, it := range s.Items {
		if it.ID == "" {
			return fmt.Errorf("%s: an item has an empty ID", s.Source)
		}
		if n := len(kept); n > 0 && kept[n-1].ID == it.ID {
			// A list that names one item twice the same way is read as
			// naming it once; two different versions of it are an error.
			if maps.Equal(kept[n-1].Attrs, it.Attrs) {
				continue
			}
			return fmt.Errorf("%s: item %q appears twice", s.Source, it.ID)
		}
		kept = append(kept, it)
	}
	s.Items = kept
	return nil
}

// Index maps item IDs to items.
func (s *Snapshot) Index() map[string]Item {
	m := make(map[string]Item, len(s.Items))
	for _, it := range s.Items {
		m[it.ID] = it
	}
	return m
}
