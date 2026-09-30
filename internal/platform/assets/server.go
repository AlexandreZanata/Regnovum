package assets

// Serving the build this manifest describes (P18-T07C).
//
// A page references two kinds of address, and the manifest knows both: the
// published one, which carries the content hash, and the stable one of the ES
// module graph, which a module resolves by relative path and cannot know a hash
// for. This file serves exactly those, and nothing else in the directory.
//
// Three properties shape it:
//
//   - the file set is an allowlist built at composition time. A request that is
//     not one of the declared addresses is a 404, so there is no directory
//     listing, no guessed filename and no way to reach a file the manifest never
//     mentioned — traversal is not filtered, it is unrepresentable;
//   - the cache policy is a property of the address, not of the handler: a name
//     that carries the content hash is immutable for a year, a stable name is
//     revalidated every time, exactly as docs/DEPLOYMENT.md section 9 records;
//   - a manifest that does not describe one servable build is refused at
//     composition instead of being served half-way.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// The cache policy of one build, as docs/DEPLOYMENT.md section 9 records it.
const (
	// CacheHashed is the policy of an address that carries the content hash.
	// The same address can never mean different bytes, so the browser may stop
	// asking for a year.
	CacheHashed = "public, max-age=31536000, immutable"
	// CacheStable is the policy of the stable address of the module graph.
	// The bytes behind it change when the build changes, so a cache must ask
	// before reusing them.
	CacheStable = "no-cache"
)

// contentTypeByExtension is the closed vocabulary of the asset pipeline. It
// mirrors cmd/assetgen, which publishes stylesheets and ES modules only: a new
// kind of asset is a change to the pipeline and to this list, never a silent
// fallback to a guessed media type.
var contentTypeByExtension = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
}

// Config is the build one server serves.
type Config struct {
	// Directory is the build the manifest was read from (ARENA_ASSETS_DIR).
	Directory string
	// Manifest is the manifest of that build, already loaded: the composition
	// that mounts pages without one would serve links to files nobody serves.
	Manifest Manifest
	// Logger records the operational faults of the surface (a file that
	// disappeared after the manifest was read). It is optional: logging is
	// observability, not correctness.
	Logger *slog.Logger
}

// entry is one servable address: the file behind it, how to announce it and how
// long a browser may keep it.
type entry struct {
	file         string
	contentType  string
	cacheControl string
	etag         string
}

// Server answers the addresses of one build.
type Server struct {
	logger  *slog.Logger
	prefix  string
	entries map[string]entry
}

