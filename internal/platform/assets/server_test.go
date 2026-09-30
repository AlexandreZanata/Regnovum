package assets_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
)

// The build under test, written the way cmd/assetgen writes one: the same bytes
// under the hashed address the manifest publishes and under the stable name the
// ES module graph resolves by relative path.
const (
	stylesheet = "styles/arena.css"
	module     = "pages/arena.js"
)

// build writes a build directory and returns its manifest. Every file is
// content-addressed, so the test asserts the policy of an address, never a
// literal hash.
func build(t *testing.T) (string, assets.Manifest) {
	t.Helper()

	directory := t.TempDir()
	manifest := assets.Manifest{Version: 1, Assets: map[string]assets.Record{}}

	for name, body := range map[string]string{
		stylesheet: ":root { --ga-ink: #0f172a; }\n",
		module:     "export const participation = true;\n",
	} {
		digest := sha256.Sum256([]byte(body))
		hashed := hashedName(name, hex.EncodeToString(digest[:])[:12])

		writeFile(t, directory, hashed, body)
		writeFile(t, directory, name, body)

		manifest.Assets[name] = assets.Record{
			Path:   "/assets/" + hashed,
			SHA256: hex.EncodeToString(digest[:]),
		}
	}
	return directory, manifest
}

// writeFile writes one asset of the build.
func writeFile(t *testing.T, directory, name, body string) {
	t.Helper()

	target := filepath.Join(directory, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// hashedName mirrors the naming of cmd/assetgen: the digest goes in front of
// the extension, and the directories stay.
func hashedName(name, digest string) string {
	extension := filepath.Ext(name)
	return strings.TrimSuffix(name, extension) + "-" + digest + extension
}

// server composes the serving surface over one build.
func server(t *testing.T) (*assets.Server, assets.Manifest) {
	t.Helper()

	directory, manifest := build(t)
	composed, err := assets.NewServer(assets.Config{
		Directory: directory,
		Manifest:  manifest,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return composed, manifest
}

// request asks the handler directly, whether or not the mux would have cleaned
// the address first: the allowlist, not the router, is what refuses.
func request(t *testing.T, composed *assets.Server, target string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	composed.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

// TestThePrefixComesFromTheManifest: the address space is a property of the
// build, not a configured constant that can drift from the pages.
func TestThePrefixComesFromTheManifest(t *testing.T) {
	t.Parallel()

	composed, _ := server(t)
	if prefix := composed.Prefix(); prefix != "/assets" {
		t.Errorf("Prefix() = %q, want the prefix the manifest publishes", prefix)
	}
}

// TestEveryDeclaredAddressIsServed is the first half of the task's validation:
// both names of every asset answer 200, with the media type of the pipeline and
// the cache policy of the address.
func TestEveryDeclaredAddressIsServed(t *testing.T) {
	t.Parallel()

	composed, manifest := server(t)

	cases := []struct {
		name         string
		target       string
		contentType  string
		cacheControl string
	}{
		{
			name:         "the published stylesheet is immutable",
			target:       manifest.Assets[stylesheet].Path,
			contentType:  "text/css; charset=utf-8",
			cacheControl: assets.CacheHashed,
		},
		{
			name:         "the stable stylesheet is revalidated",
			target:       "/assets/" + stylesheet,
			contentType:  "text/css; charset=utf-8",
			cacheControl: assets.CacheStable,
		},
		{
			name:         "the published module is immutable",
			target:       manifest.Assets[module].Path,
			contentType:  "text/javascript; charset=utf-8",
			cacheControl: assets.CacheHashed,
		},
		{
			name:         "the stable module is revalidated",
			target:       "/assets/" + module,
			contentType:  "text/javascript; charset=utf-8",
			cacheControl: assets.CacheStable,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			response := request(t, composed, testCase.target)
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", testCase.target, response.Code)
			}
			if contentType := response.Header().Get("Content-Type"); contentType != testCase.contentType {
				t.Errorf("GET %s Content-Type = %q, want %q", testCase.target, contentType, testCase.contentType)
			}
			if cache := response.Header().Get("Cache-Control"); cache != testCase.cacheControl {
				t.Errorf("GET %s Cache-Control = %q, want %q", testCase.target, cache, testCase.cacheControl)
			}
			if body := response.Body.String(); !strings.HasPrefix(body, "export ") && !strings.HasPrefix(body, ":root") {
				t.Errorf("GET %s served %q, want the bytes of the build", testCase.target, body)
			}
		})
	}
}

// TestTheDigestIsTheValidator: the manifest already knows the content, so the
// answer carries it and a browser that holds the bytes gets a 304 without the
// server reading the file again.
func TestTheDigestIsTheValidator(t *testing.T) {
	t.Parallel()

	composed, manifest := server(t)
	target := manifest.Assets[module].Path

	response := request(t, composed, target)
	etag := response.Header().Get("ETag")
	if etag != `"`+manifest.Assets[module].SHA256+`"` {
		t.Fatalf("ETag = %q, want the digest the manifest records", etag)
	}

	conditional := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("If-None-Match", etag)
	composed.ServeHTTP(conditional, request)
	if conditional.Code != http.StatusNotModified {
		t.Errorf("GET %s with If-None-Match = %d, want 304", target, conditional.Code)
	}
}

// TestNothingOutsideTheManifestIsServed is the second half of the validation:
// a file that exists in the build but is not declared, a directory, a listing
// and a path that tries to walk out of the build all answer the same thing.
func TestNothingOutsideTheManifestIsServed(t *testing.T) {
	t.Parallel()

	directory, manifest := build(t)
	// The build holds more than the manifest declares: nothing serves it.
	writeFile(t, directory, "styles/private.css", "/* not in the manifest */\n")
	writeFile(t, directory, "secrets.env", "TOKEN=not-a-real-secret\n")

	composed, err := assets.NewServer(assets.Config{Directory: directory, Manifest: manifest})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	for _, target := range []string{
		"/assets/styles/private.css",
		"/assets/secrets.env",
		"/assets/",
		"/assets",
		"/assets/styles",
		"/assets/styles/",
		"/assets/unknown-hash.css",
		"/assets/../../secrets.env",
		"/assets/%2e%2e/secrets.env",
		"/assets/../styles/private.css",
		"/assets/styles/../secrets.env",
		"//assets/styles/private.css",
		"/assets/pages/../../secrets.env",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			response := request(t, composed, target)
			if response.Code != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404", target, response.Code)
			}
			if body := response.Body.String(); strings.Contains(body, "TOKEN") || strings.Contains(body, "not in the manifest") {
				t.Errorf("GET %s leaked the contents of a file the manifest does not declare", target)
			}
		})
	}
}

