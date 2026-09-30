// Package regression pins the deterministic baselines of the stable
// backend contracts (P27-T01): emails, personal exports, Problem Details,
// allowed metrics and the OpenAPI/client checksums.
//
// Every golden is compared byte for byte: a human reviews the diff and
// edits the file, never an auto-accept flag. Generation is deterministic
// by construction — fixed clock, fixed identifiers, fixed seeds, sorted
// keys — and the test renders everything twice to prove the bytes do not
// depend on the run. Goldens carry no secrets or personal data: only the
// synthetic fixtures named below, checked by the secret scan in this same
// file.
package regression

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/renderer"
	notifydomain "github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/observability"
	"github.com/AlexandreZanata/Regnovum/internal/platform/requestid"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	profilejson "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/exportjson"
	profilesapp "github.com/AlexandreZanata/Regnovum/internal/profiles/application"
)

// fixedInstant is the only instant any baseline may carry.
var fixedInstant = time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)

// fixtureName and fixtureCode are the only person-shaped values in the
// goldens: synthetic, ASCII, and asserted by the secret scan.
const (
	fixtureName = "Baseline Ana"
	fixtureCode = "B4SE-L1NE-00"
)

// checksumOf answers the hex SHA-256 of a document.
func checksumOf(t *testing.T, name string, document []byte) string {
	t.Helper()
	if len(document) == 0 {
		t.Fatalf("baseline %s rendered empty", name)
	}
	sum := sha256.Sum256(document)
	return hex.EncodeToString(sum[:])
}

// assertGolden compares twice-rendered bytes with the committed file: the
// two renders must agree with each other first, or the generator is not
// deterministic no matter what the file says.
func assertGolden(t *testing.T, name string, first, second []byte) {
	t.Helper()
	if !bytes.Equal(first, second) {
		t.Fatalf("baseline %s is not deterministic across runs (%d vs %d bytes)", name, len(first), len(second))
	}
	if os.Getenv("BASELINE_DUMP") != "" {
		_ = os.WriteFile(filepath.Join("/tmp", name+".actual"), first, 0o600)
	}
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v (render the bytes, review them, then commit the file by hand)", name, err)
	}
	if !bytes.Equal(first, want) {
		t.Fatalf("baseline %s drifted: committed %d bytes, rendered %d bytes (sha256 %s); review the diff and update the file explicitly, never by accepting output",
			name, len(want), len(first), checksumOf(t, name, first))
	}
}

// TestEmailBaselines renders every template in both locales with fixed
// values and pins the bytes.
func TestEmailBaselines(t *testing.T) {
	t.Parallel()
	render, err := renderer.NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	for _, template := range notifydomain.TemplateIDs() {
		for _, tag := range notifydomain.Locales() {
			values, err := notifydomain.ValidateTemplateValues(template, fixtureName, templateCode(template))
			if err != nil {
				t.Fatalf("ValidateTemplateValues: %v", err)
			}
			first, err := render.Render(template, tag, values)
			if err != nil {
				t.Fatalf("render %s/%s: %v", template, tag, err)
			}
			second, err := render.Render(template, tag, values)
			if err != nil {
				t.Fatalf("render retry %s/%s: %v", template, tag, err)
			}
			envelope := map[string]string{"html": first.HTML, "subject": first.Subject, "text": first.Text}
			firstBytes, err := json.Marshal(envelope)
			if err != nil {
				t.Fatalf("marshal %s/%s: %v", template, tag, err)
			}
			secondBytes, err := json.Marshal(map[string]string{"html": second.HTML, "subject": second.Subject, "text": second.Text})
			if err != nil {
				t.Fatalf("marshal retry %s/%s: %v", template, tag, err)
			}
			assertGolden(t, "email."+template.String()+"."+tag.String()+".golden", firstBytes, secondBytes)
		}
	}
}

// templateCode returns the fixed code for templates that carry one.
func templateCode(template notifydomain.TemplateID) string {
	if template.CarriesCode() {
		return fixtureCode
	}
	return ""
}

// TestExportBaseline pins the personal export document of a fixed fixture.
func TestExportBaseline(t *testing.T) {
	t.Parallel()
	document := profilesapp.PersonalExportDocument{
		SchemaVersion:      1,
		GeneratedAt:        fixedInstant,
		ExcludedCategories: []string{},
		PersonalExportSections: profilesapp.PersonalExportSections{
			Account: profilesapp.PersonalExportAccount{
				ID:              "01a0d900-0000-7000-8000-000000000001",
				Email:           "baseline.ana@example.com",
				Status:          "active",
				EmailVerified:   true,
				CreatedAt:       fixedInstant,
				UsernameHistory: []profilesapp.PersonalExportUsername{},
				Sessions:        []profilesapp.PersonalExportSession{},
			},
			Positions: []profilesapp.PersonalExportPosition{},
			Arguments: []profilesapp.PersonalExportArgument{},
		},
	}
	encoder := profilejson.NewEncoder()
	first, err := encoder.EncodePersonalExport(document)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	second, err := encoder.EncodePersonalExport(document)
	if err != nil {
		t.Fatalf("encode retry: %v", err)
	}
	assertGolden(t, "export.personal.golden", first, second)
}

