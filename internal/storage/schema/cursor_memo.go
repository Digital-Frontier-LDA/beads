package schema

import (
	"context"
	"sync"
)

// verifiedCursorMemo records, per process, the (database, cursor table) pairs
// whose at-latest cursor the schema has already corroborated, so
// currentVersion skips cursorContradictedBySchema's sentinel probes on every
// later read in the same process (beads-jwn).
//
// Scope, and why skipping is sound:
//
//   - The sentinels exist to catch a database MATERIALIZED out of band: a
//     table-by-table copy, dump restore or clone that carried the clone-local
//     cursor rows without the tables they describe (gh 5033, gh 4356) or the
//     historical ignored-v16 collision that left leases without granted_node.
//     That damage is present before this process opens the database, so the
//     first read in the process sees it and heals it; nothing in bd removes a
//     sentinel table or column afterwards.
//   - Only an at-latest, corroborated read is memoized. A contradicted
//     cursor, a probe error, or a cursor behind latest is never recorded, so
//     the heal path (cursor_reality_test.go) runs exactly as before on every
//     read until the schema really does corroborate the cursor.
//   - The memo is opt-in through the context (withVerifiedCursorMemo), and
//     only MigrateUpWithLock opts in, keyed by the server identity its caller
//     passes (WithServerIdentity: a transport endpoint, never credentials)
//     plus its databaseName. Two servers each holding a database named
//     "beads" are two keys; with no server identity the memo stays off. That is the
//     shared sql-server path where each probe costs a cross-database scan and
//     where one process opens the database through more than one layer.
//     Embedded Dolt (bare MigrateUp) never opts in: its tests drop sentinel
//     tables in-process and expect the next MigrateUp to heal them.
//
// The one thing a process-lifetime memo cannot see is a sentinel dropped out
// of band WHILE a long-lived process holds the database open; such a process
// heals it on its next restart rather than its next open.
var (
	verifiedCursorMu   sync.Mutex
	verifiedCursorSeen = map[string]struct{}{}
)

// verifiedCursorMemoOff disables the memo process-wide. The package's own
// tests set it in TestMain: their ordered sqlmock expectations script every
// cursor read's probes and reuse one database name across tests, so a memo
// carried from one test into the next would change the query sequence. The
// memo's own tests switch it back on.
var verifiedCursorMemoOff bool

type verifiedCursorMemoCtxKey struct{}

// verifiedCursorScope is the memo scope a context carries: which server, and
// which database on it.
type verifiedCursorScope struct {
	server   string
	database string
}

// withVerifiedCursorMemo opts the reads made under ctx into the memo for
// databaseName on the server named by serverIdentity. Either one empty leaves
// ctx unchanged (memo off): a database name alone would alias same-named
// databases on different servers.
func withVerifiedCursorMemo(ctx context.Context, serverIdentity, databaseName string) context.Context {
	if serverIdentity == "" || databaseName == "" {
		return ctx
	}
	return context.WithValue(ctx, verifiedCursorMemoCtxKey{}, verifiedCursorScope{server: serverIdentity, database: databaseName})
}

// verifiedCursorMemoKey returns the memo key for m under ctx, and whether the
// memo applies at all. A series with no sentinels never probes, so it has
// nothing to memoize.
func verifiedCursorMemoKey(ctx context.Context, m migrationSource) (string, bool) {
	if verifiedCursorMemoOff || (len(m.sentinelTables) == 0 && len(m.sentinelColumns) == 0) {
		return "", false
	}
	scope, ok := ctx.Value(verifiedCursorMemoCtxKey{}).(verifiedCursorScope)
	if !ok || scope.server == "" || scope.database == "" {
		return "", false
	}
	// NUL cannot appear in an endpoint, database or table name, so the key is
	// unambiguous.
	return scope.server + "\x00" + scope.database + "\x00" + m.cursorTable, true
}

func verifiedCursorMemoHas(key string) bool {
	verifiedCursorMu.Lock()
	defer verifiedCursorMu.Unlock()
	_, ok := verifiedCursorSeen[key]
	return ok
}

func verifiedCursorMemoStore(key string) {
	verifiedCursorMu.Lock()
	defer verifiedCursorMu.Unlock()
	verifiedCursorSeen[key] = struct{}{}
}
