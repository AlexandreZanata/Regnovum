package domain

import (
	"errors"
	"testing"
	"time"
)

func appealAnchor() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func appealAct() RoyalAct {
	anchor := appealAnchor()
	return RoyalAct{
		ID: "decreto-40", Author: "rainha-1", Season: "temporada-1",
		Reign: 2, Competence: "patrimonial", Kind: ActEconomic,
		Reason: "reparacao devida com origem identificada",
		Target: "conta/ana", Effect: "transferir 1000 de tesouro-livre sem mint",
		DecreedAt: anchor, Effective: anchor.Add(time.Hour),
		EndsAt:  anchor.Add(30 * 24 * time.Hour),
		Charter: "v3", Origin: "tesouro-livre", Amount: 1000,
	}
}

func appealBook(t *testing.T) RoyalBook {
	t.Helper()
	var book RoyalBook
	record, err := Summarize(appealAct(), "")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if err := book.Append(record); err != nil {
		t.Fatalf("Append: %v", err)
	}
	return book
}

func appealCorrective() RoyalAct {
	anchor := appealAnchor()
	return RoyalAct{
		ID: "decreto-41", Author: "rainha-1", Season: "temporada-1",
		Reign: 2, Competence: "patrimonial", Kind: ActEconomic,
		Reason: "correcao do decreto anterior por revisao independente",
		Target: "conta/ana", Effect: "transferir 1000 de tesouro-livre sem mint",
		DecreedAt: anchor.Add(48 * time.Hour), Effective: anchor.Add(49 * time.Hour),
		EndsAt:  anchor.Add(60 * 24 * time.Hour),
		Charter: "v3", Origin: "tesouro-livre", Amount: 1000,
		Corrects: "decreto-40",
	}
}

func appealRestitution() Restitution {
	return Restitution{
		Act: "decreto-40", Corrective: "decreto-41",
		Debtor: "rainha-1", Creditor: "ana",
		Origin: "cofre-pessoal-rainha-1", Amount: 1000,
		Season: "temporada-1",
	}
}

func TestAppealRepairsWithoutErasing(t *testing.T) {
	book := appealBook(t)
	filing, err := FilePetition(book, "peticao-1", "decreto-40", "ana", "valor devido a menor", appealAnchor().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("FilePetition: %v", err)
	}
	review, err := ReviewPetition(book, filing, ReviewInput{ID: "revisao-1", Auditor: "auditor-1", Verdict: VerdictRepair, Harm: true, Responsible: "rainha-1", Harmed: "ana", At: appealAnchor().Add(3 * time.Hour)})
	if err != nil {
		t.Fatalf("ReviewPetition: %v", err)
	}
	restitution := appealRestitution()
	order, err := OrderRepair(appealCorrective(), review, &restitution)
	if err != nil {
		t.Fatalf("OrderRepair: %v", err)
	}
	if !order.Restored || order.Original != "decreto-40" || order.Corrective != "decreto-41" {
		t.Fatalf("order = %+v, want the funded repair of the original", order)
	}
	correction, err := Summarize(appealCorrective(), "recurso-4")
	if err != nil {
		t.Fatalf("Summarize correction: %v", err)
	}
	if err := book.Append(correction); err != nil {
		t.Fatalf("Append correction: %v", err)
	}
	original, err := book.Find("decreto-40")
	if err != nil {
		t.Fatalf("Find original: %v", err)
	}
	before, err := Summarize(appealAct(), "")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if original.Digest != before.Digest {
		t.Fatalf("original moved: appeal never erases the appealed act")
	}
	if err := AuditCoverage([]ActID{"decreto-40", "decreto-41"}, book); err != nil {
		t.Fatalf("coverage: %v", err)
	}
}

