package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
)

// ownershipMethods are the methods that make a type ownable: a type that declares
// one of them can be waited for, stopped or closed, so the goroutine that runs on
// it names an owner even when it never mentions a context.
var ownershipMethods = map[string]bool{
	"Stop": true, "Close": true, "Wait": true, "Shutdown": true, "Join": true, "ShutdownAndWait": true,
}

// moduleIndex answers the questions that belong to the tree and not to the file.
// Two of this gate's rules are questions about a declaration somewhere else:
// whether the function a statement calls answers with an error (the ignored
// error), and whether the method a goroutine starts belongs to a type that can be
// stopped (the ownerless goroutine). Both are answered by reading the
// declarations of the module, and only the module: a call into the standard
// library or a vendor is left to the gap the report prints.
type moduleIndex struct {
	root         string
	paths        map[string]string // file path -> package directory
	returnsError map[string]map[string]bool
	aliases      map[string]map[string]string   // file path -> import alias -> directory
	methods      map[string]map[string][]string // directory -> type name -> method names
	owners       map[string]map[string]bool     // directory -> method name -> belongs to an ownable type
	ownableTypes map[string]map[string]bool     // directory -> type name -> declares a way to stop it
	closesGiven  map[string]map[string]bool     // directory -> function or method name -> closes a value it was handed
}

func newModuleIndex() *moduleIndex {
	return &moduleIndex{
		paths:        map[string]string{},
		returnsError: map[string]map[string]bool{},
		aliases:      map[string]map[string]string{},
		methods:      map[string]map[string][]string{},
		owners:       map[string]map[string]bool{},
		ownableTypes: map[string]map[string]bool{},
		closesGiven:  map[string]map[string]bool{},
	}
}

// moduleRoot reads the module path from the go.mod of the tree the gate was
// pointed at: an import is the module's own when it starts with that path, and
// the directory of the package is what is left after it.
func moduleRoot() (string, error) {
	raw, err := os.ReadFile("go.mod")
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if after, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(after), nil
		}
	}
	return "", fmt.Errorf("go.mod declares no module path: the gate cannot tell an import of this tree from a foreign one")
}

// read records what one file declares. The directory of a file is its package,
// and the receiver type of a method is read from the declaration so that a
// goroutine started on it can be traced back to its owner.
func (index *moduleIndex) read(path string, file *ast.File) {
	directory := filepath.ToSlash(filepath.Dir(path))
	index.paths[path] = directory
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if closesWhatItWasGiven(function) {
			if index.closesGiven[directory] == nil {
				index.closesGiven[directory] = map[string]bool{}
			}
			index.closesGiven[directory][function.Name.Name] = true
		}
		if function.Recv == nil {
			if index.returnsError[directory] == nil {
				index.returnsError[directory] = map[string]bool{}
			}
			index.returnsError[directory][function.Name.Name] = answersWithError(function.Type.Results)
			continue
		}
		receiver := receiverName(function)
		if receiver == "" {
			continue
		}
		if index.methods[directory] == nil {
			index.methods[directory] = map[string][]string{}
		}
		index.methods[directory][receiver] = append(index.methods[directory][receiver], function.Name.Name)
	}
}

// closesWhatItWasGiven answers whether a declaration closes one of the values it
// was handed: the receiver or one of its named parameters reaches a `Close`
// method in its own body. It is the connection-server shape — the goroutine runs
// the function that owns the connection it serves —, and it is a question about
// the declaration, not about the call site, which is why the index asks it here.
func closesWhatItWasGiven(function *ast.FuncDecl) bool {
	if function.Body == nil {
		return false
	}
	handed := map[string]bool{}
	if name := receiverName(function); name != "" {
		handed[name] = true
	}
	for _, field := range function.Type.Params.List {
		for _, name := range field.Names {
			if name.Name != "_" {
				handed[name.Name] = true
			}
		}
	}
	if len(handed) == 0 {
		return false
	}
	closes := false
	ast.Inspect(function.Body, func(node ast.Node) bool {
		if closes {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Close" {
			return true
		}
		closes = handed[selectorText(selector.X)]
		return true
	})
	return closes
}

