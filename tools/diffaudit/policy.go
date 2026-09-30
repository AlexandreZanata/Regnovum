package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The roles a class gives a file. A role is what the judgments ask about: a
// `production` file is what the `same-area-test` demand is about, an `evidence`
// file is what satisfies it, and an `exempt` file is a change the policy states
// on purpose that no evidence is demanded of.
const (
	roleProduction   = "production"
	roleEvidence     = "evidence"
	roleGenerated    = "generated"
	roleRoute        = "route"
	roleGovernance   = "governance"
	roleExempt       = "exempt"
	roleFormat       = "format"
	roleUnclassified = "unclassified"
)

// The demand kinds. The vocabulary is closed: the policy states **when** a
// demand applies, and the gate is the only place that knows how to satisfy it.
// A kind the gate does not implement is a policy bug and is refused instead of
// ignored — a demand nobody executes is a demand that reads as if it did.
const (
	demandSameAreaTest   = "same-area-test"
	demandMigration      = "migration-evidence"
	demandContract       = "contract"
	demandProvenancePair = "provenance-pair"
)

// policy is the whole document of `quality/diff-policy.json`.
type policy struct {
	Schema   int    `json:"schema"`
	Note     string `json:"note"`
	Bypass   bypass `json:"bypass"`
	Areas    areas  `json:"areas"`
	Kinds    kinds  `json:"kinds"`
	Route    route  `json:"route"`
	Contract struct {
		Note   string `json:"note"`
		Family string `json:"family"`
	} `json:"contract"`
	Classes []class `json:"classes"`
}

// bypass is the vocabulary a commit message may not carry: a message that
// announces the gate was skipped is the one bypass the program forbids by name,
// because it is the only one that leaves no trace in the tree.
type bypass struct {
	Note   string   `json:"note"`
	Tokens []string `json:"tokens"`
}

// areas is how an area is derived from a path: the first prefix that matches
// decides the depth, and a path that matches none belongs to its own directory.
type areas struct {
	Note     string     `json:"note"`
	Prefixes []areaRule `json:"prefixes"`
}

type areaRule struct {
	Prefix string `json:"prefix"`
	Depth  int    `json:"depth"`
}

// kinds is the split of the catalog's evidence categories into the nominal
// proof and the adversarial one.
type kinds struct {
	Note        string   `json:"note"`
	Nominal     []string `json:"nominal"`
	Adversarial []string `json:"adversarial"`
}

// route is the content marker of the route surface and the registry that is
// its source of truth.
type route struct {
	Note     string `json:"note"`
	Marker   string `json:"marker"`
	Registry string `json:"registry"`
}

// class is one row of the policy: a name, the role it gives the files it
// matches, the reason breaking it costs something, the matchers, and the
// evidence the change owes when it touches such a file.
type class struct {
	Name    string   `json:"name"`
	Role    string   `json:"role"`
	Reason  string   `json:"reason"`
	Origin  string   `json:"origin,omitempty"`
	Demands []demand `json:"demands,omitempty"`

	Prefixes []string `json:"prefixes,omitempty"`
	Suffixes []string `json:"suffixes,omitempty"`
	Segments []string `json:"segments,omitempty"`
	Names    []string `json:"names,omitempty"`

	// Marker matches by content instead of by path: the class is a property of
	// what the file says, which is how the route surface is recognized without a
	// list that ages.
	Marker bool `json:"marker,omitempty"`
	// Paths matches the registry declared by the policy itself.
	Paths bool `json:"paths,omitempty"`
}

// The statuses a demand can be attached to, in the vocabulary of the policy. A
// demand without one applies to every touch of the class.
const (
	whenAdded  = "added"
	whenEdited = "edited"
)

// demand is one piece of evidence a class owes, with the data that demand
// needs to be judged.
type demand struct {
	Kind string `json:"kind"`
	// When is the status that triggers the demand: `added` means the demand is
	// about new behavior, `edited` about a change to an existing file, and an
	// empty value means every touch of the class.
	When string `json:"when,omitempty"`
	// Areas names where the evidence of a demand has to be, when the demand is
	// satisfied by an area rather than by a file.
	Areas []string `json:"areas,omitempty"`
}

// policyPath is the register the gate reads. It is a path inside the tree and
// never a flag default that could point somewhere else: the policy that judges
// a checkout is the one the checkout versions.
const policyPath = "quality/diff-policy.json"

