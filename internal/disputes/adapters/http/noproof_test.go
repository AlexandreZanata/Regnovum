package http_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestNoProofTextInLogs drives the whole staged lifecycle and proves
// the request log carries routing only: no proof digest, no grounds,
// no dispute object and no account ever reaches a log line.
func TestNoProofTextInLogs(t *testing.T) {
	h := newDisputeHarness(t)

	disputeRequest(t, h, http.MethodGet, "/api/v1/me/disputes/cases/caso-jornada",
		disputeClaimantToken, "pt-BR", nil)
	disputeRequest(t, h, http.MethodPost, "/api/v1/me/disputes/cases/caso-jornada/accepts",
		disputeRespondentToken, "pt-BR", nil)
	disputeRequest(t, h, http.MethodPost, "/api/v1/me/disputes/cases/caso-jornada/defenses",
		disputeClaimantToken, "pt-BR", map[string]string{"digest": "prova-requerente"})
	disputeRequest(t, h, http.MethodGet, "/api/v1/me/disputes/cases/caso-sentenca/ruling",
		disputeRespondentToken, "en-US", nil)
	disputeRequest(t, h, http.MethodPost, "/api/v1/me/disputes/cases/caso-sentenca/appeals",
		disputeRespondentToken, "pt-BR", map[string]string{"reason": "reexame do lote 7"})

	if len(*h.logs) == 0 {
		t.Fatal("no log entries: the proof gate would judge nothing")
	}
	secrets := []string{"prova-requerente", "aplica o termo", "entrega do lote 7", "requerente", "requerida", "estranha", "reexame"}
	for _, entry := range *h.logs {
		for _, secret := range secrets {
			if strings.Contains(entry, secret) {
				t.Fatalf("log leaks proof text %q in %q", secret, entry)
			}
		}
	}
}
