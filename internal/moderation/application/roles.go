// The first administrator, promoted locally and audited (P19-T09).
//
// An installation has no administrator until one is promoted, and no account is
// allowed to promote itself or another through HTTP: the act belongs to an
// operator on the host, holding the database DSN. This file owns that act —
// which account may become one, what is required of it before it can, and what
// the trail records — and it is written to be reachable from a command line and
// from nothing else. The administrative HTTP surface grants no role and
// registers no route for one.
//
// Three requirements decide a promotion, and each closes a hole the others do
// not:
//
//   - the account must exist and its email must be verified: an operator who
//     mistypes an address, or names a registration that was never confirmed,
//     promotes nobody;
//
//   - the account must already hold a confirmed second factor. The
//     administrative gate requires a session that presented one
//     (docs/SECURITY.md), so an administrator without an enrollment could not
//     act at all, and granting the role anyway would hand an administrative
//     capability to an account protected by a single secret;
//
//   - the account must be able to authenticate. A suspended or deleted account
//     keeps the verified address it had, so the two rules above would let one
//     through and the installation would end with an administrator that cannot
//     sign in — an administrator nobody can use and, worse, a first
//     administrator that blocks the bootstrap while granting nothing;
//
//   - the installation must have no active administrative assignment. That is
//     what makes this the bootstrap of the *first* administrator instead of a
//     privilege escalator for whoever can read the DSN, and it is what makes
//     the command the recovery path: a demotion returns the installation to the
//     state this command requires, and nothing else does.
//
// The state change and its audit event commit together: a promotion the trail
// could not record is a promotion that did not happen.
package application

