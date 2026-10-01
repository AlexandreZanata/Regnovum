package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func proceedingAnchor() time.Time {
	return time.Date(2026, time.February, 2, 10, 0, 0, 0, time.UTC)
}

func proceedingProof() string {
	return strings.Repeat("cd", 32)
}

func proceedingReport(id, reporter, subject string) Report {
	return Report{
		ID: ReportID(id), Kind: ViolationFraud,
		Reporter: Subject(reporter), Subject: Subject(subject),
		Charter: "v2", Reason: "risco concreto com prova selada",
		Evidence: proceedingProof(), FiledAt: proceedingAnchor(),
	}
}

func proceedingRequest() OpenRequest {
	anchor := proceedingAnchor()
	return OpenRequest{
		ID:         "caso-1",
		Report:     proceedingReport("denuncia-1", "guarda-1", "ana"),
		Inquisitor: "inquisidor-1", Arbiter: "arbitro-1", Auditor: "auditor-1",
		King:            "rei-1",
		Interested:      []Subject{"socio-ana"},
		Competence:      "inquisicao-severa: fraude contra a Carta",
		Mandate:         Mandate{Holder: "inquisidor-1", IssuedAt: anchor, ExpiresAt: anchor.Add(24 * time.Hour)},
		OpenedAt:        anchor.Add(time.Hour),
		Deadline:        anchor.Add(5 * time.Hour),
		MaxDuration:     72 * time.Hour,
		ExistingIDs:     nil,
		ExistingReports: nil,
	}
}

func TestSevereOpeningPublishesCompetenceAndDeadline(t *testing.T) {
	req := proceedingRequest()
	opened, err := OpenSevereCase(req)
	if err != nil {
		t.Fatalf("OpenSevereCase: %v", err)
	}
	if opened.Report != req.Report.ID || opened.Accused != req.Report.Subject {
		t.Fatalf("opened = %+v, want the proven report", opened)
	}
	if opened.Inquisitor != "inquisidor-1" || opened.Arbiter != "arbitro-1" || opened.Auditor != "auditor-1" {
		t.Fatalf("opened = %+v, want three distinct offices", opened)
	}
	if opened.Competence != req.Competence {
		t.Fatalf("opened = %+v, want published competence", opened)
	}
	if !opened.Deadline.After(opened.OpenedAt) {
		t.Fatalf("opened = %+v, want a published deadline after the opening", opened)
	}
	if err := ReviewSuccessionEligibility("rival-1", "auditor-1", "rei-1", []Subject{"socio-ana"}); err != nil {
		t.Fatalf("neutral succession review: %v, want eligible", err)
	}
}

func TestInterestedProsecutionRefused(t *testing.T) {
	accuserOpens := proceedingRequest()
	accuserOpens.Inquisitor = "guarda-1"
	accuserOpens.Mandate.Holder = "guarda-1"
	if _, err := OpenSevereCase(accuserOpens); !errors.Is(err, ErrInterestedProsecution) {
		t.Fatalf("accuser as inquisitor = %v, want ErrInterestedProsecution", err)
	}
	selfAccuses := proceedingRequest()
	selfAccuses.Inquisitor = "ana"
	selfAccuses.Mandate.Holder = "ana"
	if _, err := OpenSevereCase(selfAccuses); !errors.Is(err, ErrInterestedProsecution) {
		t.Fatalf("accused as inquisitor = %v, want ErrInterestedProsecution", err)
	}
	partnerOpens := proceedingRequest()
	partnerOpens.Inquisitor = "socio-ana"
	partnerOpens.Mandate.Holder = "socio-ana"
	if _, err := OpenSevereCase(partnerOpens); !errors.Is(err, ErrInterestedProsecution) {
		t.Fatalf("interested opener = %v, want ErrInterestedProsecution", err)
	}
	judgeConflict := proceedingRequest()
	judgeConflict.Arbiter = "ana"
	if _, err := OpenSevereCase(judgeConflict); !errors.Is(err, ErrInterestedProsecution) {
		t.Fatalf("accused as arbiter = %v, want ErrInterestedProsecution", err)
	}
	auditorConflict := proceedingRequest()
	auditorConflict.Auditor = "socio-ana"
	if _, err := OpenSevereCase(auditorConflict); !errors.Is(err, ErrInterestedProsecution) {
		t.Fatalf("interested auditor = %v, want ErrInterestedProsecution", err)
	}
	bare := proceedingRequest()
	bare.Report.Evidence = ""
	if _, err := OpenSevereCase(bare); !errors.Is(err, ErrUnprovenAccusation) {
		t.Fatalf("unevidenced severe case = %v, want ErrUnprovenAccusation", err)
	}
}

func TestSelfReviewRefused(t *testing.T) {
	sameBench := proceedingRequest()
	sameBench.Arbiter = "juiz-unico"
	sameBench.Auditor = "juiz-unico"
	if _, err := OpenSevereCase(sameBench); !errors.Is(err, ErrSelfReview) {
		t.Fatalf("arbiter as auditor = %v, want ErrSelfReview", err)
	}
	openerJudges := proceedingRequest()
	openerJudges.Arbiter = "inquisidor-1"
	if _, err := OpenSevereCase(openerJudges); !errors.Is(err, ErrSelfReview) {
		t.Fatalf("inquisitor as arbiter = %v, want ErrSelfReview", err)
	}
	openerAudits := proceedingRequest()
	openerAudits.Auditor = "inquisidor-1"
	if _, err := OpenSevereCase(openerAudits); !errors.Is(err, ErrSelfReview) {
		t.Fatalf("inquisitor as auditor = %v, want ErrSelfReview", err)
	}
	if err := ReviewSuccessionEligibility("rival-1", "rival-1", "rei-1", nil); !errors.Is(err, ErrSelfReview) {
		t.Fatalf("self succession review = %v, want ErrSelfReview", err)
	}
}

