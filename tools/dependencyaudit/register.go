package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// registerSchemaVersion is the only version of the register this gate reads. A
// document written against another version is refused instead of interpreted:
// every judgment below it is about a component, and a document that disagreed
// with the loader about what a component is would make all of them a guess.
const registerSchemaVersion = 1

// The kinds a component can be. The vocabulary is closed because the census
// knows how to read exactly these from the tree, and a kind the census does not
// read is a kind whose approval nothing would check. `platform` is the declared
// exception: it is a service the project consumes without a file of its own —
// the CDN, the runner, the SaaS telemetry endpoint — so it has an owner, a
// purpose and a license to prove and no manifest line to be extracted from.
const (
	kindModule   = "module"
	kindNPM      = "npm"
	kindTool     = "tool"
	kindImage    = "image"
	kindAction   = "action"
	kindPlatform = "platform"
)

// The roles a class can play. The role is what decides how strict the pin has
// to be: code that is compiled into the binary or shipped to the browser is
// held to the strictest pin, and a tool isolated from the artifact is allowed
// the licenses the runtime classes are not.
const (
	roleRuntime  = "runtime"
	roleBuild    = "build"
	roleTooling  = "tooling"
	roleInfra    = "infra"
	roleCI       = "ci"
	rolePlatform = "platform"
)

// The pin shapes. `exact` demands a version and nothing but a version; `digest`
// demands the image reference carry an immutable digest in the production
// files; `commit` demands a workflow action point at a full commit SHA; and
// `named` is the declared exception for a tool the tree requires by name and
// whose version is chosen by the machine that runs it — the gate measures that
// population and prints it instead of pretending the pin exists.
const (
	pinExact  = "exact"
	pinDigest = "digest"
	pinCommit = "commit"
	pinNamed  = "named"
)

// registerPath is the versioned document this gate judges: the policy and the
// inventory in one file, because an allowance and the thing it allows are the
// same decision.
const registerPath = "quality/dependencies.json"

// register is what the tree is judged against: the classes with the licenses
// each one homologates, the vocabulary the project bans by name, the paths the
// census reads, and the inventory itself.
type register struct {
	Schema            int      `json:"schema"`
	Note              string   `json:"note"`
	Statement         string   `json:"statement"`
	Banned            []string `json:"banned"`
	BrowserSpecifiers []string `json:"browser_specifiers"`
	Paths             paths    `json:"paths"`
	Classes           []class  `json:"classes"`
	Entries           []entry  `json:"entries"`
}

// paths says where the census looks. Every one of them is versioned here and
// not in the code: a gate that hard-codes the file it reads cannot be told to
// read another one, and a fixture has to be able to say so.
type paths struct {
	GoMod            string   `json:"go_mod"`
	Manifests        []string `json:"manifests"`
	BrowserManifests []string `json:"browser_manifests"`
	Images           []string `json:"images"`
	ProductionImages []string `json:"production_images"`
	Workflows        []string `json:"workflows"`
	Sources          []string `json:"sources"`
	SBOM             string   `json:"sbom"`
}

// class is one class of component, with the licenses it homologates and the pin
// it demands. Both are the policy's decision and not the gate's: what is
// acceptable for code compiled into the binary is not what is acceptable for a
// linter that never touches the artifact, and the table says which is which.
type class struct {
	Name     string   `json:"name"`
	Role     string   `json:"role"`
	Pin      string   `json:"pin"`
	Licenses []string `json:"licenses"`
	Reason   string   `json:"reason"`
}

// entry is one approved component: what it is, which version is approved, who
// owns it, what it is for, how far it reaches and under which license. The
// owner, the purpose and the scope are the reason an allowlist is worth
// anything: a name without them is a name nobody can triage.
type entry struct {
	Kind            string     `json:"kind"`
	Name            string     `json:"name"`
	Version         string     `json:"version"`
	Class           string     `json:"class"`
	Owner           string     `json:"owner"`
	Purpose         string     `json:"purpose"`
	Scope           string     `json:"scope"`
	License         string     `json:"license"`
	Label           string     `json:"label,omitempty"`
	Direct          *bool      `json:"direct,omitempty"`
	VersionEvidence []evidence `json:"version_evidence,omitempty"`
	Note            string     `json:"note,omitempty"`
}

// evidence is where the tree declares a version the manifest does not carry:
// the pinned scanner in the workflow, the tool version in the Makefile. The
// version the tree actually declares is what the entry is compared against, so
// a pin that moved without the register moving is a refusal.
type evidence struct {
	Path  string `json:"path"`
	Regex string `json:"regex"`
}

