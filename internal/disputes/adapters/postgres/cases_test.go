package postgres_test

// P56-T03 — the narrow bridge proves the envelope survives
// PostgreSQL: file one proposal, read it back identical through a
// second repository instance (the reload half), overwrite it with
// the accepted terms, and refuse unknown keys with the domain
// absent error. A second instance reading the first one's bytes is
// what "persists" means here — no process memory is involved.

import (
	"errors"
	"reflect"
	"testing"
	"time"

	adapterpg "github.com/AlexandreZanata/Regnovum/internal/disputes/adapters/postgres"
	disputesapp "github.com/AlexandreZanata/Regnovum/internal/disputes/application"
	disputesdomain "github.com/AlexandreZanata/Regnovum/internal/disputes/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func bridgeInstant() time.Time {
	return time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
}

func bridgeProposal(t *testing.T, key string, at time.Time) disputesdomain.Proposal {
	t.Helper()
	proposal, err := disputesdomain.Propose(disputesdomain.ProposalRequest{
		Key: key, Version: 1, Object: "entrega do lote 7",
		Claimant: "requerente", Respondent: "requerida", ValueMilli: 20000,
		EscrowRef: "caucao-" + key, Rite: "rito-acordo-v1", Evidence: "regras-prova-v1",
		Costs: disputesdomain.CostsSplit, ExpiresAt: at.Add(48 * time.Hour), Execution: "liberar caucao ao adimplente",
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	return proposal
}

func TestCaseRecordsSurviveReload(t *testing.T) {
	testDB := dbtest.New(t)
	writer, err := adapterpg.NewRepository(testDB.Pool.Pool())
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	reader, err := adapterpg.NewRepository(testDB.Pool.Pool())
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	if _, err := adapterpg.NewRepository(nil); err == nil {
		t.Fatal("nil pool must refuse construction")
	}

	now := bridgeInstant()
	filed := disputesapp.CaseRecord{Proposal: bridgeProposal(t, "caso-ponte", now)}
	if err := writer.Put(filed); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// A second instance reads the first one's bytes: the record
	// rests on PostgreSQL, not in process memory.
	reloaded, err := reader.Get("caso-ponte")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(reloaded, filed) {
		t.Fatalf("reloaded = %+v, want %+v", reloaded, filed)
	}

	// The accepted terms overwrite the envelope: the next reload
	// observes the move, never the stale filing.
	accepted, err := filed.Proposal.Accept("requerente", now)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	filed.Proposal = accepted
	if err := writer.Put(filed); err != nil {
		t.Fatalf("Put accepted: %v", err)
	}
	again, err := reader.Get("caso-ponte")
	if err != nil {
		t.Fatalf("Get accepted: %v", err)
	}
	if !reflect.DeepEqual(again, filed) {
		t.Fatalf("reloaded accepted = %+v, want %+v", again, filed)
	}

	if _, err := reader.Get("caso-ausente"); !errors.Is(err, disputesdomain.ErrUnknownCase) {
		t.Fatalf("unknown key err = %v, want ErrUnknownCase", err)
	}
}
