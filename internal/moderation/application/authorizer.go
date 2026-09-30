// Package application defines the moderation authorization policy: who may
// take which administrative action, under which freshness and impartiality
// conditions. It depends only on the moderation domain and the Go standard
// library; persistence arrives through the RoleRepository port.
package application

import (
	"context"
	"fmt"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

// Clock exposes wall-clock time to moderation use cases, keeping them
// deterministic under test.
type Clock interface {
	Now() time.Time
}

// RoleAssignment is the active-or-revoked administrative assignment of one
// account, as read from persistence.
type RoleAssignment struct {
	AccountID domain.AccountID
	Role      domain.Role
	Revoked   bool
}

// RoleRepository resolves administrative assignments by account identifier.
// It keys on identifiers only: emails, frontend flags, payment state and
// popularity never cross this port.
type RoleRepository interface {
	// AssignmentFor returns the assignment of the account, or nil when the
	// account holds none. A revoked assignment is returned (not hidden) so
	// the caller can distinguish "never authorized" from "revoked".
	AssignmentFor(ctx context.Context, accountID domain.AccountID) (*RoleAssignment, error)
}

// AuthorizeCommand is everything the policy needs: the actor, the action,
// the owner of the targeted account (or the empty value when the target is
// not an account), the reporter of the underlying report (or empty when
// unknown), and how long ago the session completed full authentication.
type AuthorizeCommand struct {
	Actor       domain.AccountID
	Action      domain.Action
	TargetOwner domain.AccountID
	Reporter    domain.AccountID
	SessionAge  time.Duration
}

// AuthorizeResult is the explicit outcome. Conflict is reported alongside
// the denial so the caller can route the case to another reviewer.
type AuthorizeResult struct {
	Role     domain.Role
	Conflict bool
}

// Authorizer enforces the administrative authorization matrix:
//
//  1. The actor must hold an active assignment; revoked assignments deny
//     even inside an otherwise active session.
//  2. A detected conflict of interest denies, whatever the role.
//  3. The role must list the action in the domain matrix; payment state,
//     popularity and staff status are not inputs and cannot change the
//     outcome.
//  4. High-impact actions additionally require step-up freshness.
type Authorizer struct {
	roles RoleRepository
	clock Clock
}

// NewAuthorizer builds the authorizer, refusing incomplete composition.
func NewAuthorizer(roles RoleRepository, clock Clock) (*Authorizer, error) {
	if roles == nil || clock == nil {
		return nil, ErrInvalidAuthorizerConfig
	}
	return &Authorizer{roles: roles, clock: clock}, nil
}

// EnsureAuthorized enforces the matrix or returns a sentinel describing the
// denial. It never authorizes by frontend claim or email: unknown accounts
// resolve to no assignment and deny.
func (a *Authorizer) EnsureAuthorized(ctx context.Context, cmd AuthorizeCommand) (*AuthorizeResult, error) {
	if cmd.Actor.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	if !cmd.Action.IsValid() {
		return nil, domain.ErrInvalidAction
	}
	if cmd.SessionAge < 0 {
		return nil, domain.ErrInvalidSessionAge
	}

	assignment, err := a.roles.AssignmentFor(ctx, cmd.Actor)
	if err != nil {
		return nil, err
	}
	if assignment == nil {
		return nil, ErrNotAuthorized
	}
	if assignment.Revoked {
		return nil, fmt.Errorf("%w: assignment of %s is revoked", ErrRoleRevoked, assignment.Role)
	}
	if !assignment.Role.IsValid() {
		return nil, domain.ErrInvalidRole
	}

	if domain.HasConflict(cmd.Actor, cmd.TargetOwner, cmd.Reporter) {
		return &AuthorizeResult{Role: assignment.Role, Conflict: true}, ErrConflictOfInterest
	}

	if !assignment.Role.MayPerform(cmd.Action) {
		return nil, fmt.Errorf("%w: role %s may not take %s", domain.ErrRoleNotAuthorized, assignment.Role, cmd.Action)
	}

	satisfied, err := domain.StepUpSatisfied(cmd.Action, cmd.SessionAge)
	if err != nil {
		return nil, err
	}
	if !satisfied {
		return nil, fmt.Errorf("%w: %s requires authentication within %s", ErrStepUpRequired, cmd.Action, domain.StepUpWindow)
	}

	return &AuthorizeResult{Role: assignment.Role}, nil
}
