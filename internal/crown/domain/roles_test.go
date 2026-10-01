package domain

import (
	"errors"
	"testing"
	"time"
)

func roleAnchor() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func roleCurrent() CurrentReign {
	anchor := roleAnchor()
	return CurrentReign{
		Season: "temporada-1", Holder: "rainha-1", Reign: 2,
		StartsAt: anchor, EndsAt: anchor.Add(7776000 * time.Second), Open: true,
	}
}

func roleAppointment() Appointment {
	anchor := roleAnchor()
	return Appointment{
		ID: "nomeacao-1", Office: OfficeInquisitor, Holder: "inquisidor-1",
		Season: "temporada-1", Reign: 2, Competence: "instrucao",
		Appointer: "rainha-1",
		StartsAt:  anchor, EndsAt: anchor.Add(30 * 24 * time.Hour),
	}
}

func rolePanel() Panel {
	return Panel{
		Proceeding: "processo-1", Season: "temporada-1", Reign: 2,
		Inquisitor: "inquisidor-1", Accuser: "acusador-1", Accused: "ana",
		Judge: "juiz-1", Defender: "defensor-1",
		Witnesses: []HolderSubject{"testemunha-1"},
		Executor:  "carrasco-1", Auditor: "auditor-1",
		Interested: []HolderSubject{"lesada-1"},
	}
}

func roleFee() Honorarium {
	return Honorarium{
		ID: "honorario-1", Proceeding: "processo-1", Recipient: "juiz-1",
		Office: OfficeJustice, Season: "temporada-1", Reign: 2,
		Service: "presidir audiencia de instrucao", Amount: 500,
	}
}

func TestRolesSeatHappyPathAndMandateExpiry(t *testing.T) {
	current := roleCurrent()
	sealed, err := DefineAppointment(roleAppointment(), current)
	if err != nil {
		t.Fatalf("DefineAppointment: %v", err)
	}
	at := roleAnchor().Add(time.Hour)
	if err := IsAppointmentActive(sealed, current, at); err != nil {
		t.Fatalf("IsAppointmentActive: %v", err)
	}
	if _, err := SeatPanel(rolePanel(), current, at); err != nil {
		t.Fatalf("SeatPanel: %v", err)
	}
	if _, err := AuthorizeHonorarium(roleFee()); err != nil {
		t.Fatalf("AuthorizeHonorarium: %v", err)
	}
	past := roleAnchor().Add(31 * 24 * time.Hour)
	if err := IsAppointmentActive(sealed, current, past); !errors.Is(err, ErrOfficeExpired) {
		t.Fatalf("past mandate = %v, want ErrOfficeExpired", err)
	}
	next := current
	next.Season = "temporada-2"
	if err := IsAppointmentActive(sealed, next, at); !errors.Is(err, ErrSeasonMismatch) {
		t.Fatalf("next season = %v, want ErrSeasonMismatch without a fresh designation", err)
	}
}

func TestRolesRefusesAccuserJudge(t *testing.T) {
	current := roleCurrent()
	at := roleAnchor().Add(time.Hour)
	accuser := rolePanel()
	accuser.Judge = accuser.Accuser
	if _, err := SeatPanel(accuser, current, at); !errors.Is(err, ErrConflictedOffice) {
		t.Fatalf("accuser judge = %v, want ErrConflictedOffice", err)
	}
	inquisitor := rolePanel()
	inquisitor.Judge = inquisitor.Inquisitor
	if _, err := SeatPanel(inquisitor, current, at); !errors.Is(err, ErrConflictedOffice) {
		t.Fatalf("inquisitor judge = %v, want ErrConflictedOffice", err)
	}
}

func TestRolesRefusesInterestedWitnessExecutorAndKing(t *testing.T) {
	current := roleCurrent()
	at := roleAnchor().Add(time.Hour)
	witness := rolePanel()
	witness.Witnesses = []HolderSubject{"lesada-1"}
	if _, err := SeatPanel(witness, current, at); !errors.Is(err, ErrConflictedOffice) {
		t.Fatalf("interested witness = %v, want ErrConflictedOffice", err)
	}
	executor := rolePanel()
	executor.Executor = "lesada-1"
	if _, err := SeatPanel(executor, current, at); !errors.Is(err, ErrConflictedOffice) {
		t.Fatalf("interested executor = %v, want ErrConflictedOffice", err)
	}
	king := rolePanel()
	king.Accuser = current.Holder
	king.Judge = current.Holder
	if _, err := SeatPanel(king, current, at); !errors.Is(err, ErrConflictedOffice) {
		t.Fatalf("king judging rival in own cause = %v, want ErrConflictedOffice", err)
	}
	auditor := rolePanel()
	auditor.Auditor = auditor.Judge
	if _, err := SeatPanel(auditor, current, at); !errors.Is(err, ErrConflictedOffice) {
		t.Fatalf("dependent auditor = %v, want ErrConflictedOffice", err)
	}
}

func TestRolesRefusesOutcomePayAndUnknownMandate(t *testing.T) {
	if _, err := ParseOfficeKind("cardeal"); !errors.Is(err, ErrUnknownOffice) {
		t.Fatalf("unknown office = %v, want ErrUnknownOffice", err)
	}
	current := roleCurrent()
	unknown := roleAppointment()
	unknown.Office = "cardeal"
	if _, err := DefineAppointment(unknown, current); !errors.Is(err, ErrUnknownOffice) {
		t.Fatalf("unknown appointment = %v, want ErrUnknownOffice", err)
	}
	royal := roleAppointment()
	royal.Holder = royal.Appointer
	if _, err := DefineAppointment(royal, current); !errors.Is(err, ErrConflictedOffice) {
		t.Fatalf("king occupying office = %v, want ErrConflictedOffice", err)
	}
	beyond := roleAppointment()
	beyond.EndsAt = roleAnchor().Add(7776000*time.Second + time.Hour)
	if _, err := DefineAppointment(beyond, current); !errors.Is(err, ErrSeasonClosed) {
		t.Fatalf("mandate past season = %v, want ErrSeasonClosed", err)
	}
	outcome := roleFee()
	outcome.ForConviction = true
	if _, err := AuthorizeHonorarium(outcome); !errors.Is(err, ErrOutcomePay) {
		t.Fatalf("conviction pay = %v, want ErrOutcomePay", err)
	}
}
