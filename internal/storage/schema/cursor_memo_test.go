package schema

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// expectIgnoredCursorRead mocks one ignoredSource.currentVersion read up to,
// and excluding, the sentinel probes.
func expectIgnoredCursorRead(mock sqlmock.Sqlmock, version int) {
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", version)
}

// readIgnoredCursor reads the ignored cursor twice under ctx; sqlmock's
// ordered expectations are the assertion about which reads probed.
func readIgnoredCursorTwice(t *testing.T, ctx context.Context, db DBConn) (first, second int) {
	t.Helper()
	var err error
	if first, err = ignoredSource.currentVersion(ctx, db); err != nil {
		t.Fatalf("first currentVersion: %v", err)
	}
	if second, err = ignoredSource.currentVersion(ctx, db); err != nil {
		t.Fatalf("second currentVersion: %v", err)
	}
	return first, second
}

// TestVerifiedCursorMemoSkipsSecondProbe is AC2 of beads-jwn: once the schema
// has corroborated an at-latest cursor for a database in this process, later
// reads of that cursor skip the sentinel probes. sqlmock rejects any query not
// expected below, so a second round of probes fails the test.
func TestVerifiedCursorMemoSkipsSecondProbe(t *testing.T) {
	enableVerifiedCursorMemo(t)
	db, mock := newProbeMock(t)
	ctx := withVerifiedCursorMemo(context.Background(), "beads")

	expectIgnoredCursorRead(mock, LatestIgnoredVersion())
	expectIgnoredSentinelProbes(mock, true)
	expectIgnoredCursorRead(mock, LatestIgnoredVersion())

	first, second := readIgnoredCursorTwice(t, ctx, db)
	if first != LatestIgnoredVersion() || second != LatestIgnoredVersion() {
		t.Fatalf("cursor reads = %d, %d; want %d twice", first, second, LatestIgnoredVersion())
	}
}

// A contradicted cursor is never memoized: the gh 5033 heal must keep
// disbelieving it on every read until the schema really corroborates it.
func TestVerifiedCursorMemoNeverRecordsContradictedCursor(t *testing.T) {
	enableVerifiedCursorMemo(t)
	db, mock := newProbeMock(t)
	ctx := withVerifiedCursorMemo(context.Background(), "beads")

	for range 2 {
		expectIgnoredCursorRead(mock, LatestIgnoredVersion())
		expectIgnoredSentinelProbes(mock, false)
	}

	first, second := readIgnoredCursorTwice(t, ctx, db)
	if first != 0 || second != 0 {
		t.Fatalf("contradicted cursor reads = %d, %d; want 0 twice (disbelieved both times)", first, second)
	}
}

// The column sentinel (leases.granted_node) gets the same treatment: tables
// present, column missing, is contradicted on every read.
func TestVerifiedCursorMemoNeverRecordsMissingSentinelColumn(t *testing.T) {
	enableVerifiedCursorMemo(t)
	db, mock := newProbeMock(t)
	ctx := withVerifiedCursorMemo(context.Background(), "beads")

	for range 2 {
		expectIgnoredCursorRead(mock, LatestIgnoredVersion())
		for _, table := range ignoredSource.sentinelTables {
			expectTableProbe(mock, table, true)
		}
		sc := ignoredSource.sentinelColumns[0]
		expectColumnProbe(mock, sc.table, sc.column, false)
	}

	first, second := readIgnoredCursorTwice(t, ctx, db)
	if first != 0 || second != 0 {
		t.Fatalf("cursor reads with %s missing = %d, %d; want 0 twice", "leases.granted_node", first, second)
	}
}

