package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

var kingdomTerminal = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// kingdomPolicy is the test fixture: explicit per-jurisdiction
// horizons for the horizon classes, obligations for the retained
// ones. The values are fixtures of this test, not product
// policy: production keeps no universal deadline.
func kingdomPolicy() []domain.KingdomSchedule {
	br := func(window time.Duration) map[string]time.Duration {
		return map[string]time.Duration{"BR": window}
	}
	return []domain.KingdomSchedule{
		{Class: domain.KingdomClassSealedProof, Action: domain.RetentionActionAnonymize, Purpose: "defesa e auditoria", Basis: "defesa-juridica", Windows: br(5 * 365 * 24 * time.Hour), ReasonCode: "prova_selada"},
		{Class: domain.KingdomClassReport, Action: domain.RetentionActionAnonymize, Purpose: "seguranca e auditoria", Basis: "defesa-juridica", Windows: br(5 * 365 * 24 * time.Hour), ReasonCode: "denuncia_severa"},
		{Class: domain.KingdomClassContract, Action: domain.RetentionActionRetain, Purpose: "prova contratual", Basis: "contrato", Indefinite: true, ReasonCode: "contrato_vinculante"},
		{Class: domain.KingdomClassEscrow, Action: domain.RetentionActionRetain, Purpose: "prova financeira", Basis: "obrigacao-legal", Indefinite: true, ReasonCode: "custodia_financeira"},
		{Class: domain.KingdomClassManifestCutoff, Action: domain.RetentionActionRetain, Purpose: "prova do selo", Basis: "obrigacao-legal", Indefinite: true, ReasonCode: "selo_do_livro"},
		{Class: domain.KingdomClassRanking, Action: domain.RetentionActionAnonymize, Purpose: "memoria da competicao", Basis: "interesse-legitimo", Windows: br(2 * 365 * 24 * time.Hour), ReasonCode: "ranking_historico"},
		{Class: domain.KingdomClassReigns, Action: domain.RetentionActionRetain, Purpose: "historia institucional", Basis: "obrigacao-legal", Indefinite: true, ReasonCode: "sequencia_de_reinados"},
		{Class: domain.KingdomClassEconomicRecords, Action: domain.RetentionActionRetain, Purpose: "integridade do ledger", Basis: "obrigacao-legal", Indefinite: true, ReasonCode: "evidencia_contabil"},
	}
}

func kingdomSchedule(t *testing.T, class domain.KingdomClass) domain.KingdomSchedule {
	t.Helper()
	for _, schedule := range kingdomPolicy() {
		if schedule.Class == class {
			return schedule
		}
	}
	t.Fatalf("fixture has no schedule for %q", class)
	return domain.KingdomSchedule{}
}

func TestKingdomVocabularyIsClosed(t *testing.T) {
	classes := map[domain.KingdomClass]string{
		domain.KingdomClassSealedProof:     "prova-selada",
		domain.KingdomClassReport:          "denuncia",
		domain.KingdomClassContract:        "contrato",
		domain.KingdomClassEscrow:          "escrow",
		domain.KingdomClassManifestCutoff:  "manifesto-corte",
		domain.KingdomClassRanking:         "ranking",
		domain.KingdomClassReigns:          "reinados",
		domain.KingdomClassEconomicRecords: "registros-economicos",
	}
	for class, want := range classes {
		if string(class) != want {
			t.Errorf("class %q rendered as %q, want %q", class, string(class), want)
		}
		parsed, known := domain.ParseKingdomClass(want)
		if !known || parsed != class {
			t.Errorf("ParseKingdomClass(%q) = (%q, %v), want (%q, true)", want, parsed, known, class)
		}
	}
	for _, unknown := range []string{"", "RANKING", "ranking ", "wallet", "tokens"} {
		if _, known := domain.ParseKingdomClass(unknown); known {
			t.Errorf("ParseKingdomClass(%q) accepted an unknown class", unknown)
		}
	}
	for _, schedule := range kingdomPolicy() {
		if err := schedule.IsValid(); err != nil {
			t.Errorf("%s fixture is invalid: %v", schedule.Class, err)
		}
	}
}

func TestCollectionNeedsClassDeadlineAndBasis(t *testing.T) {
	policy := kingdomPolicy()
	if err := domain.AuthorizeCollection(domain.KingdomClassRanking, "interesse-legitimo", "BR", policy); err != nil {
		t.Fatalf("governed collection: %v", err)
	}
	if err := domain.AuthorizeCollection(domain.KingdomClassContract, "contrato", "XX", policy); err != nil {
		t.Fatalf("indefinite collection needs no horizon: %v", err)
	}
	if err := domain.AuthorizeCollection("carteira", "contrato", "BR", policy); !errors.Is(err, domain.ErrUnknownKingdomClass) {
		t.Fatalf("unknown class = %v, want ErrUnknownKingdomClass", err)
	}
	if err := domain.AuthorizeCollection(domain.KingdomClassRanking, "  ", "BR", policy); !errors.Is(err, domain.ErrMissingCollectionBasis) {
		t.Fatalf("baseless collection = %v, want ErrMissingCollectionBasis", err)
	}
	if err := domain.AuthorizeCollection(domain.KingdomClassRanking, "interesse-legitimo", "XX", policy); !errors.Is(err, domain.ErrNoJurisdictionWindow) {
		t.Fatalf("horizonless jurisdiction = %v, want ErrNoJurisdictionWindow", err)
	}
}

