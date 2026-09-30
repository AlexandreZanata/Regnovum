package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/transparency/application"
)

const exportTestSecret = "0123456789abcdef0123456789abcdef"

const (
	exportArenaID       = "018f6b2a-0000-7000-8000-0000000000a1"
	exportFirstArgID    = "018f6b2a-0000-7000-8000-0000000000b1"
	exportSecondArgID   = "018f6b2a-0000-7000-8000-0000000000b2"
	withdrawnSecretText = "withdrawn-secret-content"
)

var exportTestInstant = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

// fakeExportRepository serves a fixed page and records the requested page
// limits, so the HTTP layer can be observed without a database.
type fakeExportRepository struct {
	header    *application.ExportHeader
	arguments []application.ExportArgument
	limits    []int
}

func (f *fakeExportRepository) GetArenaExportHeader(_ context.Context, arenaID string) (*application.ExportHeader, error) {
	if f.header == nil || arenaID != f.header.Arena.ID {
		return nil, application.ErrArenaNotFound
	}
	return f.header, nil
}

func (f *fakeExportRepository) ListArenaExportArguments(_ context.Context, _ string, _ *application.ExportPosition, limit int) ([]application.ExportArgument, error) {
	f.limits = append(f.limits, limit)
	return f.arguments, nil
}

func newExportMux(t *testing.T, repository *fakeExportRepository) http.Handler {
	t.Helper()
	codec, err := application.NewExportCursorCodec([]byte(exportTestSecret))
	if err != nil {
		t.Fatalf("NewExportCursorCodec: %v", err)
	}
	useCase, err := application.NewGetArenaExportUseCase(repository, codec)
	if err != nil {
		t.Fatalf("NewGetArenaExportUseCase: %v", err)
	}
	handler := adapterhttp.NewExportHandler(adapterhttp.ExportHandlerConfig{UseCase: useCase})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return mux
}

func exportTestHeader(participants int64) *application.ExportHeader {
	return &application.ExportHeader{
		Arena: application.ExportArena{
			ID:          exportArenaID,
			Slug:        "export-probe-arena",
			Statement:   "Export probe statement",
			Context:     "Export probe context",
			Category:    "technology",
			Language:    "pt-BR",
			Status:      "published",
			PublishedAt: exportTestInstant,
		},
		Positions: application.ExportPositions{
			Initial:         application.ExportDistribution{Agree: 3, Disagree: 1, Undecided: 1},
			Current:         application.ExportDistribution{Agree: 2, Disagree: 2, Undecided: 1},
			Participants:    participants,
			PositionChanges: 7,
		},
		Influence: application.ExportInfluence{ValidAttributions: 9, InfluencedAuthors: 4},
	}
}

func exportTestArguments() []application.ExportArgument {
	firstContent := "First public argument"
	withdrawnContent := withdrawnSecretText
	withdrawnAt := exportTestInstant.Add(2 * time.Minute)
	sourceDescription := "Source description"

	return []application.ExportArgument{
		{
			ID:        exportFirstArgID,
			Relation:  "support",
			Content:   &firstContent,
			Status:    "published",
			CreatedAt: exportTestInstant.Add(time.Minute),
			Sources: []application.ExportSource{
				{URL: "https://example.com/source", Description: &sourceDescription},
				{URL: "https://example.com/other"},
			},
			Influence: application.ExportArgumentInfluence{ValidAttributions: 5, DistinctPeople: 3},
		},
		{
			ID:          exportSecondArgID,
			ParentID:    exportFirstArgID,
			Relation:    "oppose",
			Content:     &withdrawnContent,
			Status:      "withdrawn",
			CreatedAt:   exportTestInstant.Add(2 * time.Minute),
			WithdrawnAt: &withdrawnAt,
			// The real repository withholds content and sources for
			// withdrawn arguments; the fixture mirrors that contract.
			Influence: application.ExportArgumentInfluence{},
		},
	}
}

const exportGoldenDocument = `{
  "schema_version": 1,
  "arena": {
    "id": "018f6b2a-0000-7000-8000-0000000000a1",
    "slug": "export-probe-arena",
    "statement": "Export probe statement",
    "context": "Export probe context",
    "category": "technology",
    "language": "pt-BR",
    "status": "published",
    "published_at": "2026-09-18T12:00:00Z",
    "closes_at": null
  },
  "positions": {
    "participants_total": 12,
    "suppressed": false,
    "position_changes": 7,
    "initial": {"agree": 3, "disagree": 1, "undecided": 1},
    "current": {"agree": 2, "disagree": 2, "undecided": 1}
  },
  "influence": {"valid_attributions": 9, "influenced_authors": 4},
  "arguments": {
    "items": [
      {
        "id": "018f6b2a-0000-7000-8000-0000000000b1",
        "parent_id": null,
        "relation": "support",
        "content": "First public argument",
        "status": "published",
        "created_at": "2026-09-18T12:01:00Z",
        "withdrawn_at": null,
        "sources": [
          {"url": "https://example.com/source", "description": "Source description"},
          {"url": "https://example.com/other", "description": null}
        ],
        "influence": {"valid_attributions": 5, "distinct_people": 3}
      },
      {
        "id": "018f6b2a-0000-7000-8000-0000000000b2",
        "parent_id": "018f6b2a-0000-7000-8000-0000000000b1",
        "relation": "oppose",
        "content": null,
        "status": "withdrawn",
        "created_at": "2026-09-18T12:02:00Z",
        "withdrawn_at": "2026-09-18T12:02:00Z",
        "sources": [],
        "influence": {"valid_attributions": 0, "distinct_people": 0}
      }
    ],
    "next_cursor": null
  }
}`

