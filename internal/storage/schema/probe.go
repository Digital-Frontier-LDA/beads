package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dberrors"
)

// Schema-shape probes.
//
// These answer "does this table / column / primary key exist in the current
// database?" with SHOW statements scoped to one database or one table, never
// with INFORMATION_SCHEMA.TABLES / COLUMNS / TABLE_CONSTRAINTS. Dolt does not
// push a TABLE_SCHEMA = DATABASE() predicate down into those views: it
// materializes every table of every database on the server and filters
// afterwards. On a shared sql-server with 132 databases one COLUMNS probe took
// 29.6 s, against 0.6 s for SHOW COLUMNS on the same table, and the probe runs
// on every bd open (beads-jwn). no_information_schema_test.go keeps the
// TABLES / COLUMNS views out of this package and cmd/bd/doctor.
//
// Every probe is ordered so that it never issues a statement that can fail on
// a missing table: SHOW TABLES always succeeds, and the per-table SHOW runs
// only once SHOW TABLES has listed the table. A failing statement pins a Dolt
// session to its pre-statement catalog snapshot (be-bv7x), so "probe, then
// read" matters here exactly as it does for the migration cursor.
//
// Names are compared exactly after fetching, never with LIKE, whose '_' and '%'
// are wildcards: a LIKE-only probe for "wisp_deps" would match "wispXdeps".

// quoteIdent backtick-quotes a MySQL identifier, doubling any embedded
// backtick, so a table name can never terminate the quoting early.
func quoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// showColumnValues runs a SHOW statement and returns every row's value for
// the result column named want (case-insensitive header match). An empty want
// selects the first column, for SHOW TABLES, whose header embeds the database
// name ("Tables_in_<db>").
func showColumnValues(ctx context.Context, db DBConn, query, want string) ([]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	idx := -1
	if want == "" && len(cols) > 0 {
		idx = 0
	}
	for i, c := range cols {
		if want != "" && strings.EqualFold(c, want) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("%s: result has no %q column (got %v)", query, want, cols)
	}

	// Scan every column into RawBytes: SHOW output width varies by server
	// version, and only one column is read.
	cells := make([]sql.RawBytes, len(cols))
	dest := make([]any, len(cols))
	for i := range cells {
		dest[i] = &cells[i]
	}
	var values []string
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		values = append(values, string(cells[idx]))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func containsExact(values []string, name string) bool {
	for _, v := range values {
		if v == name {
			return true
		}
	}
	return false
}

// isNoDatabaseSelected reports MySQL error 1046. The INFORMATION_SCHEMA probes
// these replace compared against DATABASE(), which is NULL on a session with
// no database selected, so they answered "absent" there rather than failing.
func isNoDatabaseSelected(err error) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1046 {
		return true
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no database selected")
}

// TableExists reports whether table (a base table or view) exists in the
// session's current database. A session with no database selected has no
// tables.
func TableExists(ctx context.Context, db DBConn, table string) (bool, error) {
	tables, err := showColumnValues(ctx, db, "SHOW TABLES", "")
	if err != nil {
		if isNoDatabaseSelected(err) {
			return false, nil
		}
		return false, fmt.Errorf("listing tables: %w", err)
	}
	return containsExact(tables, table), nil
}

// ColumnExists reports whether table.column exists in the session's current
// database. A missing table has no columns: false, not an error.
func ColumnExists(ctx context.Context, db DBConn, table, column string) (bool, error) {
	present, err := TableExists(ctx, db, table)
	if err != nil || !present {
		return false, err
	}
	fields, err := showColumnValues(ctx, db, "SHOW COLUMNS FROM "+quoteIdent(table), "Field")
	if err != nil {
		// A concurrent DROP between the two statements.
		if dberrors.IsTableNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("listing columns of %s: %w", table, err)
	}
	return containsExact(fields, column), nil
}

// HasPrimaryKey reports whether table carries a PRIMARY KEY, regardless of
// which column(s) compose it. A missing table has none: false, not an error.
func HasPrimaryKey(ctx context.Context, db DBConn, table string) (bool, error) {
	present, err := TableExists(ctx, db, table)
	if err != nil || !present {
		return false, err
	}
	keys, err := showColumnValues(ctx, db, "SHOW KEYS FROM "+quoteIdent(table), "Key_name")
	if err != nil {
		if dberrors.IsTableNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("listing keys of %s: %w", table, err)
	}
	// MySQL and Dolt name the primary key index PRIMARY, always.
	return containsExact(keys, "PRIMARY"), nil
}
