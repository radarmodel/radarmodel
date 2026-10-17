// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/radarmodel/radarmodel/core"
)

// Object kinds. An item's ID is "KIND PATH", such as "column public.users.email".
const (
	KindTable      = "table"
	KindView       = "view"
	KindColumn     = "column"
	KindIndex      = "index"
	KindConstraint = "constraint"
	KindTrigger    = "trigger"
)

// Attribute names.
const (
	AttrType       = "type"       // table kind (table, view, ...) or column type
	AttrNullable   = "nullable"   // "yes" or "no"
	AttrDefault    = "default"    // column default expression
	AttrPosition   = "position"   // column position in its table
	AttrGenerated  = "generated"  // generated or identity column expression
	AttrDefinition = "definition" // index, constraint, view or trigger definition
	AttrEngine     = "engine"     // MySQL storage engine
	AttrCollation  = "collation"  // MySQL column collation
	AttrSQL        = "sql"        // SQLite CREATE statement of a table
)

// querier is what readers need: a transaction or a connection.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// builder collects objects while a reader scans.
type builder struct {
	items map[string]core.Item
}

func newBuilder() *builder { return &builder{items: map[string]core.Item{}} }

// add records an object, leaving out empty attributes. Two objects with the
// same ID are an error: it means the catalog query is wrong, and guessing
// which one to keep could hide a change.
func (b *builder) add(kind, path string, kv ...string) error {
	id := kind + " " + path
	if _, ok := b.items[id]; ok {
		return fmt.Errorf("the catalog listed %s twice", id)
	}
	it := core.Item{ID: id}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			if it.Attrs == nil {
				it.Attrs = map[string]string{}
			}
			it.Attrs[kv[i]] = kv[i+1]
		}
	}
	b.items[id] = it
	return nil
}

func (b *builder) list() []core.Item {
	out := make([]core.Item, 0, len(b.items))
	for _, it := range b.items {
		out = append(out, it)
	}
	return out
}

// Split returns an item ID's kind and qualified path.
func Split(id string) (kind, path string) {
	k, p, _ := strings.Cut(id, " ")
	return k, p
}

// parent is the table path of a column, index, constraint or trigger path.
func parent(path string) string {
	i := strings.LastIndexByte(path, '.')
	if i < 0 {
		return path
	}
	return path[:i]
}

var spaces = regexp.MustCompile(`\s+`)

// normalize collapses runs of whitespace, so reformatting a view or a CREATE
// statement isn't reported as a change.
func normalize(s string) string {
	return strings.TrimSpace(spaces.ReplaceAllString(s, " "))
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// scanRows runs a query and calls fn for each row; any error, including one
// that ends the rows early, fails the whole read.
func scanRows(ctx context.Context, q querier, query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return rows.Close()
}

func str(n sql.NullString) string {
	if !n.Valid {
		return ""
	}
	return n.String
}

// AlignSchemas lets two databases whose objects live in differently named
// schemas be compared (app_staging against app): when each side uses exactly
// one schema, b's is renamed to a's, in object names and in definitions.
func AlignSchemas(a, b *core.Snapshot) {
	as, bs := onlySchema(a), onlySchema(b)
	if as == "" || bs == "" || as == bs {
		return
	}
	for i, it := range b.Items {
		kind, path := Split(it.ID)
		it.ID = kind + " " + as + strings.TrimPrefix(path, bs)
		if len(it.Attrs) > 0 {
			attrs := make(map[string]string, len(it.Attrs))
			for k, v := range it.Attrs {
				attrs[k] = strings.ReplaceAll(v, bs+".", as+".")
			}
			it.Attrs = attrs
		}
		b.Items[i] = it
	}
	b.Normalize()
}

func onlySchema(s *core.Snapshot) string {
	name := ""
	for _, it := range s.Items {
		_, path := Split(it.ID)
		sch, _, _ := strings.Cut(path, ".")
		if name == "" {
			name = sch
		} else if sch != name {
			return ""
		}
	}
	return name
}
