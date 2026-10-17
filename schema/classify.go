// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/radarmodel/radarmodel/core"
)

// Ignored reports whether an item ID matches one of the ignore globs, which
// are matched against the object's qualified name ("public.users.email").
// A pattern that matches a table also covers everything in it.
func Ignored(id string, globs []string) bool {
	_, path := Split(id)
	for _, g := range globs {
		if core.Glob(g, path) || core.Glob(g+".*", path) {
			return true
		}
	}
	return false
}

// dropIgnored returns the items not matched by the globs. It is applied to
// both snapshots before comparing, so adding an ignore rule never looks like
// objects being dropped.
func dropIgnored(items []core.Item, globs []string) []core.Item {
	if len(globs) == 0 {
		return items
	}
	var out []core.Item
	for _, it := range items {
		if !Ignored(it.ID, globs) {
			out = append(out, it)
		}
	}
	return out
}

// Classify sets the severity of each change. Any change made outside a
// migration is drift, so nothing is silently fine; what varies is how likely
// it is to break the application.
//
//   - a table, view or column gone: breaking
//   - a column's type changed, or it became NOT NULL: breaking
//   - a new table, view, column, index, constraint or trigger: warning
//   - an index, constraint or trigger gone, or its definition changed: warning
//   - a column's default, generated expression or collation changed, or it
//     became nullable: warning
//   - a view's definition changed: warning
//   - a table's storage engine or kind changed: warning
//   - a column's position changed: info
//   - anything inside a table that was itself added or dropped: info, since
//     the table's own change already says it
func Classify(changes []core.Change) {
	tableChanged := map[string]bool{}
	childChanged := map[string]bool{}
	for _, c := range changes {
		kind, path := Split(c.ID)
		switch {
		case (kind == KindTable || kind == KindView) && (c.Kind == core.Added || c.Kind == core.Removed):
			tableChanged[path] = true
		case kind != KindTable && kind != KindView:
			childChanged[parent(path)] = true
		}
	}
	for i := range changes {
		c := &changes[i]
		kind, path := Split(c.ID)
		if kind != KindTable && kind != KindView && tableChanged[parent(path)] {
			c.Severity = core.Info
			continue
		}
		switch c.Kind {
		case core.Added:
			c.Severity = core.Warning
		case core.Removed:
			switch kind {
			case KindTable, KindView, KindColumn:
				c.Severity = core.Breaking
			default:
				c.Severity = core.Warning
			}
		case core.Changed:
			c.Severity = changed(c, kind, childChanged[path])
		}
	}
}

func changed(c *core.Change, kind string, childrenChanged bool) core.Severity {
	switch c.Field {
	case AttrType:
		if kind == KindColumn {
			return core.Breaking
		}
		return core.Warning
	case AttrNullable:
		if c.New == "no" {
			c.Note = "inserts without it now fail"
			return core.Breaking
		}
		return core.Warning
	case AttrPosition:
		return core.Info
	case AttrDefinition:
		if kind == KindView && c.New == "" {
			c.Note = "the definition isn't visible to this user"
			return core.Info
		}
		return core.Warning
	case AttrSQL:
		// SQLite keeps CHECK constraints and other details only in the CREATE
		// statement. When columns or indexes changed too, they say it better.
		if childrenChanged {
			return core.Info
		}
		return core.Warning
	}
	return core.Warning
}
