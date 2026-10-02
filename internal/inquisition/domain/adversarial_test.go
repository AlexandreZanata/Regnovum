package domain

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func advAnchor() time.Time {
	return time.Date(2026, time.February, 10, 10, 0, 0, 0, time.UTC)
}

func advProof() string {
	return strings.Repeat("ab", 32)
}

func advReport(suffix string) Report {
	report, err := FileReport(ReportDraft{
		ID: ReportID("denuncia-adv-" + suffix), KindRaw: "fraude",
		Reporter: "guarda-1", Subject: "ana",
		Charter: "v2", Reason: "risco concreto com prova selada",
		Evidence: advProof(), FiledAt: advAnchor(),
	})
	if err != nil {
		panic(err)
	}
	return report
}

func advCase(report Report, suffix string) SevereCase {
	opened, err := OpenSevereCase(OpenRequest{
		ID: CaseID("caso-adv-" + suffix), Report: report,
		Inquisitor: "inquisidor-1", Arbiter: "arbitro-1", Auditor: "auditor-1",
		King: "rei-1", Competence: "inquisicao-severa: fraude contra a Carta",
		Mandate: Mandate{
			Holder:   "inquisidor-1",
			IssuedAt: advAnchor(), ExpiresAt: advAnchor().Add(48 * time.Hour),
		},
		OpenedAt:    advAnchor().Add(time.Hour),
		Deadline:    advAnchor().Add(73 * time.Hour),
		MaxDuration: 168 * time.Hour,
	})
	if err != nil {
		panic(err)
	}
	return opened
}

func advSentence(suffix string) Sentence {
	report := advReport(suffix)
	opened := advCase(report, suffix)
	envelope, err := SealEvidence(SealRequest{
		ID: EvidenceID("peca-adv-" + suffix), Case: opened.ID,
		KindRaw: "documento", Digest: advProof(),
		SealedAt: advAnchor().Add(2 * time.Hour), Sealer: "inquisidor-1",
	})
	if err != nil {
		panic(err)
	}
	notice, err := NotifyDefense(envelope.ID, "defensor-1", advAnchor().Add(2*time.Hour+30*time.Minute))
	if err != nil {
		panic(err)
	}
	view, err := RevealForDefense(RevealRequest{
		Envelope: envelope, Notice: notice, Viewer: "defensor-1",
		Digest: advProof(), At: advAnchor().Add(3 * time.Hour),
	})
	if err != nil {
		panic(err)
	}
	phase, err := RequireSealedPhase(PhaseRequest{
		Case: opened.ID, Envelope: envelope, HasSeal: true,
		DecidedAt: advAnchor().Add(3*time.Hour + 30*time.Minute),
	})
	if err != nil {
		panic(err)
	}
	sentence, err := DecideSentence(SentenceRequest{
		ID: SentenceID("sentenca-adv-" + suffix), Case: opened, Report: report,
		Envelope: envelope, HasSeal: true, Phase: phase,
		Disclosure: view, HasDisclosure: true,
		Decider: "arbitro-1", SanctionRaw: "restricao",
		Competence: "inquisicao-severa: fraude contra a Carta",
		CharterRaw: "v2", FactDigest: advProof(),
		Motive:    "fraude provada pelo selo, restricao temporaria proporcional ao dano contido",
		DecidedAt: advAnchor().Add(4 * time.Hour),
	})
	if err != nil {
		panic(err)
	}
	return sentence
}

func advExecuted(suffix string) Execution {
	sentence := advSentence(suffix)
	pending, err := AuthorizeExecution(AuthorizeRequest{
		ID: ExecutionID("execucao-adv-" + suffix), Sentence: sentence,
		Authority: "inquisidor-1", At: advAnchor().Add(6 * time.Hour),
		Window: 48 * time.Hour, MaxWindow: 72 * time.Hour,
	})
	if err != nil {
		panic(err)
	}
	done, err := PerformExecution(PerformRequest{
		Execution: pending, Executor: "carrasco-1",
		Office: OfficeCarrasco, At: advAnchor().Add(7 * time.Hour),
	})
	if err != nil {
		panic(err)
	}
	return done
}

