// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/radarmodel/radarmodel/core"
)

// SQLite is read from sqlite_master and the table_xinfo, foreign_key_list
// pragmas, inside one read transaction. The file is opened read-only with
// mode=ro, so a wrong path is an error and never creates an empty database
// that would look like every table was dropped.

// sqliteDSN builds a read-only URI for path.
func sqliteDSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", abs)
	}
	// "file:" plus the path with forward slashes: file:/srv/app.db, or
	// file:C:/data/app.db on Windows. Characters with a meaning in a URI are
	// escaped.
	p := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(filepath.ToSlash(abs))
	return "file:" + p + "?mode=ro&_pragma=busy_timeout(5000)", nil
}

func readSQLite(ctx context.Context, q querier, _ *DatabaseConfig) ([]core.Item, error) {
	b := newBuilder()
	var tables []string
	virtual := map[string]bool{}
	err := scanRows(ctx, q, `SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\'`, nil,
		func(r *sql.Rows) error {
			var typ, name, table string
			var stmt sql.NullString
			if err := r.Scan(&typ, &name, &table, &stmt); err != nil {
				return err
			}
			def := normalize(str(stmt))
			switch typ {
			case "table":
				tables = append(tables, name)
				kind := "table"
				if strings.HasPrefix(strings.ToUpper(def), "CREATE VIRTUAL TABLE") {
					kind = "virtual table"
					virtual[name] = true
				}
				return b.add(KindTable, "main."+name, AttrType, kind, AttrSQL, def)
			case "view":
				return b.add(KindView, "main."+name, AttrType, "view", AttrDefinition, def)
			case "index":
				return b.add(KindIndex, "main."+table+"."+name, AttrDefinition, def)
			case "trigger":
				return b.add(KindTrigger, "main."+table+"."+name, AttrDefinition, def)
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading sqlite_master: %w", err)
	}
	for _, t := range tables {
		if virtual[t] {
			// A virtual table's columns come from its module, which may not
			// be built into this SQLite (fts4, rtree); its CREATE statement
			// says everything that can drift.
			continue
		}
		err := scanRows(ctx, q, `SELECT cid, name, type, "notnull", dflt_value, pk, hidden FROM pragma_table_xinfo(?)`, []any{t},
			func(r *sql.Rows) error {
				var cid, notNull, pk, hidden int
				var name, typ string
				var dflt sql.NullString
				if err := r.Scan(&cid, &name, &typ, &notNull, &dflt, &pk, &hidden); err != nil {
					return err
				}
				gen := ""
				switch hidden {
				case 1:
					gen = "hidden"
				case 2:
					gen = "generated virtual"
				case 3:
					gen = "generated stored"
				}
				return b.add(KindColumn, "main."+t+"."+name,
					AttrType, typ, AttrNullable, yesNo(notNull == 0 && pk == 0), AttrDefault, str(dflt),
					AttrGenerated, gen, AttrPosition, fmt.Sprint(cid+1))
			})
		if err != nil {
			return nil, fmt.Errorf("reading the columns of %s: %w", t, err)
		}
		type fk struct {
			table, onUpdate, onDelete string
			from, to                  []string
		}
		fks := map[int]*fk{}
		var order []int
		err = scanRows(ctx, q, `SELECT id, "table", "from", "to", on_update, on_delete FROM pragma_foreign_key_list(?) ORDER BY id, seq`, []any{t},
			func(r *sql.Rows) error {
				var id int
				var ref, from, onUpd, onDel string
				var to sql.NullString
				if err := r.Scan(&id, &ref, &from, &to, &onUpd, &onDel); err != nil {
					return err
				}
				x := fks[id]
				if x == nil {
					x = &fk{table: ref, onUpdate: onUpd, onDelete: onDel}
					fks[id] = x
					order = append(order, id)
				}
				x.from = append(x.from, from)
				x.to = append(x.to, str(to))
				return nil
			})
		if err != nil {
			return nil, fmt.Errorf("reading the foreign keys of %s: %w", t, err)
		}
		// SQLite foreign keys have no names: they are named after their
		// columns and the table they reference, and in the rare case of two
		// such keys on the same columns, numbered within that group.
		base := func(x *fk) string { return "main." + t + ".fk_" + strings.Join(x.from, "_") + "_" + x.table }
		uses, seen := map[string]int{}, map[string]int{}
		for _, id := range order {
			uses[base(fks[id])]++
		}
		for _, id := range order {
			x := fks[id]
			path := base(x)
			if uses[path] > 1 {
				seen[path]++
				path += fmt.Sprintf("_%d", seen[path])
			}
			def := fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s) ON UPDATE %s ON DELETE %s",
				strings.Join(x.from, ", "), x.table, strings.Join(x.to, ", "), x.onUpdate, x.onDelete)
			if err := b.add(KindConstraint, path, AttrType, "foreign key", AttrDefinition, def); err != nil {
				return nil, err
			}
		}
	}
	return b.list(), nil
}
