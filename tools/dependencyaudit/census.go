package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// component is one thing the tree declares: a module the build requires, a
// package the frontend installs, an image a file names, an action a workflow
// runs, or a version evidence line of a tool. It carries where it was read from
// because a refusal that does not say where is a refusal nobody can check.
type component struct {
	Kind     string
	Name     string
	Version  string
	Digest   string
	File     string
	Line     int
	Indirect bool
	Runtime  bool
}

// censusCounts is what one run measured. The numbers travel in the report
// because a boundary without a number is a boundary nobody reviews.
type censusCounts struct {
	Modules          int
	Direct           int
	Indirect         int
	Manifests        int
	Packages         int
	BrowserManifests int
	Images           int
	ProductionImages int
	Digests          int
	Actions          int
	CommitPins       int
	Tools            int
	Platforms        int
	Entries          int
	Banned           int
	Imports          int
	BareImports      int
	StatementRows    int
	EvidenceSites    int
	EvidenceMissing  int
}

// census is the measured tree: the components it declares, the browser imports
// it holds, the rows its statement document carries and the counts behind all
// of them.
type census struct {
	Components []component
	Imports    []browserImport
	Rows       []statementRow
	Counts     censusCounts
}

// browserImport is one import specifier read from the browser sources.
type browserImport struct {
	Specifier string
	File      string
	Line      int
}

// statementRow is one row of the document that states the licenses: the label a
// human reads and the license the row claims for it.
type statementRow struct {
	Label   string
	Line    int
	License string
	Text    string
}

// skippedDirectories are the directories the census refuses to enter. Generated
// and vendored trees are not the tree: a `package.json` inside `node_modules` is
// another project's dependency, and a fixture inside `testdata` is a fixture.
var skippedDirectories = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "testdata": true,
	".local": true, "dist": true, "test-build": true, "web/generated": true,
}

// censusComponents reads every component the register's paths point at.
func censusComponents(root string, document register) ([]component, error) {
	modules, err := goModules(root, document.Paths.GoMod)
	if err != nil {
		return nil, err
	}
	packages, err := npmPackages(root, document)
	if err != nil {
		return nil, err
	}
	images, err := imageComponents(root, document)
	if err != nil {
		return nil, err
	}
	actions, err := actionComponents(root, document)
	if err != nil {
		return nil, err
	}
	tools, err := toolComponents(root, document)
	if err != nil {
		return nil, err
	}
	components := append(modules, packages...)
	components = append(components, images...)
	components = append(components, actions...)
	return append(components, tools...), nil
}

// goModules reads the module requirements of a `go.mod`: the direct ones and the
// indirect ones, because a transitive package that nobody approved is the case
// this gate exists for.
func goModules(root, file string) ([]component, error) {
	raw, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	components := []component{}
	inBlock := false
	for index, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "require ("):
			inBlock = true
			continue
		case inBlock && trimmed == ")":
			inBlock = false
			continue
		case !inBlock && !strings.HasPrefix(trimmed, "require "):
			continue
		}
		body := strings.TrimPrefix(trimmed, "require ")
		body = strings.TrimSuffix(body, "(")
		fields := strings.Fields(body)
		if len(fields) < 2 {
			continue
		}
		components = append(components, component{
			Kind: kindModule, Name: fields[0], Version: fields[1],
			File: file, Line: index + 1,
			Indirect: strings.Contains(line, "// indirect"),
		})
	}
	return components, nil
}

// npmPackages reads every manifest the register points at. A dependency of the
// `dependencies` block is runtime and one of `devDependencies` is not, which is
// the difference the browser rule reads.
func npmPackages(root string, document register) ([]component, error) {
	files, err := auditkit.Files(root, skippedDirectories, func(name string) bool {
		return name == "package.json"
	})
	if err != nil {
		return nil, err
	}
	components := []component{}
	for _, file := range files {
		relative, err := filepath.Rel(root, file)
		if err != nil {
			return nil, err
		}
		relative = filepath.ToSlash(relative)
		if !matchesAny(document.Paths.Manifests, relative) {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", relative, err)
		}
		var manifest struct {
			Dependencies         map[string]string `json:"dependencies"`
			DevDependencies      map[string]string `json:"devDependencies"`
			OptionalDependencies map[string]string `json:"optionalDependencies"`
			PeerDependencies     map[string]string `json:"peerDependencies"`
		}
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return nil, fmt.Errorf("%s does not decode as a manifest: %w", relative, err)
		}
		for _, group := range []struct {
			packages map[string]string
			runtime  bool
		}{
			{manifest.Dependencies, true},
			{manifest.OptionalDependencies, true},
			{manifest.PeerDependencies, true},
			{manifest.DevDependencies, false},
		} {
			names := make([]string, 0, len(group.packages))
			for name := range group.packages {
				names = append(names, name)
			}
			sortStrings(names)
			for _, name := range names {
				components = append(components, component{
					Kind: kindNPM, Name: name, Version: group.packages[name],
					File: relative, Line: lineOf(string(raw), `"`+name+`"`),
					Runtime: group.runtime,
				})
			}
		}
	}
	return components, nil
}