import (
	"context"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

// The rules the trail records beside each transition. They are stable codes,
// not prose: a reviewer filters on them.
const (
	// RuleFirstAdministrator is the only reason a grant happens here — the
	// installation had no active administrative assignment.
	RuleFirstAdministrator = "first_administrator"
	// RuleLocalDemotion is the reversal: an operator removed the assignment
	// from the host. A demotion is never refused for the state of the
	// account, because losing access must always be possible.
	RuleLocalDemotion = "local_demotion"
)

// The assignment statuses the trail records as previous or new state. They
// describe the schema's own vocabulary: no row, a dated revocation, or the role
// that was held.
const (
	// StatusNone means the account held no assignment at all.
	StatusNone = "none"
	// StatusRevoked means the account held an assignment that was revoked.
	StatusRevoked = "revoked"
)

// UnitOfWork runs a function inside one database transaction. The promotion
// and its audit event are one fact, so they commit or roll back together; the
// concrete manager is composed at bootstrap.
type UnitOfWork interface {
	// WithinTransaction begins a transaction, makes it available to
	// participants through the context and commits only when fn returns nil.
	WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}

// AdministrationTarget is the local account a target address resolves to, with
// the facts the promotion requires of it. It carries facts, never decisions:
// the use case decides, so every refusal is this module's own vocabulary.
type AdministrationTarget struct {
	AccountID domain.AccountID
	// EmailVerified reports whether the address was proved to belong to
	// somebody, which is what makes it a person a promotion can name.
	EmailVerified bool
	// SecondFactorConfirmed reports whether the account holds a confirmed
	// second factor.
	SecondFactorConfirmed bool
	// CanAuthenticate reports whether the account may sign in at all. A
	// suspended or deleted account cannot, and promoting one would create an
	// administrator that is unreachable.
	CanAuthenticate bool
}

// AdministrationTargetDirectory resolves an address to the account it belongs
// to. An identity adapter answers this consumer-oriented port; the moderation
// module never reads the account schema and never parses a stored email.
type AdministrationTargetDirectory interface {
	// LookupByEmail returns ErrInvalidAdministrationTarget for something that
	// is not an address at all, and ErrAdministrationTargetNotFound for an
	// address that identifies no account.
	LookupByEmail(ctx context.Context, email string) (*AdministrationTarget, error)
}

// RoleGrantRequest is one assignment write. GrantedBy is the account the
// assignment is attributed to; the local command has no operator account, so
// the promoted account is its own origin and the trail carries the rule that
// allowed the transition — see the adapter for the exact statement.
type RoleGrantRequest struct {
	AccountID domain.AccountID
	Role      domain.Role
	GrantedBy domain.AccountID
	GrantedAt time.Time
}

// RoleGrantRecord is the stored assignment after the write.
type RoleGrantRecord struct {
	AccountID domain.AccountID
	Role      domain.Role
	GrantedBy domain.AccountID
	GrantedAt time.Time
}

// RoleAdministration is the write side of the assignment schema.
//
// LockAssignments serializes the decision "this installation has no
// administrator" against the write that follows it. Without the lock two
// commands starting together would both read an empty table, both insert, and
// the installation would end with two first administrators. Every
// administration takes the same lock, inside the caller transaction, and the
// adapter refuses a call that carries no transaction.
type RoleAdministration interface {
	// LockAssignments takes the administrative write lock.
	LockAssignments(ctx context.Context) error

	// AnyActiveAssignment reports whether any account holds an active
	// assignment, whatever the role.
	AnyActiveAssignment(ctx context.Context) (bool, error)

	// Grant writes the assignment: it inserts the row, or revives a revoked
	// one keeping its origin.
	Grant(ctx context.Context, grant RoleGrantRequest) (*RoleGrantRecord, error)

	// Revoke dates the revocation of an active assignment and reports
	// whether this call was the one that dated it. A false result is a
	// concurrent demotion, not an error.
	Revoke(ctx context.Context, accountID domain.AccountID, revokedAt time.Time) (bool, error)
}

// AdministrativeGrantEvent is the recorded promotion. PreviousStatus says what
// the account held before, so a trail reader can tell a first grant from the
// revival of a revoked assignment without joining anything.
type AdministrativeGrantEvent struct {
	AccountID      string
	Role           domain.Role
	PreviousStatus string
	Rule           string
	OccurredAt     time.Time
}

// AdministrativeRevocationEvent is the recorded demotion. Rule says why the
// transition was allowed, so the trail reader of a demotion finds the same
// stable code the grant carries.
type AdministrativeRevocationEvent struct {
	AccountID  string
	Role       domain.Role
	Rule       string
	OccurredAt time.Time
}

// RoleAudit records the administrative facts of this module in the trail.
type RoleAudit interface {
	// RecordAdministrativeGrant records that an account became an
	// administrator.
	RecordAdministrativeGrant(ctx context.Context, event AdministrativeGrantEvent) error
	// RecordAdministrativeRevocation records that an account stopped being
	// one.
	RecordAdministrativeRevocation(ctx context.Context, event AdministrativeRevocationEvent) error
}

// GrantFirstAdministratorCommand names the account to promote.
type GrantFirstAdministratorCommand struct {
	Email string
}

// GrantFirstAdministratorResult is what the operator is told and what the trail
// recorded. Revived distinguishes a first grant from the revival of a revoked
// assignment, which is the same write and a different history.
type GrantFirstAdministratorResult struct {
	AccountID domain.AccountID
	Role      domain.Role
	Revived   bool
}

// GrantFirstAdministratorUseCase promotes the installation's first
// administrator. It promotes the administrative role and nothing else: the
// other roles of the vocabulary are assigned by an administrative flow, not by
// a command on the host.
type GrantFirstAdministratorUseCase struct {
	targets        AdministrationTargetDirectory
	roles          RoleRepository
	administration RoleAdministration
	audit          RoleAudit
	clock          Clock
	uow            UnitOfWork
}

// NewGrantFirstAdministratorUseCase builds the use case. Missing dependencies
// fail at construction: a bootstrap without the trail would be an unaudited
// privilege escalation, and that is not a state this composition may reach.
func NewGrantFirstAdministratorUseCase(
	targets AdministrationTargetDirectory,
	roles RoleRepository,
	administration RoleAdministration,
	audit RoleAudit,
	clock Clock,
	uow UnitOfWork,
) (*GrantFirstAdministratorUseCase, error) {
	if targets == nil || roles == nil || administration == nil || audit == nil || clock == nil || uow == nil {
		return nil, ErrInvalidRoleAdministrationConfig
	}
	return &GrantFirstAdministratorUseCase{
		targets:        targets,
		roles:          roles,
		administration: administration,
		audit:          audit,
		clock:          clock,
		uow:            uow,
	}, nil
}

// Execute promotes the account the address belongs to, or refuses without
// writing anything.
func (uc *GrantFirstAdministratorUseCase) Execute(ctx context.Context, cmd GrantFirstAdministratorCommand) (*GrantFirstAdministratorResult, error) {
	target, err := uc.resolveTarget(ctx, cmd.Email)
	if err != nil {
		return nil, err
	}

	var result *GrantFirstAdministratorResult
	err = uc.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		// The decision and the write are one atomic step.
		if err := uc.administration.LockAssignments(txCtx); err != nil {
			return err
		}

		active, err := uc.administration.AnyActiveAssignment(txCtx)
		if err != nil {
			return err
		}
		if active {
			return ErrAdministratorAlreadyExists
		}

		current, err := uc.roles.AssignmentFor(txCtx, target.AccountID)
		if err != nil {
			return err
		}
		if current != nil && !current.Revoked {
			return ErrAssignmentAlreadyActive
		}

		grantedAt := uc.clock.Now().UTC()
		record, err := uc.administration.Grant(txCtx, RoleGrantRequest{
			AccountID: target.AccountID,
			Role:      domain.RoleAdmin,
			// The local command has no account of its own. Attributing the
			// assignment to the account that holds it is the only honest
			// provenance available: inventing an operator identity would put
			// a name in the trail that never acted.
			GrantedBy: target.AccountID,
			GrantedAt: grantedAt,
		})
		if err != nil {
			return err
		}

		if err := uc.audit.RecordAdministrativeGrant(txCtx, AdministrativeGrantEvent{
			AccountID:      record.AccountID.String(),
			Role:           record.Role,
			PreviousStatus: assignmentStatus(current),
			Rule:           RuleFirstAdministrator,
			OccurredAt:     grantedAt,
		}); err != nil {
			return err
		}

		result = &GrantFirstAdministratorResult{
			AccountID: record.AccountID,
			Role:      record.Role,
			Revived:   current != nil && current.Revoked,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// resolveTarget applies everything a promotion requires of the named account,
// before any transaction is opened and before any state is read.
func (uc *GrantFirstAdministratorUseCase) resolveTarget(ctx context.Context, email string) (*AdministrationTarget, error) {
	if strings.TrimSpace(email) == "" {
		return nil, ErrEmptyAdministrationTarget
	}
	target, err := uc.targets.LookupByEmail(ctx, strings.TrimSpace(email))
	if err != nil {
		return nil, err
	}
	if target == nil || target.AccountID.IsZero() {
		return nil, ErrAdministrationTargetNotFound
	}
	if !target.EmailVerified {
		return nil, ErrAdministrationTargetEmailUnverified
	}
	if !target.CanAuthenticate {
		return nil, ErrAdministrationTargetNotActive
	}
	if !target.SecondFactorConfirmed {
		return nil, ErrAdministrationTargetWithoutSecondFactor
	}
	return target, nil
}

// RevokeAdministratorCommand names the account to demote.
type RevokeAdministratorCommand struct {
	Email string
}

// RevokeAdministratorResult is what the operator is told and what the trail
// recorded.
type RevokeAdministratorResult struct {
	AccountID domain.AccountID
	Role      domain.Role
	RevokedAt time.Time
}

// RevokeAdministratorUseCase removes one administrative assignment. It is the
// reversal of the bootstrap and it is audited by the same trail: the phase's
// gate requires the stack to be reversible as well as reproducible, and a
// promotion nobody can undo is the half that is missing.
//
// It deliberately does not require a verified email or a second factor: losing
// access must always be possible, and a demotion that could be refused because
// the account lost its factor would leave an installation with an
// administrator it cannot remove.
type RevokeAdministratorUseCase struct {
	targets        AdministrationTargetDirectory
	roles          RoleRepository
	administration RoleAdministration
	audit          RoleAudit
	clock          Clock
	uow            UnitOfWork
}

// NewRevokeAdministratorUseCase builds the use case, failing closed on an
// incomplete composition for the same reason the bootstrap does.
func NewRevokeAdministratorUseCase(
	targets AdministrationTargetDirectory,
	roles RoleRepository,
	administration RoleAdministration,
	audit RoleAudit,
	clock Clock,
	uow UnitOfWork,
) (*RevokeAdministratorUseCase, error) {
	if targets == nil || roles == nil || administration == nil || audit == nil || clock == nil || uow == nil {
		return nil, ErrInvalidRoleAdministrationConfig
	}
	return &RevokeAdministratorUseCase{
		targets:        targets,
		roles:          roles,
		administration: administration,
		audit:          audit,
		clock:          clock,
		uow:            uow,
	}, nil
}

// Execute dates the revocation of the account's active assignment, or refuses
// without writing anything. A second call for the same account is refused
// exactly like a demotion of an account that holds nothing: no write, no second
// audit event.
func (uc *RevokeAdministratorUseCase) Execute(ctx context.Context, cmd RevokeAdministratorCommand) (*RevokeAdministratorResult, error) {
	if strings.TrimSpace(cmd.Email) == "" {
		return nil, ErrEmptyAdministrationTarget
	}
	target, err := uc.targets.LookupByEmail(ctx, strings.TrimSpace(cmd.Email))
	if err != nil {
		return nil, err
	}
	if target == nil || target.AccountID.IsZero() {
		return nil, ErrAdministrationTargetNotFound
	}

	var result *RevokeAdministratorResult
	err = uc.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := uc.administration.LockAssignments(txCtx); err != nil {
			return err
		}

		current, err := uc.roles.AssignmentFor(txCtx, target.AccountID)
		if err != nil {
			return err
		}
		if current == nil || current.Revoked {
			return ErrNoActiveAssignment
		}

		revokedAt := uc.clock.Now().UTC()
		revoked, err := uc.administration.Revoke(txCtx, target.AccountID, revokedAt)
		if err != nil {
			return err
		}
		if !revoked {
			// The row stopped being active between the read and the write.
			// Somebody else demoted it first, and that call recorded the
			// fact.
			return ErrNoActiveAssignment
		}

		if err := uc.audit.RecordAdministrativeRevocation(txCtx, AdministrativeRevocationEvent{
			AccountID:  target.AccountID.String(),
			Role:       current.Role,
			Rule:       RuleLocalDemotion,
			OccurredAt: revokedAt,
		}); err != nil {
			return err
		}

		result = &RevokeAdministratorResult{
			AccountID: target.AccountID,
			Role:      current.Role,
			RevokedAt: revokedAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// assignmentStatus renders the state an account held before a write, in the
// trail's vocabulary.
func assignmentStatus(assignment *RoleAssignment) string {
	if assignment == nil {
		return StatusNone
	}
	if assignment.Revoked {
		return StatusRevoked
	}
	return assignment.Role.String()
}