func TestAdversarialViaIntegralComClocksControlados(t *testing.T) {
	t.Parallel()

	done := advExecuted("via")
	if !done.Executed || done.Lapsed || done.Pardoned {
		t.Fatalf("execution = %+v, want a single executed death", done)
	}
	if done.Case != "caso-adv-via" || done.Sentence != "sentenca-adv-via" {
		t.Fatalf("execution = %+v, want the debated case and sentence", done)
	}
	if !done.Deadline.Equal(done.AuthorizedAt.Add(48 * time.Hour)) {
		t.Fatalf("execution = %+v, want the bounded carrasco window", done)
	}
	edge, err := PerformExecution(PerformRequest{
		Execution: advExecuted("tick"), Executor: "carrasco-1",
		Office: OfficeCarrasco, At: advAnchor().Add(7 * time.Hour),
	})
	_ = edge
	if err != nil {
		t.Fatalf("concurrent chain: %v", err)
	}

	var wg sync.WaitGroup
	failures := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			suffix := strings.Repeat("c", n+1) + "par"
			if _, err := FileReport(ReportDraft{
				ID: ReportID("denuncia-adv-" + suffix), KindRaw: "fraude",
				Reporter: "guarda-1", Subject: "ana",
				Charter: "v2", Reason: "risco concreto com prova selada",
				Evidence: advProof(), FiledAt: advAnchor(),
			}); err != nil {
				failures <- err.Error()
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Fatalf("concurrent filing: %s", failure)
	}
}

func TestAdversarialSelfReviewMutantsDie(t *testing.T) {
	t.Parallel()

	report := advReport("self")
	interested := OpenRequest{
		ID: "caso-adv-self", Report: report,
		Inquisitor: "guarda-1", Arbiter: "arbitro-1", Auditor: "auditor-1",
		King: "rei-1", Competence: "inquisicao-severa: fraude contra a Carta",
		Mandate: Mandate{
			Holder:   "guarda-1",
			IssuedAt: advAnchor(), ExpiresAt: advAnchor().Add(48 * time.Hour),
		},
		OpenedAt: advAnchor().Add(time.Hour),
		Deadline: advAnchor().Add(73 * time.Hour), MaxDuration: 168 * time.Hour,
	}
	if _, err := OpenSevereCase(interested); !errors.Is(err, ErrInterestedProsecution) {
		t.Fatalf("reporter opening = %v, want ErrInterestedProsecution", err)
	}
	throne := interested
	throne.Inquisitor = "inquisidor-1"
	throne.Mandate.Holder = "inquisidor-1"
	throne.Arbiter = "ana"
	if _, err := OpenSevereCase(throne); !errors.Is(err, ErrInterestedProsecution) {
		t.Fatalf("accused judging = %v, want ErrInterestedProsecution", err)
	}
	king := interested
	king.Inquisitor = "inquisidor-1"
	king.Mandate.Holder = "inquisidor-1"
	king.King = "guarda-1"
	king.Auditor = "guarda-1"
	if _, err := OpenSevereCase(king); !errors.Is(err, ErrConflictedThrone) {
		t.Fatalf("interested king deciding = %v, want ErrConflictedThrone", err)
	}
	sentence := advSentence("self")
	late := AppealRequest{
		ID: "recurso-adv-self", Sentence: sentence,
		Appellant: "ana", Reviewer: "arbitro-1",
		FiledAt: sentence.DecidedAt.Add(time.Hour),
		Window:  72 * time.Hour, MaxWindow: 168 * time.Hour,
	}
	if _, err := FileAppeal(late); !errors.Is(err, ErrSelfReview) {
		t.Fatalf("decider reviewing = %v, want ErrSelfReview", err)
	}
	reopen := ReopenRequest{
		ID: "reabertura-adv-self", Sentence: sentence,
		GroundRaw: "erro-decisivo", Reviewer: "arbitro-1",
		Detail:    "fundamento concreto com referencia aos autos",
		DecidedAt: advAnchor().Add(24 * time.Hour),
	}
	if _, err := DecideReopening(reopen); !errors.Is(err, ErrSelfReview) {
		t.Fatalf("decider reopening = %v, want ErrSelfReview", err)
	}
	pending, err := AuthorizeExecution(AuthorizeRequest{
		ID: "execucao-adv-self", Sentence: sentence,
		Authority: "inquisidor-1", At: advAnchor().Add(6 * time.Hour),
		Window: 48 * time.Hour, MaxWindow: 72 * time.Hour,
	})
	if err != nil {
		t.Fatalf("AuthorizeExecution: %v", err)
	}
	if _, err := PerformExecution(PerformRequest{
		Execution: pending, Executor: "ana",
		Office: OfficeCarrasco, At: advAnchor().Add(7 * time.Hour),
	}); !errors.Is(err, ErrInterestedProsecution) {
		t.Fatalf("accused executing = %v, want ErrInterestedProsecution", err)
	}
	pardonReq := PardonRequest{
		ID: "perdao-adv-self", Sentence: sentence,
		Granter: "arbitro-1", Detail: "decisor perdoando a propria sentenca",
		At: advAnchor().Add(8 * time.Hour),
	}
	if _, err := GrantPardon(pardonReq); !errors.Is(err, ErrSelfReview) {
		t.Fatalf("decider pardoning = %v, want ErrSelfReview", err)
	}
	if err := ReviewSuccessionEligibility("ana", "ana", "rei-1", nil); !errors.Is(err, ErrSelfReview) {
		t.Fatalf("own eligibility = %v, want ErrSelfReview", err)
	}
}