// imageComponents reads the container references. A `FROM` line and an `image:`
// key are the same declaration read from two kinds of file, and a stage
// reference — `FROM build` — is not an image.
func imageComponents(root string, document register) ([]component, error) {
	files, err := matchedFiles(root, document.Paths.Images)
	if err != nil {
		return nil, err
	}
	components := []component{}
	for _, file := range files {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		stages := map[string]bool{}
		for index, line := range strings.Split(string(raw), "\n") {
			reference, ok := imageReference(line, stages)
			if !ok {
				continue
			}
			name, version, digest := splitImage(reference)
			if name == "" {
				continue
			}
			stages[strings.ToLower(name)] = true
			components = append(components, component{
				Kind: kindImage, Name: name, Version: version, Digest: digest,
				File: file, Line: index + 1,
			})
		}
	}
	return components, nil
}

// imageReference answers the reference a line declares, teaching the caller
// which names are build stages of the same file as it goes.
func imageReference(line string, stages map[string]bool) (string, bool) {
	trimmed := strings.TrimSpace(line)
	var value string
	switch {
	case strings.HasPrefix(strings.ToUpper(trimmed), "FROM "):
		fields := strings.Fields(trimmed[len("FROM "):])
		if len(fields) == 0 {
			return "", false
		}
		value = fields[0]
		if strings.HasPrefix(strings.ToUpper(value), "--") {
			// `FROM --platform=...` names the image in the next field.
			if len(fields) < 2 {
				return "", false
			}
			value = fields[1]
		}
	case strings.HasPrefix(trimmed, "image:"):
		value = strings.TrimSpace(strings.TrimPrefix(trimmed, "image:"))
		value = strings.Trim(value, `"'`)
	default:
		return "", false
	}
	if value == "" || strings.Contains(value, "$") {
		return "", false
	}
	if !strings.ContainsAny(value, ":/@") || stages[strings.ToLower(value)] {
		return "", false
	}
	return value, true
}

// splitImage cuts a reference into the repository, the tag and the digest. The
// digest is what an immutable pin is made of, and a reference without a tag is
// still a reference: `repo@sha256:...` pins the bytes and not the label.
func splitImage(reference string) (string, string, string) {
	digest := ""
	if at := strings.Index(reference, "@"); at >= 0 {
		digest = reference[at+1:]
		reference = reference[:at]
	}
	name, version := reference, ""
	if colon := strings.LastIndex(reference, ":"); colon >= 0 && !strings.Contains(reference[colon+1:], "/") {
		name, version = reference[:colon], reference[colon+1:]
	}
	return name, version, digest
}

// actionComponents reads the actions every workflow runs. A `uses:` that names a
// local path is the repository's own composite action and is not a third party.
func actionComponents(root string, document register) ([]component, error) {
	files, err := matchedFiles(root, document.Paths.Workflows)
	if err != nil {
		return nil, err
	}
	components := []component{}
	for _, file := range files {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		for index, line := range strings.Split(string(raw), "\n") {
			// The step may be written as a list item with the key on its own
			// line or inline after the dash, so the dash is not part of the key.
			trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
			if !strings.HasPrefix(trimmed, "uses:") {
				continue
			}
			reference := strings.TrimSpace(strings.TrimPrefix(trimmed, "uses:"))
			// The tag the workflow keeps as a comment is documentation and not
			// part of the reference: the pin is the first field, and a comment
			// that travelled into the version would make every action drift.
			reference = strings.Trim(reference, `"'`)
			if fields := strings.Fields(reference); len(fields) > 0 {
				reference = fields[0]
			}
			if reference == "" || strings.HasPrefix(reference, "./") {
				continue
			}
			name, version, _ := strings.Cut(reference, "@")
			components = append(components, component{
				Kind: kindAction, Name: name, Version: version, File: file, Line: index + 1,
			})
		}
	}
	return components, nil
}