// finish computes what can only be known once every declaration was read: which
// methods belong to a type that can be stopped, waited for or closed.
func (index *moduleIndex) finish() error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	index.root = root
	for directory, types := range index.methods {
		for _, names := range types {
			owner := false
			for _, name := range names {
				if ownershipMethods[name] {
					owner = true
				}
			}
			if !owner {
				continue
			}
			if index.owners[directory] == nil {
				index.owners[directory] = map[string]bool{}
			}
			for _, name := range names {
				index.owners[directory][name] = true
			}
		}
	}
	for directory, types := range index.methods {
		for typeName, names := range types {
			for _, name := range names {
				if !ownershipMethods[name] {
					continue
				}
				if index.ownableTypes[directory] == nil {
					index.ownableTypes[directory] = map[string]bool{}
				}
				index.ownableTypes[directory][typeName] = true
			}
		}
	}
	return nil
}

// readAliases records the imports of a file, which is how a selector on an
// imported package is traced back to the directory of that package.
func (index *moduleIndex) readAliases(path string, file *ast.File) {
	aliases := map[string]string{}
	for _, specification := range file.Imports {
		importPath := strings.Trim(literalText(specification.Path), `"`)
		directory, ok := index.directoryOf(importPath)
		if !ok {
			continue
		}
		name := filepath.Base(importPath)
		if specification.Name != nil {
			name = specification.Name.Name
		}
		aliases[name] = directory
	}
	index.aliases[path] = aliases
}

// directoryOf maps an import path to the directory of the package when the
// import is the module's own.
func (index *moduleIndex) directoryOf(importPath string) (string, bool) {
	after, ok := strings.CutPrefix(importPath, index.root+"/")
	if !ok {
		return "", false
	}
	return after, true
}

// callAnswersWithError answers whether a call names a function of this module
// whose declaration answers with an error. A call the gate cannot trace — a
// method, a standard library function, a variable holding a function — is not
// guessed: it is counted, and the report prints how many were left out.
func (index *moduleIndex) callAnswersWithError(path string, call *ast.CallExpr) (bool, bool) {
	directory := index.paths[path]
	switch callee := call.Fun.(type) {
	case *ast.Ident:
		answers, known := index.returnsError[directory][callee.Name]
		return answers, known
	case *ast.SelectorExpr:
		prefix, ok := callee.X.(*ast.Ident)
		if !ok {
			return false, false
		}
		imported, ok := index.aliases[path][prefix.Name]
		if !ok {
			return false, false
		}
		answers, known := index.returnsError[imported][callee.Sel.Name]
		return answers, known
	}
	return false, false
}

// methodBelongsToOwner answers whether a method name started by a goroutine in
// this directory belongs to a type that declares a way to stop it. A method the
// gate cannot trace is not guessed.
func (index *moduleIndex) methodBelongsToOwner(path, name string) bool {
	return index.owners[index.paths[path]][name]
}

// closesWhatItWasGiven answers whether the function of this directory started by
// a goroutine closes a value it was handed.
func (index *moduleIndex) closesWhatItWasGiven(path, name string) bool {
	return index.closesGiven[index.paths[path]][name]
}

// typeOwns answers whether a type declared in this directory declares a way to
// stop it. A type the gate cannot trace is not guessed.
func (index *moduleIndex) typeOwns(path, typeName string) bool {
	return index.ownableTypes[index.paths[path]][typeName]
}

// answersWithError reports whether a result list carries an error, which is the
// question that makes a discarded call an ignored error.
func answersWithError(results *ast.FieldList) bool {
	if results == nil {
		return false
	}
	for _, field := range results.List {
		if identifier, ok := field.Type.(*ast.Ident); ok && identifier.Name == "error" {
			return true
		}
	}
	return false
}

// receiverName reads the type a method is declared on, which is the name the
// ownership index is keyed by.
func receiverName(function *ast.FuncDecl) string {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		return ""
	}
	switch receiver := function.Recv.List[0].Type.(type) {
	case *ast.Ident:
		return receiver.Name
	case *ast.StarExpr:
		if identifier, ok := receiver.X.(*ast.Ident); ok {
			return identifier.Name
		}
	}
	return ""
}