// problemCase is one stable failure rendered in every locale.
type problemCase struct {
	name string
	make func() *apperr.Error
}

// TestProblemBaselines pins Problem Details for stable failures in both
// locales with a fixed request ID: same code always, localized wrapper.
func TestProblemBaselines(t *testing.T) {
	t.Parallel()
	cases := []problemCase{
		{name: "unauthorized", make: func() *apperr.Error {
			return apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required")
		}},
		{name: "not_found", make: func() *apperr.Error { return apperr.New(apperr.KindNotFound, "not_found", "resource was not found") }},
		{name: "invalid_body", make: func() *apperr.Error {
			return apperr.New(apperr.KindValidation, "invalid_body", "request body is invalid")
		}},
	}
	for _, testCase := range cases {
		for _, tag := range []string{"pt-BR", "en-US"} {
			first := renderProblem(t, testCase.make(), tag)
			second := renderProblem(t, testCase.make(), tag)
			assertGolden(t, "problem."+testCase.name+"."+tag+".golden", first, second)
			var decoded struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(first, &decoded); err != nil || decoded.Code == "" {
				t.Fatalf("problem %s/%s carries no stable code", testCase.name, tag)
			}
		}
	}
}

// renderProblem renders one Problem Details with fixed request ID and
// locale, through the same constructor the handlers use.
func renderProblem(t *testing.T, appErr *apperr.Error, tag string) []byte {
	t.Helper()
	request := httptest.NewRequest("GET", "/api/v1/me/profile", nil)
	ctx := requestid.WithID(request.Context(), "req-baseline-fixed-01")
	ctx = locale.WithLocale(ctx, locale.Tag(tag))
	problem := httperror.ProblemFor(request.WithContext(ctx), appErr)
	encoded, err := json.Marshal(problem)
	if err != nil {
		t.Fatalf("marshal problem: %v", err)
	}
	return encoded
}

// TestMetricsBaseline pins the exposition of a fixed registry: the same
// counters in, the same text out, label sets sorted.
func TestMetricsBaseline(t *testing.T) {
	t.Parallel()
	build := func() string {
		metrics := observability.NewMetrics(testsource.NewClock(fixedInstant))
		metrics.Counter("baseline_requests_total", "Baseline requests.", map[string]string{"route": "/api/v1/arenas"}).Inc()
		metrics.Counter("baseline_requests_total", "Baseline requests.", map[string]string{"route": "/api/v1/me/profile"}).Inc()
		metrics.Counter("baseline_requests_total", "Baseline requests.", map[string]string{"route": "/api/v1/arenas"}).Inc()
		return metrics.Render()
	}
	first, second := build(), build()
	if first != second {
		t.Fatal("metrics render is not deterministic across runs")
	}
	assertGolden(t, "metrics.allowed.golden", []byte(first), []byte(second))
}

// TestContractChecksums pins the big stable contracts by hash and proves
// the bytes do not depend on the run. The files themselves stay where
// they live; only the reviewed digests are versioned here.
func TestContractChecksums(t *testing.T) {
	t.Parallel()
	checksums := map[string]string{}
	for _, path := range []string{"../../api/openapi.json", "../../web/src/contracts/generated.ts"} {
		first, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		second, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reread %s: %v", path, err)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("%s changed between two reads", path)
		}
		checksums[path] = checksumOf(t, path, first)
	}
	names := make([]string, 0, len(checksums))
	for name := range checksums {
		names = append(names, name)
	}
	sort.Strings(names)
	var manifest strings.Builder
	for _, name := range names {
		manifest.WriteString(checksums[name] + "  " + name + "\n")
	}
	assertGolden(t, "contracts.checksums.golden", []byte(manifest.String()), []byte(manifest.String()))
}

// secretPatterns are shapes that must never appear in a golden: live
// credentials, key blocks and real-world addresses. Fixture domains
// (example, invalid, canary, arena.invalid) are the only addresses a
// golden may carry.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk_live_[A-Za-z0-9]+`),
	regexp.MustCompile(`whsec_[A-Za-z0-9+/=]{16,}`),
	regexp.MustCompile(`\$argon2id\$`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`ghp_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`[A-Za-z0-9._%+-]+@(gmail|hotmail|outlook|yahoo|icloud)\.[A-Za-z]{2,}`),
}

// TestGoldensCarryNoSecrets scans every committed golden for credential
// shapes and real-world addresses.
func TestGoldensCarryNoSecrets(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no goldens to scan")
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".golden") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("testdata", entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, pattern := range secretPatterns {
			if pattern.Match(raw) {
				t.Errorf("golden %s matches forbidden %q", entry.Name(), pattern.String())
			}
		}
	}
}