// TestAPathThatLeavesTheBuildDirectoryIsRefusedAtComposition: the allowlist is
// built from the manifest, so a manifest that describes an address outside the
// build never reaches a mux.
func TestAPathThatLeavesTheBuildDirectoryIsRefusedAtComposition(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		record assets.Record
		key    string
	}{
		{
			name:   "a published address that walks up",
			key:    "../../etc/passwd.css",
			record: assets.Record{Path: "/assets/../../etc/passwd.css", SHA256: "deadbeef"},
		},
		{
			name:   "a source name that walks up",
			key:    "../escape.css",
			record: assets.Record{Path: "/assets/styles/escape.css", SHA256: "deadbeef"},
		},
		{
			name:   "an absolute source name",
			key:    "/etc/passwd.css",
			record: assets.Record{Path: "/assets/styles/passwd.css", SHA256: "deadbeef"},
		},
		{
			name:   "a name that is not a file inside a directory",
			key:    "styles/escape.css",
			record: assets.Record{Path: "/assets", SHA256: "deadbeef"},
		},
		{
			name:   "a published address under another prefix",
			key:    "styles/escape.css",
			record: assets.Record{Path: "/media/styles/escape.css", SHA256: "deadbeef"},
		},
		{
			name:   "an asset the pipeline does not publish",
			key:    "fonts/inter.woff2",
			record: assets.Record{Path: "/assets/fonts/inter.woff2", SHA256: "deadbeef"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			directory, manifest := build(t)
			manifest.Assets[testCase.key] = testCase.record

			if _, err := assets.NewServer(assets.Config{Directory: directory, Manifest: manifest}); err == nil {
				t.Fatal("NewServer() accepted a manifest that does not describe one servable build")
			}
		})
	}
}

