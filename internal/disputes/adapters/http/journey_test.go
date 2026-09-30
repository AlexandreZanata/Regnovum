package http_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDisputeJourneyAcceptDefend(t *testing.T) {
	h := newDisputeHarness(t)

	file := disputeRequest(t, h, http.MethodGet, "/api/v1/me/disputes/cases/caso-proposta",
		disputeClaimantToken, "pt-BR", nil)
	if file.Code != http.StatusOK {
		t.Fatalf("case status = %d, body %s", file.Code, file.Body.String())
	}
	assertDisputeNoStore(t, file)
	document := disputeDecode(t, file.Body.Bytes())
	if document["accepts"] != float64(1) || document["status"] != "proposed" {
		t.Fatalf("file = %v, want one acceptance and proposed status", document)
	}
	if document["version"] != float64(1) || document["value_milli"] != float64(20000) {
		t.Fatalf("file = %v, want version 1 and value 20000", document)
	}
	notices, _ := document["notices"].([]any)
	if len(notices) != 5 {
		t.Fatalf("notices = %v, want the five lifecycle notices", document["notices"])
	}
	for _, key := range []string{"title", "key", "version", "kind", "status", "role", "object", "value_milli", "escrow_ref", "accepts", "defenses", "appealed", "expires_at", "notices"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("file missing key %q: %v", key, document)
		}
	}
	if _, ok := document["verdict"]; ok {
		t.Fatalf("file carries a verdict before any ruling: %v", document)
	}

	accepted := disputeRequest(t, h, http.MethodPost, "/api/v1/me/disputes/cases/caso-proposta/accepts",
		disputeRespondentToken, "pt-BR", nil)
	if accepted.Code != http.StatusOK {
		t.Fatalf("accept status = %d, body %s", accepted.Code, accepted.Body.String())
	}
	assertDisputeNoStore(t, accepted)
	bound := disputeDecode(t, accepted.Body.Bytes())
	if bound["accepts"] != float64(2) || bound["status"] != "open" {
		t.Fatalf("bound = %v, want bilateral acceptance opening the entry", bound)
	}

	defense := disputeRequest(t, h, http.MethodPost, "/api/v1/me/disputes/cases/caso-jornada/defenses",
		disputeClaimantToken, "pt-BR", map[string]string{"digest": "prova-requerente"})
	if defense.Code != http.StatusOK {
		t.Fatalf("defense status = %d, body %s", defense.Code, defense.Body.String())
	}
	defended := disputeDecode(t, defense.Body.Bytes())
	if defended["defenses"] != float64(1) {
		t.Fatalf("defended = %v, want one filed defense counted, never its digest", defended)
	}
	if body := defense.Body.String(); strings.Contains(body, "prova-requerente") {
		t.Fatalf("defense document leaks the proof digest: %s", body)
	}

	ruling := disputeRequest(t, h, http.MethodGet, "/api/v1/me/disputes/cases/caso-jornada/ruling",
		disputeClaimantToken, "pt-BR", nil)
	if ruling.Code != http.StatusNotFound {
		t.Fatalf("early ruling = %d, want 404 before any reasoned decision", ruling.Code)
	}
}

func TestDisputeRulingAppealOnce(t *testing.T) {
	h := newDisputeHarness(t)

	ruling := disputeRequest(t, h, http.MethodGet, "/api/v1/me/disputes/cases/caso-sentenca/ruling",
		disputeRespondentToken, "pt-BR", nil)
	if ruling.Code != http.StatusOK {
		t.Fatalf("ruling status = %d, body %s", ruling.Code, ruling.Body.String())
	}
	assertDisputeNoStore(t, ruling)
	document := disputeDecode(t, ruling.Body.Bytes())
	if document["verdict"] != "uphold-claimant" || document["award_milli"] != float64(10000) {
		t.Fatalf("ruling = %v, want the stable verdict with the capped award", document)
	}
	code, _ := document["decision_code"].(string)
	if !strings.Contains(code, "uphold-claimant") || !strings.Contains(code, document["terms_hash"].(string)) {
		t.Fatalf("ruling = %v, want the decision code binding verdict to sealed terms", document)
	}
	for _, key := range []string{"title", "key", "decision_code", "terms_hash", "verdict", "award_milli", "decided_at", "appeal_due_at", "appealed"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("ruling missing key %q: %v", key, document)
		}
	}
	if body := ruling.Body.String(); strings.Contains(body, "prova-requerente") || strings.Contains(body, "aplica o termo") {
		t.Fatalf("ruling document leaks proof: %s", body)
	}

	appeal := disputeRequest(t, h, http.MethodPost, "/api/v1/me/disputes/cases/caso-sentenca/appeals",
		disputeRespondentToken, "pt-BR", map[string]string{"reason": "reexame do lote 7"})
	if appeal.Code != http.StatusOK {
		t.Fatalf("appeal status = %d, body %s", appeal.Code, appeal.Body.String())
	}
	appealed := disputeDecode(t, appeal.Body.Bytes())
	if appealed["appealed"] != true {
		t.Fatalf("appealed = %v, want the single previsto recurso opened", appealed)
	}

	again := disputeRequest(t, h, http.MethodPost, "/api/v1/me/disputes/cases/caso-sentenca/appeals",
		disputeClaimantToken, "pt-BR", map[string]string{"reason": "reexame do lote 7"})
	if again.Code != http.StatusConflict {
		t.Fatalf("second appeal = %d, want 409", again.Code)
	}
	assertDisputeNoStore(t, again)
}

