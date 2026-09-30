package httperror

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/requestid"
)

// requestFor builds a test request carrying the given request ID and
// interface locale in its context, mirroring the requestid and locale
// middlewares.
func requestFor(requestID string, tag locale.Tag) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	ctx := request.Context()
	if requestID != "" {
		ctx = requestid.WithID(ctx, requestID)
	}
	if tag != "" {
		ctx = locale.WithLocale(ctx, tag)
	}
	return request.WithContext(ctx)
}

// kindCase fixes the expected public shape of one kind.
type kindCase struct {
	status int
	typ    string
	title  string
}

// kindCases covers the validation minimum: one case per stable kind with
// the exact public status mapping. Titles are asserted in the en-US
// catalog locale, pinning the English problem surface.
func kindCases() map[apperr.Kind]kindCase {
	return map[apperr.Kind]kindCase{
		apperr.KindValidation:   {http.StatusBadRequest, "validation", "The request is invalid"},
		apperr.KindUnauthorized: {http.StatusUnauthorized, "unauthorized", "Authentication is required"},
		apperr.KindForbidden:    {http.StatusForbidden, "forbidden", "You are not allowed to do this"},
		apperr.KindNotFound:     {http.StatusNotFound, "not_found", "Resource not found"},
		apperr.KindConflict:     {http.StatusConflict, "conflict", "The request conflicts with the current state"},
		apperr.KindRateLimited:  {http.StatusTooManyRequests, "rate_limited", "Too many requests"},
		apperr.KindInternal:     {http.StatusInternalServerError, "internal", "Internal error"},
	}
}

func TestKindMappingTable(t *testing.T) {
	for kind, want := range kindCases() {
		err := apperr.New(kind, "ARENA-TEST", "public detail")
		problem := ProblemFor(requestFor("req-1", locale.AmericanEnglish), err)

		if problem.Status != want.status {
			t.Errorf("kind %s: status = %d, want %d", kind, problem.Status, want.status)
		}
		if problem.Type != "https://goyim-arena.dev/problems/"+want.typ {
			t.Errorf("kind %s: type = %q", kind, problem.Type)
		}
		if problem.Title != want.title {
			t.Errorf("kind %s: title = %q, want %q", kind, problem.Title, want.title)
		}
		if problem.Code != "ARENA-TEST" {
			t.Errorf("kind %s: code = %q, want stable code", kind, problem.Code)
		}
		if problem.RequestID != "req-1" {
			t.Errorf("kind %s: request id = %q", kind, problem.RequestID)
		}
	}
}

func TestWriteProblemRendersProblemDetails(t *testing.T) {
	for kind, want := range kindCases() {
		recorder := httptest.NewRecorder()
		status := WriteProblem(recorder, requestFor("req-42", locale.AmericanEnglish), apperr.New(kind, "ARENA-TEST", "public detail"))

		if status != want.status {
			t.Errorf("kind %s: rendered status = %d, want %d", kind, status, want.status)
		}
		if recorder.Code != want.status {
			t.Errorf("kind %s: response code = %d, want %d", kind, recorder.Code, want.status)
		}
		if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
			t.Errorf("kind %s: content type = %q, want application/problem+json", kind, got)
		}

		var problem Problem
		if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
			t.Fatalf("kind %s: body is not valid JSON: %v\n%s", kind, err, recorder.Body.String())
		}
		if problem.Status != want.status || problem.Code != "ARENA-TEST" || problem.RequestID != "req-42" {
			t.Errorf("kind %s: unexpected problem: %+v", kind, problem)
		}
	}
}

// TestProblemTitlesLocalizeByNegotiatedLocale pins the P02-T08 exit gate:
// problem titles localize through the typed catalog using the request's
// negotiated interface locale, for every kind and every supported locale.
func TestProblemTitlesLocalizeByNegotiatedLocale(t *testing.T) {
	locales := []locale.Tag{locale.BrazilianPortuguese, locale.AmericanEnglish}
	for _, tag := range locales {
		for kind := range kindCases() {
			request := requestFor("req", tag)
			got := ProblemFor(request, apperr.New(kind, "ARENA-TEST", "")).Title

			want, err := i18n.Message(string(tag), "errors."+string(kind)+".title")
			if err != nil {
				t.Fatalf("catalog missing %s title for %s: %v", kind, tag, err)
			}
			if got != want {
				t.Errorf("%s %s: title = %q, want catalog %q", tag, kind, got, want)
			}
		}
	}

	// Exact spot checks keep the catalog from drifting silently.
	pt := ProblemFor(requestFor("req", locale.BrazilianPortuguese), apperr.New(apperr.KindNotFound, "ARENA-TEST", ""))
	if pt.Title != "Recurso não encontrado" {
		t.Errorf("pt-BR not_found title = %q", pt.Title)
	}
	en := ProblemFor(requestFor("req", locale.AmericanEnglish), apperr.New(apperr.KindValidation, "ARENA-TEST", ""))
	if en.Title != "The request is invalid" {
		t.Errorf("en-US validation title = %q", en.Title)
	}
}