// TestAnEmptyOrMissingBuildIsRefused keeps the refusal in the composition: a
// surface mounted over a directory that is not there would answer 500 for every
// page of the site.
func TestAnEmptyOrMissingBuildIsRefused(t *testing.T) {
	t.Parallel()

	directory, manifest := build(t)

	if _, err := assets.NewServer(assets.Config{Manifest: manifest}); err == nil {
		t.Error("NewServer() accepted an empty build directory")
	}
	if _, err := assets.NewServer(assets.Config{Directory: directory}); err == nil {
		t.Error("NewServer() accepted a manifest that declares no asset")
	}
	if _, err := assets.NewServer(assets.Config{Directory: filepath.Join(directory, stylesheet), Manifest: manifest}); err == nil {
		t.Error("NewServer() accepted a file where a build directory was expected")
	}
}

// TestAMissingFileIsAServerFault: the manifest promised the address, so losing
// the file after boot is a deployment fault and not a not-found of the reader —
// and the answer names no path of the build.
func TestAMissingFileIsAServerFault(t *testing.T) {
	t.Parallel()

	directory, manifest := build(t)
	composed, err := assets.NewServer(assets.Config{Directory: directory, Manifest: manifest})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	target := manifest.Assets[module].Path
	if err := os.Remove(filepath.Join(directory, filepath.FromSlash(strings.TrimPrefix(target, "/assets/")))); err != nil {
		t.Fatalf("remove declared asset: %v", err)
	}

	response := request(t, composed, target)
	if response.Code != http.StatusInternalServerError {
		t.Errorf("GET %s = %d, want 500", target, response.Code)
	}
	if body := response.Body.String(); strings.Contains(body, directory) {
		t.Errorf("the refusal leaks the build layout: %q", body)
	}
}

// TestMountServesTheBuildInsideTheRouter proves the address space is registered
// where the composition mounts it, and that the router does not have to know
// the addresses.
func TestMountServesTheBuildInsideTheRouter(t *testing.T) {
	t.Parallel()

	composed, manifest := server(t)
	mux := http.NewServeMux()
	if err := composed.Mount(mux); err != nil {
		t.Fatalf("Mount() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, manifest.Assets[stylesheet].Path, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s through the mux = %d, want 200", manifest.Assets[stylesheet].Path, recorder.Code)
	}
	if cache := recorder.Header().Get("Cache-Control"); cache != assets.CacheHashed {
		t.Errorf("Cache-Control through the mux = %q, want %q", cache, assets.CacheHashed)
	}

	// The placeholder of an address the build does not declare, and the refusal
	// of a method that is not a read.
	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/assets/styles/private.css", nil))
	if missing.Code != http.StatusNotFound {
		t.Errorf("GET an undeclared address through the mux = %d, want 404", missing.Code)
	}
	writing := httptest.NewRecorder()
	mux.ServeHTTP(writing, httptest.NewRequest(http.MethodPost, manifest.Assets[stylesheet].Path, nil))
	if writing.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST an asset = %d, want 405", writing.Code)
	}

	if err := composed.Mount(nil); err == nil {
		t.Error("Mount(nil) accepted a nil mux")
	}
}

// TestTheRouterNeverServesAFileOutsideTheBuild pins the traversal answer at the
// level a client actually reaches. Whatever the router does with a path that is
// not canonical — today it redirects to the cleaned one — the answer is never
// the bytes: the allowlist is what decides, and the address it was asked for is
// not in it.
func TestTheRouterNeverServesAFileOutsideTheBuild(t *testing.T) {
	t.Parallel()

	directory, manifest := build(t)
	writeFile(t, directory, "secrets.env", "TOKEN=not-a-real-secret\n")

	composed, err := assets.NewServer(assets.Config{Directory: directory, Manifest: manifest})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	mux := http.NewServeMux()
	if err := composed.Mount(mux); err != nil {
		t.Fatalf("Mount() error = %v", err)
	}

	for _, target := range []string{
		"/assets/../secrets.env",
		"/assets/%2e%2e/secrets.env",
		"/assets/styles/../../secrets.env",
		"/assets/pages/../../../secrets.env",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
			if recorder.Code == http.StatusOK {
				t.Fatalf("GET %s through the mux = 200: the build served something it does not declare", target)
			}
			if strings.Contains(recorder.Body.String(), "TOKEN") {
				t.Fatalf("GET %s leaked a file outside the build", target)
			}
		})
	}
}
