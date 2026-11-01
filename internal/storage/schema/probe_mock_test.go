package schema

import (
	"regexp"

	"github.com/DATA-DOG/go-sqlmock"
)

// sqlmock helpers for the SHOW-based probes in probe.go. TableExists is one
// SHOW TABLES; ColumnExists and HasPrimaryKey are SHOW TABLES followed, only
// when the table is listed, by SHOW COLUMNS / SHOW KEYS on that table.

const showTablesQuery = `^SHOW TABLES$`

// mockWorldTables is the table list returned by probes whose caller does not
// name the table in the test (expectColumnExists, expectTableListed). It holds
// every table those probes ask about, so "present" is answered for any of them.
var mockWorldTables = []string{
	"comments", "dependencies", "events", "issues", "labels", "leases",
	"local_metadata", "wisp_comments", "wisp_dependencies", "wisp_events",
	"wisp_labels", "wisps",
}

// expectShowTables mocks one SHOW TABLES returning exactly tables.
func expectShowTables(mock sqlmock.Sqlmock, tables ...string) {
	rows := sqlmock.NewRows([]string{"Tables_in_beads"})
	for _, t := range tables {
		rows.AddRow(t)
	}
	mock.ExpectQuery(showTablesQuery).WillReturnRows(rows)
}

// expectShowTablesError mocks a failing SHOW TABLES.
func expectShowTablesError(mock sqlmock.Sqlmock, err error) {
	mock.ExpectQuery(showTablesQuery).WillReturnError(err)
}

// expectTableProbe mocks TableExists(table).
func expectTableProbe(mock sqlmock.Sqlmock, table string, exists bool) {
	if exists {
		expectShowTables(mock, "unrelated_table", table)
		return
	}
	expectShowTables(mock, "unrelated_table")
}

// expectTableListed mocks a TableExists probe for an unnamed table: present
// lists mockWorldTables, absent lists nothing.
func expectTableListed(mock sqlmock.Sqlmock, present bool) {
	if present {
		expectShowTables(mock, mockWorldTables...)
		return
	}
	expectShowTables(mock)
}

// expectColumnProbe mocks ColumnExists(table, column). Absent is modelled as
// the table present without the column, so the SHOW COLUMNS leg runs too.
func expectColumnProbe(mock sqlmock.Sqlmock, table, column string, exists bool) {
	expectShowTables(mock, "unrelated_table", table)
	fields := []string{"unrelated_column"}
	if exists {
		fields = append(fields, column)
	}
	mock.ExpectQuery("^" + regexp.QuoteMeta("SHOW COLUMNS FROM "+quoteIdent(table)) + "$").
		WillReturnRows(showColumnsRows(fields...))
}

// expectColumnExists mocks a ColumnExists probe for an unnamed id-column
// check (the dependency/aux re-key paths): present lists the table and an id
// column; absent lists no tables at all, so SHOW COLUMNS never runs.
func expectColumnExists(mock sqlmock.Sqlmock, present bool) {
	if !present {
		expectShowTables(mock)
		return
	}
	expectShowTables(mock, mockWorldTables...)
	mock.ExpectQuery("^SHOW COLUMNS FROM `\\w+`$").
		WillReturnRows(showColumnsRows("id"))
}

// expectCursorProbe mocks the existence probe migrationSource.currentVersion
// issues before it ever reads the cursor table (be-bv7x: a not-yet-created
// cursor table must never poison the pooled Dolt session with a failing
// statement). It is a SHOW TABLES, which always succeeds.
func expectCursorProbe(mock sqlmock.Sqlmock, table string, exists bool) {
	expectTableProbe(mock, table, exists)
}

// expectIgnoredSentinelProbes mocks the probes currentVersion issues to
// confirm a non-zero ignored cursor against the schema it claims (gh 5033).
// They fire only for a non-zero cursor, in ignoredSource's table then column
// sentinel order; an absent first table short-circuits the rest.
func expectIgnoredSentinelProbes(mock sqlmock.Sqlmock, present bool) {
	if !present {
		expectTableProbe(mock, ignoredSource.sentinelTables[0], false)
		return
	}
	for _, table := range ignoredSource.sentinelTables {
		expectTableProbe(mock, table, true)
	}
	for _, sc := range ignoredSource.sentinelColumns {
		expectColumnProbe(mock, sc.table, sc.column, true)
	}
}

// expectCommittableTables mocks existingCommittableTables: SHOW FULL TABLES
// listing tables as base tables, then one dolt_ignore check per table, none
// of them ignored.
func expectCommittableTables(mock sqlmock.Sqlmock, tables ...string) {
	rows := sqlmock.NewRows([]string{"Tables_in_beads", "Table_type"})
	for _, t := range tables {
		rows.AddRow(t, "BASE TABLE")
	}
	mock.ExpectQuery(`^SHOW FULL TABLES$`).WillReturnRows(rows)
	for _, t := range tables {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_ignore WHERE ignored = 1 AND ? LIKE pattern")).
			WithArgs(t).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	}
}

// expectPKProbe mocks HasPrimaryKey(table) on a present table.
func expectPKProbe(mock sqlmock.Sqlmock, table string, hasPK bool) {
	expectShowTables(mock, "unrelated_table", table)
	rows := sqlmock.NewRows([]string{"Table", "Non_unique", "Key_name", "Seq_in_index", "Column_name"})
	rows.AddRow(table, 1, "idx_unrelated", 1, "x")
	if hasPK {
		rows.AddRow(table, 0, "PRIMARY", 1, "id")
	}
	mock.ExpectQuery("^" + regexp.QuoteMeta("SHOW KEYS FROM "+quoteIdent(table)) + "$").WillReturnRows(rows)
}