func TestAdversarialErasedProofMutantsDie(t *testing.T) {
	t.Parallel()

	report := advReport("proof")
	opened := advCase(report, "proof")
	if _, err := SealEvidence(SealRequest{
		ID: "peca-adv-raw", Case: opened.ID,
		KindRaw: "documento", Digest: "prova-crua",
		SealedAt: advAnchor().Add(2 * time.Hour), Sealer: "inquisidor-1",
	}); !errors.Is(err, ErrUnprovenAccusation) {
		t.Fatalf("raw proof = %v, want ErrUnprovenAccusation", err)
	}
	if _, err := SealEvidence(SealRequest{
		ID: "peca-adv-danger", Case: opened.ID,
		KindRaw: "executavel", Digest: advProof(),
		SealedAt: advAnchor().Add(2 * time.Hour), Sealer: "inquisidor-1",
	}); !errors.Is(err, ErrUnsafeEvidence) {
		t.Fatalf("dangerous file = %v, want ErrUnsafeEvidence", err)
	}
	envelope, err := SealEvidence(SealRequest{
		ID: "peca-adv-proof", Case: opened.ID,
		KindRaw: "documento", Digest: advProof(),
		SealedAt: advAnchor().Add(2 * time.Hour), Sealer: "inquisidor-1",
	})
	if err != nil {
		t.Fatalf("SealEvidence: %v", err)
	}
	notice, err := NotifyDefense(envelope.ID, "defensor-1", advAnchor().Add(2*time.Hour+30*time.Minute))
	if err != nil {
		t.Fatalf("NotifyDefense: %v", err)
	}
	swapped := RevealRequest{
		Envelope: envelope, Notice: notice, Viewer: "defensor-1",
		Digest: strings.Repeat("09", 32), At: advAnchor().Add(3 * time.Hour),
	}
	if _, err := RevealForDefense(swapped); !errors.Is(err, ErrTamperedEvidence) {
		t.Fatalf("swapped digest = %v, want ErrTamperedEvidence", err)
	}
	stranger := RevealRequest{
		Envelope: envelope, Notice: notice, Viewer: "estranho-1",
		Digest: advProof(), At: advAnchor().Add(3 * time.Hour),
	}
	if _, err := RevealForDefense(stranger); !errors.Is(err, ErrUnnotifiedDefense) {
		t.Fatalf("stranger viewing = %v, want ErrUnnotifiedDefense", err)
	}
	if _, err := RequireSealedPhase(PhaseRequest{
		Case: opened.ID, DecidedAt: advAnchor().Add(3 * time.Hour),
	}); !errors.Is(err, ErrUnsealedPhase) {
		t.Fatalf("bare phase = %v, want ErrUnsealedPhase", err)
	}
	view, err := RevealForDefense(RevealRequest{
		Envelope: envelope, Notice: notice, Viewer: "defensor-1",
		Digest: advProof(), At: advAnchor().Add(3 * time.Hour),
	})
	if err != nil {
		t.Fatalf("RevealForDefense: %v", err)
	}
	phase, err := RequireSealedPhase(PhaseRequest{
		Case: opened.ID, Envelope: envelope, HasSeal: true,
		DecidedAt: advAnchor().Add(3*time.Hour + 30*time.Minute),
	})
	if err != nil {
		t.Fatalf("RequireSealedPhase: %v", err)
	}
	rewritten := SentenceRequest{
		ID: "sentenca-adv-rewritten", Case: opened, Report: report,
		Envelope: envelope, HasSeal: true, Phase: phase,
		Disclosure: view, HasDisclosure: true,
		Decider: "arbitro-1", SanctionRaw: "restricao",
		Competence: "inquisicao-severa: fraude contra a Carta",
		CharterRaw: "v2", FactDigest: strings.Repeat("09", 32),
		Motive:    "fraude provada pelo selo, restricao temporaria proporcional",
		DecidedAt: advAnchor().Add(4 * time.Hour),
	}
	if _, err := DecideSentence(rewritten); !errors.Is(err, ErrTamperedEvidence) {
		t.Fatalf("rewritten facts = %v, want ErrTamperedEvidence", err)
	}
	anachronistic := rewritten
	anachronistic.ID = "sentenca-adv-anachronistic"
	anachronistic.FactDigest = advProof()
	anachronistic.CharterRaw = "v3"
	if _, err := DecideSentence(anachronistic); !errors.Is(err, ErrCharterMismatch) {
		t.Fatalf("wrong charter = %v, want ErrCharterMismatch", err)
	}
}