// toolComponents reads the version every entry declares where the tree declares
// it. A tool whose evidence does not resolve is not silently unpinned: the
// judgment below refuses it by name.
func toolComponents(root string, document register) ([]component, error) {
	components := []component{}
	for _, item := range document.Entries {
		if item.Kind != kindTool || len(item.VersionEvidence) == 0 {
			continue
		}
		for _, site := range item.VersionEvidence {
			raw, err := os.ReadFile(filepath.Join(root, site.Path))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", site.Path, err)
			}
			pattern, err := compileEvidence(site.Regex)
			if err != nil {
				return nil, err
			}
			match := pattern.FindSubmatch(raw)
			if match == nil {
				continue
			}
			components = append(components, component{
				Kind: kindTool, Name: item.Name, Version: string(match[1]), File: site.Path,
			})
		}
	}
	return components, nil
}

// compileEvidence compiles one evidence pattern, requiring exactly one capture
// group: a pattern without a group extracts nothing and is refused as a
// document that promises less than the gate reads into it.
func compileEvidence(pattern string) (*regexp.Regexp, error) {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	if compiled.NumSubexp() != 1 {
		return nil, fmt.Errorf("o padrão de evidência %q captura %d grupo(s) e o portão lê um", pattern, compiled.NumSubexp())
	}
	return compiled, nil
}

// matchedFiles lists the files of the tree that match the declared globs.
func matchedFiles(root string, globs []string) ([]string, error) {
	files, err := auditkit.Files(root, skippedDirectories, func(name string) bool { return true })
	if err != nil {
		return nil, err
	}
	matched := []string{}
	for _, file := range files {
		relative, err := filepath.Rel(root, file)
		if err != nil {
			return nil, err
		}
		relative = filepath.ToSlash(relative)
		if matchesAny(globs, relative) {
			matched = append(matched, relative)
		}
	}
	return matched, nil
}

// browserImports reads every import specifier of the browser sources. The
// browser runs no third party at all, so the rule is about one thing: whether
// the specifier leaves the tree.
func browserImports(root string, document register) ([]browserImport, error) {
	files, err := auditkit.Files(root, skippedDirectories, func(name string) bool {
		return strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".js")
	})
	if err != nil {
		return nil, err
	}
	imports := []browserImport{}
	for _, file := range files {
		relative, err := filepath.Rel(root, file)
		if err != nil {
			return nil, err
		}
		relative = filepath.ToSlash(relative)
		if !underAny(document.Paths.Sources, relative) {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", relative, err)
		}
		for index, line := range strings.Split(string(raw), "\n") {
			for _, specifier := range specifiersOf(line) {
				imports = append(imports, browserImport{Specifier: specifier, File: relative, Line: index + 1})
			}
		}
	}
	return imports, nil
}

var (
	fromSpecifier = regexp.MustCompile(`\bfrom\s+['"]([^'"]+)['"]`)
	bareSpecifier = regexp.MustCompile(`\bimport\s+['"]([^'"]+)['"]`)
)

// specifiersOf answers the module specifiers one line imports.
func specifiersOf(line string) []string {
	found := []string{}
	for _, pattern := range []*regexp.Regexp{fromSpecifier, bareSpecifier} {
		for _, match := range pattern.FindAllStringSubmatch(line, -1) {
			found = append(found, match[1])
		}
	}
	return found
}

// underAny answers whether a path lives under one of the declared directories.
func underAny(directories []string, candidate string) bool {
	for _, directory := range directories {
		trimmed := strings.TrimSuffix(directory, "/")
		if candidate == trimmed || strings.HasPrefix(candidate, trimmed+"/") {
			return true
		}
	}
	return false
}