// labelOf is the name a human reads in the statement document.
func (e entry) labelOf() string {
	if e.Label != "" {
		return e.Label
	}
	return e.Name
}

// classNamed answers the class by name.
func (r register) classNamed(name string) (class, bool) {
	for _, entry := range r.Classes {
		if entry.Name == name {
			return entry, true
		}
	}
	return class{}, false
}

// readRegister reads and validates the register. Every refusal here is about a
// document that promises less than the gate would read into it, and the gate
// would rather stop than judge the tree against a policy that says nothing.
func readRegister(root, file string) (register, error) {
	raw, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		return register{}, fmt.Errorf("%s: %w", file, err)
	}
	var document register
	if err := decodeJSON(raw, &document); err != nil {
		return register{}, fmt.Errorf("%s does not decode as the dependency register, which declares `schema`, `note`, `statement`, the banned names, the browser specifiers, the paths it is read with, the classes and the entries: %w", file, err)
	}
	if err := validateRegister(file, document); err != nil {
		return register{}, err
	}
	return document, nil
}

// validateRegister refuses the register that promises less than the judgments
// read into it. A fixture proves a rule against a document the gate accepted, so
// the validation has to be reachable without a file on disk.
func validateRegister(file string, document register) error {
	if document.Schema != registerSchemaVersion {
		return fmt.Errorf("%s declares schema %d, and this gate reads %d", file, document.Schema, registerSchemaVersion)
	}
	if strings.TrimSpace(document.Statement) == "" {
		return fmt.Errorf("%s declares no statement document: the license of every entry has to be stated somewhere the tree holds, and an approval whose license lives only in the register is a claim nobody can check", file)
	}
	if err := checkPaths(file, document.Paths); err != nil {
		return err
	}
	if err := checkClasses(file, document.Classes); err != nil {
		return err
	}
	if len(document.Entries) == 0 {
		return fmt.Errorf("%s approves no component: an allowlist with nothing in it is the shape of a gate that stopped working", file)
	}
	for _, item := range document.Entries {
		if err := checkEntry(file, document, item); err != nil {
			return err
		}
	}
	return nil
}

// decodeJSON reads one document, refusing an unknown field: a document that
// carries a key this gate does not read is a document whose author expected it
// to mean something.
func decodeJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// checkPaths refuses the paths the census cannot read.
func checkPaths(file string, declared paths) error {
	required := []struct {
		name  string
		value string
	}{
		{"go_mod", declared.GoMod},
		{"sbom", declared.SBOM},
	}
	for _, wanted := range required {
		if strings.TrimSpace(wanted.value) == "" {
			return fmt.Errorf("%s declares no %s: the census reads it, and a gate that reads nothing answers green over nothing", file, wanted.name)
		}
	}
	for name, group := range map[string][]string{
		"manifests": declared.Manifests, "browser_manifests": declared.BrowserManifests,
		"images": declared.Images, "production_images": declared.ProductionImages,
		"workflows": declared.Workflows, "sources": declared.Sources,
	} {
		if len(group) == 0 {
			return fmt.Errorf("%s declares no %s: an empty list is a class of file nobody looks at, and this gate would rather refuse than judge a tree with a blind spot", file, name)
		}
	}
	return nil
}

// checkClasses refuses the class table that promises less than the judgments
// read into it.
func checkClasses(file string, classes []class) error {
	if len(classes) == 0 {
		return fmt.Errorf("%s declares no class: the licenses a class homologates are the whole license policy of this gate, and without a class there is nothing to compare a license against", file)
	}
	seen := map[string]bool{}
	roles := map[string]bool{roleRuntime: true, roleBuild: true, roleTooling: true, roleInfra: true, roleCI: true, rolePlatform: true}
	pins := map[string]bool{pinExact: true, pinDigest: true, pinCommit: true, pinNamed: true}
	for _, item := range classes {
		if strings.TrimSpace(item.Name) == "" {
			return fmt.Errorf("%s declares a class without a name", file)
		}
		if seen[item.Name] {
			return fmt.Errorf("%s declares the class %s twice", file, item.Name)
		}
		seen[item.Name] = true
		if !roles[item.Role] {
			return fmt.Errorf("%s declares the class %s with role %q, and the vocabulary is %s", file, item.Name, item.Role, strings.Join(sortedKeys(roles), ", "))
		}
		if !pins[item.Pin] {
			return fmt.Errorf("%s declares the class %s with pin %q, and the vocabulary is %s", file, item.Name, item.Pin, strings.Join(sortedKeys(pins), ", "))
		}
		if len(item.Licenses) == 0 && item.Role != rolePlatform {
			return fmt.Errorf("%s declares the class %s with no license: a class that homologates nothing is a class that refuses everything, and the refusal would arrive as a license problem instead of as the policy decision it is", file, item.Name)
		}
		if strings.TrimSpace(item.Reason) == "" {
			return fmt.Errorf("%s declares the class %s without saying why it exists", file, item.Name)
		}
	}
	return nil
}

