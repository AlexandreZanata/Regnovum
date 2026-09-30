package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// changeSchemaVersion is the only version of the change document this gate
// reads. A document written against another version is refused instead of
// interpreted: the judgments below it are about files and commits, and a
// document that disagreed with the loader about what a file record is would make
// every one of them a guess.
const changeSchemaVersion = 1

// The statuses a file record carries, in git's own letters. A status outside
// this vocabulary is a refusal: a change the gate cannot name is a change it
// cannot demand evidence of.
const (
	newFile      = "A"
	modifiedFile = "M"
	deletedFile  = "D"
	renamedFile  = "R"
	copiedFile   = "C"
)

// fileRecord is one file one commit touched. `before` and `after` carry the
// content of the two sides when a judgment needs it — the format-only question
// and the route set are answered by reading tokens, and the catalog question by
// reading two documents — and are absent when no judgment needs them.
type fileRecord struct {
	Status string `json:"status"`
	Path   string `json:"path"`
	From   string `json:"from,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// commitRecord is one commit of the range: its identity, its message — which is
// what the bypass rule reads — and the files it touched. A merge commit carries
// no files on purpose: the range judge reads merges for the message only, and
// the reason is printed with the count.
type commitRecord struct {
	SHA     string       `json:"sha"`
	Message string       `json:"message"`
	Merge   bool         `json:"merge,omitempty"`
	Files   []fileRecord `json:"files,omitempty"`
}

// changeDocument is what the gate judges: a range of commits, and the two sides
// of the catalog when the change touches the catalog. The empty range is a
// legitimate document — a push that changes nothing between base and head — and
// the caller has to say so out loud, which is why `Base` and `Head` travel here.
type changeDocument struct {
	Schema      int            `json:"schema"`
	Base        string         `json:"base"`
	Head        string         `json:"head"`
	CatalogBase string         `json:"catalog_before,omitempty"`
	CatalogHead string         `json:"catalog_after,omitempty"`
	Commits     []commitRecord `json:"commits"`
}

// readChange reads a change document from a file. This is the entry point of
// every fixture of this gate, and it is why the judgments are provable without a
// repository: the document is the input, and the git builder below produces the
// same document from a range.
func readChange(path string) (changeDocument, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return changeDocument{}, fmt.Errorf("%s: %w", path, err)
	}
	var document changeDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return changeDocument{}, fmt.Errorf("%s does not decode as a change document: it declares `schema`, `base`, `head`, optionally the two sides of the catalog and the commits: %w", path, err)
	}
	if document.Schema != changeSchemaVersion {
		return changeDocument{}, fmt.Errorf("%s declares schema %d, and this gate reads %d", path, document.Schema, changeSchemaVersion)
	}
	if len(document.Commits) == 0 {
		return changeDocument{}, fmt.Errorf("%s declares no commit: a change document that judged nothing is the shape of a gate that stopped working", path)
	}
	return document, nil
}

// git runs one git command inside the checkout and returns its standard output.
// The gate reads the range from git and never writes to it: `show`, `diff`,
// `rev-list` and `merge-base` are the whole vocabulary.
func git(root string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	var out bytes.Buffer
	var problem bytes.Buffer
	command.Stdout = &out
	command.Stderr = &problem
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(problem.String()))
	}
	return out.String(), nil
}

// resolveRange answers the two sides of the diff. The head is `HEAD` unless the
// caller names another commit; the base is the commit the caller names, or the
// merge base of `origin/main` and then of `main`, which is the branch a phase
// branch is compared against. A base that cannot be resolved is a **refusal**:
// a gate that cannot see the diff must not answer green over it, because that
// green is the one that lets a change through unjudged.
func resolveRange(root, base, head string) (string, string, error) {
	headSHA, err := git(root, "rev-parse", "--verify", head+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("não foi possível resolver a cabeça %q: %w", head, err)
	}
	headSHA = strings.TrimSpace(headSHA)

	candidates := []string{base}
	if base == "" {
		candidates = []string{"origin/main", "main"}
	}
	var lastErr error
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		mergeBase, err := git(root, "merge-base", candidate, headSHA)
		if err != nil {
			lastErr = err
			continue
		}
		return strings.TrimSpace(mergeBase), headSHA, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("nenhum candidato de base foi nomeado")
	}
	return "", "", fmt.Errorf("não foi possível resolver a base da mudança (tentei %s): o portão de diff precisa dos dois lados para classificar a mudança, e responder verde sem eles seria responder sobre nada: %w",
		strings.Join(candidates, ", "), lastErr)
}

// buildChange produces the change document of a range from git: one record per
// commit, the file records of the commits that are not merges, the commit
// messages, and the two sides of the catalog when the catalog is in the change.
//
// The content of a Go file travels with the record because two judgments read
// tokens and not names — the format-only question and the route set — and the
// content of the catalog travels because the Q0 question compares the rule
// table of the two sides. Nothing else is read: the gate judges the change, and
// the whole tree only enters through the registers it already versions.
func buildChange(root, base, head string) (changeDocument, error) {
	baseSHA, headSHA, err := resolveRange(root, base, head)
	if err != nil {
		return changeDocument{}, err
	}
	document := changeDocument{Schema: changeSchemaVersion, Base: baseSHA, Head: headSHA}

	listing, err := git(root, "rev-list", "--reverse", baseSHA+".."+headSHA)
	if err != nil {
		return changeDocument{}, err
	}
	for _, sha := range strings.Fields(listing) {
		record, err := readCommit(root, sha)
		if err != nil {
			return changeDocument{}, err
		}
		document.Commits = append(document.Commits, record)
	}

	if catalogs, err := catalogSides(root, baseSHA, headSHA, document.Commits); err == nil {
		document.CatalogBase, document.CatalogHead = catalogs[0], catalogs[1]
	}
	return document, nil
}

// catalogSides reads the catalog at both ends of the range. Reading it is only
// worth the git call when a commit of the range touched the catalog, and a
// missing side is not an error here: the gate reads the tree when the document
// does not carry the document, and it says so in the gap it prints.
func catalogSides(root, base, head string, commits []commitRecord) ([2]string, error) {
	touched := false
	for _, commit := range commits {
		for _, file := range commit.Files {
			if file.Path == catalogPath {
				touched = true
			}
		}
	}
	if !touched {
		return [2]string{"", ""}, nil
	}
	var sides [2]string
	if raw, err := git(root, "show", base+":"+catalogPath); err == nil {
		sides[0] = raw
	}
	if raw, err := git(root, "show", head+":"+catalogPath); err == nil {
		sides[1] = raw
	}
	return sides, nil
}

// readCommit reads one commit of the range. A merge is recorded for its message
// alone; every other commit carries the files it touched, with the content of
// the two sides for the files a judgment reads.
func readCommit(root, sha string) (commitRecord, error) {
	message, err := git(root, "show", "-s", "--format=%B", sha)
	if err != nil {
		return commitRecord{}, err
	}
	record := commitRecord{SHA: sha, Message: strings.TrimRight(message, "\n")}

	parents, err := git(root, "rev-list", "--parents", "-n", "1", sha)
	if err != nil {
		return commitRecord{}, err
	}
	if len(strings.Fields(parents)) > 2 {
		record.Merge = true
		return record, nil
	}

	names, err := git(root, "diff", "--name-status", "-M", "--no-color", sha+"^", sha)
	if err != nil {
		return commitRecord{}, err
	}
	for _, line := range strings.Split(strings.TrimRight(names, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			return commitRecord{}, fmt.Errorf("%s: o git devolveu uma linha de status que este portão não lê: %q", sha, line)
		}
		file := fileRecord{Status: string(fields[0][0])}
		switch file.Status {
		case renamedFile, copiedFile:
			if len(fields) < 3 {
				return commitRecord{}, fmt.Errorf("%s: a renomeação %q não nomeia a origem", sha, line)
			}
			file.From, file.Path = fields[1], fields[2]
		default:
			file.Path = fields[1]
		}
		if err := fillContent(root, sha, &file); err != nil {
			return commitRecord{}, err
		}
		record.Files = append(record.Files, file)
	}
	return record, nil
}

// fillContent reads the content of the two sides of a file when a judgment reads
// tokens: the Go sources — the format question and the route set — and the
// catalog. A side that cannot be read stays empty, and the gate treats the
// absence as "cannot tell", which demands evidence instead of excusing it.
func fillContent(root, sha string, file *fileRecord) error {
	if !contentWanted(file.Path) {
		return nil
	}
	if file.Status != deletedFile {
		after, err := git(root, "show", sha+":"+file.Path)
		if err != nil {
			return err
		}
		file.After = after
	}
	if file.Status != newFile {
		before, err := git(root, "show", sha+"^:"+beforePath(file))
		if err == nil {
			file.Before = before
		}
	}
	return nil
}

// beforePath answers which path to read on the left side: the old name of a
// rename, and the same name otherwise.
func beforePath(file *fileRecord) string {
	if file.From != "" {
		return file.From
	}
	return file.Path
}

// contentWanted answers whether a judgment reads the content of a file. Keeping
// the answer in one place is what keeps the document small: the gate reads the
// tokens of Go sources and the two sides of the catalog, and nothing else.
func contentWanted(path string) bool {
	return strings.HasSuffix(path, ".go") || path == catalogPath
}