func TestExportJSONGoldenSchemaCacheAndPrivacy(t *testing.T) {
	repository := &fakeExportRepository{header: exportTestHeader(12), arguments: exportTestArguments()}
	mux := newExportMux(t, repository)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/arenas/"+exportArenaID+"/export", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	cacheControl := recorder.Header().Get("Cache-Control")
	if !strings.Contains(cacheControl, "public") || !strings.Contains(cacheControl, "max-age=60") || strings.Contains(cacheControl, "no-store") {
		t.Fatalf("Cache-Control = %q, want public, max-age=60", cacheControl)
	}
	if recorder.Header().Get("ETag") == "" {
		t.Fatal("public export must carry a strong ETag")
	}

	var golden map[string]any
	if err := json.Unmarshal([]byte(exportGoldenDocument), &golden); err != nil {
		t.Fatalf("golden document is invalid: %v", err)
	}
	document := decodeTransparencyJSON(t, recorder.Body.Bytes())
	if !reflect.DeepEqual(document, golden) {
		t.Fatalf("export document diverged from the golden schema:\n got: %#v\nwant: %#v", document, golden)
	}

	body := recorder.Body.String()
	for _, marker := range []string{
		"account_id", "author_id", "attributor_id", "attributor\"", "email", "@", "stripe", "cus_", "creator",
		withdrawnSecretText, "position_history",
	} {
		if strings.Contains(body, marker) {
			t.Fatalf("public export leaks private marker %q", marker)
		}
	}

	// Revalidation is cheap: the ETag (content hash) answers 304 without a
	// body.
	etag := recorder.Header().Get("ETag")
	revalidated := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/arenas/"+exportArenaID+"/export", nil)
	request.Header.Set("If-None-Match", etag)
	mux.ServeHTTP(revalidated, request)
	if revalidated.Code != http.StatusNotModified {
		t.Fatalf("revalidation status = %d, want 304", revalidated.Code)
	}
	if revalidated.Body.Len() != 0 {
		t.Fatalf("304 body has %d bytes, want empty", revalidated.Body.Len())
	}
}

func TestExportSuppressesSmallSamplesThroughHTTP(t *testing.T) {
	repository := &fakeExportRepository{header: exportTestHeader(3), arguments: exportTestArguments()}
	mux := newExportMux(t, repository)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/arenas/"+exportArenaID+"/export", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", recorder.Code, recorder.Body.String())
	}

	document := decodeTransparencyJSON(t, recorder.Body.Bytes())
	positions, _ := document["positions"].(map[string]any)
	if positions["suppressed"] != true || positions["participants_total"] != float64(0) || positions["position_changes"] != float64(0) {
		t.Fatalf("small sample must publish no counts: %v", positions)
	}
	for _, name := range []string{"initial", "current"} {
		distribution, _ := positions[name].(map[string]any)
		for key, value := range distribution {
			if value != float64(0) {
				t.Fatalf("small sample distribution %s.%s = %v, want zero", name, key, value)
			}
		}
	}
}

func TestExportValidatesInputAndClampsLimit(t *testing.T) {
	repository := &fakeExportRepository{header: exportTestHeader(12), arguments: exportTestArguments()}
	mux := newExportMux(t, repository)

	unknown := httptest.NewRecorder()
	mux.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/api/v1/arenas/018f6b2a-0000-7000-8000-00000000dead/export?probe=deadbeef", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown arena status = %d, want 404", unknown.Code)
	}
	if contentType := unknown.Header().Get("Content-Type"); contentType != "application/problem+json" {
		t.Fatalf("problem Content-Type = %q", contentType)
	}
	problem := decodeTransparencyJSON(t, unknown.Body.Bytes())
	if problem["code"] != "arena_not_found" {
		t.Fatalf("problem code = %v, want arena_not_found", problem["code"])
	}
	if strings.Contains(unknown.Body.String(), "deadbeef") {
		t.Fatal("unknown arena must never be reflected back")
	}

	forged := httptest.NewRecorder()
	mux.ServeHTTP(forged, httptest.NewRequest(http.MethodGet, "/api/v1/arenas/"+exportArenaID+"/export?cursor=forged.cursor", nil))
	if forged.Code != http.StatusBadRequest {
		t.Fatalf("forged cursor status = %d, want 400", forged.Code)
	}
	if problem := decodeTransparencyJSON(t, forged.Body.Bytes()); problem["code"] != "invalid_cursor" {
		t.Fatalf("forged cursor problem = %v", problem)
	}

	for _, limit := range []string{"abc", "-1"} {
		invalid := httptest.NewRecorder()
		mux.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/v1/arenas/"+exportArenaID+"/export?limit="+limit, nil))
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("limit %q status = %d, want 400", limit, invalid.Code)
		}
	}

	// Above the maximum, the page is clamped before the repository is
	// reached: one request can never materialize an unbounded Arena.
	clamped := httptest.NewRecorder()
	mux.ServeHTTP(clamped, httptest.NewRequest(http.MethodGet, "/api/v1/arenas/"+exportArenaID+"/export?limit=100000", nil))
	if clamped.Code != http.StatusOK {
		t.Fatalf("clamped status = %d (body: %s)", clamped.Code, clamped.Body.String())
	}
	if len(repository.limits) != 1 || repository.limits[0] != application.MaxExportPageLimit+1 {
		t.Fatalf("repository limits = %v, want the clamped lookahead %d", repository.limits, application.MaxExportPageLimit+1)
	}
}
