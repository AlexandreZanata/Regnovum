package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func jurisdictionAnchor() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func jurisdictionProof() string {
	return strings.Repeat("ab", 32)
}

func jurisdictionDraft(kind, evidence string) ReportDraft {
	return ReportDraft{
		ID: ReportID("denuncia-1"), KindRaw: kind,
		Reporter: "guarda-1", Subject: "ana",
		Charter: "v2", Reason: "risco concreto com prova selada",
		Evidence: evidence, FiledAt: jurisdictionAnchor(),
	}
}

func jurisdictionOrder() ContainOrder {
	return ContainOrder{
		ID: "contencao-1", Authority: "inquisidor-1",
		Motive: "conter o risco ate a apuracao",
		Window: 6 * time.Hour, MaxWindow: 24 * time.Hour,
		At: jurisdictionAnchor().Add(time.Hour),
	}
}

func TestSafetyJurisdictionAuthorizesTemporaryContainment(t *testing.T) {
	for _, kind := range []string{"ameaca", "fraude", "assedio", "dados", "malware"} {
		draft := jurisdictionDraft(kind, "")
		draft.ID = ReportID("denuncia-" + kind)
		if _, err := FileReport(draft); err != nil {
			t.Fatalf("FileReport(%s): %v", kind, err)
		}
	}
	report, err := FileReport(jurisdictionDraft("malware", jurisdictionProof()))
	if err != nil {
		t.Fatalf("FileReport: %v", err)
	}
	hold, err := AuthorizeContainment(report, jurisdictionOrder())
	if err != nil {
		t.Fatalf("AuthorizeContainment: %v", err)
	}
	if hold.Report != report.ID || hold.Subject != report.Subject {
		t.Fatalf("hold = %+v, want the reported case", hold)
	}
	if hold.Authority != "inquisidor-1" || hold.Motive != "conter o risco ate a apuracao" {
		t.Fatalf("hold = %+v, want recorded authority and motive", hold)
	}
	if !hold.EndsAt.Equal(hold.StartsAt.Add(6 * time.Hour)) {
		t.Fatalf("hold = %+v, want the bounded temporary window", hold)
	}
}

func TestPrivateAccusationActivatesNothing(t *testing.T) {
	for _, kind := range []string{"divida", "desafio"} {
		if _, err := FileReport(jurisdictionDraft(kind, jurisdictionProof())); !errors.Is(err, ErrPrivateMatter) {
			t.Fatalf("FileReport(%s) = %v, want ErrPrivateMatter", kind, err)
		}
	}
	if _, err := FileReport(jurisdictionDraft("profecia", jurisdictionProof())); !errors.Is(err, ErrUnknownViolation) {
		t.Fatalf("unknown kind = %v, want ErrUnknownViolation", err)
	}
	bare, err := FileReport(jurisdictionDraft("fraude", ""))
	if err != nil {
		t.Fatalf("unevidenced filing: %v", err)
	}
	if _, err := AuthorizeContainment(bare, jurisdictionOrder()); !errors.Is(err, ErrUnprovenAccusation) {
		t.Fatalf("unevidenced containment = %v, want ErrUnprovenAccusation", err)
	}
	if _, err := FileReport(jurisdictionDraft("fraude", "prova-crua")); !errors.Is(err, ErrUnprovenAccusation) {
		t.Fatalf("raw evidence = %v, want sealed proof or nothing", err)
	}
}

func TestUngroundedContainmentRefused(t *testing.T) {
	report, err := FileReport(jurisdictionDraft("ameaca", jurisdictionProof()))
	if err != nil {
		t.Fatalf("FileReport: %v", err)
	}
	anonymous := jurisdictionOrder()
	anonymous.Authority = ""
	if _, err := AuthorizeContainment(report, anonymous); !errors.Is(err, ErrUngroundedContainment) {
		t.Fatalf("anonymous hold = %v, want ErrUngroundedContainment", err)
	}
	muteless := jurisdictionOrder()
	muteless.Motive = "  "
	if _, err := AuthorizeContainment(report, muteless); !errors.Is(err, ErrUngroundedContainment) {
		t.Fatalf("motiveless hold = %v, want ErrUngroundedContainment", err)
	}
	instant := jurisdictionOrder()
	instant.Window = 0
	if _, err := AuthorizeContainment(report, instant); !errors.Is(err, ErrUngroundedContainment) {
		t.Fatalf("windowless hold = %v, want ErrUngroundedContainment", err)
	}
	endless := jurisdictionOrder()
	endless.Window = 48 * time.Hour
	if _, err := AuthorizeContainment(report, endless); !errors.Is(err, ErrUngroundedContainment) {
		t.Fatalf("overcap hold = %v, want the bounded temporary window", err)
	}
}

func TestFilingShapeAndCharterBinding(t *testing.T) {
	nameless := jurisdictionDraft("dados", jurisdictionProof())
	nameless.Reporter = ""
	if _, err := FileReport(nameless); !errors.Is(err, ErrInvalidReport) {
		t.Fatalf("nameless filing = %v, want ErrInvalidReport", err)
	}
	mirror := jurisdictionDraft("dados", jurisdictionProof())
	mirror.Subject = mirror.Reporter
	if _, err := FileReport(mirror); !errors.Is(err, ErrInvalidReport) {
		t.Fatalf("self report = %v, want ErrInvalidReport", err)
	}
	for _, charter := range []string{"", "current", "v0", "v01"} {
		draft := jurisdictionDraft("dados", jurisdictionProof())
		draft.Charter = charter
		if _, err := FileReport(draft); !errors.Is(err, ErrAmbiguousCharter) {
			t.Fatalf("charter %q = %v, want ErrAmbiguousCharter", charter, err)
		}
	}
}