// TestProblemTitleFallsBackToDefaultLocale proves the negotiation-miss
// path: without a locale in the context the problem renders in the default
// catalog locale, never in request-controlled data.
func TestProblemTitleFallsBackToDefaultLocale(t *testing.T) {
	request := requestFor("req", "")
	problem := ProblemFor(request, apperr.New(apperr.KindNotFound, "ARENA-TEST", ""))

	want, err := i18n.Message(i18n.DefaultLocale, "errors.not_found.title")
	if err != nil {
		t.Fatalf("catalog missing default title: %v", err)
	}
	if problem.Title != want || problem.Title == "" {
		t.Errorf("fallback title = %q, want default catalog %q", problem.Title, want)
	}
}

// TestInternalCauseNeverSerializes is the security invariant: the wrapped
// cause (and the internal detail) must never reach the public body, while
// staying available for logs via errors.Is/As.
func TestInternalCauseNeverSerializes(t *testing.T) {
	sensitive := fmt.Errorf("pq: password authentication failed for user %q dsn=postgres://arena:hunter2@db", "admin")
	internal := apperr.New(apperr.KindInternal, "ARENA-DB-FAIL", "database unavailable").
		WithCause(sensitive)

	recorder := httptest.NewRecorder()
	status := WriteProblem(recorder, requestFor("req-7", locale.AmericanEnglish), internal)

	body := recorder.Body.String()
	for _, leaked := range []string{"hunter2", "admin", "pq:", "database unavailable"} {
		if strings.Contains(body, leaked) {
			t.Errorf("internal body leaked %q: %s", leaked, body)
		}
	}
	if status != http.StatusInternalServerError {
		t.Fatalf("internal status = %d", status)
	}

	var problem Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if problem.Detail != "" {
		t.Errorf("internal problem must have empty detail, got %q", problem.Detail)
	}
	if problem.Title != "Internal error" {
		t.Errorf("internal problem must keep the generic catalog title, got %q", problem.Title)
	}
	if problem.Code != "ARENA-DB-FAIL" {
		t.Errorf("internal problem keeps the stable vocabulary code, got %q", problem.Code)
	}

	// The cause stays available for logs through unwrapping.
	if !errors.Is(internal, sensitive) {
		t.Error("internal cause must remain unwrappable for logging")
	}
	var appError *apperr.Error
	if !errors.As(internal, &appError) || appError.Code() != "ARENA-DB-FAIL" {
		t.Error("internal code must remain available for logs")
	}
}

func TestForeignErrorCollapsesToGenericInternalProblem(t *testing.T) {
	foreign := fmt.Errorf("explosive detail: secret-value-123")

	recorder := httptest.NewRecorder()
	status := WriteProblem(recorder, requestFor("req-8", locale.AmericanEnglish), foreign)

	if status != http.StatusInternalServerError {
		t.Fatalf("foreign error status = %d", status)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "secret-value-123") || strings.Contains(body, "explosive") {
		t.Errorf("foreign error text leaked into the body: %s", body)
	}

	var problem Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if problem.Code != "ARENA-internal" || problem.Detail != "" {
		t.Errorf("foreign error must render the generic problem: %+v", problem)
	}
}

func TestPublicDetailExposedForSafeKindsOnly(t *testing.T) {
	safe := []apperr.Kind{
		apperr.KindValidation, apperr.KindUnauthorized, apperr.KindForbidden,
		apperr.KindNotFound, apperr.KindConflict, apperr.KindRateLimited,
	}
	for _, kind := range safe {
		problem := ProblemFor(requestFor("req", locale.AmericanEnglish), apperr.New(kind, "ARENA-X", "username already taken"))
		if problem.Detail != "username already taken" {
			t.Errorf("kind %s: safe detail hidden: %q", kind, problem.Detail)
		}
	}

	internal := ProblemFor(requestFor("req", locale.AmericanEnglish), apperr.New(apperr.KindInternal, "ARENA-X", "row lock timeout"))
	if internal.Detail != "" {
		t.Errorf("internal detail must be hidden, got %q", internal.Detail)
	}
}

func TestRateLimitedProblemShape(t *testing.T) {
	problem := ProblemFor(requestFor("req-9", locale.AmericanEnglish), apperr.New(apperr.KindRateLimited, "ARENA-RL-1", "try again in 30 seconds"))
	if problem.Status != http.StatusTooManyRequests {
		t.Fatalf("rate limit status = %d", problem.Status)
	}
	if problem.Detail != "try again in 30 seconds" {
		t.Fatalf("rate limit detail = %q", problem.Detail)
	}
}