func TestHoldPreservesOnlyWhatItNames(t *testing.T) {
	holds := []domain.KingdomHold{
		{ID: "hold-1", Class: domain.KingdomClassReport, ReasonCode: "litigio", PlacedAt: kingdomTerminal},
		{ID: "hold-2", Class: domain.KingdomClassRanking, AccountID: "ana", ReasonCode: "recurso", PlacedAt: kingdomTerminal},
	}
	for _, hold := range holds {
		if err := hold.Validate(); err != nil {
			t.Fatalf("hold %s must be valid: %v", hold.ID, err)
		}
	}
	if !domain.HeldBy(holds, domain.KingdomClassReport, "boa") {
		t.Error("a class-wide hold must preserve every account of its class")
	}
	if !domain.HeldBy(holds, domain.KingdomClassRanking, "ana") {
		t.Error("an account hold must preserve its account")
	}
	if domain.HeldBy(holds, domain.KingdomClassRanking, "boa") {
		t.Error("an account hold must not preserve another account")
	}
	if domain.HeldBy(holds, domain.KingdomClassEscrow, "ana") {
		t.Error("a hold must not preserve a class it does not name")
	}
	if err := (domain.KingdomHold{Class: "carteira", ReasonCode: "x", PlacedAt: kingdomTerminal}).Validate(); !errors.Is(err, domain.ErrUnknownKingdomClass) {
		t.Fatalf("unknown hold class = %v, want ErrUnknownKingdomClass", err)
	}
}

func TestExpiryDisposesWithoutBreakingLedger(t *testing.T) {
	denuncia := kingdomSchedule(t, domain.KingdomClassReport)
	dueAt, hasDue := denuncia.DueAt(kingdomTerminal, "BR")
	if !hasDue {
		t.Fatal("denuncia must have a due instant in BR")
	}
	settle := func(schedule domain.KingdomSchedule, now time.Time, account string, archived bool, holds []domain.KingdomHold) domain.SettleOutcome {
		outcome, err := domain.Settle(domain.SettleRequest{
			Schedule: schedule, Jurisdiction: "BR",
			TerminalAt: kingdomTerminal, Now: now,
			Account: account, Archived: archived, Holds: holds,
		})
		if err != nil {
			t.Fatalf("Settle(%s): %v", schedule.Class, err)
		}
		return outcome
	}
	if got := settle(denuncia, dueAt.Add(-time.Second), "ana", false, nil); got != domain.SettleWait {
		t.Fatalf("early record = %q, want aguardar", got)
	}
	if got := settle(denuncia, dueAt, "ana", false, nil); got != domain.SettleAnonymized {
		t.Fatalf("due record = %q, want anonimizado", got)
	}
	held := []domain.KingdomHold{{ID: "hold-3", Class: domain.KingdomClassReport, ReasonCode: "litigio", PlacedAt: kingdomTerminal}}
	if got := settle(denuncia, dueAt.Add(365*24*time.Hour), "ana", false, held); got != domain.SettleHeld {
		t.Fatalf("held record = %q, want preservado", got)
	}
	ranking := kingdomSchedule(t, domain.KingdomClassRanking)
	rankingDue, _ := ranking.DueAt(kingdomTerminal, "BR")
	if got := settle(ranking, rankingDue, "ana", true, nil); got != domain.SettleAnonymized {
		t.Fatalf("archived ranking = %q, want anonimizado: archive never blocks it", got)
	}
	exact := int64(800000000000)
	disclosed := domain.AnonymizeRanking(domain.RankingDisclosure{Alias: "ana-publica", ExactBalance: &exact})
	if disclosed.Alias != "ana-publica" || disclosed.ExactBalance != nil {
		t.Fatalf("disclosed = %+v, want the alias kept and the exact balance dropped", disclosed)
	}
	economic := kingdomSchedule(t, domain.KingdomClassEconomicRecords)
	if got := settle(economic, kingdomTerminal.Add(100*365*24*time.Hour), "ana", true, nil); got != domain.SettleRetained {
		t.Fatalf("economic record = %q, want mantido: expiry never breaks the ledger", got)
	}
}

func TestNoUniversalDeadlineIsInvented(t *testing.T) {
	bare := kingdomSchedule(t, domain.KingdomClassRanking)
	bare.Windows = nil
	if err := bare.IsValid(); !errors.Is(err, domain.ErrInvalidRetentionSchedule) {
		t.Fatalf("windowless horizon = %v, want ErrInvalidRetentionSchedule", err)
	}
	brasil, _ := kingdomSchedule(t, domain.KingdomClassRanking).DueAt(kingdomTerminal, "BR")
	if kingdomSchedule(t, domain.KingdomClassRanking).Due(kingdomTerminal, brasil, "XX") {
		t.Fatal("a BR horizon must never decide an unlisted jurisdiction")
	}
	if _, hasDue := kingdomSchedule(t, domain.KingdomClassContract).DueAt(kingdomTerminal, "BR"); hasDue {
		t.Fatal("an indefinite class must not yield a due instant in any jurisdiction")
	}
	negative := kingdomSchedule(t, domain.KingdomClassReport)
	negative.Windows = map[string]time.Duration{"BR": -time.Second}
	if err := negative.IsValid(); !errors.Is(err, domain.ErrInvalidRetentionSchedule) {
		t.Fatalf("negative window = %v, want ErrInvalidRetentionSchedule", err)
	}
	purposeless := kingdomSchedule(t, domain.KingdomClassReport)
	purposeless.Purpose = "  "
	if err := purposeless.IsValid(); !errors.Is(err, domain.ErrInvalidRetentionSchedule) {
		t.Fatalf("purposeless schedule = %v, want ErrInvalidRetentionSchedule", err)
	}
}