func TestAdversarialDuplicatedPenaltyMutantsDie(t *testing.T) {
	t.Parallel()

	report := advReport("dupe")
	opened := advCase(report, "dupe")
	dupID := OpenRequest{
		ID: opened.ID, Report: advReport("dupe-outro"),
		Inquisitor: "inquisidor-1", Arbiter: "arbitro-1", Auditor: "auditor-1",
		King: "rei-1", Competence: "inquisicao-severa: fraude contra a Carta",
		Mandate: Mandate{
			Holder:   "inquisidor-1",
			IssuedAt: advAnchor(), ExpiresAt: advAnchor().Add(48 * time.Hour),
		},
		OpenedAt: advAnchor().Add(time.Hour),
		Deadline: advAnchor().Add(73 * time.Hour), MaxDuration: 168 * time.Hour,
		ExistingIDs: []CaseID{opened.ID},
	}
	if _, err := OpenSevereCase(dupID); !errors.Is(err, ErrDuplicateCase) {
		t.Fatalf("same case twice = %v, want ErrDuplicateCase", err)
	}
	sentence := advSentence("dupe")
	denied := []DeniedPlea{}
	openedCount := 0
	for i := 0; i < 20; i++ {
		req := ReopenRequest{
			ID: ReopeningID("reabertura-adv-dupe"), Sentence: sentence,
			GroundRaw: "caucao-maior", Reviewer: "auditor-1",
			Detail: "fundamento concreto com referencia aos autos",
			Denied: denied, DecidedAt: advAnchor().Add(24 * time.Hour),
		}
		_, err := DecideReopening(req)
		if err == nil {
			openedCount++
			continue
		}
		if i == 0 {
			if !errors.Is(err, ErrInsufficientGround) {
				t.Fatalf("first bond = %v, want ErrInsufficientGround", err)
			}
			denied = append(denied, DeniedPlea{Ground: "caucao-maior"})
			continue
		}
		if !errors.Is(err, ErrRepeatedPetition) {
			t.Fatalf("plea %d = %v, want ErrRepeatedPetition", i+1, err)
		}
	}
	if openedCount != 0 {
		t.Fatalf("opened = %d, want no case from repeated pleas", openedCount)
	}
	pending, err := AuthorizeExecution(AuthorizeRequest{
		ID: "execucao-adv-dupe", Sentence: sentence,
		Authority: "inquisidor-1", At: advAnchor().Add(6 * time.Hour),
		Window: 48 * time.Hour, MaxWindow: 72 * time.Hour,
	})
	if err != nil {
		t.Fatalf("AuthorizeExecution: %v", err)
	}
	done, err := PerformExecution(PerformRequest{
		Execution: pending, Executor: "carrasco-1",
		Office: OfficeCarrasco, At: advAnchor().Add(7 * time.Hour),
	})
	if err != nil {
		t.Fatalf("PerformExecution: %v", err)
	}
	rival := PerformRequest{
		Execution: done, Executor: "carrasco-2",
		Office: OfficeCarrasco, At: advAnchor().Add(8 * time.Hour),
	}
	if _, err := PerformExecution(rival); !errors.Is(err, ErrDuplicateExecution) {
		t.Fatalf("second carrasco = %v, want ErrDuplicateExecution", err)
	}
	retry, err := PerformExecution(PerformRequest{
		Execution: done, Executor: "carrasco-1",
		Office: OfficeCarrasco, At: done.ExecutedAt,
	})
	if err != nil || !retry.Executed {
		t.Fatalf("identical retry = %v, want the same single state", err)
	}
	pardon, err := GrantPardon(PardonRequest{
		ID: "perdao-adv-dupe", Sentence: sentence,
		Granter: "rei-1", Detail: "perdao real anterior ao ato, so a execucao",
		At: advAnchor().Add(8 * time.Hour),
	})
	if err != nil {
		t.Fatalf("GrantPardon: %v", err)
	}
	if _, err := ApplyPardon(done, pardon); !errors.Is(err, ErrDuplicateExecution) {
		t.Fatalf("late pardon = %v, want ErrDuplicateExecution", err)
	}
}

