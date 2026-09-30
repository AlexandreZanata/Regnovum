package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The register is the record of what generates what. It is data and not code on
// purpose: the review of a regeneration is then a diff of digests, and a human
// who reviews the diff sees exactly which artifact moved. The gate never
// rewrites the file — `-print-register` prints a refreshed document for a human
// to commit, the same decision the other gates of the phase record for their
// baselines.
type register struct {
	Schema   int        `json:"schema"`
	Note     string     `json:"note"`
	Families []pipeline `json:"families"`
}

// family is one pipeline: a generator, the version that pins it, what it reads
// and what it writes.
type pipeline struct {
	Name string `json:"name"`
	// Generator is what runs, as a reader would type it. Command is the target
	// that runs it, which is the answer to "how do I regenerate this".
	Generator string `json:"generator"`
	Command   string `json:"command"`
	// Version and VersionEvidence are the pin and where the tree declares it: the
	// gate reads the evidence file and requires the text to be there, so a version
	// edited in this document without regenerating the artifact is refused.
	Version         string `json:"version"`
	VersionEvidence string `json:"versionEvidence"`
	// Render names the generator this gate can run itself, in process. It is what
	// makes reproducibility provable here instead of merely declared.
	Render *render `json:"render,omitempty"`
	// VerifyCommand is the target that runs the generator again and compares the
	// artifact with the tree, for the families this gate does not run.
	VerifyCommand string `json:"verifyCommand,omitempty"`
	// Sources are the generator's own files, Inputs what it reads and Outputs what
	// it writes. An input or a source that moved means the output is stale.
	Sources []entry `json:"sources,omitempty"`
	Inputs  []entry `json:"inputs"`
	Outputs []entry `json:"outputs"`
	// BuildOutputs are the products of this pipeline that belong to a build and
	// never to the repository: the census does not read them, and `.gitignore`
	// has to hold them.
	BuildOutputs []string `json:"buildOutputs,omitempty"`
	// OwnerTest is the test that proves the property this gate cannot: the
	// determinism of a generator the gate does not run. The gate checks that the
	// test exists, which is the same shape the static-analysis gate uses for the
	// families it delegates.
	OwnerTest *ownerTest `json:"ownerTest,omitempty"`
}

type render struct {
	// Kind is the renderer the gate knows how to call, in process.
	Kind string `json:"kind"`
	// Root is where the renderer reads from; Outputs carries the targets.
	Root      string `json:"root"`
	GOPackage string `json:"goPackage"`
}

type ownerTest struct {
	Path     string `json:"path"`
	Function string `json:"function"`
}

// entry is one path a family reads or writes, with the digest of its content.
// `include` turns the path into a set of files — the sorted names of the set are
// part of the digest, so a file that appears without being declared moves it.
type entry struct {
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	Digest  string   `json:"digest"`
	// Header is the disclosure every file of this entry has to carry: a generated
	// file that does not say so is a generated file a reader will edit.
	Header string `json:"header,omitempty"`
}

func readRegister(path string) (register, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return register{}, fmt.Errorf("read the provenance register: %w", err)
	}
	var parsed register
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return register{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if parsed.Schema != 1 {
		return register{}, fmt.Errorf("%s declares schema %d, and this gate reads 1", path, parsed.Schema)
	}
	if len(parsed.Families) == 0 {
		return register{}, fmt.Errorf("%s declares no family: a register with no family records nothing", path)
	}
	return parsed, nil
}

// named answers the family a name belongs to.
func (record register) named(name string) (pipeline, bool) {
	for _, entry := range record.Families {
		if entry.Name == name {
			return entry, true
		}
	}
	return pipeline{}, false
}

// resolve lists the files an entry stands for, as paths relative to the root and
// in a fixed order. An entry with an include list is a set: the files of the
// directory whose base name matches one of the patterns and none of the
// exclusions. An entry without one is a single file, and a directory there is a
// mistake rather than an empty set.
func resolve(root string, item entry) ([]string, error) {
	target := filepath.Join(root, filepath.FromSlash(item.Path))
	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", item.Path, err)
	}
	if !info.IsDir() {
		if len(item.Include) > 0 {
			return nil, fmt.Errorf("%s is a file and the entry declares an include list", item.Path)
		}
		return []string{item.Path}, nil
	}
	if len(item.Include) == 0 {
		return nil, fmt.Errorf("%s is a directory and the entry declares no include list", item.Path)
	}
	files := []string{}
	err = filepath.WalkDir(target, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if !matchesAny(item.Include, name) || matchesAny(item.Exclude, name) {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", item.Path, err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("%s holds no file matching %s", item.Path, strings.Join(item.Include, ", "))
	}
	return files, nil
}

func matchesAny(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if matched, err := filepath.Match(pattern, name); err == nil && matched {
			return true
		}
	}
	return false
}

// digestOf hashes the set as a set: the content of every file and the name it
// has, so that renaming a file moves the digest and a file that appears without
// being declared moves it too.
func digestOf(root string, files []string) (string, error) {
	hash := sha256.New()
	for _, relative := range files {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", relative, err)
		}
		file := sha256.Sum256(body)
		if _, err := fmt.Fprintf(hash, "%s\t%s\n", relative, hex.EncodeToString(file[:])); err != nil {
			return "", fmt.Errorf("hash %s: %w", relative, err)
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// digestEntry resolves an entry and hashes what it stands for.
func digestEntry(root string, item entry) (string, []string, error) {
	files, err := resolve(root, item)
	if err != nil {
		return "", nil, err
	}
	digest, err := digestOf(root, files)
	return digest, files, err
}

// printRegister recomputes every digest of a register and prints the document a
// human commits. A path a family declares and the tree does not have is refused:
// a refreshed register over a missing artifact would record the absence as the
// truth. The root is a parameter because the fixtures of this gate are refreshed
// the same way the delivered register is.
func printRegister(root, path string) error {
	record, err := readRegister(path)
	if err != nil {
		return err
	}
	if err := refreshRegister(root, &record); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the register: %w", err)
	}
	fmt.Printf("%s\n", encoded)
	return nil
}

// refreshRegister fills every digest of the register. The sets share their
// backing array with the family they came from, so a refreshed digest is written
// where the family reads it.
func refreshRegister(root string, record *register) error {
	for index := range record.Families {
		pipe := &record.Families[index]
		for _, set := range []struct {
			what  string
			items []entry
		}{
			{"gerador", pipe.Sources},
			{"insumo", pipe.Inputs},
			{"artefato", pipe.Outputs},
		} {
			for position := range set.items {
				digest, _, err := digestEntry(root, set.items[position])
				if err != nil {
					return fmt.Errorf("family %q: %s %s: %w", pipe.Name, set.what, set.items[position].Path, err)
				}
				set.items[position].Digest = digest
			}
		}
	}
	return nil
}