// checkEntry refuses an entry the judgments would have to guess about.
func checkEntry(file string, document register, item entry) error {
	kinds := map[string]bool{kindModule: true, kindNPM: true, kindTool: true, kindImage: true, kindAction: true, kindPlatform: true}
	if !kinds[item.Kind] {
		return fmt.Errorf("%s declares the entry %q of kind %q, and the vocabulary is %s", file, item.Name, item.Kind, strings.Join(sortedKeys(kinds), ", "))
	}
	if strings.TrimSpace(item.Name) == "" {
		return fmt.Errorf("%s declares an entry of kind %s without a name", file, item.Kind)
	}
	if _, ok := document.classNamed(item.Class); !ok {
		return fmt.Errorf("%s declares the entry %q with the class %q, which the register does not declare: an entry in a class nobody defined is an entry with no license policy", file, item.Name, item.Class)
	}
	for _, site := range item.VersionEvidence {
		if strings.TrimSpace(site.Path) == "" || strings.TrimSpace(site.Regex) == "" {
			return fmt.Errorf("%s declares the entry %q with a version evidence without path or pattern", file, item.Name)
		}
		if _, err := compileEvidence(site.Regex); err != nil {
			return fmt.Errorf("%s declares the entry %q with the pattern %q, which does not compile: %w", file, item.Name, site.Regex, err)
		}
	}
	return nil
}

// printRegister prints the register the tree would be judged against if every
// component the census measured were approved as it is: the class, the owner,
// the purpose and the license travel from the entry that approved the component
// and stay empty for one that nobody did. The gate never writes the document —
// an approval is a human decision and the printed document is what a human
// commits — and the empty fields are exactly the ones the next run refuses.
func printRegister(root string, document register) error {
	refreshed := document
	refreshed.Entries = syncEntries(root, document)
	raw, err := marshalDocument(refreshed)
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", raw)
	return nil
}

// syncEntries answers the inventory the tree declares: the entries the register
// already has, kept in their order, plus one per component nobody approved, and
// with the version every evidence site declares.
func syncEntries(root string, document register) []entry {
	components, err := censusComponents(root, document)
	if err != nil {
		return document.Entries
	}
	kept := []entry{}
	seen := map[string]bool{}
	for _, item := range document.Entries {
		key := componentKey(item.Kind, item.Name)
		seen[key] = true
		found := false
		for _, measured := range components {
			if componentKey(measured.Kind, measured.Name) != key {
				continue
			}
			found = true
			if measured.Version != "" {
				item.Version = measured.Version
			}
		}
		// The platform and the tool the tree requires by name have no manifest
		// line to be extracted from: their approval is the declaration itself,
		// and the inventory keeps them.
		if found || declaredOnly(document, item) {
			kept = append(kept, item)
		}
	}
	for _, measured := range components {
		key := componentKey(measured.Kind, measured.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		kept = append(kept, entry{Kind: measured.Kind, Name: measured.Name, Version: measured.Version})
	}
	sort.SliceStable(kept, func(one, other int) bool {
		if kept[one].Kind != kept[other].Kind {
			return kept[one].Kind < kept[other].Kind
		}
		if kept[one].Name != kept[other].Name {
			return kept[one].Name < kept[other].Name
		}
		return kept[one].Version < kept[other].Version
	})
	return kept
}

// printSBOM prints the bill of materials of the delivered tree: one component
// per line of the census, with the class, the license, the owner and the scope
// the register approves it with. It is derived and not hand-written — the same
// census and the same register produce the same bytes — so the document on disk
// that stops matching the tree is a refusal and not a document nobody reads.
func printSBOM(root string, document register) error {
	raw, err := marshalDocument(buildSBOM(root, document))
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", raw)
	return nil
}

// sbom is the bill of materials: what the delivered tree is made of.
type sbom struct {
	Schema     int             `json:"schema"`
	Note       string          `json:"note"`
	Components []sbomComponent `json:"components"`
}

// sbomComponent is one line of the bill of materials.
type sbomComponent struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Class   string   `json:"class"`
	License string   `json:"license"`
	Owner   string   `json:"owner"`
	Scope   string   `json:"scope"`
	Digest  string   `json:"digest,omitempty"`
	Files   []string `json:"files"`
}

