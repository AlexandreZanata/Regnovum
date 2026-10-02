package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func evidenceAnchor() time.Time {
	return time.Date(2026, time.February, 3, 10, 0, 0, 0, time.UTC)
}

func evidenceProof() string {
	return strings.Repeat("ef", 32)
}

func evidenceSeal(id, kind string) SealRequest {
	return SealRequest{
		ID: EvidenceID(id), Case: "caso-1", KindRaw: kind,
		Digest: evidenceProof(), SealedAt: evidenceAnchor(),
		Sealer: "inquisidor-1",
	}
}

func evidenceNotice(id, notified string, at time.Time) DefenseNotice {
	return DefenseNotice{
		Envelope: EvidenceID(id), Notified: Subject(notified),
		NotifiedAt: at,
	}
}

func TestSealedPhaseDisclosesMinimallyToNotifiedDefense(t *testing.T) {
	envelope, err := SealEvidence(evidenceSeal("peca-1", "documento"))
	if err != nil {
		t.Fatalf("SealEvidence: %v", err)
	}
	notice, err := NotifyDefense(envelope.ID, "defensor-1", evidenceAnchor().Add(time.Hour))
	if err != nil {
		t.Fatalf("NotifyDefense: %v", err)
	}
	view, err := RevealForDefense(RevealRequest{
		Envelope: envelope, Notice: notice, Viewer: "defensor-1",
		Digest: envelope.Digest, At: evidenceAnchor().Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("RevealForDefense: %v", err)
	}
	if view.Digest != envelope.Digest || view.Case != envelope.Case {
		t.Fatalf("view = %+v, want the sealed binding", view)
	}
	if view.Viewer != "defensor-1" {
		t.Fatalf("view = %+v, want the notified defense only", view)
	}
	phase, err := RequireSealedPhase(PhaseRequest{
		Case: envelope.Case, Envelope: envelope, HasSeal: true,
		DecidedAt: evidenceAnchor().Add(3 * time.Hour),
	})
	if err != nil {
		t.Fatalf("RequireSealedPhase: %v", err)
	}
	if phase.Envelope != envelope.ID || phase.Waived {
		t.Fatalf("phase = %+v, want the sealed envelope", phase)
	}
	log, err := AppendDisclosure(nil, DisclosureRecord{
		Envelope: envelope.ID, Digest: envelope.Digest,
		Viewer: "defensor-1", RevealedAt: view.RevealedAt,
	})
	if err != nil {
		t.Fatalf("AppendDisclosure: %v", err)
	}
	replayed, err := AppendDisclosure(log, DisclosureRecord{
		Envelope: envelope.ID, Digest: envelope.Digest,
		Viewer: "defensor-1", RevealedAt: view.RevealedAt,
	})
	if err != nil {
		t.Fatalf("AppendDisclosure replay: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("log = %d entries, want no duplication on replay", len(replayed))
	}
}

func TestSwappedProofAfterSealDisclosesNothing(t *testing.T) {
	envelope, err := SealEvidence(evidenceSeal("peca-2", "registro"))
	if err != nil {
		t.Fatalf("SealEvidence: %v", err)
	}
	notice := evidenceNotice("peca-2", "defensor-1", evidenceAnchor().Add(time.Hour))
	swapped := strings.Repeat("09", 32)
	if _, err := RevealForDefense(RevealRequest{
		Envelope: envelope, Notice: notice, Viewer: "defensor-1",
		Digest: swapped, At: evidenceAnchor().Add(2 * time.Hour),
	}); !errors.Is(err, ErrTamperedEvidence) {
		t.Fatalf("swapped digest = %v, want ErrTamperedEvidence", err)
	}
	log, err := AppendDisclosure(nil, DisclosureRecord{
		Envelope: envelope.ID, Digest: envelope.Digest,
		Viewer: "defensor-1", RevealedAt: evidenceAnchor().Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("AppendDisclosure: %v", err)
	}
	if _, err := AppendDisclosure(log, DisclosureRecord{
		Envelope: envelope.ID, Digest: swapped,
		Viewer: "defensor-1", RevealedAt: evidenceAnchor().Add(2 * time.Hour),
	}); !errors.Is(err, ErrTamperedEvidence) {
		t.Fatalf("rewritten record = %v, want ErrTamperedEvidence", err)
	}
}

func TestDisclosureWithoutNoticeIsBlocked(t *testing.T) {
	envelope, err := SealEvidence(evidenceSeal("peca-3", "imagem"))
	if err != nil {
		t.Fatalf("SealEvidence: %v", err)
	}
	stranger := evidenceNotice("peca-3", "defensor-1", evidenceAnchor().Add(time.Hour))
	if _, err := RevealForDefense(RevealRequest{
		Envelope: envelope, Notice: stranger, Viewer: "escutador-1",
		Digest: envelope.Digest, At: evidenceAnchor().Add(2 * time.Hour),
	}); !errors.Is(err, ErrUnnotifiedDefense) {
		t.Fatalf("stranger viewer = %v, want ErrUnnotifiedDefense", err)
	}
	late := evidenceNotice("peca-3", "defensor-1", evidenceAnchor().Add(3*time.Hour))
	if _, err := RevealForDefense(RevealRequest{
		Envelope: envelope, Notice: late, Viewer: "defensor-1",
		Digest: envelope.Digest, At: evidenceAnchor().Add(2 * time.Hour),
	}); !errors.Is(err, ErrUnnotifiedDefense) {
		t.Fatalf("late notice = %v, want ErrUnnotifiedDefense", err)
	}
	if _, err := RequireSealedPhase(PhaseRequest{
		Case: "caso-1", DecidedAt: evidenceAnchor(),
	}); !errors.Is(err, ErrUnsealedPhase) {
		t.Fatalf("bare case = %v, want ErrUnsealedPhase", err)
	}
	bareWaiver := PhaseRequest{
		Case: "caso-1", Waived: true,
		Authority: "arbitro-1", DecidedAt: evidenceAnchor(),
	}
	if _, err := RequireSealedPhase(bareWaiver); !errors.Is(err, ErrUnnotifiedDefense) {
		t.Fatalf("motiveless waiver = %v, want ErrUnnotifiedDefense", err)
	}
	waived, err := RequireSealedPhase(PhaseRequest{
		Case: "caso-1", Waived: true, Motive: "prova pública e suficiente nos autos",
		Authority: "arbitro-1", DecidedAt: evidenceAnchor(),
	})
	if err != nil {
		t.Fatalf("motivated waiver: %v", err)
	}
	if !waived.Waived || waived.Motive == "" {
		t.Fatalf("phase = %+v, want the recorded motive", waived)
	}
}

func TestDangerousFileNeverCirculates(t *testing.T) {
	for _, kind := range []string{"executavel", "script"} {
		if _, err := SealEvidence(evidenceSeal("peca-4", kind)); !errors.Is(err, ErrUnsafeEvidence) {
			t.Fatalf("SealEvidence(%s) = %v, want ErrUnsafeEvidence", kind, err)
		}
	}
	if _, err := SealEvidence(evidenceSeal("peca-4", "profecia")); !errors.Is(err, ErrUnsafeEvidence) {
		t.Fatalf("unknown kind = %v, want ErrUnsafeEvidence", err)
	}
}

func TestVictimOrMinorLeakIsBlocked(t *testing.T) {
	sealed := evidenceSeal("peca-5", "video")
	sealed.Victim = true
	sealed.Minor = true
	envelope, err := SealEvidence(sealed)
	if err != nil {
		t.Fatalf("SealEvidence: %v", err)
	}
	notice := evidenceNotice("peca-5", "defensor-1", evidenceAnchor().Add(time.Hour))
	if _, err := RevealForDefense(RevealRequest{
		Envelope: envelope, Notice: notice, Viewer: "defensor-1",
		Digest: envelope.Digest, At: evidenceAnchor().Add(2 * time.Hour),
	}); !errors.Is(err, ErrExposedVictim) {
		t.Fatalf("unredacted victim disclosure = %v, want ErrExposedVictim", err)
	}
	view, err := RevealForDefense(RevealRequest{
		Envelope: envelope, Notice: notice, Viewer: "defensor-1",
		Digest: envelope.Digest, Redacted: true,
		At: evidenceAnchor().Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("redacted disclosure: %v", err)
	}
	if !view.Redacted || view.Digest != envelope.Digest {
		t.Fatalf("view = %+v, want the redacted binding", view)
	}
}
