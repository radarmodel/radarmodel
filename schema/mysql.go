// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/radarmodel/radarmodel/core"
)

// MySQL (and MariaDB) are read from information_schema. It only lists what the
// user has privileges on, so a revoked GRANT makes objects vanish from it;
// probeMySQL tells that apart from a real DROP before anything is reported.
// information_schema isn't transactional, so the run reads it twice and only
// accepts two identical reads (see Run).

// MySQL error numbers used to tell "gone" from "not allowed to see".
const (
	myNoSuchTable    = 1146 // ER_NO_SUCH_TABLE
	myBadField       = 1054 // ER_BAD_FIELD_ERROR
	myBadTable       = 1109 // ER_UNKNOWN_TABLE (an information_schema table this server lacks)
	myBadDB          = 1049 // ER_BAD_DB_ERROR
	myTableDenied    = 1142 // ER_TABLEACCESS_DENIED_ERROR
	myColumnDenied   = 1143 // ER_COLUMNACCESS_DENIED_ERROR
	myDBDenied       = 1044 // ER_DBACCESS_DENIED_ERROR
	mySpecificDenied = 1227 // ER_SPECIFIC_ACCESS_DENIED_ERROR
)

func myErrno(err error) uint16 {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number
	}
	return 0
}

func myIn(schemas []string, args *[]any) string {
	ph := make([]string, len(schemas))
	for i, s := range schemas {
		*args = append(*args, s)
		ph[i] = "?"
	}
	return "(" + strings.Join(ph, ", ") + ")"
}

func mySchemas(ctx context.Context, q querier, d *DatabaseConfig) ([]string, error) {
	if len(d.Schemas) > 0 {
		return d.Schemas, nil
	}
	var cur sql.NullString
	if err := scanRows(ctx, q, "SELECT DATABASE()", nil, func(r *sql.Rows) error { return r.Scan(&cur) }); err != nil {
		return nil, err
	}
	if !cur.Valid || cur.String == "" {
		return nil, errors.New("the connection has no default database; put one in the connection string or list it under schemas")
	}
	return []string{cur.String}, nil
}