// Only an AT-LATEST corroborated cursor is memoized; a cursor behind latest
// has pending work and is re-verified on each read.
func TestVerifiedCursorMemoIgnoresCursorBehindLatest(t *testing.T) {
	enableVerifiedCursorMemo(t)
	db, mock := newProbeMock(t)
	ctx := withVerifiedCursorMemo(context.Background(), "beads")

	behind := LatestIgnoredVersion() - 1
	for range 2 {
		expectIgnoredCursorRead(mock, behind)
		expectIgnoredSentinelProbes(mock, true)
	}
	first, second := readIgnoredCursorTwice(t, ctx, db)
	if first != behind || second != behind {
		t.Fatalf("cursor reads = %d, %d; want %d twice", first, second, behind)
	}
}

// A probe error is surfaced (TestCursorProbeErrorIsNotTreatedAsContradicted)
// and never memoized: the next read probes again.
func TestVerifiedCursorMemoIgnoresProbeError(t *testing.T) {
	enableVerifiedCursorMemo(t)
	db, mock := newProbeMock(t)
	ctx := withVerifiedCursorMemo(context.Background(), "beads")

	boom := errors.New("connection reset")
	expectIgnoredCursorRead(mock, LatestIgnoredVersion())
	expectShowTablesError(mock, boom)
	expectIgnoredCursorRead(mock, LatestIgnoredVersion())
	expectIgnoredSentinelProbes(mock, true)

	if _, err := ignoredSource.currentVersion(ctx, db); !errors.Is(err, boom) {
		t.Fatalf("first currentVersion err = %v, want %v", err, boom)
	}
	if got, err := ignoredSource.currentVersion(ctx, db); err != nil || got != LatestIgnoredVersion() {
		t.Fatalf("second currentVersion = (%d, %v), want (%d, nil)", got, err, LatestIgnoredVersion())
	}
}

// The memo is opt-in: without a database name in the context (embedded Dolt,
// whose tests drop sentinel tables in-process and expect the next MigrateUp to
// heal them) every read probes.
func TestVerifiedCursorMemoOffWithoutDatabaseInContext(t *testing.T) {
	enableVerifiedCursorMemo(t)
	db, mock := newProbeMock(t)

	for range 2 {
		expectIgnoredCursorRead(mock, LatestIgnoredVersion())
		expectIgnoredSentinelProbes(mock, true)
	}
	readIgnoredCursorTwice(t, context.Background(), db)
}

// The memo is keyed by database: verifying one database says nothing about
// another on the same server.
func TestVerifiedCursorMemoIsPerDatabase(t *testing.T) {
	enableVerifiedCursorMemo(t)
	db, mock := newProbeMock(t)

	for range 2 {
		expectIgnoredCursorRead(mock, LatestIgnoredVersion())
		expectIgnoredSentinelProbes(mock, true)
	}
	if _, err := ignoredSource.currentVersion(withVerifiedCursorMemo(context.Background(), "df_grafana"), db); err != nil {
		t.Fatal(err)
	}
	if _, err := ignoredSource.currentVersion(withVerifiedCursorMemo(context.Background(), "akash_console"), db); err != nil {
		t.Fatal(err)
	}
}

// MigrateUpWithLock is the production opt-in. Two converged opens of the same
// database in one process: the second reads both cursors but issues no
// sentinel probe.
func TestMigrateUpWithLockMemoizesVerifiedCursor(t *testing.T) {
	enableVerifiedCursorMemo(t)
	conn, mock, cleanup := newMockConn(t)
	defer cleanup()

	expectConvergedProbe(mock, "testdb")
	// Second open: expectConvergedProbe minus expectIgnoredSentinelProbes.
	expectCurrentDatabase(mock, "testdb")
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
	expectIgnoredCursorRead(mock, LatestIgnoredVersion())
	expectContentHashColumnExists(mock)
	expectContentHashColumnExists(mock)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_types", "count", 1)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_statuses", "count", 1)
	expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
	expectMigrationLockProbe(mock, "testdb", 1)

	for i := range 2 {
		if _, err := MigrateUpWithLock(context.Background(), conn, "testdb"); err != nil {
			t.Fatalf("MigrateUpWithLock #%d: %v", i+1, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