func TestDuplicateCaseRefused(t *testing.T) {
	req := proceedingRequest()
	if _, err := OpenSevereCase(req); err != nil {
		t.Fatalf("first opening: %v", err)
	}
	replayID := proceedingRequest()
	replayID.ExistingIDs = []CaseID{"caso-1"}
	if _, err := OpenSevereCase(replayID); !errors.Is(err, ErrDuplicateCase) {
		t.Fatalf("same id = %v, want ErrDuplicateCase", err)
	}
	replayReport := proceedingRequest()
	replayReport.ID = "caso-2"
	replayReport.ExistingReports = []ReportID{"denuncia-1"}
	if _, err := OpenSevereCase(replayReport); !errors.Is(err, ErrDuplicateCase) {
		t.Fatalf("same report = %v, want ErrDuplicateCase", err)
	}
}

func TestExpiredMandateRefused(t *testing.T) {
	early := proceedingRequest()
	early.OpenedAt = proceedingAnchor().Add(-time.Hour)
	early.Deadline = proceedingAnchor().Add(time.Hour)
	if _, err := OpenSevereCase(early); !errors.Is(err, ErrExpiredMandate) {
		t.Fatalf("opening before mandate = %v, want ErrExpiredMandate", err)
	}
	anchor := proceedingAnchor()
	atExpiry := proceedingRequest()
	atExpiry.OpenedAt = anchor.Add(24 * time.Hour)
	atExpiry.Deadline = anchor.Add(25 * time.Hour)
	if _, err := OpenSevereCase(atExpiry); !errors.Is(err, ErrExpiredMandate) {
		t.Fatalf("opening at expiry = %v, want ErrExpiredMandate", err)
	}
	stranger := proceedingRequest()
	stranger.Mandate.Holder = "inquisidor-9"
	if _, err := OpenSevereCase(stranger); !errors.Is(err, ErrExpiredMandate) {
		t.Fatalf("foreign mandate = %v, want ErrExpiredMandate", err)
	}
}

func TestThroneConflictAndDenunciationAlone(t *testing.T) {
	kingJudgesRival := proceedingRequest()
	kingJudgesRival.Report = proceedingReport("denuncia-2", "guarda-1", "rival-1")
	kingJudgesRival.King = "rei-1"
	kingJudgesRival.Interested = []Subject{"rei-1"}
	kingJudgesRival.Arbiter = "rei-1"
	if _, err := OpenSevereCase(kingJudgesRival); !errors.Is(err, ErrConflictedThrone) {
		t.Fatalf("interested King as arbiter = %v, want ErrConflictedThrone", err)
	}
	kingReviewsRival := proceedingRequest()
	kingReviewsRival.Report = proceedingReport("denuncia-3", "guarda-2", "rival-1")
	kingReviewsRival.King = "rei-1"
	kingReviewsRival.Interested = []Subject{"rei-1"}
	kingReviewsRival.Auditor = "rei-1"
	if _, err := OpenSevereCase(kingReviewsRival); !errors.Is(err, ErrConflictedThrone) {
		t.Fatalf("interested King as auditor = %v, want ErrConflictedThrone", err)
	}
	if err := ReviewSuccessionEligibility("rival-1", "rei-1", "rei-1", []Subject{"rei-1"}); !errors.Is(err, ErrConflictedThrone) {
		t.Fatalf("interested King reviewing rival = %v, want ErrConflictedThrone", err)
	}
	denounced := proceedingReport("denuncia-9", "guarda-9", "rival-9")
	if _, err := FileReport(ReportDraft{
		ID: denounced.ID, KindRaw: string(denounced.Kind),
		Reporter: denounced.Reporter, Subject: denounced.Subject,
		Charter: string(denounced.Charter), Reason: denounced.Reason,
		Evidence: denounced.Evidence, FiledAt: proceedingAnchor(),
	}); err != nil {
		t.Fatalf("FileReport: %v", err)
	}
	if err := ReviewSuccessionEligibility("rival-9", "auditor-9", "rei-9", nil); err != nil {
		t.Fatalf("denunciation alone blocks succession: %v, want eligible", err)
	}
	unpublished := proceedingRequest()
	unpublished.Competence = "  "
	if _, err := OpenSevereCase(unpublished); !errors.Is(err, ErrUnpublishedProceeding) {
		t.Fatalf("blank competence = %v, want ErrUnpublishedProceeding", err)
	}
	eternal := proceedingRequest()
	eternal.Deadline = eternal.OpenedAt.Add(720 * time.Hour)
	if _, err := OpenSevereCase(eternal); !errors.Is(err, ErrUnpublishedProceeding) {
		t.Fatalf("deadline beyond cap = %v, want ErrUnpublishedProceeding", err)
	}
}