func TestAdversarialConfiscationAndMintMutantsDie(t *testing.T) {
	t.Parallel()

	if _, err := ParseReopenGround("caucao-maior"); !errors.Is(err, ErrInsufficientGround) {
		t.Fatalf("larger bond ground = %v, want ErrInsufficientGround", err)
	}
	sentence := advSentence("custody")
	bond := ReopenRequest{
		ID: "reabertura-adv-bond", Sentence: sentence,
		GroundRaw: "caucao-maior", Reviewer: "auditor-1",
		Detail:    "fundamento concreto com referencia aos autos",
		DecidedAt: advAnchor().Add(24 * time.Hour),
	}
	if _, err := DecideReopening(bond); !errors.Is(err, ErrInsufficientGround) {
		t.Fatalf("bond alone = %v, want ErrInsufficientGround", err)
	}
	pending, err := AuthorizeExecution(AuthorizeRequest{
		ID: "execucao-adv-custody", Sentence: sentence,
		Authority: "inquisidor-1", At: advAnchor().Add(6 * time.Hour),
		Window: 48 * time.Hour, MaxWindow: 72 * time.Hour,
	})
	if err != nil {
		t.Fatalf("AuthorizeExecution: %v", err)
	}
	pardon, err := GrantPardon(PardonRequest{
		ID: "perdao-adv-custody", Sentence: sentence,
		Granter: "rei-1", Detail: "perdao real anterior ao ato, so a execucao",
		At: advAnchor().Add(7 * time.Hour),
	})
	if err != nil {
		t.Fatalf("GrantPardon: %v", err)
	}
	blocked, err := ApplyPardon(pending, pardon)
	if err != nil {
		t.Fatalf("ApplyPardon: %v", err)
	}
	if !blocked.Pardoned || blocked.Executed {
		t.Fatalf("blocked = %+v, want pardon stopping only the execution", blocked)
	}
	if blocked.Sentence != sentence.ID || sentence.Sanction != SanctionRestriction {
		t.Fatalf("sentence = %+v, want other sanctions standing past pardon", sentence)
	}
	lapsed, err := ExpireExecution(pending, pending.Deadline)
	if err != nil {
		t.Fatalf("ExpireExecution: %v", err)
	}
	if !lapsed.Lapsed || lapsed.Executed {
		t.Fatalf("lapsed = %+v, want lapse without execution", lapsed)
	}
	banned := []string{"Amount", "Balance", "Ledger", "Treasury", "Tesouro", "Escrow", "Custody", "Custodia", "Mint", "Wealth", "Riqueza", "Vault", "Cofre", "Money", "Valor", "Price", "Pagamento", "Payment"}
	values := []any{
		Report{}, Containment{}, SevereCase{}, SealedEnvelope{},
		DisclosureView{}, SealedPhase{}, Sentence{}, Appeal{},
		Reopening{}, Execution{}, Pardon{},
	}
	for _, value := range values {
		fields := []string{}
		for i := 0; i < reflect.TypeOf(value).NumField(); i++ {
			fields = append(fields, reflect.TypeOf(value).Field(i).Name)
		}
		for _, field := range fields {
			for _, deny := range banned {
				if strings.Contains(field, deny) {
					t.Fatalf("%T carries %s: sanctions never move custody", value, field)
				}
			}
		}
	}
}