func TestAppealRefusesImproperPetitionerAndHiddenActs(t *testing.T) {
	book := appealBook(t)
	if _, err := FilePetition(book, "peticao-1", "decreto-40", "rainha-1", "recurso proprio", appealAnchor().Add(2*time.Hour)); !errors.Is(err, ErrImproperPetitioner) {
		t.Fatalf("author petition = %v, want ErrImproperPetitioner", err)
	}
	if _, err := FilePetition(book, "peticao-2", "decreto-99", "ana", "ato oculto", appealAnchor().Add(2*time.Hour)); !errors.Is(err, ErrUnrecordedAct) {
		t.Fatalf("hidden act = %v, want ErrUnrecordedAct", err)
	}
	if _, err := FilePetition(book, "peticao-3", "decreto-40", "ana", "cedo demais", appealAnchor()); !errors.Is(err, ErrInvalidAuthority) {
		t.Fatalf("early filing = %v, want order before effects", err)
	}
}

func TestReviewRefusesInterestedAuditor(t *testing.T) {
	book := appealBook(t)
	filing, err := FilePetition(book, "peticao-1", "decreto-40", "ana", "valor devido a menor", appealAnchor().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("FilePetition: %v", err)
	}
	for name, auditor := range map[string]string{
		"author": "rainha-1", "petitioner": "ana",
	} {
		if _, err := ReviewPetition(book, filing, ReviewInput{ID: "revisao-1", Auditor: HolderSubject(auditor), Verdict: VerdictRepair, Harm: true, Responsible: "rainha-1", Harmed: "ana", At: appealAnchor().Add(3 * time.Hour)}); !errors.Is(err, ErrInterestedAuditor) {
			t.Fatalf("%s auditor = %v, want ErrInterestedAuditor", name, err)
		}
	}
	if _, err := ReviewPetition(book, filing, ReviewInput{ID: "revisao-1", Auditor: "rainha-1", Verdict: VerdictRepair, Harm: true, Responsible: "auditor-1", Harmed: "ana", At: appealAnchor().Add(3 * time.Hour)}); !errors.Is(err, ErrInterestedAuditor) {
		t.Fatalf("responsible auditor = %v, want ErrInterestedAuditor", err)
	}
}

func TestRepairRequiresVerdictAndFundedObligation(t *testing.T) {
	book := appealBook(t)
	filing, err := FilePetition(book, "peticao-1", "decreto-40", "ana", "valor devido a menor", appealAnchor().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("FilePetition: %v", err)
	}
	upheld, err := ReviewPetition(book, filing, ReviewInput{ID: "revisao-1", Auditor: "auditor-1", Verdict: VerdictUphold, At: appealAnchor().Add(3 * time.Hour)})
	if err != nil {
		t.Fatalf("ReviewPetition: %v", err)
	}
	if _, err := OrderRepair(appealCorrective(), upheld, nil); !errors.Is(err, ErrUnreviewedRepair) {
		t.Fatalf("upheld repair = %v, want ErrUnreviewedRepair", err)
	}
	harmful, err := ReviewPetition(book, filing, ReviewInput{ID: "revisao-2", Auditor: "auditor-1", Verdict: VerdictRepair, Harm: true, Responsible: "rainha-1", Harmed: "ana", At: appealAnchor().Add(3 * time.Hour)})
	if err != nil {
		t.Fatalf("ReviewPetition: %v", err)
	}
	if _, err := OrderRepair(appealCorrective(), harmful, nil); !errors.Is(err, ErrIncompleteAct) {
		t.Fatalf("harm without obligation = %v, want the funded repair", err)
	}
	treasury := appealRestitution()
	treasury.Origin = "tesouro-livre"
	if _, err := OrderRepair(appealCorrective(), harmful, &treasury); !errors.Is(err, ErrTreasuryFundedRestitution) {
		t.Fatalf("treasury restitution = %v, want ErrTreasuryFundedRestitution", err)
	}
	plain, err := ReviewPetition(book, filing, ReviewInput{ID: "revisao-3", Auditor: "auditor-1", Verdict: VerdictRepair, At: appealAnchor().Add(3 * time.Hour)})
	if err != nil {
		t.Fatalf("ReviewPetition: %v", err)
	}
	gratuitous := appealRestitution()
	if _, err := OrderRepair(appealCorrective(), plain, &gratuitous); !errors.Is(err, ErrIncompleteAct) {
		t.Fatalf("largesse without harm = %v, want refusal", err)
	}
	if _, err := OrderRepair(appealCorrective(), plain, nil); err != nil {
		t.Fatalf("harmless correction: %v", err)
	}
}