// buildSBOM derives the bill of materials from the census and the register. The
// document lists each component **once**, with every file it appears in: a bill
// of materials is the set of what is delivered and not the count of the lines
// that mention it, and the same action used by six jobs is one component.
func buildSBOM(root string, document register) sbom {
	components, err := censusComponents(root, document)
	if err != nil {
		return sbom{Schema: registerSchemaVersion, Note: sbomNote}
	}
	byKey := map[string]*sbomComponent{}
	for _, measured := range components {
		key := sbomKey(sbomComponent{Kind: measured.Kind, Name: measured.Name, Version: measured.Version})
		line, seen := byKey[key]
		if !seen {
			line = &sbomComponent{Kind: measured.Kind, Name: measured.Name, Version: measured.Version, Digest: measured.Digest}
			if approved, ok := approvingEntry(document, measured); ok {
				line.Class = approved.Class
				line.License = approved.License
				line.Owner = approved.Owner
				line.Scope = approved.Scope
			}
			byKey[key] = line
		}
		if line.Digest == "" {
			line.Digest = measured.Digest
		}
		line.Files = appendUnique(line.Files, measured.File)
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]sbomComponent, 0, len(keys))
	for _, key := range keys {
		line := *byKey[key]
		sort.Strings(line.Files)
		lines = append(lines, line)
	}
	return sbom{Schema: registerSchemaVersion, Note: sbomNote, Components: lines}
}

// appendUnique adds a file to the list of the ones a component was read from.
func appendUnique(values []string, wanted string) []string {
	for _, value := range values {
		if value == wanted {
			return values
		}
	}
	return append(values, wanted)
}

const sbomNote = "Lista de materiais do que a árvore entregue declara, derivada pelo censo do `tools/dependencyaudit` e aprovada pelo registro `quality/dependencies.json`. Documento derivado: a linha que a árvore não produz mais é recusa, e a que ela passou a produzir sem registro é recusa. Gerado por `$(GO) run ./tools/dependencyaudit -print-sbom`; nunca editado à mão."

// marshalDocument encodes a document the way every printer of this gate does,
// with the indentation and the trailing newline of the committed file, so that
// the document on disk is comparable with the document the tree implies.
func marshalDocument(document any) (string, error) {
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return "", fmt.Errorf("não foi possível serializar o documento: %w", err)
	}
	return string(raw), nil
}

// componentKey identifies a component the way the census and the register agree
// to name it: the kind and the name, without the version, because the version is
// what the approval is about and matching on it would hide the drift.
func componentKey(kind, name string) string {
	return kind + " " + name
}

// approvingEntry answers the entry that approves a component, comparing the
// version too: an approval is about a version, and an approval that approved
// another one is the drift this gate exists to refuse.
func approvingEntry(document register, measured component) (entry, bool) {
	for _, item := range document.Entries {
		if item.Kind != measured.Kind || item.Name != measured.Name {
			continue
		}
		if versionMatches(item, measured) {
			return item, true
		}
	}
	return entry{}, false
}

// versionMatches answers whether the approved version is the version the tree
// declares. An entry whose version is empty approves whatever the tree says,
// which is how a component without a version of its own — a platform service —
// is approved at all.
func versionMatches(item entry, measured component) bool {
	if item.Version == "" {
		return true
	}
	return item.Version == measured.Version
}

// declaredOnly answers whether an entry is a declaration the census cannot
// extract: a platform service, or a tool of a class whose pin is the name.
func declaredOnly(document register, item entry) bool {
	if item.Kind == kindPlatform {
		return true
	}
	class, ok := document.classNamed(item.Class)
	return ok && class.Pin == pinNamed
}

// contains answers whether a list holds a value, ignoring case: the licenses
// are written by humans in two documents and the comparison is about the
// license and not about the spelling.
func contains(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(wanted)) {
			return true
		}
	}
	return false
}

// matchesAny answers whether a path matches one of the declared globs.
func matchesAny(globs []string, candidate string) bool {
	for _, pattern := range globs {
		if matched, err := path.Match(pattern, candidate); err == nil && matched {
			return true
		}
	}
	return false
}

// sortedKeys orders the vocabulary for a refusal message: a message that lists
// the words in a different order on two runs is a message nobody compares.
func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
