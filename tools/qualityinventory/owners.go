package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// routeOwners answers "which module serves this route?" from the source that
// serves it: every adapter declares its routes in a `routes.go` that lists the
// path literals, so the owner of a route is the module whose routes.go declares
// it. It is read rather than guessed — the contract's tags are display names
// ("auth", "arenas"), and a tag is not a package: `/login` is served by
// internal/identity, and a report that said otherwise would be wrong in the one
// column a reader would trust.
//
// A route nothing declares — the operator surface, whose routes live in the
// command that starts the server — belongs to the platform, and saying so is
// better than leaving it ownerless: an owner that is a real package is what
// lets the join ask whether any rule reaches it.
type routeOwners struct {
	byPath map[string]string
}

func (owners routeOwners) of(path string) string {
	if owner, ok := owners.byPath[path]; ok {
		return owner
	}
	return "internal/platform"
}

// routeDeclarationPattern reads one route of an adapter's route table.
var routeDeclarationPattern = regexp.MustCompile(`Path:\s*"([^"]+)"`)

// modulePattern reads the module out of an adapter path:
// internal/<module>/adapters/<kind>/routes.go.
var modulePattern = regexp.MustCompile(`^internal/([^/]+)/`)

// readRouteOwners walks the adapters and builds the path-to-module index. The
// walk is over the module packages only: the command directory registers the
// operational routes, and those are the platform's by the fallback.
func readRouteOwners(root string) (routeOwners, error) {
	owners := routeOwners{byPath: map[string]string{}}
	internal := filepath.Join(root, "internal")
	err := filepath.WalkDir(internal, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != "routes.go" {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		match := modulePattern.FindStringSubmatch(filepath.ToSlash(relative))
		if match == nil {
			return nil
		}
		owner := "internal/" + match[1]
		for _, declaration := range routeDeclarationPattern.FindAllStringSubmatch(string(raw), -1) {
			// The first module in sorted order wins an ambiguous path: two
			// modules declaring the same route is a fact about the tree, not
			// something the inventory can resolve, and the report says which
			// package it attributed the route to.
			if existing, seen := owners.byPath[declaration[1]]; seen && existing <= owner {
				continue
			}
			owners.byPath[declaration[1]] = owner
		}
		return nil
	})
	if err != nil {
		return owners, err
	}
	if len(owners.byPath) == 0 {
		return owners, errDeclaresNoRoute
	}
	return owners, nil
}

// errDeclaresNoRoute is refused rather than tolerated: an index that came out
// empty would attribute every route to the platform and the join would then be
// answering a question about an index nobody built.
var errDeclaresNoRoute = &routeIndexError{}

type routeIndexError struct{}

func (e *routeIndexError) Error() string {
	return "no adapter declares a route: the path-to-module index is empty, and every owner would be a guess"
}
