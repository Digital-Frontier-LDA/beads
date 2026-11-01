package schema

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// See verifiedCursorMemoOff: the scripted sqlmock sequences in this
	// package assume every cursor read probes. cursor_memo_test.go opts back
	// in per test with enableVerifiedCursorMemo.
	verifiedCursorMemoOff = true
	os.Exit(m.Run())
}

// enableVerifiedCursorMemo turns the memo on, empty, for one test and restores
// the package default (off, empty) afterwards.
func enableVerifiedCursorMemo(t *testing.T) {
	t.Helper()
	resetVerifiedCursorMemo()
	verifiedCursorMemoOff = false
	t.Cleanup(func() {
		verifiedCursorMemoOff = true
		resetVerifiedCursorMemo()
	})
}

// resetVerifiedCursorMemo clears the process-wide verified-cursor memo.
func resetVerifiedCursorMemo() {
	verifiedCursorMu.Lock()
	defer verifiedCursorMu.Unlock()
	verifiedCursorSeen = map[string]struct{}{}
}
