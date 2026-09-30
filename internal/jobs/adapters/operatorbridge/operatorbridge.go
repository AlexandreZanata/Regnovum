// Package operatorbridge answers the jobs module's operator question from the
// role assignments (P15-T06).
//
// The jobs module states the capability it needs — "may this account act on the
// queue" — and this adapter is the only place that knows where the answer comes
// from. The role vocabulary and the assignment store stay with the module that
// owns them, which is what lets the operational surface be authorized without
// either module importing the other's domain.
//
// Only the administrative role qualifies. A moderator triages content, and the
// job queue is not content: retrying a dead job runs a handler by hand, which
// is an administrative act. A revoked assignment denies exactly like one that
// never existed, so the gate cannot be used to probe who holds what.
package operatorbridge

import (
	"context"
	"errors"

	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	moderationdomain "github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

// Directory implements the jobs operator port over the role assignments.
type Directory struct {
	roles moderationapp.RoleRepository
}

var _ jobsapp.OperatorDirectory = (*Directory)(nil)

// NewDirectory wires the bridge. A nil repository fails at construction: an
// authorization port that cannot answer must not be able to say yes.
func NewDirectory(roles moderationapp.RoleRepository) (*Directory, error) {
	if roles == nil {
		return nil, errors.New("jobs: operator bridge needs the role repository")
	}
	return &Directory{roles: roles}, nil
}

// IsOperator reports whether the account holds an active administrative
// assignment.
func (d *Directory) IsOperator(ctx context.Context, accountID string) (bool, error) {
	if d == nil || d.roles == nil {
		return false, errors.New("jobs: operator bridge is not wired")
	}
	if ctx == nil {
		return false, errors.New("jobs: nil context")
	}
	assignment, err := d.roles.AssignmentFor(ctx, moderationdomain.AccountID(accountID))
	if err != nil {
		return false, err
	}
	if assignment == nil || assignment.Revoked {
		return false, nil
	}
	return assignment.Role == moderationdomain.RoleAdmin, nil
}
