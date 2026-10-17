// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
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
	myNoSuchTrigger  = 1360 // ER_TRG_DOES_NOT_EXIST
	myViewInvalid    = 1356 // ER_VIEW_INVALID: the view exists, but what it reads doesn't
)

// intWidth matches an integer type's display width. MySQL 8.0.19 stopped
// printing it, so it is left out everywhere (it changes nothing that's stored
// unless ZEROFILL is set), and an upgrade isn't reported as type changes.
var intWidth = regexp.MustCompile(`^(tinyint|smallint|mediumint|int|bigint)\(\d+\)`)

func myColumnType(typ string) string {
	if strings.Contains(typ, "zerofill") {
		return typ
	}
	return intWidth.ReplaceAllString(typ, "$1")
}

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
			AttrType, myColumnType(typ), AttrNullable, yesNo(nullable == "YES"), AttrDefault, str(dflt),
			AttrGenerated, generated, AttrCollation, str(coll), AttrPosition, fmt.Sprint(pos))
	})
	if err != nil {
		return nil, fmt.Errorf("reading columns: %w", err)
	}

	type idx struct {
		unique, hidden bool
		typ            string
		cols           []string
	}
	var indexes map[string]*idx
	// Sort order, functional index expressions and visibility are read where
	// the server has them: EXPRESSION and IS_VISIBLE on MySQL 8, IGNORED on
	// MariaDB 10.6 and later.
	variants := []string{
		"COLLATION, EXPRESSION, IS_VISIBLE = 'NO'",
		"COLLATION, NULL, IGNORED = 'YES'",
		"COLLATION, NULL, 0",
	}
	for i, extra := range variants {
		indexes = map[string]*idx{}
		err = scanRows(ctx, q, `
SELECT TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, NON_UNIQUE, COLUMN_NAME, SUB_PART, INDEX_TYPE, `+extra+`
FROM information_schema.STATISTICS WHERE TABLE_SCHEMA IN `+in+`
ORDER BY TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, args, func(r *sql.Rows) error {
			var s, t, n, typ string
			var nonUnique int
			var col, coll, expr sql.NullString
			var sub sql.NullInt64
			var hidden sql.NullBool
			if err := r.Scan(&s, &t, &n, &nonUnique, &col, &sub, &typ, &coll, &expr, &hidden); err != nil {
				return err
			}
			path := s + "." + t + "." + n
			x := indexes[path]
			if x == nil {
				x = &idx{unique: nonUnique == 0, typ: typ, hidden: hidden.Valid && hidden.Bool}
				indexes[path] = x
			}
			c := "(expression)"
			switch {
			case col.Valid:
				c = col.String
			case expr.Valid:
				c = "(" + normalize(expr.String) + ")"
			}
			if sub.Valid {
				c += fmt.Sprintf("(%d)", sub.Int64)
			}
			if str(coll) == "D" {
				c += " DESC"
			}
			x.cols = append(x.cols, c)
			return nil
		})
		if myErrno(err) != myBadField || i == len(variants)-1 {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("reading indexes: %w", err)
	}
	for path, x := range indexes {
		def := x.typ + " (" + strings.Join(x.cols, ", ") + ")"
		if x.unique {
			def = "UNIQUE " + def
		}
		if x.hidden {
			def += " INVISIBLE"
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
	// MySQL lets constraints of different types share a name on one table (a
	// UNIQUE key and a FOREIGN KEY both called fk_a, or a CHECK named like a
	// foreign key), so constraints are keyed by name and type.
	type con struct {
		typ, path               string
		cols, refCols           []string
		ref, rules, notEnforced string
	}
	cons := map[string]*con{}
	names := map[string]int{}
	key := func(s, t, n, typ string) string { return s + "." + t + "." + n + "\x00" + typ }
	var err error
	for i, enforced := range []string{"ENFORCED", "'YES'"} { // ENFORCED: MySQL 8.0.16 and later
		cons, names = map[string]*con{}, map[string]int{}
		err = scanRows(ctx, q, `
SELECT CONSTRAINT_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, CONSTRAINT_TYPE, `+enforced+`
FROM information_schema.TABLE_CONSTRAINTS WHERE CONSTRAINT_SCHEMA IN `+in, args, func(r *sql.Rows) error {
			var s, t, n, typ, enf string
			if err := r.Scan(&s, &t, &n, &typ, &enf); err != nil {
				return err
			}
			typ = strings.ToLower(typ)
			x := &con{typ: typ, path: s + "." + t + "." + n}
			if enf == "NO" {
				x.notEnforced = " NOT ENFORCED"
			}
			cons[key(s, t, n, typ)] = x
			names[x.path]++
			return nil
		})
		if myErrno(err) != myBadField || i == 1 {
			break
		}
	}
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
		// A row with a referenced table belongs to the foreign key; one
		// without, to the primary key or unique key of that name.
		var x *con
		if rt.Valid {
			x = cons[key(s, t, n, "foreign key")]
		} else if x = cons[key(s, t, n, "primary key")]; x == nil {
			x = cons[key(s, t, n, "unique")]
		}
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
		if x := cons[key(s, t, n, "foreign key")]; x != nil {
			x.rules = "ON UPDATE " + upd + " ON DELETE " + del
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reading foreign key rules: %w", err)
	}
	// CHECK_CONSTRAINTS has TABLE_NAME on MariaDB, not on MySQL (where check
	// names are unique per schema).
	checks := map[string]string{}
	err = scanRows(ctx, q, `SELECT CONSTRAINT_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS WHERE CONSTRAINT_SCHEMA IN `+in, args,
		func(r *sql.Rows) error {
			var s, t, n, c string
			if err := r.Scan(&s, &t, &n, &c); err != nil {
				return err
			}
			checks[s+"."+t+"."+n] = c
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
	if err != nil {
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
			c, ok := checks[x.path]
			if !ok {
				s, rest, _ := strings.Cut(x.path, ".")
				c, ok = checks[s+"\x00"+rest[strings.IndexByte(rest, '.')+1:]]
			}
			if !ok {
				return fmt.Errorf("check constraint %s has no clause in information_schema", x.path)
			}
			def = "CHECK (" + normalize(c) + ")"
		case "foreign key":
			def = fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s) %s", strings.Join(x.cols, ", "), x.ref, strings.Join(x.refCols, ", "), x.rules)
		default:
			def = strings.ToUpper(x.typ) + " (" + strings.Join(x.cols, ", ") + ")"
		}
		path := x.path
		if names[path] > 1 {
			path += " (" + x.typ + ")"
		}
		if err := b.add(KindConstraint, path, AttrType, x.typ, AttrDefinition, strings.TrimSpace(def)+x.notEnforced); err != nil {
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
		case KindTrigger:
			// information_schema.TRIGGERS only lists triggers the user holds
			// the TRIGGER privilege on.
			table := parent(path)
			if containsID(removed, KindTable+" "+table) {
				continue
			}
			s, _, _ := strings.Cut(table, ".")
			query = "SHOW CREATE TRIGGER " + myQuote(s) + "." + myQuote(path[len(table)+1:])
		default:
			continue
		}
		rows, err := db.QueryContext(ctx, query)
		if err == nil {
			rows.Close()
			return fmt.Errorf("%s is missing from information_schema but still answers queries; the user's privileges probably changed, so nothing is reported as dropped", id)
		}
		switch myErrno(err) {
		case myNoSuchTable, myBadField, myBadDB, myNoSuchTrigger:
			continue // really gone
		case myTableDenied, myColumnDenied, myDBDenied, mySpecificDenied:
			return fmt.Errorf("%s is hidden by missing privileges, not dropped (%v); the scan is treated as failed. Grant the user SELECT, SHOW VIEW and TRIGGER on everything it should watch, or run accept if the change is real", id, err)
		default:
			return fmt.Errorf("checking whether %s was dropped: %v", id, err)
		}
	}
	return nil
}

// checkMySQLSelect makes sure the user can SELECT from every table and view
// it sees. information_schema lists a table for any privilege on it, but
// only SELECT lets a later scan tell a dropped table from a hidden one.
func checkMySQLSelect(ctx context.Context, db *sql.DB, items []core.Item) error {
	for _, it := range items {
		kind, path := Split(it.ID)
		if kind != KindTable && kind != KindView {
			continue
		}
		s, t, _ := strings.Cut(path, ".")
		rows, err := db.QueryContext(ctx, "SELECT 1 FROM "+myQuote(s)+"."+myQuote(t)+" LIMIT 0")
		if err == nil {
			rows.Close()
			continue
		}
		switch myErrno(err) {
		case myViewInvalid:
			continue
		case myTableDenied, myColumnDenied, myDBDenied, mySpecificDenied:
			return fmt.Errorf("the user can't SELECT from %s; grant it SELECT, SHOW VIEW and TRIGGER on everything it should watch (or ignore %s), so a dropped table can be told apart from a hidden one", path, path)
		}
		return fmt.Errorf("checking access to %s: %v", path, err)
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
