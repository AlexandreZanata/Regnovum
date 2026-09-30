// Package application owns the private case lifecycle: thin use
// cases over the sealed disputes domain, behind ports the staged
// HTTP adapter implements with an in-memory store. Production stays
// disabled until P44; this layer holds decision-free orchestration
// only, with no wiring, no routes and no movement of value.
package application

import (
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/disputes/domain"
)

// CaseRecord is one private case file: the sealed proposal, the
// opened entry and, as the rite advances, the instructed hearing
// and the reasoned decision. Absent stages read as nil, never as
// someone else's file.
type CaseRecord struct {
	Proposal domain.Proposal
	Entry    domain.Case
	Hearing  *domain.Hearing
	Decision *domain.Decision
}

// CaseRecords is the port behind the lifecycle: filed case records
// by negotiation key. The staged adapter stores them in memory;
// future adapters persist them without changing these use cases.
type CaseRecords interface {
	Get(key string) (CaseRecord, error)
	Put(record CaseRecord) error
}

// scope refuses reads and moves by strangers: only a named party
// touches its own file. Unknown keys read as absent through the
// domain error, so strangers learn nothing.
func scope(record CaseRecord, account string) error {
	if account != record.Proposal.Claimant && account != record.Proposal.Respondent {
		return domain.ErrCaseNotParty
	}
	return nil
}

// AcceptCaseUseCase records one party's acceptance of the filed
// terms and opens the entry once both sides accepted. Replays of
// the same party return the file unchanged.
type AcceptCaseUseCase struct {
	records CaseRecords
}

// NewAcceptCaseUseCase creates the use case, refusing incomplete
// composition.
func NewAcceptCaseUseCase(records CaseRecords) (*AcceptCaseUseCase, error) {
	if records == nil {
		return nil, domain.ErrInvalidCase
	}
	return &AcceptCaseUseCase{records: records}, nil
}

// Execute accepts the filed terms as one account at one instant. The
// instant arrives per call — the use case never reads the wall
// clock.
func (uc *AcceptCaseUseCase) Execute(key, account string, now time.Time) (CaseRecord, error) {
	record, err := uc.records.Get(key)
	if err != nil {
		return CaseRecord{}, err
	}
	if err := scope(record, account); err != nil {
		return CaseRecord{}, err
	}
	proposal, err := record.Proposal.Accept(account, now)
	if err != nil {
		return CaseRecord{}, err
	}
	record.Proposal = proposal
	if proposal.Bound() && record.Entry.Status == "" {
		entry, err := domain.OpenConsentCase(domain.CaseArbitration, proposal, account, now)
		if err != nil {
			return CaseRecord{}, err
		}
		record.Entry = entry
	}
	if err := uc.records.Put(record); err != nil {
		return CaseRecord{}, err
	}
	return record, nil
}

// DefendCaseUseCase files one proportional-access exhibit of one
// named party before the evidence deadline.
type DefendCaseUseCase struct {
	records CaseRecords
}

// NewDefendCaseUseCase creates the use case, refusing incomplete
// composition.
func NewDefendCaseUseCase(records CaseRecords) (*DefendCaseUseCase, error) {
	if records == nil {
		return nil, domain.ErrInvalidCase
	}
	return &DefendCaseUseCase{records: records}, nil
}

// Execute files the exhibit. Without an instructed hearing there is
// nothing to defend yet.
func (uc *DefendCaseUseCase) Execute(key, account, digest string, now time.Time) (CaseRecord, error) {
	record, err := uc.records.Get(key)
	if err != nil {
		return CaseRecord{}, err
	}
	if err := scope(record, account); err != nil {
		return CaseRecord{}, err
	}
	if record.Hearing == nil {
		return CaseRecord{}, domain.ErrInvalidCase
	}
	hearing, err := record.Hearing.SubmitEvidence(account, digest, now)
	if err != nil {
		return CaseRecord{}, err
	}
	record.Hearing = &hearing
	if err := uc.records.Put(record); err != nil {
		return CaseRecord{}, err
	}
	return record, nil
}

// AppealCaseUseCase contests one ruling once, by a named party
// within the appeal window and with an explicit reason.
type AppealCaseUseCase struct {
	records CaseRecords
}

// NewAppealCaseUseCase creates the use case, refusing incomplete
// composition.
func NewAppealCaseUseCase(records CaseRecords) (*AppealCaseUseCase, error) {
	if records == nil {
		return nil, domain.ErrInvalidCase
	}
	return &AppealCaseUseCase{records: records}, nil
}

// Execute appeals the filed ruling. Without a ruling there is
// nothing to contest yet.
func (uc *AppealCaseUseCase) Execute(key, account, reason string, now time.Time) (CaseRecord, error) {
	record, err := uc.records.Get(key)
	if err != nil {
		return CaseRecord{}, err
	}
	if err := scope(record, account); err != nil {
		return CaseRecord{}, err
	}
	if record.Decision == nil {
		return CaseRecord{}, domain.ErrInvalidCase
	}
	decision, err := record.Decision.Appeal(account, reason, now)
	if err != nil {
		return CaseRecord{}, err
	}
	record.Decision = &decision
	if err := uc.records.Put(record); err != nil {
		return CaseRecord{}, err
	}
	return record, nil
}

// ReadCaseFileUseCase reads one owned case file: proposal, entry,
// defenses counted, ruling and appeal. Strangers read absence, so
// third parties learn nothing about other people's cases.
type ReadCaseFileUseCase struct {
	records CaseRecords
}

// NewReadCaseFileUseCase creates the use case, refusing incomplete
// composition.
func NewReadCaseFileUseCase(records CaseRecords) (*ReadCaseFileUseCase, error) {
	if records == nil {
		return nil, domain.ErrInvalidCase
	}
	return &ReadCaseFileUseCase{records: records}, nil
}

// Execute reads the file scoped to one account.
func (uc *ReadCaseFileUseCase) Execute(key, account string) (CaseRecord, error) {
	record, err := uc.records.Get(key)
	if err != nil {
		return CaseRecord{}, err
	}
	if err := scope(record, account); err != nil {
		return CaseRecord{}, domain.ErrUnknownCase
	}
	return record, nil
}
