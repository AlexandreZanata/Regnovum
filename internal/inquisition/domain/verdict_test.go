package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func verdictAnchor() time.Time {
	return time.Date(2026, time.February, 4, 10, 0, 0, 0, time.UTC)
}

func verdictProof() string {
	return strings.Repeat("ab", 32)
}

func verdictCase() SevereCase {
	return SevereCase{
		ID: "caso-1", Report: "denuncia-1", Accused: "ana",
		Inquisitor: "inquisidor-1", Arbiter: "arbitro-1", Auditor: "auditor-1",
		King: "rei-1", Competence: "inquisicao-severa: fraude contra a Carta",
		OpenedAt: verdictAnchor(), Deadline: verdictAnchor().Add(72 * time.Hour),
	}
}

func verdictReport() Report {
	return Report{
		ID: "denuncia-1", Kind: ViolationFraud,
		Reporter: "guarda-1", Subject: "ana",
		Charter: "v2", Reason: "risco concreto com prova selada",
		Evidence: verdictProof(), FiledAt: verdictAnchor(),
	}
}

func verdictEnvelope() SealedEnvelope {
	envelope, err := SealEvidence(SealRequest{
		ID: "peca-1", Case: "caso-1", KindRaw: "documento",
		Digest: verdictProof(), SealedAt: verdictAnchor().Add(time.Hour),
		Sealer: "inquisidor-1",
	})
	if err != nil {
		panic(err)
	}
	return envelope
}

func verdictDisclosure(envelope SealedEnvelope) DisclosureView {
	notice, err := NotifyDefense(envelope.ID, "defensor-1", verdictAnchor().Add(2*time.Hour))
	if err != nil {
		panic(err)
	}
	view, err := RevealForDefense(RevealRequest{
		Envelope: envelope, Notice: notice, Viewer: "defensor-1",
		Digest: envelope.Digest, At: verdictAnchor().Add(3 * time.Hour),
	})
	if err != nil {
		panic(err)
	}
	return view
}

func verdictPhase(envelope SealedEnvelope) SealedPhase {
	phase, err := RequireSealedPhase(PhaseRequest{
		Case: envelope.Case, Envelope: envelope, HasSeal: true,
		DecidedAt: verdictAnchor().Add(3*time.Hour + 30*time.Minute),
	})
	if err != nil {
		panic(err)
	}
	return phase
}

func verdictRequest() SentenceRequest {
	envelope := verdictEnvelope()
	return SentenceRequest{
		ID: "sentenca-1", Case: verdictCase(), Report: verdictReport(),
		Envelope: envelope, HasSeal: true, Phase: verdictPhase(envelope),
		Disclosure: verdictDisclosure(envelope), HasDisclosure: true,
		Decider: "arbitro-1", SanctionRaw: "restricao",
		Competence: "inquisicao-severa: fraude contra a Carta",
		CharterRaw: "v2", FactDigest: envelope.Digest,
		Motive:    "fraude provada pelo selo, restricao temporaria proporcional ao dano contido",
		DecidedAt: verdictAnchor().Add(4 * time.Hour),
	}
}

func TestMotivatedSentenceAndTimelyAppeal(t *testing.T) {
	sentence, err := DecideSentence(verdictRequest())
	if err != nil {
		t.Fatalf("DecideSentence: %v", err)
	}
	if sentence.FactDigest != verdictProof() {
		t.Fatalf("sentence = %+v, want the debated facts preserved", sentence)
	}
	if sentence.Charter != "v2" || sentence.Sanction != SanctionRestriction {
		t.Fatalf("sentence = %+v, want the contemporary rule and the closed sanction", sentence)
	}
	appeal, err := FileAppeal(AppealRequest{
		ID: "recurso-1", Sentence: sentence,
		Appellant: "ana", Reviewer: "auditor-1",
		FiledAt: sentence.DecidedAt.Add(24 * time.Hour),
		Window:  72 * time.Hour, MaxWindow: 168 * time.Hour,
	})
	if err != nil {
		t.Fatalf("FileAppeal: %v", err)
	}
	if appeal.Sentence != sentence.ID || appeal.Case != sentence.Case {
		t.Fatalf("appeal = %+v, want the reviewed sentence", appeal)
	}
	if !appeal.Deadline.Equal(sentence.DecidedAt.Add(72 * time.Hour)) {
		t.Fatalf("appeal = %+v, want the published deadline", appeal)
	}
	waived, err := RequireSealedPhase(PhaseRequest{
		Case: "caso-1", Waived: true, Motive: "prova pública e suficiente nos autos",
		Authority: "arbitro-1", DecidedAt: verdictAnchor().Add(3 * time.Hour),
	})
	if err != nil {
		t.Fatalf("motivated waiver: %v", err)
	}
	waivedReq := verdictRequest()
	waivedReq.ID = "sentenca-9"
	waivedReq.Phase = waived
	waivedReq.HasDisclosure = false
	waivedReq.FactDigest = waivedReq.Report.Evidence
	if _, err := DecideSentence(waivedReq); err != nil {
		t.Fatalf("sentence on motivated waiver: %v, want the excused disclosure", err)
	}
}

