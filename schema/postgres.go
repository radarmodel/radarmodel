// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/radarmodel/radarmodel/core"
)

// Postgres is read from pg_catalog, not information_schema: information_schema
// only lists objects the user has privileges on, so a revoked GRANT would look
// like dropped tables. pg_catalog lists everything, whatever the privileges.
// The whole read runs in one REPEATABLE READ, READ ONLY transaction, so it sees
// one consistent catalog.

const pgMinVersion = 120000 // generated columns (attgenerated) arrived in 12

func pgSchemaFilter(col string, schemas []string, args *[]any) string {
	if len(schemas) == 0 {
		return fmt.Sprintf("%[1]s NOT IN ('pg_catalog', 'information_schema') AND %[1]s NOT LIKE 'pg\\_toast%%' AND %[1]s NOT LIKE 'pg\\_temp\\_%%'", col)
	}
	ph := make([]string, len(schemas))
	for i, s := range schemas {
		*args = append(*args, s)
		ph[i] = fmt.Sprintf("$%d", len(*args))
	}
	return fmt.Sprintf("%s IN (%s)", col, strings.Join(ph, ", "))
}

// pgOwn leaves out relations that belong to an extension (PostGIS's tables,
// pg_cron's job table): an extension upgrade isn't drift in the app's schema.
func pgOwn(rel string) string {
	return "NOT EXISTS (SELECT 1 FROM pg_depend e WHERE e.classid = 'pg_class'::regclass AND e.objid = " + rel + " AND e.deptype = 'e')"
}

var pgRelKinds = map[string]string{
	"r": "table", "p": "partitioned table", "f": "foreign table",
	"v": "view", "m": "materialized view",
}

var pgConTypes = map[string]string{
	"p": "primary key", "u": "unique", "f": "foreign key", "c": "check", "x": "exclusion",
}