// statementRows reads the document that states the licenses. The first cell of
// every table row is the label a human reads, and the license token of the row
// is what the entry's claim is compared against.
func statementRows(root, statement string) ([]statementRow, error) {
	raw, err := os.ReadFile(filepath.Join(root, statement))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", statement, err)
	}
	rows := []statementRow{}
	for index, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") || strings.Contains(trimmed, "---") {
			continue
		}
		cells := strings.Split(strings.Trim(trimmed, "|"), "|")
		if len(cells) < 4 {
			continue
		}
		label := strings.ReplaceAll(strings.TrimSpace(cells[0]), "`", "")
		if label == "" || strings.HasPrefix(label, "Dependência") {
			continue
		}
		rows = append(rows, statementRow{
			Label: label, Line: index + 1,
			License: strings.TrimSpace(cells[len(cells)-1]), Text: trimmed,
		})
	}
	return rows, nil
}

// lineOf answers the first line of a document that mentions a text, which is
// what turns a name read out of a manifest into a place a human can open.
func lineOf(body, needle string) int {
	for index, line := range strings.Split(body, "\n") {
		if strings.Contains(line, needle) {
			return index + 1
		}
	}
	return 0
}

// readCensus reads everything one run measures about the tree.
func readCensus(root string, document register) (census, error) {
	components, err := censusComponents(root, document)
	if err != nil {
		return census{}, err
	}
	imports, err := browserImports(root, document)
	if err != nil {
		return census{}, err
	}
	rows, err := statementRows(root, document.Statement)
	if err != nil {
		return census{}, err
	}
	measured := census{Components: components, Imports: imports, Rows: rows}
	count(&measured, document)
	return measured, nil
}

// count fills the numbers the report prints and the boundaries the judgments
// declare.
func count(measured *census, document register) {
	counts := censusCounts{Entries: len(document.Entries), Platforms: 0}
	for _, component := range measured.Components {
		if contains(document.Banned, component.Name) || bannedPrefix(document.Banned, component.Name) {
			counts.Banned++
		}
		switch component.Kind {
		case kindNPM:
			if underAny(document.Paths.BrowserManifests, component.File) {
				counts.BrowserManifests++
			}
		}
		if matchesAny(document.Paths.ProductionImages, component.File) {
			counts.ProductionImages++
		}
		switch component.Kind {
		case kindModule:
			counts.Modules++
			if component.Indirect {
				counts.Indirect++
			} else {
				counts.Direct++
			}
		case kindNPM:
			counts.Packages++
		case kindImage:
			counts.Images++
			if component.Digest != "" {
				counts.Digests++
			}
		case kindAction:
			counts.Actions++
			if isCommitSHA(component.Version) {
				counts.CommitPins++
			}
		case kindTool:
			counts.Tools++
		}
	}
	for _, entry := range document.Entries {
		if entry.Kind == kindPlatform {
			counts.Platforms++
			continue
		}
		counts.EvidenceSites += len(entry.VersionEvidence)
		if entry.Kind == kindTool && len(entry.VersionEvidence) == 0 {
			counts.EvidenceMissing++
		}
	}
	counts.Manifests = countManifests(measured, document)
	for _, importLine := range measured.Imports {
		counts.Imports++
		if !strings.HasPrefix(importLine.Specifier, ".") && !strings.HasPrefix(importLine.Specifier, "/") {
			counts.BareImports++
		}
	}
	counts.StatementRows = len(measured.Rows)
	measured.Counts = counts
}

// isCommitSHA answers whether a reference is a full commit: an action pinned to
// a branch or to a tag is a moving target.
func isCommitSHA(reference string) bool {
	if len(reference) != 40 {
		return false
	}
	for _, character := range reference {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

// manifestRuntime answers whether a manifest declares runtime packages, which is
// the half of the browser rule the manifest carries.
func manifestRuntime(root string, document register) ([]component, error) {
	packages, err := npmPackages(root, document)
	if err != nil {
		return nil, err
	}
	runtime := []component{}
	for _, component := range packages {
		if component.Runtime && underAny(document.Paths.BrowserManifests, component.File) {
			runtime = append(runtime, component)
		}
	}
	return runtime, nil
}

// countManifests answers how many manifests the census read.
func countManifests(measured *census, document register) int {
	files := map[string]bool{}
	for _, component := range measured.Components {
		if component.Kind == kindNPM && matchesAny(document.Paths.Manifests, component.File) {
			files[component.File] = true
		}
	}
	return len(files)
}
