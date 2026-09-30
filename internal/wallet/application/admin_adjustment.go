package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// AdminAdjustmentEvent is the audit record of one administrative
// adjustment. The ledger operation itself already carries reason and actor
// atomically; this event feeds the administrative audit trail.
type AdminAdjustmentEvent struct {
	ActorAccountID domain.AccountID
	AccountID      domain.AccountID
	OperationID    string
	OperationType  domain.OperationType
	Allocation     domain.Allocation
	Reason         domain.Reason
	Reference      domain.Reference
	IdempotencyKey domain.IdempotencyKey
	Replayed       bool
	OccurredAt     time.Time
}

// AdminAuditRecorder persists administrative audit events. Implementations
// must be idempotent by IdempotencyKey: the adjustment use case reports both
// fresh and replayed outcomes, so a retry can repair a lost record without
// duplicating it.
type AdminAuditRecorder interface {
	RecordAdminAdjustment(ctx context.Context, event AdminAdjustmentEvent) error
}

// AdministratorAuthorizer verifies that the acting account may perform
// administrative adjustments. The wallet module never decides roles: the
// identity/moderation adapter answers this consumer-oriented port.
type AdministratorAuthorizer interface {
	// EnsureAdministrator returns ErrNotAuthorized when the actor may not
	// perform administrative adjustments.
	EnsureAdministrator(ctx context.Context, actorAccountID domain.AccountID) error
}

// AdminAdjustmentResult is the outcome of an administrative adjustment.
type AdminAdjustmentResult struct {
	Operation  domain.Operation
	Allocation domain.Allocation
	Replayed   bool
}
