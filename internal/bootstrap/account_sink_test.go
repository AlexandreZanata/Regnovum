// Tests of the local email sink as the composition installs it (P18-T07): the
// code of a registration is read back from the filesystem — the reader opens no
// in-process handle, it lists a directory and parses documents, which is what a
// harness running beside the server does — and the journey is then completed
// over real PostgreSQL.
package bootstrap_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/emailsink"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// readSinkMessages is a reader from outside the process under test: it knows
// the directory and the document, nothing else.
func readSinkMessages(t *testing.T, directory string) []emailsink.Message {
	t.Helper()

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", directory, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	messages := make([]emailsink.Message, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", name, err)
		}
		var message emailsink.Message
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatalf("message %s is not a complete JSON document: %v", name, err)
		}
		messages = append(messages, message)
	}
	return messages
}

// lastToken is the most recent code delivered to one address, which is how a
// harness completes a journey it did not initiate.
func lastToken(t *testing.T, directory, email string, kind emailsink.Kind) string {
	t.Helper()

	token := ""
	for _, message := range readSinkMessages(t, directory) {
		if message.Email == email && message.Kind == kind {
			token = message.Token
		}
	}
	if token == "" {
		t.Fatalf("the directory holds no %s message for %s", kind, email)
	}
	return token
}

// TestTheDirectorySinkCarriesTheJourneyToAnotherProcess is the validation of
// the sink's purpose: with the directory configured, the server delivers the
// registration code to a file, and a reader that never touches the server's
// memory completes the account journey with it.
func TestTheDirectorySinkCarriesTheJourneyToAnotherProcess(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "email-sink")
	database := dbtest.New(t)
	surface, err := bootstrap.ComposeAccount(bootstrap.Options{
		Env:     config.EnvTest,
		Logger:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool:    database.Pool.Pool(),
		Clock:   clockseed.NewClock(),
		Random:  clockseed.NewRandom(),
		Assets:  manifestFixture(t),
		SinkDir: directory,
	})
	if err != nil {
		t.Fatalf("ComposeAccount() error = %v", err)
	}

	if surface.LocalSink() != nil {
		t.Error("the composition installed an in-process sink and a directory at the same time")
	}
	if surface.SinkDirectory() != directory {
		t.Errorf("SinkDirectory() = %q, want %q", surface.SinkDirectory(), directory)
	}

	clock := clockseed.NewClock()
	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clock)
	mux, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{}, []httpserver.Surface{surface.Surface()})
	if err != nil {
		t.Fatalf("httpserver.NewMuxWith() error = %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := browser(t)
	const email = "sink-reader@example.test"
	const password = "correct horse battery staple"

	register := url.Values{
		"email":      {email},
		"password":   {password},
		"csrf_token": {csrfToken(t, openPage(t, client, server, "/register"))},
	}
	if response := submit(t, client, server, "/register", register); response.StatusCode != http.StatusOK {
		t.Fatalf("POST /register status = %d, want 200", response.StatusCode)
	}

	// The reader of another process: a directory listing, a parsed document and
	// the code it carries.
	token := lastToken(t, directory, email, emailsink.KindVerification)

	verify := url.Values{
		"token":      {token},
		"csrf_token": {csrfToken(t, openPage(t, client, server, "/verify"))},
	}
	if response := submit(t, client, server, "/verify", verify); response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("POST /verify status = %d, want 200 (body: %.200s)", response.StatusCode, body)
	}

	// Only an active account reaches the session, so a 303 proves that the code
	// read from the directory was the one the account was waiting for.
	login := url.Values{
		"email":      {email},
		"password":   {password},
		"csrf_token": {csrfToken(t, openPage(t, client, server, "/login"))},
	}
	if response := submit(t, client, server, "/login", login); response.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("POST /login status = %d, want 303 (body: %.200s)", response.StatusCode, body)
	}
}

// TestTheCompositionRefusesTheDirectorySinkInProduction guards the rule at the
// composition, not only at the configuration edge: whoever builds the surface
// cannot install a directory of account codes in an environment that serves
// real accounts.
func TestTheCompositionRefusesTheDirectorySinkInProduction(t *testing.T) {
	t.Parallel()

	database := dbtest.New(t)
	_, err := bootstrap.ComposeAccount(bootstrap.Options{
		Env:     config.EnvProduction,
		Logger:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool:    database.Pool.Pool(),
		Clock:   clockseed.NewClock(),
		Random:  clockseed.NewRandom(),
		Assets:  manifestFixture(t),
		SinkDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("the composition accepted the local email sink in production")
	}
	if !strings.Contains(err.Error(), "sink") || !strings.Contains(err.Error(), "production") {
		t.Errorf("the refusal must name the replacement and the environment: %v", err)
	}
}
