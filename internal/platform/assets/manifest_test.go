package assets_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
)

const manifestJSON = `{"version":1,"assets":{"main.js":{"path":"/assets/main-abc123.js","sha256":"abc123"}}}`

// TestLoadFileReadsTheManifestOfABuildDirectory covers P18-T07A: the server
// reads back what the asset pipeline wrote, and the refusal names the file, so
// an operator can tell a missing build from a corrupt one.
func TestLoadFileReadsTheManifestOfABuildDirectory(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, assets.ManifestFileName)
	if err := os.WriteFile(path, []byte(manifestJSON), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	manifest, err := assets.LoadFile(directory)
	if err != nil {
		t.Fatalf("LoadFile(%q) error = %v", directory, err)
	}
	if got, err := manifest.URL("main.js"); err != nil || got != "/assets/main-abc123.js" {
		t.Fatalf("URL(main.js) = %q, %v from a file-backed manifest", got, err)
	}

	_, err = assets.LoadFile(filepath.Join(directory, "absent"))
	if err == nil {
		t.Fatal("LoadFile on a directory without a manifest succeeded")
	}
	if !strings.Contains(err.Error(), filepath.Join("absent", assets.ManifestFileName)) {
		t.Errorf("LoadFile error = %v, want it to name the file it could not open", err)
	}

	if _, err := assets.LoadFile("   "); err == nil {
		t.Error("LoadFile accepted an empty directory")
	}
}

func TestManifestResolvesOnlyDeclaredAssets(t *testing.T) {
	manifest, err := assets.Load(strings.NewReader(manifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := manifest.URL("main.js"); err != nil || got != "/assets/main-abc123.js" {
		t.Fatalf("URL(main.js) = %q, %v", got, err)
	}
	if _, err := manifest.URL("missing.js"); err == nil {
		t.Fatal("missing asset must fail instead of falling back to an unhashed URL")
	}
}

func TestManifestTemplateHelperResolvesHashedURL(t *testing.T) {
	manifest, err := assets.Load(strings.NewReader(manifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	page, err := template.New("page").Funcs(manifest.TemplateFuncs()).Parse(`<script src="{{asset "main.js"}}"></script>`)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := page.Execute(&output, nil); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != `<script src="/assets/main-abc123.js"></script>` {
		t.Fatalf("rendered page = %q", got)
	}
}

func TestManifestRejectsMalformedRecords(t *testing.T) {
	for _, input := range []string{
		`{"version":2,"assets":{"main.js":{"path":"/main.js","sha256":"x"}}}`,
		`{"version":1,"assets":{"main.js":{"path":"","sha256":"x"}}}`,
		`{"version":1,"assets":{}}`,
	} {
		if _, err := assets.Load(strings.NewReader(input)); err == nil {
			t.Fatalf("manifest %s was accepted", input)
		}
	}
}
