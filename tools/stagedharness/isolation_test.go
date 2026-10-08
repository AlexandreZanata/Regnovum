package stagedharness_test

// P56-T02 — the isolation guard: the four staged HTTP adapters must
// stay out of every production wiring. Only test files (here, the
// per-module suites and this harness) may import them. Anyone wiring
// a staged handler into bootstrap, cmd/ or any other non-test
// package trips this guard before the route can ever be served —
// that is the "tentativa de expor staged por engano" detector the
// phase requires, and it runs without a database.

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var stagedAdapterImports = []string{
	"github.com/AlexandreZanata/Regnovum/internal/seasons/adapters/http",
	"github.com/AlexandreZanata/Regnovum/internal/metering/adapters/http",
	"github.com/AlexandreZanata/Regnovum/internal/commerce/adapters/http",
	"github.com/AlexandreZanata/Regnovum/internal/disputes/adapters/http",
}

func TestStagedAdaptersStayOutOfProductionWiring(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repository root: %v", err)
	}
	staged := map[string]bool{}
	for _, path := range stagedAdapterImports {
		staged[path] = true
	}
	offenders := []string{}
	scanRoots := []string{"internal", "cmd"}
	for _, top := range scanRoots {
		scanDir := filepath.Join(root, top)
		err := filepath.WalkDir(scanDir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
				full := path
				fset := token.NewFileSet()
				parsed, parseErr := parser.ParseFile(fset, full, nil, parser.ImportsOnly)
				if parseErr != nil {
					t.Fatalf("parse %s: %v", full, parseErr)
				}
				for _, spec := range parsed.Imports {
					importPath := strings.Trim(spec.Path.Value, `"`)
					if staged[importPath] {
						rel, _ := filepath.Rel(root, full)
						offenders = append(offenders, rel+" imports "+importPath)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("staged adapters wired outside tests (production exposure):\n  - %s", strings.Join(offenders, "\n  - "))
	}
}