func TestDisputeAPIRequiresAuthentication(t *testing.T) {
	h := newDisputeHarness(t)
	paths := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/me/disputes/cases/caso-jornada"},
		{http.MethodPost, "/api/v1/me/disputes/cases/caso-jornada/accepts"},
		{http.MethodPost, "/api/v1/me/disputes/cases/caso-jornada/defenses"},
		{http.MethodGet, "/api/v1/me/disputes/cases/caso-sentenca/ruling"},
		{http.MethodPost, "/api/v1/me/disputes/cases/caso-sentenca/appeals"},
	}
	for _, route := range paths {
		recorder := disputeRequest(t, h, route.method, route.path, "", "", nil)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s = %d, want 401", route.method, route.path, recorder.Code)
		}
	}
}

func TestDisputeStrangerLearnsNothing(t *testing.T) {
	h := newDisputeHarness(t)
	reads := []string{
		"/api/v1/me/disputes/cases/caso-jornada",
		"/api/v1/me/disputes/cases/caso-sentenca/ruling",
	}
	for _, path := range reads {
		recorder := disputeRequest(t, h, http.MethodGet, path, disputeStrangerToken, "", nil)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404: strangers read absence", path, recorder.Code)
		}
	}
	moves := []struct {
		path string
		body any
	}{
		{"/api/v1/me/disputes/cases/caso-jornada/accepts", nil},
		{"/api/v1/me/disputes/cases/caso-jornada/defenses", map[string]string{"digest": "prova-estranha"}},
		{"/api/v1/me/disputes/cases/caso-sentenca/appeals", map[string]string{"reason": "reexame estranho"}},
	}
	for _, move := range moves {
		recorder := disputeRequest(t, h, http.MethodPost, move.path, disputeStrangerToken, "", move.body)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("POST %s = %d, want 403", move.path, recorder.Code)
		}
		assertDisputeNoStore(t, recorder)
	}
	missing := disputeRequest(t, h, http.MethodGet, "/api/v1/me/disputes/cases/caso-inexistente",
		disputeClaimantToken, "", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown case = %d, want 404", missing.Code)
	}
}

func TestDisputeMutationsRequireCSRF(t *testing.T) {
	h := newDisputeHarness(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/me/disputes/cases/caso-jornada/accepts", nil)
	request.AddCookie(&http.Cookie{Name: "arena_session", Value: disputeRespondentToken})
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("CSRF-less POST = %d, want 403", recorder.Code)
	}
}

func TestDisputeFileIsLocaleIndependent(t *testing.T) {
	h := newDisputeHarness(t)
	pt := disputeDecode(t, disputeRequest(t, h, http.MethodGet,
		"/api/v1/me/disputes/cases/caso-sentenca", disputeClaimantToken, "pt-BR", nil).Body.Bytes())
	en := disputeDecode(t, disputeRequest(t, h, http.MethodGet,
		"/api/v1/me/disputes/cases/caso-sentenca", disputeClaimantToken, "en-US", nil).Body.Bytes())
	for _, key := range []string{"version", "value_milli", "accepts", "defenses", "award_milli", "appealed", "key", "escrow_ref"} {
		if pt[key] != en[key] {
			t.Fatalf("locale changed %q: pt=%v en=%v; translated text must never enter the computation", key, pt[key], en[key])
		}
	}
	if pt["title"] == en["title"] {
		t.Fatalf("titles do not differ: pt=%v en=%v", pt["title"], en["title"])
	}
	ptNotices, _ := pt["notices"].([]any)
	enNotices, _ := en["notices"].([]any)
	if len(ptNotices) != 5 || len(enNotices) != 5 {
		t.Fatalf("notices lost events: pt=%v en=%v", pt["notices"], en["notices"])
	}
	for i := range ptNotices {
		ptEntry, _ := ptNotices[i].(map[string]any)
		enEntry, _ := enNotices[i].(map[string]any)
		if ptEntry["event"] != enEntry["event"] {
			t.Fatalf("event codes diverge: pt=%v en=%v", ptEntry, enEntry)
		}
		if ptEntry["title"] == enEntry["title"] {
			t.Fatalf("notice %v is not translated", ptEntry["event"])
		}
	}
}
