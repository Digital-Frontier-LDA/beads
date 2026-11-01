package doctor

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

func TestDoctorDependencyTargetExprMatchesCanonical(t *testing.T) {
	if doctorDependencyTargetExpr != issueops.DepTargetExpr {
		t.Fatalf("doctorDependencyTargetExpr = %q, want %q", doctorDependencyTargetExpr, issueops.DepTargetExpr)
	}
}

func TestCheckMailThreadIntegrityRequiresBothDependencyThreadColumns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectThreadIDColumns(mock, true, false)

	check := checkMailThreadIntegrity(db)
	if check.Status != StatusOK {
		t.Fatalf("Status = %q, want %q: %s", check.Status, StatusOK, check.Message)
	}
	if !strings.Contains(check.Message, "N/A") {
		t.Fatalf("Message = %q, want N/A schema message", check.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCheckMailThreadIntegrityWarnsWhenSchemaGateFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnError(errors.New("information schema unavailable"))

	check := checkMailThreadIntegrity(db)
	if check.Status != StatusWarning {
		t.Fatalf("Status = %q, want %q: %s", check.Status, StatusWarning, check.Message)
	}
	if !strings.Contains(check.Message, "Unable to check thread integrity schema") {
		t.Fatalf("Message = %q, want schema warning", check.Message)
	}
	if !strings.Contains(check.Detail, "information schema unavailable") {
		t.Fatalf("Detail = %q, want query error", check.Detail)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCheckMailThreadIntegrityRunsUnionWhenBothTablesHaveThreadID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectThreadIDColumns(mock, true, true)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT d.thread_id, COUNT(*) as refs")).
		WillReturnRows(sqlmock.NewRows([]string{"thread_id", "refs"}))

	check := checkMailThreadIntegrity(db)
	if check.Status != StatusOK {
		t.Fatalf("Status = %q, want %q: %s", check.Status, StatusOK, check.Message)
	}
	if !strings.Contains(check.Message, "All thread references valid") {
		t.Fatalf("Message = %q, want valid thread message", check.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCheckMailThreadIntegrityWarnsForOrphanedUnionRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectThreadIDColumns(mock, true, true)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT d.thread_id, COUNT(*) as refs")).
		WillReturnRows(sqlmock.NewRows([]string{"thread_id", "refs"}).AddRow("missing-thread", 2))

	check := checkMailThreadIntegrity(db)
	if check.Status != StatusWarning {
		t.Fatalf("Status = %q, want %q: %s", check.Status, StatusWarning, check.Message)
	}
	if !strings.Contains(check.Message, "Found 2 orphaned thread references") {
		t.Fatalf("Message = %q, want orphan warning", check.Message)
	}
	if !strings.Contains(check.Detail, "missing-thread (2 refs)") {
		t.Fatalf("Detail = %q, want missing thread detail", check.Detail)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// expectThreadIDColumns mocks the mail-thread schema gate: schema.ColumnExists
// for dependencies.thread_id, then (only if present) wisp_dependencies.thread_id.
func expectThreadIDColumns(mock sqlmock.Sqlmock, depHas, wispHas bool) {
	for _, tc := range []struct {
		table string
		has   bool
	}{{"dependencies", depHas}, {"wisp_dependencies", wispHas}} {
		mock.ExpectQuery(`^SHOW TABLES$`).
			WillReturnRows(sqlmock.NewRows([]string{"Tables_in_beads"}).AddRow("dependencies").AddRow("wisp_dependencies"))
		fields := sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
			AddRow("issue_id", "varchar(255)", "NO", "", nil, "")
		if tc.has {
			fields.AddRow("thread_id", "varchar(255)", "YES", "", nil, "")
		}
		mock.ExpectQuery("^SHOW COLUMNS FROM `" + tc.table + "`$").WillReturnRows(fields)
		if !tc.has {
			return
		}
	}
}
