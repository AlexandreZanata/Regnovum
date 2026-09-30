package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// DeletionRequest is the owner-scoped state of one account deletion.
type DeletionRequest struct {
	ID          string
	AccountID   domain.AccountID
	Status      domain.DeletionRequestStatus
	RequestedAt time.Time
	ExecutedAt  *time.Time
	CanceledAt  *time.Time
}

// DeletionOutcome reports whether the call created the request or resolved
// an existing record.
type DeletionOutcome struct {
	Request  DeletionRequest
	Replayed bool
}

// DeletionAuditEvent is one administrative fact of the deletion workflow.
// It carries identifiers and stable codes only: the cancel reason travels
// in the restricted record, never in the trail.
type DeletionAuditEvent struct {
	AccountID  string
	Action     string
	ReasonCode string
	OccurredAt time.Time
}

// DeletionAuditRecorder records one deletion workflow fact in the platform
// trail, joining the caller transaction when there is one, so the mutation
// and its evidence commit or roll back together.
type DeletionAuditRecorder interface {
	RecordAccountDeletion(ctx context.Context, event DeletionAuditEvent) error
}

// DeletionRepository persists the deletion state machine and executes the
// anonymization workflow. Execution must be atomic: every private row is
// purged, the account is anonymized and the request is marked executed in
// one transaction, or nothing changes.
type DeletionRepository interface {
	// CreateDeletionRequest creates the request or replays the existing
	// record. Replayed is true when the account already had one.
	CreateDeletionRequest(ctx context.Context, accountID domain.AccountID, requestedAt time.Time) (*DeletionRequest, bool, error)

	// GetDeletionRequest returns the account's request or
	// ErrDeletionRequestNotFound.
	GetDeletionRequest(ctx context.Context, accountID domain.AccountID) (*DeletionRequest, error)

	// CancelDeletionRequest cancels a requested record inside its window.
	// A missing, foreign or terminal record answers ErrDeletionNotCancellable.
	CancelDeletionRequest(ctx context.Context, accountID domain.AccountID, reason string, canceledAt time.Time) (*DeletionRequest, error)

	// ListDueDeletionRequests returns requested records whose cooldown
	// elapsed as of the instant.
	ListDueDeletionRequests(ctx context.Context, now time.Time) ([]DeletionRequest, error)

	// ExecuteDeletionRequest anonymizes one account and marks its request
	// executed in one transaction. The account must be requested and due.
	ExecuteDeletionRequest(ctx context.Context, accountID domain.AccountID, executedAt time.Time) error
}

// RequestDeletionCommand addresses one deletion request.
type RequestDeletionCommand struct {
	AccountID domain.AccountID
}

// RequestDeletionUseCase starts the deletion state machine inside one
// transaction: the request and its audit evidence commit or roll back
// together. Requesting is owner-scoped and idempotent: a second call
// replays the existing record instead of restarting the cooldown.
type RequestDeletionUseCase struct {
	deletions DeletionRepository
	audit     DeletionAuditRecorder
	uow       UnitOfWork
	clock     Clock
}

// NewRequestDeletionUseCase builds the use case, refusing incomplete
// composition.
func NewRequestDeletionUseCase(deletions DeletionRepository, audit DeletionAuditRecorder, uow UnitOfWork, clock Clock) (*RequestDeletionUseCase, error) {
	if deletions == nil || audit == nil || uow == nil || clock == nil {
		return nil, ErrInvalidDeletionConfig
	}
	return &RequestDeletionUseCase{deletions: deletions, audit: audit, uow: uow, clock: clock}, nil
}

// Execute requests the deletion.
func (uc *RequestDeletionUseCase) Execute(ctx context.Context, command RequestDeletionCommand) (*DeletionOutcome, error) {
	if command.AccountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}

	now := uc.clock.Now().UTC()
	outcome := &DeletionOutcome{}
	err := uc.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		request, replayed, err := uc.deletions.CreateDeletionRequest(txCtx, command.AccountID, now)
		if err != nil {
			return err
		}
		if !replayed {
			if err := uc.audit.RecordAccountDeletion(txCtx, DeletionAuditEvent{
				AccountID:  command.AccountID.String(),
				Action:     "account.deletion_requested",
				ReasonCode: "holder_request",
				OccurredAt: now,
			}); err != nil {
				return err
			}
		}
		outcome.Request = *request
		outcome.Replayed = replayed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return outcome, nil
}

// CancelDeletionCommand addresses one cancellation.
type CancelDeletionCommand struct {
	AccountID domain.AccountID
	Reason    string
}

// CancelDeletionUseCase aborts a request inside its cooldown window. The
// reason is optional for the holder but always recorded in the restricted
// record when supplied; a terminal record is never cancellable. The state
// transition and its audit evidence commit or roll back together.
type CancelDeletionUseCase struct {
	deletions DeletionRepository
	audit     DeletionAuditRecorder
	uow       UnitOfWork
	clock     Clock
}