func TestAdversarialPrivacyChecksGreen(t *testing.T) {
	t.Parallel()

	report := advReport("privacy")
	opened := advCase(report, "privacy")
	envelope, err := SealEvidence(SealRequest{
		ID: "peca-adv-privacy", Case: opened.ID,
		KindRaw: "documento", Digest: advProof(),
		SealedAt: advAnchor().Add(2 * time.Hour), Sealer: "inquisidor-1",
		Victim: true,
	})
	if err != nil {
		t.Fatalf("SealEvidence: %v", err)
	}
	notice, err := NotifyDefense(envelope.ID, "defensor-1", advAnchor().Add(2*time.Hour+30*time.Minute))
	if err != nil {
		t.Fatalf("NotifyDefense: %v", err)
	}
	bare := RevealRequest{
		Envelope: envelope, Notice: notice, Viewer: "defensor-1",
		Digest: advProof(), At: advAnchor().Add(3 * time.Hour),
	}
	if _, err := RevealForDefense(bare); !errors.Is(err, ErrExposedVictim) {
		t.Fatalf("unredacted victim = %v, want ErrExposedVictim", err)
	}
	redacted := bare
	redacted.Redacted = true
	view, err := RevealForDefense(redacted)
	if err != nil {
		t.Fatalf("redacted victim: %v", err)
	}
	if view.Digest != advProof() || view.Case != opened.ID {
		t.Fatalf("view = %+v, want the binding without raw payload", view)
	}
	for _, field := range []string{"Payload", "Conteudo", "Content", "Raw", "Vitima", "Victim", "Menor", "Minor"} {
		for i := 0; i < reflect.TypeOf(view).NumField(); i++ {
			if reflect.TypeOf(view).Field(i).Name == field {
				t.Fatalf("DisclosureView carries %s: minimal access only", field)
			}
		}
	}
	nameless := ReportDraft{
		ID: "", KindRaw: "fraude",
		Reporter: "guarda-1", Subject: "ana",
		Charter: "v2", Reason: "risco concreto com prova selada",
		Evidence: advProof(), FiledAt: advAnchor(),
	}
	if _, err := FileReport(nameless); !errors.Is(err, ErrInvalidReport) {
		t.Fatalf("nameless filing = %v, want ErrInvalidReport", err)
	}
	for _, charter := range []string{"current", "v0", "v01"} {
		draft := ReportDraft{
			ID: "denuncia-adv-privacy", KindRaw: "fraude",
			Reporter: "guarda-1", Subject: "ana",
			Charter: charter, Reason: "risco concreto com prova selada",
			Evidence: advProof(), FiledAt: advAnchor(),
		}
		if _, err := FileReport(draft); !errors.Is(err, ErrAmbiguousCharter) {
			t.Fatalf("charter %q = %v, want ErrAmbiguousCharter", charter, err)
		}
	}
}
