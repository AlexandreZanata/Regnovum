package application

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/disputes/domain"
)

type memoryRecords struct {
	files map[string]CaseRecord
}

func newMemoryRecords() *memoryRecords {
	return &memoryRecords{files: map[string]CaseRecord{}}
}

func (m *memoryRecords) Get(key string) (CaseRecord, error) {
	record, ok := m.files[key]
	if !ok {
		return CaseRecord{}, domain.ErrUnknownCase
	}
	return record, nil
}

func (m *memoryRecords) Put(record CaseRecord) error {
	m.files[record.Proposal.Key] = record
	return nil
}

func lifecycleDeadline() time.Time {
	return time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
}

func lifecycleProposal(t *testing.T) domain.Proposal {
	t.Helper()
	proposal, err := domain.Propose(domain.ProposalRequest{
		Key: "caso-alpha", Version: 1, Object: "entrega do lote 7",
		Claimant: "requerente", Respondent: "requerida", ValueMilli: 20000,
		EscrowRef: "caucao-alpha", Rite: "rito-acordo-v1", Evidence: "regras-prova-v1",
		Costs: domain.CostsSplit, ExpiresAt: lifecycleDeadline(), Execution: "liberar caucao ao adimplente",
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	return proposal
}

func TestLifecycleCompositionRefuses(t *testing.T) {
	if _, err := NewAcceptCaseUseCase(nil); !errors.Is(err, domain.ErrInvalidCase) {
		t.Fatalf("accept nil records = %v, want ErrInvalidCase", err)
	}
	if _, err := NewDefendCaseUseCase(nil); !errors.Is(err, domain.ErrInvalidCase) {
		t.Fatalf("defend nil records = %v, want ErrInvalidCase", err)
	}
	if _, err := NewAppealCaseUseCase(nil); !errors.Is(err, domain.ErrInvalidCase) {
		t.Fatalf("appeal nil records = %v, want ErrInvalidCase", err)
	}
	if _, err := NewReadCaseFileUseCase(nil); !errors.Is(err, domain.ErrInvalidCase) {
		t.Fatalf("read nil records = %v, want ErrInvalidCase", err)
	}
}

func TestLifecycleAcceptOpensAndScopes(t *testing.T) {
	stores := newMemoryRecords()
	proposal := lifecycleProposal(t)
	now := lifecycleDeadline().Add(-2 * time.Hour)
	once, err := proposal.Accept("requerente", now)
	if err != nil {
		t.Fatalf("first Accept: %v", err)
	}
	if err := stores.Put(CaseRecord{Proposal: once}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	accept, err := NewAcceptCaseUseCase(stores)
	if err != nil {
		t.Fatalf("NewAcceptCaseUseCase: %v", err)
	}
	read, err := NewReadCaseFileUseCase(stores)
	if err != nil {
		t.Fatalf("NewReadCaseFileUseCase: %v", err)
	}
	if _, err := accept.Execute("caso-alpha", "estranha", now); !errors.Is(err, domain.ErrCaseNotParty) {
		t.Fatalf("stranger accept = %v, want ErrCaseNotParty", err)
	}
	if _, err := read.Execute("caso-alpha", "estranha"); !errors.Is(err, domain.ErrUnknownCase) {
		t.Fatalf("stranger read = %v, want ErrUnknownCase: absence, never someone else's file", err)
	}
	if _, err := read.Execute("caso-inexistente", "requerente"); !errors.Is(err, domain.ErrUnknownCase) {
		t.Fatalf("unknown read = %v, want ErrUnknownCase", err)
	}
	bound, err := accept.Execute("caso-alpha", "requerida", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second accept: %v", err)
	}
	if !bound.Proposal.Bound() || bound.Entry.Status != domain.StatusOpen {
		t.Fatalf("bound = %+v, want the entry opened over bilateral terms", bound)
	}
	replay, err := accept.Execute("caso-alpha", "requerida", now.Add(time.Hour))
	if err != nil || !replay.Proposal.Bound() {
		t.Fatalf("replay = %+v/%v, want the file unchanged", replay, err)
	}
}