func readPostgres(ctx context.Context, q querier, d *DatabaseConfig) ([]core.Item, error) {
	var version int
	if err := scanRows(ctx, q, "SELECT current_setting('server_version_num')::int", nil, func(r *sql.Rows) error {
		return r.Scan(&version)
	}); err != nil {
		return nil, err
	}
	if version < pgMinVersion {
		return nil, fmt.Errorf("Postgres %d.%d is too old; RadarModel Schema needs 12 or newer", version/10000, version%10000/100)
	}
	b := newBuilder()

	var args []any
	filter := pgSchemaFilter("n.nspname", d.Schemas, &args)
	err := scanRows(ctx, q, `
SELECT n.nspname, c.relname, c.relkind::text,
       CASE WHEN c.relkind IN ('v', 'm') THEN pg_get_viewdef(c.oid, true) END,
       CASE WHEN c.relispartition THEN
         (SELECT quote_ident(pn.nspname) || '.' || quote_ident(p.relname)
          FROM pg_inherits i JOIN pg_class p ON p.oid = i.inhparent JOIN pg_namespace pn ON pn.oid = p.relnamespace
          WHERE i.inhrelid = c.oid) || ' ' || pg_get_expr(c.relpartbound, c.oid) END,
       CASE WHEN c.relkind = 'p' THEN pg_get_partkeydef(c.oid) END,
       CASE WHEN c.relforcerowsecurity THEN 'forced' WHEN c.relrowsecurity THEN 'on' END
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p', 'f', 'v', 'm') AND `+pgOwn("c.oid")+` AND `+filter, args, func(r *sql.Rows) error {
		var schema, name, kind string
		var def, partOf, partKey, rls sql.NullString
		if err := r.Scan(&schema, &name, &kind, &def, &partOf, &partKey, &rls); err != nil {
			return err
		}
		path := schema + "." + name
		if kind == "v" || kind == "m" {
			return b.add(KindView, path, AttrType, pgRelKinds[kind], AttrDefinition, normalize(str(def)))
		}
		return b.add(KindTable, path, AttrType, pgRelKinds[kind], AttrPartitionOf, normalize(str(partOf)),
			AttrPartitionKey, str(partKey), AttrRowSecurity, str(rls))
	})
	if err != nil {
		return nil, fmt.Errorf("reading tables: %w", err)
	}

	args = nil
	filter = pgSchemaFilter("n.nspname", d.Schemas, &args)
	err = scanRows(ctx, q, `
SELECT n.nspname, c.relname, a.attname, format_type(a.atttypid, a.atttypmod),
       a.attnotnull, pg_get_expr(ad.adbin, ad.adrelid), a.attnum,
       a.attgenerated::text, a.attidentity::text,
       CASE WHEN a.attcollation <> 0 AND a.attcollation <> ty.typcollation THEN
         (SELECT quote_ident(cn.nspname) || '.' || quote_ident(co.collname)
          FROM pg_collation co JOIN pg_namespace cn ON cn.oid = co.collnamespace WHERE co.oid = a.attcollation) END
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_type ty ON ty.oid = a.atttypid
LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
WHERE a.attnum > 0 AND NOT a.attisdropped AND c.relkind IN ('r', 'p', 'f') AND `+pgOwn("c.oid")+` AND `+filter, args, func(r *sql.Rows) error {
		var schema, table, col, typ, gen, ident string
		var notNull bool
		var def, coll sql.NullString
		var pos int
		if err := r.Scan(&schema, &table, &col, &typ, &notNull, &def, &pos, &gen, &ident, &coll); err != nil {
			return err
		}
		dflt, generated := str(def), ""
		switch {
		case gen == "s":
			generated, dflt = "stored as "+dflt, ""
		case ident == "a":
			generated = "identity always"
		case ident == "d":
			generated = "identity by default"
		}
		return b.add(KindColumn, schema+"."+table+"."+col,
			AttrType, typ, AttrNullable, yesNo(!notNull), AttrDefault, dflt,
			AttrGenerated, generated, AttrCollation, str(coll), AttrPosition, fmt.Sprint(pos))
	})
	if err != nil {
		return nil, fmt.Errorf("reading columns: %w", err)
	}

	args = nil
	filter = pgSchemaFilter("n.nspname", d.Schemas, &args)
	err = scanRows(ctx, q, `
SELECT n.nspname, t.relname, i.relname, pg_get_indexdef(x.indexrelid)
FROM pg_index x
JOIN pg_class i ON i.oid = x.indexrelid
JOIN pg_class t ON t.oid = x.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE `+pgOwn("t.oid")+` AND `+filter, args, func(r *sql.Rows) error {
		var schema, table, name, def string
		if err := r.Scan(&schema, &table, &name, &def); err != nil {
			return err
		}
		return b.add(KindIndex, schema+"."+table+"."+name, AttrDefinition, normalize(def))
	})
	if err != nil {
		return nil, fmt.Errorf("reading indexes: %w", err)
	}

	args = nil
	filter = pgSchemaFilter("n.nspname", d.Schemas, &args)
	err = scanRows(ctx, q, `
SELECT n.nspname, t.relname, con.conname, con.contype::text, pg_get_constraintdef(con.oid)
FROM pg_constraint con
JOIN pg_class t ON t.oid = con.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE con.contype IN ('p', 'u', 'f', 'c', 'x') AND `+pgOwn("t.oid")+` AND `+filter, args, func(r *sql.Rows) error {
		var schema, table, name, typ, def string
		if err := r.Scan(&schema, &table, &name, &typ, &def); err != nil {
			return err
		}
		return b.add(KindConstraint, schema+"."+table+"."+name, AttrType, pgConTypes[typ], AttrDefinition, normalize(def))
	})
	if err != nil {
		return nil, fmt.Errorf("reading constraints: %w", err)
	}

	args = nil
	filter = pgSchemaFilter("n.nspname", d.Schemas, &args)
	err = scanRows(ctx, q, `
SELECT n.nspname, t.relname, g.tgname, pg_get_triggerdef(g.oid) ||
       CASE g.tgenabled WHEN 'D' THEN ' (DISABLED)' WHEN 'R' THEN ' (ENABLE REPLICA)' WHEN 'A' THEN ' (ENABLE ALWAYS)' ELSE '' END
FROM pg_trigger g
JOIN pg_class t ON t.oid = g.tgrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE NOT g.tgisinternal AND `+pgOwn("t.oid")+` AND `+filter, args, func(r *sql.Rows) error {
		var schema, table, name, def string
		if err := r.Scan(&schema, &table, &name, &def); err != nil {
			return err
		}
		return b.add(KindTrigger, schema+"."+table+"."+name, AttrDefinition, normalize(def))
	})
	if err != nil {
		return nil, fmt.Errorf("reading triggers: %w", err)
	}
	return b.list(), nil
}