// NewServer validates the build against its manifest and builds the allowlist.
//
// It fails closed on a manifest that does not describe one servable build: an
// address that is not a clean relative path, a file extension outside the
// pipeline vocabulary, or published paths that do not share one prefix.
func NewServer(config Config) (*Server, error) {
	directory := strings.TrimSpace(config.Directory)
	if directory == "" {
		return nil, fmt.Errorf("assets: build directory is required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("assets: resolve build directory: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("assets: open build directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("assets: %s is not a directory", absolute)
	}
	if len(config.Manifest.Assets) == 0 {
		return nil, fmt.Errorf("assets: the manifest declares no asset")
	}

	prefix, err := publishPrefix(config.Manifest)
	if err != nil {
		return nil, err
	}

	server := &Server{
		logger:  config.Logger,
		prefix:  prefix,
		entries: make(map[string]entry, 2*len(config.Manifest.Assets)),
	}

	names := make([]string, 0, len(config.Manifest.Assets))
	for name := range config.Manifest.Assets {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		record := config.Manifest.Assets[name]

		published, err := relativeUnder(prefix, record.Path)
		if err != nil {
			return nil, fmt.Errorf("assets: published address of %q: %w", name, err)
		}
		if err := register(server, absolute, record.Path, published, CacheHashed, record.SHA256); err != nil {
			return nil, err
		}

		// The stable address of the module graph: the source name itself,
		// served from the same bytes under the same digest.
		stable := prefix + "/" + name
		if err := register(server, absolute, stable, name, CacheStable, record.SHA256); err != nil {
			return nil, err
		}
	}

	return server, nil
}

// register adds one address to the allowlist. Everything that could make two
// addresses mean different files is refused here, where the operator can read
// which manifest entry is wrong.
func register(server *Server, directory, address, relative, cacheControl, digest string) error {
	if err := validRelative(relative); err != nil {
		return fmt.Errorf("assets: address %s: %w", address, err)
	}
	extension := strings.ToLower(path.Ext(relative))
	contentType, known := contentTypeByExtension[extension]
	if !known {
		return fmt.Errorf("assets: address %s: the pipeline publishes no %q asset", address, extension)
	}

	file := filepath.Join(directory, filepath.FromSlash(relative))
	// The allowlist already makes an escape unrepresentable; this assertion is
	// what keeps that true if someone later relaxes validRelative.
	if !strings.HasPrefix(file, directory+string(os.PathSeparator)) {
		return fmt.Errorf("assets: address %s leaves the build directory", address)
	}
	if existing, present := server.entries[address]; present && existing.file != file {
		return fmt.Errorf("assets: address %s is declared twice with different files", address)
	}

	server.entries[address] = entry{
		file:         file,
		contentType:  contentType,
		cacheControl: cacheControl,
		etag:         `"` + digest + `"`,
	}
	return nil
}

// publishPrefix returns the one prefix every published address shares. A build
// whose published files live under different prefixes is refused: the surface
// mounts one address space, and guessing a second one would be inventing a rule
// the pipeline does not have.
func publishPrefix(manifest Manifest) (string, error) {
	names := make([]string, 0, len(manifest.Assets))
	for name := range manifest.Assets {
		names = append(names, name)
	}
	sort.Strings(names)

	var prefix []string
	for index, name := range names {
		segments := strings.Split(strings.TrimPrefix(manifest.Assets[name].Path, "/"), "/")
		if len(segments) < 2 {
			return "", fmt.Errorf("assets: published address of %q is not a file inside a directory", name)
		}
		directory := segments[:len(segments)-1]
		if index == 0 {
			prefix = directory
			continue
		}
		prefix = sharedPrefix(prefix, directory)
	}
	if len(prefix) == 0 {
		return "", fmt.Errorf("assets: the manifest publishes no shared prefix, so the build has no address space")
	}
	return "/" + strings.Join(prefix, "/"), nil
}

// sharedPrefix returns the leading segments the two paths have in common.
func sharedPrefix(left, right []string) []string {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	shared := make([]string, 0, limit)
	for index := 0; index < limit && left[index] == right[index] && left[index] != ""; index++ {
		shared = append(shared, left[index])
	}
	return shared
}

// relativeUnder returns the part of a published address below the prefix.
func relativeUnder(prefix, address string) (string, error) {
	if !strings.HasPrefix(address, prefix+"/") {
		return "", fmt.Errorf("address %q is not below %s", address, prefix)
	}
	return strings.TrimPrefix(address, prefix+"/"), nil
}

// validRelative accepts only a clean relative path with no way back up: no
// absolute form, no empty or `.` or `..` segment, no separator that is not a
// slash. path.Clean is the whole test, because a mismatch means the value was
// not canonical in the first place.
func validRelative(value string) error {
	if value == "" {
		return fmt.Errorf("is empty")
	}
	if strings.ContainsAny(value, "\\\x00") {
		return fmt.Errorf("contains a separator or a NUL byte that is not allowed")
	}
	if strings.HasPrefix(value, "/") {
		return fmt.Errorf("is not relative")
	}
	if value != path.Clean(value) || path.Clean(value) == "." {
		return fmt.Errorf("is not a clean relative path")
	}
	return nil
}

// Prefix is the address space the build is served under, taken from the
// manifest instead of configured: the pages already reference it.
func (server *Server) Prefix() string { return server.prefix }

// Mount registers the build on the mux under the prefix the manifest publishes.
// It is content, not an application endpoint: it declares no route in the
// registry and therefore takes no part in the contract.
func (server *Server) Mount(mux *http.ServeMux) error {
	if mux == nil {
		return fmt.Errorf("assets: mux is required")
	}
	mux.Handle("GET "+server.prefix+"/", server)
	return nil
}

// ServeHTTP answers one declared address and refuses every other one.
func (server *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	asset, declared := server.entries[r.URL.Path]
	if !declared {
		// No listing, no guessing, no fallback: the addresses are the ones the
		// manifest published and the answer to anything else is that it does
		// not exist.
		http.NotFound(w, r)
		return
	}

	file, err := os.Open(asset.file)
	if err != nil {
		server.report(r.Context(), "assets: declared asset is missing from the build", asset.file, err)
		// The manifest promised this address, so a missing file is a fault of
		// the deployment and not a not-found of the reader — and the answer
		// says so without naming the path.
		http.Error(w, "asset unavailable", http.StatusInternalServerError)
		return
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		server.report(r.Context(), "assets: declared asset is not a regular file", asset.file, err)
		http.Error(w, "asset unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", asset.contentType)
	w.Header().Set("Cache-Control", asset.cacheControl)
	w.Header().Set("ETag", asset.etag)
	http.ServeContent(w, r, path.Base(asset.file), info.ModTime(), file)
}

// report records an operational fault without leaking the build layout to the
// reader, carrying the context of the request that hit it: a fault logged with
// no context is a record whose cancellation nobody can follow.
func (server *Server) report(ctx context.Context, message, file string, err error) {
	if server.logger == nil {
		return
	}
	attributes := []slog.Attr{slog.String("asset", filepath.Base(file))}
	if err != nil {
		attributes = append(attributes, slog.String("error", err.Error()))
	}
	server.logger.LogAttrs(ctx, slog.LevelWarn, message, attributes...)
}
