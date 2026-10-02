package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

var kingdomExportAt = time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)

func exportRequest(account, requester string) domain.ExportRequest {
	return domain.ExportRequest{
		Account: account, Requester: requester,
		Sections: []string{"contratos", "recibos", "atos-do-titular"},
		Consent:  true, At: kingdomExportAt,
	}
}

func TestTwoCanaryAccountsExportApart(t *testing.T) {
	ana, err := domain.AuthorizeExport(exportRequest("ana", "ana"))
	if err != nil {
		t.Fatalf("ana export: %v", err)
	}
	boa, err := domain.AuthorizeExport(exportRequest("boa", "boa"))
	if err != nil {
		t.Fatalf("boa export: %v", err)
	}
	if len(ana.Sections) != 3 || len(boa.Sections) != 3 {
		t.Fatalf("grants = %+v/%+v, want the three allowlisted sections each", ana, boa)
	}
	if err := domain.CheckExportItems(ana, []domain.ExportItem{
		{ID: "contrato-1", Owner: "ana"},
		{ID: "recibo-1", Owner: "ana"},
	}); err != nil {
		t.Fatalf("ana items: %v", err)
	}
	if ana.Account == boa.Account {
		t.Fatal("two canaries must never share one export")
	}
}

func TestRefusedConsentExportsNothing(t *testing.T) {
	req := exportRequest("ana", "ana")
	req.Consent = false
	if _, err := domain.AuthorizeExport(req); !errors.Is(err, domain.ErrConsentRefused) {
		t.Fatalf("refused consent = %v, want ErrConsentRefused", err)
	}
	other := exportRequest("boa", "boa")
	other.Consent = false
	if _, err := domain.AuthorizeExport(other); !errors.Is(err, domain.ErrConsentRefused) {
		t.Fatalf("second refusal = %v, want ErrConsentRefused", err)
	}
}

func TestDeadAndReactivatedAccounts(t *testing.T) {
	stranger := exportRequest("ana", "boa")
	stranger.Dead = true
	stranger.Heir = "carlos"
	if _, err := domain.AuthorizeExport(stranger); !errors.Is(err, domain.ErrDeadAccountAccess) {
		t.Fatalf("stranger over dead = %v, want ErrDeadAccountAccess", err)
	}
	nameless := exportRequest("ana", "boa")
	nameless.Dead = true
	if _, err := domain.AuthorizeExport(nameless); !errors.Is(err, domain.ErrDeadAccountAccess) {
		t.Fatalf("heirless dead = %v, want ErrDeadAccountAccess", err)
	}
	heir := exportRequest("ana", "boa")
	heir.Dead = true
	heir.Heir = "boa"
	grant, err := domain.AuthorizeExport(heir)
	if err != nil {
		t.Fatalf("heir export: %v", err)
	}
	if grant.Account != "ana" {
		t.Fatalf("grant = %+v, want the dead holder covered for its heir", grant)
	}
	revived := exportRequest("ana", "ana")
	revived.Dead = true
	revived.Heir = "ana"
	revived.Reactivated = true
	if _, err := domain.AuthorizeExport(revived); err != nil {
		t.Fatalf("reactivated export: %v", err)
	}
	minimized, err := domain.Minimize(domain.MinimizeRequest{
		Account: "ana",
		Facts: []domain.MinimizableFact{
			{ID: "contrato-1", LegalBasis: true},
			{ID: "recurso-1", AppealRelated: true},
			{ID: "rascunho-1"},
		},
	})
	if err != nil {
		t.Fatalf("Minimize: %v", err)
	}
	if len(minimized.Kept) != 1 || len(minimized.Anonymized) != 2 {
		t.Fatalf("minimized = %+v, want legal facts kept without pending appeal", minimized)
	}
}

func TestExportNeverMixesHoldersOrLeaksProof(t *testing.T) {
	grant, err := domain.AuthorizeExport(exportRequest("ana", "ana"))
	if err != nil {
		t.Fatalf("AuthorizeExport: %v", err)
	}
	mixed := []domain.ExportItem{{ID: "recibo-9", Owner: "boa"}}
	if err := domain.CheckExportItems(grant, mixed); !errors.Is(err, domain.ErrCrossAccountExport) {
		t.Fatalf("mixed holders = %v, want ErrCrossAccountExport", err)
	}
	foreign := []domain.ExportItem{{ID: "peca-9", Owner: "ana", ForeignProof: true}}
	if err := domain.CheckExportItems(grant, foreign); !errors.Is(err, domain.ErrCrossAccountExport) {
		t.Fatalf("foreign proof = %v, want ErrCrossAccountExport", err)
	}
	alien := exportRequest("ana", "ana")
	alien.Sections = []string{"contratos", "prova-alheia"}
	if _, err := domain.AuthorizeExport(alien); !errors.Is(err, domain.ErrUnknownExportSection) {
		t.Fatalf("foreign section = %v, want ErrUnknownExportSection", err)
	}
	duped := exportRequest("ana", "ana")
	duped.Sections = []string{"recibos", "recibos"}
	if _, err := domain.AuthorizeExport(duped); !errors.Is(err, domain.ErrInvalidKingdomExport) {
		t.Fatalf("duplicated section = %v, want ErrInvalidKingdomExport", err)
	}
	if _, known := domain.ParseExportSection("saldos"); known {
		t.Fatal("balances must stay outside the export allowlist")
	}
}

func TestMinimizationKeepsLegalFactsAndAppeal(t *testing.T) {
	facts := []domain.MinimizableFact{
		{ID: "contrato-1", LegalBasis: true},
		{ID: "escrow-1", LegalBasis: true},
		{ID: "recurso-1", AppealRelated: true},
		{ID: "rascunho-1"},
	}
	pending, err := domain.Minimize(domain.MinimizeRequest{
		Account: "ana", AppealPending: true, Facts: facts,
	})
	if err != nil {
		t.Fatalf("Minimize: %v", err)
	}
	if len(pending.Kept) != 3 || len(pending.Anonymized) != 1 {
		t.Fatalf("pending = %+v, want legal facts with the appeal right", pending)
	}
	held, err := domain.Minimize(domain.MinimizeRequest{
		Account: "ana", AppealPending: false, LegalHold: true, Facts: facts,
	})
	if err != nil {
		t.Fatalf("Minimize: %v", err)
	}
	if len(held.Kept) != 4 || len(held.Anonymized) != 0 {
		t.Fatalf("held = %+v, want the hold keeping everything", held)
	}
	settled, err := domain.Minimize(domain.MinimizeRequest{
		Account: "ana", Facts: facts,
	})
	if err != nil {
		t.Fatalf("Minimize: %v", err)
	}
	if len(settled.Kept) != 2 || settled.Kept[0] != "contrato-1" || settled.Kept[1] != "escrow-1" {
		t.Fatalf("settled = %+v, want only legally necessary facts", settled)
	}
	if _, err := domain.Minimize(domain.MinimizeRequest{Facts: facts}); !errors.Is(err, domain.ErrInvalidKingdomExport) {
		t.Fatalf("accountless deletion = %v, want ErrInvalidKingdomExport", err)
	}
}