// readPolicy reads and checks the policy. Every refusal here is a refusal about
// the document, not about a change: a policy that does not say what it means
// would make every judgment below it a guess.
func readPolicy(root, path string) (policy, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return policy{}, fmt.Errorf("%s: %w", path, err)
	}
	var document policy
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return policy{}, fmt.Errorf("%s does not decode as a diff policy: it declares `schema`, `bypass`, `areas`, `kinds`, `route`, `contract` and `classes`, and a key this loader does not know is a key nobody enforces: %w", path, err)
	}
	if document.Schema != policySchemaVersion {
		return policy{}, fmt.Errorf("%s declares schema %d, and this gate reads %d", path, document.Schema, policySchemaVersion)
	}
	if len(document.Classes) == 0 {
		return policy{}, fmt.Errorf("%s declares no class: a policy that classifies nothing cannot demand evidence of anything", path)
	}
	if len(document.Bypass.Tokens) == 0 {
		return policy{}, fmt.Errorf("%s declares no bypass token: a gate without the forbidden vocabulary would accept the one bypass the program names", path)
	}
	if len(document.Areas.Prefixes) == 0 {
		return policy{}, fmt.Errorf("%s declares no area prefix: `same-area-test` would compare nothing with nothing", path)
	}
	if len(document.Kinds.Nominal) == 0 || len(document.Kinds.Adversarial) == 0 {
		return policy{}, fmt.Errorf("%s does not split the evidence categories into nominal and adversarial: the Q0 demand would accept one half as the whole proof", path)
	}
	if document.Route.Marker == "" || document.Route.Registry == "" {
		return policy{}, fmt.Errorf("%s does not name the route marker and the registry", path)
	}
	if document.Contract.Family == "" {
		return policy{}, fmt.Errorf("%s does not name the provenance family that owns the contract artifacts", path)
	}
	seen := map[string]bool{}
	for _, entry := range document.Classes {
		if entry.Name == "" || entry.Role == "" || entry.Reason == "" {
			return policy{}, fmt.Errorf("%s declares a class without a name, a role and a reason: %+v", path, entry)
		}
		if seen[entry.Name] {
			return policy{}, fmt.Errorf("%s declares the class %q twice", path, entry.Name)
		}
		seen[entry.Name] = true
		if !roleKnown(entry.Role) {
			return policy{}, fmt.Errorf("%s gives the class %q the role %q, which is not a role this gate knows", path, entry.Name, entry.Role)
		}
		if !hasMatcher(entry) {
			return policy{}, fmt.Errorf("%s gives the class %q no matcher: a class that matches nothing is a class that hides the change it was written for", path, entry.Name)
		}
		for _, owed := range entry.Demands {
			if !demandKnown(owed.Kind) {
				return policy{}, fmt.Errorf("%s asks the class %q for the demand %q, which this gate does not know how to judge", path, entry.Name, owed.Kind)
			}
			if owed.When != "" && owed.When != whenAdded && owed.When != whenEdited {
				return policy{}, fmt.Errorf("%s asks the class %q for the demand %q on the status %q, which is not a status a change has", path, entry.Name, owed.Kind, owed.When)
			}
		}
	}
	return document, nil
}

// hasMatcher answers whether a class declares anything to match with. A class
// with no matcher would never be reached in an ordered list, which makes it a
// row a reader believes is enforced.
func hasMatcher(entry class) bool {
	return len(entry.Prefixes) > 0 || len(entry.Suffixes) > 0 || len(entry.Segments) > 0 ||
		len(entry.Names) > 0 || entry.Marker || entry.Paths || entry.Origin != ""
}

func roleKnown(role string) bool {
	switch role {
	case roleProduction, roleEvidence, roleGenerated, roleRoute, roleGovernance, roleExempt, roleFormat:
		return true
	}
	return false
}

func demandKnown(kind string) bool {
	switch kind {
	case demandSameAreaTest, demandMigration, demandContract, demandProvenancePair:
		return true
	}
	return false
}

// matched answers whether a class claims a path, and reports which matcher did
// it, because the report prints the reason a file was classified the way it was.
func (entry class) matched(path string) (string, bool) {
	for _, prefix := range entry.Prefixes {
		if strings.HasPrefix(path, prefix) {
			return "prefixo " + prefix, true
		}
	}
	for _, suffix := range entry.Suffixes {
		if strings.HasSuffix(path, suffix) {
			return "sufixo " + suffix, true
		}
	}
	segments := strings.Split(path, "/")
	for _, wanted := range entry.Segments {
		for _, segment := range segments {
			if segment == wanted {
				return "segmento " + wanted, true
			}
		}
	}
	for _, name := range entry.Names {
		if path == name || filepath.Base(path) == name {
			return "nome " + name, true
		}
	}
	return "", false
}