// NewCancelDeletionUseCase builds the use case, refusing incomplete
// composition.
func NewCancelDeletionUseCase(deletions DeletionRepository, audit DeletionAuditRecorder, uow UnitOfWork, clock Clock) (*CancelDeletionUseCase, error) {
	if deletions == nil || audit == nil || uow == nil || clock == nil {
		return nil, ErrInvalidDeletionConfig
	}
	return &CancelDeletionUseCase{deletions: deletions, audit: audit, uow: uow, clock: clock}, nil
}

// Execute cancels the request.
func (uc *CancelDeletionUseCase) Execute(ctx context.Context, command CancelDeletionCommand) (*DeletionRequest, error) {
	if command.AccountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	reason := strings.TrimSpace(command.Reason)

	now := uc.clock.Now().UTC()
	var canceled *DeletionRequest
	err := uc.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		current, err := uc.deletions.GetDeletionRequest(txCtx, command.AccountID)
		if err != nil {
			return err
		}
		if !domain.DeletionCancellable(current.Status, now, current.RequestedAt) {
			return ErrDeletionNotCancellable
		}
		// The trail stores the reason under a NOT NULL guarded CHECK: an
		// empty reason would sail through validation and die as an
		// unclassified 500 inside the transaction instead.
		if reason == "" || len(reason) > 500 {
			return ErrInvalidCancelReason
		}

		record, err := uc.deletions.CancelDeletionRequest(txCtx, command.AccountID, reason, now)
		if err != nil {
			return err
		}
		if err := uc.audit.RecordAccountDeletion(txCtx, DeletionAuditEvent{
			AccountID:  command.AccountID.String(),
			Action:     "account.deletion_canceled",
			ReasonCode: "holder_cancel",
			OccurredAt: now,
		}); err != nil {
			return err
		}
		canceled = record
		return nil
	})
	if err != nil {
		return nil, err
	}
	return canceled, nil
}

// GetDeletionStatusUseCase answers the owner's deletion state. A holder
// without a request answers ErrDeletionRequestNotFound.
type GetDeletionStatusUseCase struct {
	deletions DeletionRepository
}

// NewGetDeletionStatusUseCase builds the use case, refusing incomplete
// composition.
func NewGetDeletionStatusUseCase(deletions DeletionRepository) (*GetDeletionStatusUseCase, error) {
	if deletions == nil {
		return nil, ErrInvalidDeletionConfig
	}
	return &GetDeletionStatusUseCase{deletions: deletions}, nil
}

// Execute resolves the owner's request.
func (uc *GetDeletionStatusUseCase) Execute(ctx context.Context, accountID domain.AccountID) (*DeletionRequest, error) {
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	return uc.deletions.GetDeletionRequest(ctx, accountID)
}

// ExecuteDueDeletionsUseCase runs the workflow: every requested record whose
// cooldown elapsed is executed and audited in one transaction per account.
// It is idempotent: a replayed run finds no due record, and an execution
// that loses a race is skipped.
type ExecuteDueDeletionsUseCase struct {
	deletions DeletionRepository
	audit     DeletionAuditRecorder
	uow       UnitOfWork
	clock     Clock
}

// NewExecuteDueDeletionsUseCase builds the use case, refusing incomplete
// composition.
func NewExecuteDueDeletionsUseCase(deletions DeletionRepository, audit DeletionAuditRecorder, uow UnitOfWork, clock Clock) (*ExecuteDueDeletionsUseCase, error) {
	if deletions == nil || audit == nil || uow == nil || clock == nil {
		return nil, ErrInvalidDeletionConfig
	}
	return &ExecuteDueDeletionsUseCase{deletions: deletions, audit: audit, uow: uow, clock: clock}, nil
}

// ExecutedDeletions reports how many accounts were anonymized by one run.
type ExecutedDeletions struct {
	Executed int
}

// Execute runs every due deletion.
func (uc *ExecuteDueDeletionsUseCase) Execute(ctx context.Context) (*ExecutedDeletions, error) {
	now := uc.clock.Now().UTC()
	due, err := uc.deletions.ListDueDeletionRequests(ctx, now)
	if err != nil {
		return nil, err
	}

	executed := 0
	for _, request := range due {
		if !domain.DeletionExecutable(request.RequestedAt, now) {
			continue
		}
		skipped := false
		err := uc.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
			if err := uc.deletions.ExecuteDeletionRequest(txCtx, request.AccountID, now); err != nil {
				if errors.Is(err, ErrDeletionNotExecutable) {
					skipped = true
					return nil
				}
				return err
			}
			return uc.audit.RecordAccountDeletion(txCtx, DeletionAuditEvent{
				AccountID:  request.AccountID.String(),
				Action:     "account.deletion_executed",
				ReasonCode: "cooling_off_elapsed",
				OccurredAt: now,
			})
		})
		if err != nil {
			return nil, err
		}
		if !skipped {
			executed++
		}
	}
	return &ExecutedDeletions{Executed: executed}, nil
}
