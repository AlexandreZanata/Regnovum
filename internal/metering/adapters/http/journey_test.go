package http_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMeteringJourneyPreviewConfirmReceiptStatement(t *testing.T) {
	h, _ := newMeteringHarness(t)

	preview := meteringRequest(t, h, http.MethodPost, "/api/v1/me/metering/quotes",
		meteringOwnerToken, `{"content":"texto final","service":"argument-publish"}`, "pt-BR")
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status = %d, body %s", preview.Code, preview.Body.String())
	}
	assertNoStore(t, preview)
	priced := meteringDecode(t, preview.Body.Bytes())
	if priced["units"] != float64(11) || priced["total_milli"] != float64(2750) || priced["version"] != float64(2) {
		t.Fatalf("preview = %v, want 11 units at 250 v3... units/2750/v2", priced)
	}
	for _, key := range []string{"title", "units", "price_milli", "total_milli", "version", "content_hash", "quote_hash", "accepted_at", "expires_at"} {
		if _, ok := priced[key]; !ok {
			t.Fatalf("preview missing key %q: %v", key, priced)
		}
	}

	confirm := meteringRequest(t, h, http.MethodPost, "/api/v1/me/metering/publications",
		meteringOwnerToken, `{"intention_key":"journey-1","content":"texto final","service":"argument-publish"}`, "pt-BR")
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body %s", confirm.Code, confirm.Body.String())
	}
	assertNoStore(t, confirm)
	settled := meteringDecode(t, confirm.Body.Bytes())
	if settled["total_milli"] != priced["total_milli"] {
		t.Fatalf("settled total %v diverges from preview %v", settled["total_milli"], priced["total_milli"])
	}
	publicationID, _ := settled["publication_id"].(string)
	if publicationID == "" {
		t.Fatalf("confirm missing publication_id: %v", settled)
	}

	receipt := meteringRequest(t, h, http.MethodGet, "/api/v1/me/metering/publications/"+publicationID,
		meteringOwnerToken, "", "pt-BR")
	if receipt.Code != http.StatusOK {
		t.Fatalf("receipt status = %d, body %s", receipt.Code, receipt.Body.String())
	}
	assertNoStore(t, receipt)
	document := meteringDecode(t, receipt.Body.Bytes())
	if document["total_milli"] != priced["total_milli"] || document["content_hash"] != priced["content_hash"] {
		t.Fatalf("receipt diverges from preview: %v vs %v", document, priced)
	}

	statement := meteringRequest(t, h, http.MethodGet, "/api/v1/me/metering/statement",
		meteringOwnerToken, "", "pt-BR")
	if statement.Code != http.StatusOK {
		t.Fatalf("statement status = %d, body %s", statement.Code, statement.Body.String())
	}
	assertNoStore(t, statement)
	extract := meteringDecode(t, statement.Body.Bytes())
	entries, _ := extract["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %v, want one line", extract["entries"])
	}
	if extract["balance_milli"] != float64(1000000-2750) {
		t.Fatalf("balance = %v, want journal-derived %d", extract["balance_milli"], 1000000-2750)
	}
}

func TestMeteringAPIRequiresAuthentication(t *testing.T) {
	h, _ := newMeteringHarness(t)
	paths := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/me/metering/quotes", `{"content":"x","service":"argument-publish"}`},
		{http.MethodPost, "/api/v1/me/metering/publications", `{"intention_key":"k","content":"x","service":"argument-publish"}`},
		{http.MethodGet, "/api/v1/me/metering/publications/00000000-0000-4000-8000-000000000000", ""},
		{http.MethodGet, "/api/v1/me/metering/statement", ""},
	}
	for _, target := range paths {
		recorder := meteringRequest(t, h, target.method, target.path, "", target.body, "")
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s = %d, want 401", target.method, target.path, recorder.Code)
		}
		// The shared authentication middleware owns the 401
		// document; every answer owned by this handler is
		// asserted no-store on the journey above.
	}
}

func TestMeteringMutationsRequireCSRF(t *testing.T) {
	h, _ := newMeteringHarness(t)
	for _, target := range []struct{ path, body string }{
		{"/api/v1/me/metering/quotes", `{"content":"texto final","service":"argument-publish"}`},
		{"/api/v1/me/metering/publications", `{"intention_key":"csrf-1","content":"texto final","service":"argument-publish"}`},
	} {
		request := meteringRequestWithoutCSRF(t, h, http.MethodPost, target.path, target.body)
		if request.Code != http.StatusForbidden {
			t.Fatalf("POST %s without CSRF = %d, want 403", target.path, request.Code)
		}
	}
}

func meteringRequestWithoutCSRF(t *testing.T, h *meteringHarness, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return serveBareRequest(t, h, method, path, body)
}

func TestMeteringForeignReceiptIsNotFound(t *testing.T) {
	h, _ := newMeteringHarness(t)
	confirm := meteringRequest(t, h, http.MethodPost, "/api/v1/me/metering/publications",
		meteringOwnerToken, `{"intention_key":"foreign-1","content":"texto final","service":"argument-publish"}`, "")
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm status = %d", confirm.Code)
	}
	settled := meteringDecode(t, confirm.Body.Bytes())
	id, _ := settled["publication_id"].(string)

	foreign := meteringRequest(t, h, http.MethodGet, "/api/v1/me/metering/publications/"+id,
		meteringOtherToken, "", "")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign receipt = %d, want 404", foreign.Code)
	}
}

func TestMeteringDivergentKeyConflicts(t *testing.T) {
	h, _ := newMeteringHarness(t)
	first := meteringRequest(t, h, http.MethodPost, "/api/v1/me/metering/publications",
		meteringOwnerToken, `{"intention_key":"dupe-1","content":"texto final","service":"argument-publish"}`, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first confirm = %d", first.Code)
	}
	second := meteringRequest(t, h, http.MethodPost, "/api/v1/me/metering/publications",
		meteringOwnerToken, `{"intention_key":"dupe-1","content":"texto adulterado","service":"argument-publish"}`, "")
	if second.Code != http.StatusConflict {
		t.Fatalf("divergent key = %d, body %s, want 409", second.Code, second.Body.String())
	}
}

func TestMeteringPreviewIsLocaleIndependent(t *testing.T) {
	h, _ := newMeteringHarness(t)
	pt := meteringDecode(t, meteringRequest(t, h, http.MethodPost, "/api/v1/me/metering/quotes",
		meteringOwnerToken, `{"content":"texto final","service":"argument-publish"}`, "pt-BR").Body.Bytes())
	en := meteringDecode(t, meteringRequest(t, h, http.MethodPost, "/api/v1/me/metering/quotes",
		meteringOwnerToken, `{"content":"texto final","service":"argument-publish"}`, "en-US").Body.Bytes())
	for _, key := range []string{"units", "price_milli", "total_milli", "version", "content_hash", "quote_hash"} {
		if pt[key] != en[key] {
			t.Fatalf("locale changed %q: pt=%v en=%v; translated text must never enter the computation", key, pt[key], en[key])
		}
	}
	if pt["title"] == en["title"] {
		t.Fatalf("titles do not differ: pt=%v en=%v", pt["title"], en["title"])
	}
	if !strings.Contains(pt["title"].(string), "INK") || !strings.Contains(en["title"].(string), "INK") {
		t.Fatalf("titles lost the unit: pt=%v en=%v", pt["title"], en["title"])
	}
}
