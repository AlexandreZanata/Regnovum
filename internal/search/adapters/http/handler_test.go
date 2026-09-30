package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/search/adapters/http"
	"github.com/AlexandreZanata/Regnovum/internal/search/application"
)

type arenaRepo struct{}

func (arenaRepo) SearchArenas(context.Context, string, string, *application.Cursor, int) ([]application.ArenaResult, error) {
	return []application.ArenaResult{{ID: "arena-1", Slug: "arena-1", Statement: "Fusão", Language: "pt-BR", PublishedAt: time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)}}, nil
}

type argumentRepo struct{}

func (argumentRepo) SearchArguments(context.Context, string, string, *application.Cursor, int) ([]application.ArgumentResult, error) {
	return []application.ArgumentResult{{ID: "argument-1", ArenaID: "arena-1", Content: "Energia", Language: "pt-BR", CreatedAt: time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)}}, nil
}

func newHandler(t *testing.T) *adapterhttp.Handler {
	t.Helper()
	codec, err := application.NewCursorCodec([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	arenas, err := application.NewArenaSearchUseCase(arenaRepo{}, codec)
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := application.NewArgumentSearchUseCase(argumentRepo{}, codec)
	if err != nil {
		t.Fatal(err)
	}
	return adapterhttp.NewHandler(adapterhttp.HandlerConfig{Arenas: arenas, Arguments: arguments})
}

func TestSearchHandlerReturnsPublicCacheableDocument(t *testing.T) {
	handler := newHandler(t)
	recorder := httptest.NewRecorder()
	handler.SearchArenas(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/search/arenas?q=fusão&language=pt-BR", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Cache-Control"), "public") || recorder.Header().Get("ETag") == "" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"next_cursor":null`) {
		t.Fatalf("body = %s, want explicit null next_cursor", recorder.Body.String())
	}

	revalidated := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/search/arenas?q=fusão", nil)
	request.Header.Set("If-None-Match", recorder.Header().Get("ETag"))
	handler.SearchArenas(revalidated, request)
	if revalidated.Code != http.StatusNotModified {
		t.Fatalf("revalidated status = %d, want 304", revalidated.Code)
	}
}

func TestSearchHandlerRejectsInvalidInputAsProblemDetails(t *testing.T) {
	handler := newHandler(t)
	for _, query := range []string{"", "?q=x&language=de-DE", "?q=x&limit=not-a-number"} {
		recorder := httptest.NewRecorder()
		handler.SearchArguments(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/search/arguments"+query, nil))
		if recorder.Code != http.StatusBadRequest || recorder.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatalf("query %q status=%d content-type=%q body=%s", query, recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
		}
	}
}
