package schema

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

func newProbeMock(t *testing.T) (DBConn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sql expectations: %v", err)
		}
		_ = db.Close()
	})
	return db, mock
}

// TestTableExistsMatchesExactly: a name is never treated as a pattern. "_" is
// LIKE's single-character wildcard, so a LIKE-only probe for wisp_deps would
// accept wispXdeps; and a prefix or case variant is a different name.
func TestTableExistsMatchesExactly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		listed []string
		probe  string
		want   bool
	}{
		{"exact", []string{"issues", "wisp_dependencies"}, "wisp_dependencies", true},
		{"underscore is not a wildcard", []string{"wispXdependencies"}, "wisp_dependencies", false},
		{"percent is not a wildcard", []string{"wisps"}, "wisp%", false},
		{"prefix is not a match", []string{"wisps_extra"}, "wisps", false},
		{"no tables", nil, "wisps", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newProbeMock(t)
			expectShowTables(mock, tc.listed...)
			got, err := TableExists(context.Background(), db, tc.probe)
			if err != nil {
				t.Fatalf("TableExists: %v", err)
			}
			if got != tc.want {
				t.Fatalf("TableExists(%q) over %v = %v, want %v", tc.probe, tc.listed, got, tc.want)
			}
		})
	}
}

// The INFORMATION_SCHEMA probe compared against DATABASE(), NULL on a session
// with no database selected, and so answered "absent". SHOW TABLES errors
// there instead; that must still read as absent, not fail the open.
func TestTableExistsNoDatabaseSelectedIsAbsent(t *testing.T) {
	db, mock := newProbeMock(t)
	expectShowTablesError(mock, &mysql.MySQLError{Number: 1046, Message: "No database selected"})
	got, err := TableExists(context.Background(), db, "wisps")
	if err != nil || got {
		t.Fatalf("TableExists with no database = (%v, %v), want (false, nil)", got, err)
	}
}

func TestTableExistsPropagatesOtherErrors(t *testing.T) {
	db, mock := newProbeMock(t)
	boom := errors.New("connection reset")
	expectShowTablesError(mock, boom)
	got, err := TableExists(context.Background(), db, "wisps")
	if !errors.Is(err, boom) || got {
		t.Fatalf("TableExists = (%v, %v), want (false, %v)", got, err, boom)
	}
}

func TestColumnExistsMatchesExactly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields []string
		probe  string
		want   bool
	}{
		{"exact", []string{"issue_id", "granted_node"}, "granted_node", true},
		{"underscore is not a wildcard", []string{"grantedXnode"}, "granted_node", false},
		{"prefix is not a match", []string{"granted_node_v2"}, "granted_node", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newProbeMock(t)
			expectShowTables(mock, "leases")
			mock.ExpectQuery("^" + regexp.QuoteMeta("SHOW COLUMNS FROM `leases`") + "$").
				WillReturnRows(showColumnsRows(tc.fields...))
			got, err := ColumnExists(context.Background(), db, "leases", tc.probe)
			if err != nil {
				t.Fatalf("ColumnExists: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ColumnExists(leases, %q) over %v = %v, want %v", tc.probe, tc.fields, got, tc.want)
			}
		})
	}
}

// A missing table has no columns, and the probe must find that out WITHOUT
// issuing SHOW COLUMNS against it: a failing statement pins the Dolt session
// to a stale catalog snapshot (be-bv7x). sqlmock fails the test on any query
// beyond the one SHOW TABLES expected here.
func TestColumnExistsMissingTableIsFalseWithoutFailingStatement(t *testing.T) {
	db, mock := newProbeMock(t)
	expectShowTables(mock, "issues")
	got, err := ColumnExists(context.Background(), db, "leases", "granted_node")
	if err != nil || got {
		t.Fatalf("ColumnExists on a missing table = (%v, %v), want (false, nil)", got, err)
	}
}

// A table dropped between SHOW TABLES and SHOW COLUMNS is also just absent.
func TestColumnExistsConcurrentDropIsFalse(t *testing.T) {
	db, mock := newProbeMock(t)
	expectShowTables(mock, "leases")
	mock.ExpectQuery("^" + regexp.QuoteMeta("SHOW COLUMNS FROM `leases`") + "$").
		WillReturnError(&mysql.MySQLError{Number: 1146, Message: "table not found: leases"})
	got, err := ColumnExists(context.Background(), db, "leases", "granted_node")
	if err != nil || got {
		t.Fatalf("ColumnExists after a concurrent drop = (%v, %v), want (false, nil)", got, err)
	}
}

func TestHasPrimaryKey(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		db, mock := newProbeMock(t)
		expectPKProbe(mock, "dependencies", true)
		got, err := HasPrimaryKey(context.Background(), db, "dependencies")
		if err != nil || !got {
			t.Fatalf("HasPrimaryKey = (%v, %v), want (true, nil)", got, err)
		}
	})
	t.Run("secondary indexes only", func(t *testing.T) {
		db, mock := newProbeMock(t)
		expectPKProbe(mock, "dependencies", false)
		got, err := HasPrimaryKey(context.Background(), db, "dependencies")
		if err != nil || got {
			t.Fatalf("HasPrimaryKey = (%v, %v), want (false, nil)", got, err)
		}
	})
	t.Run("an index merely named like primary", func(t *testing.T) {
		db, mock := newProbeMock(t)
		expectShowTables(mock, "dependencies")
		mock.ExpectQuery("^" + regexp.QuoteMeta("SHOW KEYS FROM `dependencies`") + "$").
			WillReturnRows(sqlmock.NewRows([]string{"Table", "Non_unique", "Key_name"}).
				AddRow("dependencies", 0, "PRIMARY_old").
				AddRow("dependencies", 0, "primary_idx"))
		got, err := HasPrimaryKey(context.Background(), db, "dependencies")
		if err != nil || got {
			t.Fatalf("HasPrimaryKey = (%v, %v), want (false, nil)", got, err)
		}
	})
	t.Run("missing table", func(t *testing.T) {
		db, mock := newProbeMock(t)
		expectShowTables(mock)
		got, err := HasPrimaryKey(context.Background(), db, "dependencies")
		if err != nil || got {
			t.Fatalf("HasPrimaryKey on a missing table = (%v, %v), want (false, nil)", got, err)
		}
	})
}

// A table name is spliced into SHOW COLUMNS / SHOW KEYS as a quoted
// identifier; an embedded backtick must be doubled, not end the quoting.
func TestProbeQuotesIdentifiers(t *testing.T) {
	if got, want := quoteIdent("we`ird"), "`we``ird`"; got != want {
		t.Fatalf("quoteIdent = %q, want %q", got, want)
	}
	db, mock := newProbeMock(t)
	expectShowTables(mock, "we`ird")
	mock.ExpectQuery("^" + regexp.QuoteMeta("SHOW COLUMNS FROM `we``ird`") + "$").
		WillReturnRows(showColumnsRows("c"))
	got, err := ColumnExists(context.Background(), db, "we`ird", "c")
	if err != nil || !got {
		t.Fatalf("ColumnExists = (%v, %v), want (true, nil)", got, err)
	}
}
