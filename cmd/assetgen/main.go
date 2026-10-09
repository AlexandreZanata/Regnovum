// Command assetgen creates immutable frontend asset names and a deterministic
// manifest. It is intentionally a Go tool, so the runtime image needs neither
// Node nor a bundler.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type stringList []string

func (items *stringList) String() string { return strings.Join(*items, ",") }
func (items *stringList) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("assetgen: input directory cannot be empty")
	}
	*items = append(*items, value)
	return nil
}

type manifest struct {
	Version int              `json:"version"`
	Assets  map[string]asset `json:"assets"`
}

type asset struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func main() {
	var inputs stringList
	output := flag.String("output", "", "directory for hashed assets")
	manifestPath := flag.String("manifest", "", "manifest JSON path")
	flag.Var(&inputs, "input", "source directory; may be repeated")
	flag.Parse()
	if len(inputs) == 0 || *output == "" || *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "usage: assetgen -input DIR [-input DIR...] -output DIR -manifest FILE")
		os.Exit(2)
	}
	if err := generate(inputs, *output, *manifestPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(inputs []string, output, manifestPath string) error {
	files := make(map[string]string)
	for _, input := range inputs {
		root, err := filepath.Abs(input)
		if err != nil {
			return fmt.Errorf("assetgen: resolve %s: %w", input, err)
		}
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !isAsset(entry.Name()) {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			key := filepath.ToSlash(relative)
			if previous, exists := files[key]; exists && previous != path {
				return fmt.Errorf("assetgen: duplicate asset %q from %s and %s", key, previous, path)
			}
			files[key] = path
			return nil
		})
		if err != nil {
			return fmt.Errorf("assetgen: scan %s: %w", input, err)
		}
	}

	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if err := os.MkdirAll(output, 0o755); err != nil {
		return fmt.Errorf("assetgen: create output: %w", err)
	}
	result := manifest{Version: 1, Assets: make(map[string]asset, len(keys))}
	for _, key := range keys {
		body, err := os.ReadFile(files[key])
		if err != nil {
			return fmt.Errorf("assetgen: read %s: %w", key, err)
		}
		digest := sha256.Sum256(body)
		hexDigest := hex.EncodeToString(digest[:])
		hashedPath := hashedName(key, hexDigest[:12])
		for _, destination := range []string{hashedPath, key} {
			// Two names of the same bytes, for two different readers.
			//
			// The manifest publishes the hashed one, and that is the address
			// the server-rendered pages reference: immutable, one year. The
			// stable one is what an ES module graph needs, because a module
			// resolves its own imports by relative path and cannot know a hash
			// that changes with the content (P18-T05 is the first consumer of
			// the pipeline that imports another module). The stable names are
			// served with revalidation rather than immutability, which
			// docs/DEPLOYMENT.md section 9 records.
			path := filepath.Join(output, filepath.FromSlash(destination))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return fmt.Errorf("assetgen: create asset directory: %w", err)
			}
			if err := os.WriteFile(path, body, 0o644); err != nil {
				return fmt.Errorf("assetgen: write %s: %w", destination, err)
			}
		}
		result.Assets[key] = asset{Path: "/assets/" + hashedPath, SHA256: hexDigest}
	}

	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("assetgen: encode manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		return fmt.Errorf("assetgen: create manifest directory: %w", err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o644); err != nil {
		return fmt.Errorf("assetgen: write manifest: %w", err)
	}
	return nil
}

func isAsset(name string) bool {
	extension := strings.ToLower(filepath.Ext(name))
	return extension == ".js" || extension == ".css" || extension == ".svg" || extension == ".webp"
}

func hashedName(path, digest string) string {
	extension := filepath.Ext(path)
	base := strings.TrimSuffix(path, extension)
	return base + "-" + digest + extension
}
