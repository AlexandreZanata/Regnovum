package domain_test

import (
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
)

func TestClassifyTerminalBranches(t *testing.T) {
	available := domain.TerminalEvidence{
		TermsComplete: true, AcceptedBeforeCutoff: true, Uncontested: true,
		EvidenceAvailable: true, IntegrityOK: true,
	}
	if got := domain.ClassifyTerminal(domain.ContractReleased, domain.TerminalEvidence{}); got != domain.TerminalNoEffect {
		t.Fatalf("released = %q, want no-effect", got)
	}
	if got := domain.ClassifyTerminal(domain.ContractRefunded, domain.TerminalEvidence{}); got != domain.TerminalNoEffect {
		t.Fatalf("refunded = %q, want no-effect", got)
	}
	if got := domain.ClassifyTerminal(domain.ContractResolved, domain.TerminalEvidence{}); got != domain.TerminalNoEffect {
		t.Fatalf("resolved = %q, want no-effect", got)
	}
	if got := domain.ClassifyTerminal(domain.ContractAccepted, available); got != domain.TerminalRelease {
		t.Fatalf("accepted incontroverso = %q, want release", got)
	}
	contested := available
	contested.Uncontested = false
	if got := domain.ClassifyTerminal(domain.ContractAccepted, contested); got != domain.TerminalBlocked {
		t.Fatalf("accepted contestado = %q, want blocked", got)
	}
	late := available
	late.AcceptedBeforeCutoff = false
	if got := domain.ClassifyTerminal(domain.ContractAccepted, late); got != domain.TerminalBlocked {
		t.Fatalf("accepted tardio = %q, want blocked", got)
	}
	if got := domain.ClassifyTerminal(domain.ContractFunded, available); got != domain.TerminalRefund {
		t.Fatalf("funded incontroverso = %q, want refund", got)
	}
	if got := domain.ClassifyTerminal(domain.ContractFunded, contested); got != domain.TerminalBlocked {
		t.Fatalf("funded contestado = %q, want blocked", got)
	}
	expiredRelease := available
	expiredRelease.ResolutionFinal = true
	expiredRelease.Resolution = domain.ResolveRelease
	if got := domain.ClassifyTerminal(domain.ContractExpired, expiredRelease); got != domain.TerminalRelease {
		t.Fatalf("expired com resolve-release = %q, want release", got)
	}
	expiredRefund := available
	expiredRefund.ResolutionFinal = true
	expiredRefund.Resolution = domain.ResolveRefund
	if got := domain.ClassifyTerminal(domain.ContractExpired, expiredRefund); got != domain.TerminalRefund {
		t.Fatalf("expired com resolve-refund = %q, want refund", got)
	}
	expiredAlone := available
	if got := domain.ClassifyTerminal(domain.ContractExpired, expiredAlone); got != domain.TerminalBlocked {
		t.Fatalf("expired sem resolucao = %q, want blocked", got)
	}
	unknown := available
	if got := domain.ClassifyTerminal(domain.ContractStatus("unknown"), unknown); got != domain.TerminalBlocked {
		t.Fatalf("desconhecido = %q, want blocked", got)
	}
	missing := domain.TerminalEvidence{TermsComplete: false, EvidenceAvailable: true, IntegrityOK: true, Uncontested: true}
	if got := domain.ClassifyTerminal(domain.ContractAccepted, missing); got != domain.TerminalBlocked {
		t.Fatalf("termos ausentes = %q, want blocked", got)
	}
	unavailable := available
	unavailable.EvidenceAvailable = false
	if got := domain.ClassifyTerminal(domain.ContractAccepted, unavailable); got != domain.TerminalBlocked {
		t.Fatalf("fonte indisponivel = %q, want blocked", got)
	}
	corrupt := available
	corrupt.IntegrityOK = false
	if got := domain.ClassifyTerminal(domain.ContractFunded, corrupt); got != domain.TerminalBlocked {
		t.Fatalf("integridade divergente = %q, want blocked", got)
	}
}

func TestSeasonalTradeTermsRefuseWithoutClause(t *testing.T) {
	ends := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Add(7776000 * time.Second)
	expires := ends.Add(-time.Hour)
	base := domain.SeasonalTradeTerms{
		Season: domain.SeasonKey("temporada-1"), PolicyRef: "terminal-v1",
		PolicyHash:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		BuyerAccept: "aceite-comprador-1", ProviderAccept: "aceite-prestador-1",
		SeasonEndsAt: ends, ResetAcknowledged: true,
	}
	if err := domain.ValidateSeasonalTradeTerms(base, expires); err != nil {
		t.Fatalf("termos validos: %v", err)
	}
	noPolicy := base
	noPolicy.PolicyRef = ""
	if err := domain.ValidateSeasonalTradeTerms(noPolicy, expires); err == nil {
		t.Fatal("sem politica financiou: ausencia deve recusar")
	}
	noBuyer := base
	noBuyer.BuyerAccept = ""
	if err := domain.ValidateSeasonalTradeTerms(noBuyer, expires); err == nil {
		t.Fatal("sem aceite do comprador financiou: ausencia deve recusar")
	}
	pastEnd := base
	if err := domain.ValidateSeasonalTradeTerms(pastEnd, ends.Add(time.Second)); err == nil {
		t.Fatal("vencimento alem do fim financiou: deadline deve ficar ate ends_at")
	}
	noReset := base
	noReset.ResetAcknowledged = false
	if err := domain.ValidateSeasonalTradeTerms(noReset, expires); err == nil {
		t.Fatal("sem reset financiou: reset omitido nunca autoriza")
	}
}