func readMySQL(ctx context.Context, q querier, d *DatabaseConfig) ([]core.Item, error) {
	schemas, err := mySchemas(ctx, q, d)
	if err != nil {
		return nil, err
	}
	b := newBuilder()
	var args []any
	in := myIn(schemas, &args)

	views := map[string]string{}
	err = scanRows(ctx, q, `SELECT TABLE_SCHEMA, TABLE_NAME, VIEW_DEFINITION FROM information_schema.VIEWS WHERE TABLE_SCHEMA IN `+in, args,
		func(r *sql.Rows) error {
			var s, n string
			var def sql.NullString
			if err := r.Scan(&s, &n, &def); err != nil {
				return err
			}
			views[s+"."+n] = normalize(str(def))
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading views: %w", err)
	}

	tables := map[string]bool{}
	err = scanRows(ctx, q, `SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE, ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA IN `+in, args,
		func(r *sql.Rows) error {
			var s, n, typ string
			var engine sql.NullString
			if err := r.Scan(&s, &n, &typ, &engine); err != nil {
				return err
			}
			path := s + "." + n
			switch typ {
			case "VIEW":
				// The definition reads empty without the SHOW VIEW privilege;
				// it is then left out rather than reported as changed.
				return b.add(KindView, path, AttrType, "view", AttrDefinition, views[path])
			case "SYSTEM VIEW":
				return nil
			}
			tables[path] = true
			return b.add(KindTable, path, AttrType, strings.ToLower(typ), AttrEngine, str(engine))
		})
	if err != nil {
		return nil, fmt.Errorf("reading tables: %w", err)
	}

	err = scanRows(ctx, q, `
SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, ORDINAL_POSITION, COLUMN_DEFAULT, IS_NULLABLE,
       COLUMN_TYPE, EXTRA, COLLATION_NAME, GENERATION_EXPRESSION
FROM information_schema.COLUMNS WHERE TABLE_SCHEMA IN `+in, args, func(r *sql.Rows) error {
		var s, t, c, nullable, typ string
		var pos int
		var dflt, extra, coll, gen sql.NullString
		if err := r.Scan(&s, &t, &c, &pos, &dflt, &nullable, &typ, &extra, &coll, &gen); err != nil {
			return err
		}
		if !tables[s+"."+t] {
			return nil // a view's column
		}
		generated := strings.TrimSpace(str(extra))
		if g := strings.TrimSpace(str(gen)); g != "" {
			generated = strings.TrimSpace(generated + " " + g)
		}
		return b.add(KindColumn, s+"."+t+"."+c,
			AttrType, typ, AttrNullable, yesNo(nullable == "YES"), AttrDefault, str(dflt),
			AttrGenerated, generated, AttrCollation, str(coll), AttrPosition, fmt.Sprint(pos))
	})
	if err != nil {
		return nil, fmt.Errorf("reading columns: %w", err)
	}

	type idx struct {
		unique bool
		typ    string
		cols   []string
	}
	indexes := map[string]*idx{}
	err = scanRows(ctx, q, `
SELECT TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, NON_UNIQUE, COLUMN_NAME, SUB_PART, INDEX_TYPE
FROM information_schema.STATISTICS WHERE TABLE_SCHEMA IN `+in+`
ORDER BY TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, args, func(r *sql.Rows) error {
		var s, t, n, typ string
		var nonUnique int
		var col sql.NullString
		var sub sql.NullInt64
		if err := r.Scan(&s, &t, &n, &nonUnique, &col, &sub, &typ); err != nil {
			return err
		}
		path := s + "." + t + "." + n
		x := indexes[path]
		if x == nil {
			x = &idx{unique: nonUnique == 0, typ: typ}
			indexes[path] = x
		}
		c := "(expression)"
		if col.Valid {
			c = col.String
		}
		if sub.Valid {
			c += fmt.Sprintf("(%d)", sub.Int64)
		}
		x.cols = append(x.cols, c)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading indexes: %w", err)
	}
	for path, x := range indexes {
		def := x.typ + " (" + strings.Join(x.cols, ", ") + ")"
		if x.unique {
			def = "UNIQUE " + def
		}
		if err := b.add(KindIndex, path, AttrDefinition, def); err != nil {
			return nil, err
		}
	}

	if err := readMySQLConstraints(ctx, q, in, args, b); err != nil {
		return nil, err
	}

	err = scanRows(ctx, q, `
SELECT TRIGGER_SCHEMA, EVENT_OBJECT_TABLE, TRIGGER_NAME, ACTION_TIMING, EVENT_MANIPULATION, ACTION_STATEMENT
FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA IN `+in, args, func(r *sql.Rows) error {
		var s, t, n, timing, event, stmt string
		if err := r.Scan(&s, &t, &n, &timing, &event, &stmt); err != nil {
			return err
		}
		return b.add(KindTrigger, s+"."+t+"."+n, AttrDefinition, normalize(timing+" "+event+" "+stmt))
	})
	if err != nil {
		return nil, fmt.Errorf("reading triggers: %w", err)
	}
	return b.list(), nil
}

func readMySQLConstraints(ctx context.Context, q querier, in string, args []any, b *builder) error {
	type con struct {
		typ, schema, table, name string
		cols, refCols            []string
		ref, rules, check        string
	}
	cons := map[string]*con{}
	key := func(s, t, n string) string { return s + "." + t + "." + n }
	err := scanRows(ctx, q, `
SELECT CONSTRAINT_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, CONSTRAINT_TYPE
FROM information_schema.TABLE_CONSTRAINTS WHERE CONSTRAINT_SCHEMA IN `+in, args, func(r *sql.Rows) error {
		var s, t, n, typ string
		if err := r.Scan(&s, &t, &n, &typ); err != nil {
			return err
		}
		cons[key(s, t, n)] = &con{typ: strings.ToLower(typ), schema: s, table: t, name: n}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reading constraints: %w", err)
	}
	err = scanRows(ctx, q, `
SELECT CONSTRAINT_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, COLUMN_NAME, REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME
FROM information_schema.KEY_COLUMN_USAGE WHERE CONSTRAINT_SCHEMA IN `+in+`
ORDER BY CONSTRAINT_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, ORDINAL_POSITION`, args, func(r *sql.Rows) error {
		var s, t, n, c string
		var rs, rt, rc sql.NullString
		if err := r.Scan(&s, &t, &n, &c, &rs, &rt, &rc); err != nil {
			return err
		}
		x := cons[key(s, t, n)]
		if x == nil {
			return nil
		}
		x.cols = append(x.cols, c)
		if rt.Valid {
			x.ref = str(rs) + "." + rt.String
			x.refCols = append(x.refCols, str(rc))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reading constraint columns: %w", err)
	}
	err = scanRows(ctx, q, `
SELECT CONSTRAINT_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, UPDATE_RULE, DELETE_RULE
FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA IN `+in, args, func(r *sql.Rows) error {
		var s, t, n, upd, del string
		if err := r.Scan(&s, &t, &n, &upd, &del); err != nil {
			return err
		}
		if x := cons[key(s, t, n)]; x != nil {
			x.rules = "ON UPDATE " + upd + " ON DELETE " + del
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reading foreign key rules: %w", err)
	}
	// CHECK_CONSTRAINTS has TABLE_NAME on MariaDB, not on MySQL (where check
	// names are unique per schema), and doesn't exist before MySQL 8.0.16.
	checks := map[string]string{}
	err = scanRows(ctx, q, `SELECT CONSTRAINT_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS WHERE CONSTRAINT_SCHEMA IN `+in, args,
		func(r *sql.Rows) error {
			var s, t, n, c string
			if err := r.Scan(&s, &t, &n, &c); err != nil {
				return err
			}
			checks[key(s, t, n)] = c
			return nil
		})
	if myErrno(err) == myBadField {
		err = scanRows(ctx, q, `SELECT CONSTRAINT_SCHEMA, CONSTRAINT_NAME, CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS WHERE CONSTRAINT_SCHEMA IN `+in, args,
			func(r *sql.Rows) error {
				var s, n, c string
				if err := r.Scan(&s, &n, &c); err != nil {
					return err
				}
				checks[s+"\x00"+n] = c
				return nil
			})
	}
	if err != nil && myErrno(err) != myBadTable {
		return fmt.Errorf("reading check constraints: %w", err)
	}
	keys := make([]string, 0, len(cons))
	for k := range cons {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		x := cons[k]
		var def string
		switch x.typ {
		case "check":
			c, ok := checks[k]
			if !ok {
				c = checks[x.schema+"\x00"+x.name]
			}
			def = "CHECK (" + normalize(c) + ")"
		case "foreign key":
			def = fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s) %s", strings.Join(x.cols, ", "), x.ref, strings.Join(x.refCols, ", "), x.rules)
		default:
			def = strings.ToUpper(x.typ) + " (" + strings.Join(x.cols, ", ") + ")"
		}
		if err := b.add(KindConstraint, k, AttrType, x.typ, AttrDefinition, strings.TrimSpace(def)); err != nil {
			return err
		}
	}
	return nil
}

func myQuote(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }

// probeMySQL checks objects that vanished from information_schema. A table
// that answers "doesn't exist" was dropped; one that answers "access denied"
// was only hidden by a privilege change, and the scan fails instead of
// reporting a removal. It returns nil when every removal is real.
func probeMySQL(ctx context.Context, db *sql.DB, removed []string, prev map[string]bool) error {
	for _, id := range removed {
		kind, path := Split(id)
		var query string
		switch kind {
		case KindTable, KindView:
			s, t, ok := strings.Cut(path, ".")
			if !ok {
				continue
			}
			query = "SELECT 1 FROM " + myQuote(s) + "." + myQuote(t) + " LIMIT 0"
		case KindColumn:
			table := parent(path)
			if !prev[KindTable+" "+table] || containsID(removed, KindTable+" "+table) {
				continue // the table itself is probed
			}
			s, t, _ := strings.Cut(table, ".")
			c := path[len(table)+1:]
			query = "SELECT " + myQuote(c) + " FROM " + myQuote(s) + "." + myQuote(t) + " LIMIT 0"
		default:
			continue
		}
		rows, err := db.QueryContext(ctx, query)
		if err == nil {
			rows.Close()
			return fmt.Errorf("%s is missing from information_schema but still answers queries; the user's privileges probably changed, so nothing is reported as dropped", id)
		}
		switch myErrno(err) {
		case myNoSuchTable, myBadField, myBadDB:
			continue // really gone
		case myTableDenied, myColumnDenied, myDBDenied, mySpecificDenied:
			return fmt.Errorf("%s is hidden by missing privileges, not dropped (%v); the scan is treated as failed. Grant the user SELECT, or at least SHOW VIEW and REFERENCES, on everything it should watch", id, err)
		default:
			return fmt.Errorf("checking whether %s was dropped: %v", id, err)
		}
	}
	return nil
}

func containsID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
