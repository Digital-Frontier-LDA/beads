package schema

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// bannedInformationSchemaViews are the INFORMATION_SCHEMA views Dolt answers
// by materializing every table of every database on the server: it does not
// push TABLE_SCHEMA = DATABASE() down. On a shared sql-server with 132
// databases one COLUMNS probe took 29.6 s (SHOW COLUMNS: 0.6 s), and ~20
// concurrent bd opens ran it at once (beads-jwn). Use TableExists /
// ColumnExists / HasPrimaryKey (probe.go) instead.
var bannedInformationSchemaViews = []string{
	"information_schema.columns",
	"information_schema.tables",
}

// informationSchemaAllowlist holds "<repo-relative path>" entries that may
// still use a banned view, each with the reason it is unavoidable. Empty:
// every former use had a SHOW-based equivalent.
var informationSchemaAllowlist = map[string]string{}

// TestNoInformationSchemaTablesOrColumns fails if any non-test Go file in this
// package or in cmd/bd/doctor (the other place that probes the live schema of
// a possibly shared server, and did so with the same COUNT(*) shape) puts
// INFORMATION_SCHEMA.TABLES or .COLUMNS in a string literal.
//
// It inspects string literals only, not comments, so prose explaining why the
// views are avoided does not trip it; matching is case-insensitive because SQL
// is. Embedded migration .sql files are out of scope: their guards run once per
// migration, not on every open.
func TestNoInformationSchemaTablesOrColumns(t *testing.T) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	pkgDir := filepath.Dir(self)
	repoRoot := filepath.Clean(filepath.Join(pkgDir, "..", "..", ".."))
	roots := []string{pkgDir, filepath.Join(repoRoot, "cmd", "bd", "doctor")}

	scanned := 0
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(repoRoot, path)
			rel = filepath.ToSlash(rel)
			scanned++
			for _, hit := range bannedViewLiterals(t, path) {
				if _, allowed := informationSchemaAllowlist[rel]; allowed {
					continue
				}
				t.Errorf("%s:%d uses %s; Dolt scans every database for it (beads-jwn). "+
					"Use schema.TableExists / ColumnExists / HasPrimaryKey (SHOW TABLES / COLUMNS / KEYS) instead.",
					rel, hit.line, hit.view)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	// A walk that silently finds nothing would pass vacuously.
	if scanned < 20 {
		t.Fatalf("scanned only %d Go files under %v; the walk is not seeing the tree", scanned, roots)
	}
}

type bannedViewHit struct {
	line int
	view string
}

func bannedViewLiterals(t *testing.T, path string) []bannedViewHit {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var hits []bannedViewHit
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		val, err := strconv.Unquote(lit.Value)
		if err != nil {
			val = lit.Value
		}
		lower := strings.ToLower(val)
		for _, view := range bannedInformationSchemaViews {
			if strings.Contains(lower, view) {
				hits = append(hits, bannedViewHit{line: fset.Position(lit.Pos()).Line, view: view})
			}
		}
		return true
	})
	return hits
}

// TestBannedViewLiteralsDetector proves the detector in both polarities: it
// flags a banned view in a string literal in any case and quoting style, and
// ignores the same text in a comment and the views that are not banned.
func TestBannedViewLiteralsDetector(t *testing.T) {
	src := "package p\n" +
		"// INFORMATION_SCHEMA.COLUMNS in a comment is fine\n" +
		"var a = `SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS WHERE x`\n" +
		"var b = \"select 1 from information_schema.tables\"\n" +
		"var c = \"FROM INFORMATION_SCHEMA.STATISTICS\"\n" +
		"var d = \"FROM information_schema.schemata\"\n"
	path := filepath.Join(t.TempDir(), "p.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	hits := bannedViewLiterals(t, path)
	if len(hits) != 2 || hits[0].line != 3 || hits[1].line != 4 {
		t.Fatalf("hits = %+v, want lines 3 (columns) and 4 (tables) only", hits)
	}
}