func TestSentenceWithoutDefenseFails(t *testing.T) {
	bare := verdictRequest()
	bare.HasDisclosure = false
	if _, err := DecideSentence(bare); !errors.Is(err, ErrDefenselessSentence) {
		t.Fatalf("defenseless sentence = %v, want ErrDefenselessSentence", err)
	}
	foreign := verdictRequest()
	other, err := SealEvidence(SealRequest{
		ID: "peca-2", Case: "caso-2", KindRaw: "documento",
		Digest: verdictProof(), SealedAt: verdictAnchor().Add(time.Hour),
		Sealer: "inquisidor-1",
	})
	if err != nil {
		t.Fatalf("SealEvidence: %v", err)
	}
	notice, err := NotifyDefense(other.ID, "defensor-1", verdictAnchor().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("NotifyDefense: %v", err)
	}
	view, err := RevealForDefense(RevealRequest{
		Envelope: other, Notice: notice, Viewer: "defensor-1",
		Digest: other.Digest, At: verdictAnchor().Add(3 * time.Hour),
	})
	if err != nil {
		t.Fatalf("RevealForDefense: %v", err)
	}
	foreign.Disclosure = view
	if _, err := DecideSentence(foreign); !errors.Is(err, ErrDefenselessSentence) {
		t.Fatalf("foreign disclosure = %v, want ErrDefenselessSentence", err)
	}
}

func TestSanctionOutsideCompetenceFails(t *testing.T) {
	unknown := verdictRequest()
	unknown.SanctionRaw = "exilio-perpetuo"
	if _, err := DecideSentence(unknown); !errors.Is(err, ErrOutOfCompetence) {
		t.Fatalf("unknown sanction = %v, want ErrOutOfCompetence", err)
	}
	divorced := verdictRequest()
	divorced.Competence = "tesouro-livre: mover valor"
	if _, err := DecideSentence(divorced); !errors.Is(err, ErrOutOfCompetence) {
		t.Fatalf("divorced competence = %v, want ErrOutOfCompetence", err)
	}
}

func TestAppealWindowAndDistinctReviewer(t *testing.T) {
	sentence, err := DecideSentence(verdictRequest())
	if err != nil {
		t.Fatalf("DecideSentence: %v", err)
	}
	late := AppealRequest{
		ID: "recurso-2", Sentence: sentence,
		Appellant: "ana", Reviewer: "auditor-1",
		FiledAt: sentence.DecidedAt.Add(73 * time.Hour),
		Window:  72 * time.Hour, MaxWindow: 168 * time.Hour,
	}
	if _, err := FileAppeal(late); !errors.Is(err, ErrUntimelyAppeal) {
		t.Fatalf("late appeal = %v, want ErrUntimelyAppeal", err)
	}
	early := late
	early.ID = "recurso-3"
	early.FiledAt = sentence.DecidedAt
	if _, err := FileAppeal(early); !errors.Is(err, ErrUntimelyAppeal) {
		t.Fatalf("appeal before sentence = %v, want ErrUntimelyAppeal", err)
	}
	selfReview := late
	selfReview.ID = "recurso-4"
	selfReview.FiledAt = sentence.DecidedAt.Add(time.Hour)
	selfReview.Reviewer = "arbitro-1"
	if _, err := FileAppeal(selfReview); !errors.Is(err, ErrSelfReview) {
		t.Fatalf("decider reviewing = %v, want ErrSelfReview", err)
	}
	ownCause := late
	ownCause.ID = "recurso-5"
	ownCause.FiledAt = sentence.DecidedAt.Add(time.Hour)
	ownCause.Reviewer = "ana"
	if _, err := FileAppeal(ownCause); !errors.Is(err, ErrSelfReview) {
		t.Fatalf("appellant reviewing = %v, want ErrSelfReview", err)
	}
}

func TestWrongCharterAndRewrittenFactsFail(t *testing.T) {
	anachronistic := verdictRequest()
	anachronistic.CharterRaw = "v3"
	if _, err := DecideSentence(anachronistic); !errors.Is(err, ErrCharterMismatch) {
		t.Fatalf("wrong charter = %v, want ErrCharterMismatch", err)
	}
	rewritten := verdictRequest()
	rewritten.FactDigest = strings.Repeat("09", 32)
	if _, err := DecideSentence(rewritten); !errors.Is(err, ErrTamperedEvidence) {
		t.Fatalf("rewritten facts = %v, want ErrTamperedEvidence", err)
	}
}
