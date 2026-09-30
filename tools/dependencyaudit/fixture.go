package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// fixture is one proof: the register and the tree it describes, in one document.
//
// The fixture of this gate is a **tree document** and not a directory of files,
// for the same reason the fixture of the change gate is a change document: what
// a rule is proved about is a set of manifests, and thirteen directories of
// three files each would bury the anti-pattern in a filesystem instead of
// showing it in one page. The document is materialized into a temporary
// directory before it is judged, so the parsers read real files with real
// names — the fixture is the description of a tree, and the gate reads the tree.
type fixture struct {
	Note     string            `json:"note"`
	Register register          `json:"register"`
	Files    map[string]string `json:"files"`
}

// readFixture reads one fixture document.
func readFixture(path string) (fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fixture{}, err
	}
	var document fixture
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return fixture{}, fmt.Errorf("%s does not decode as a tree fixture: it declares `note`, the `register` and the `files` of the tree: %w", path, err)
	}
	if len(document.Files) == 0 {
		return fixture{}, fmt.Errorf("%s describes no file: a fixture that proves nothing is the shape of a proof that stopped working", path)
	}
	return document, nil
}

// materialize writes the described tree somewhere temporary and answers where.
// Every path is relative to the fixture root and a path that escapes it is
// refused: a fixture that writes outside its own tree is not a fixture.
func materialize(document fixture) (string, func(), error) {
	root, err := os.MkdirTemp("", "dependencyaudit-fixture-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { os.RemoveAll(root) }
	paths := make([]string, 0, len(document.Files))
	for name := range document.Files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	for _, name := range paths {
		relative := filepath.FromSlash(name)
		if strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
			cleanup()
			return "", func() {}, fmt.Errorf("a fixture descreve o caminho %q, que sai da própria árvore", name)
		}
		target := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			cleanup()
			return "", func() {}, err
		}
		if err := os.WriteFile(target, []byte(document.Files[name]), 0o644); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	return root, cleanup, nil
}
