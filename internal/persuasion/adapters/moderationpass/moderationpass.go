// Package moderationpass bridges the persuasion moderator-only reads to the
// moderation boundary (P49-T04): the persuasion module depends on its own
// consumer-oriented port, and this adapter answers it from the moderation
// role store. It is composed at bootstrap and never imported by persuasion
// domain or application code.
//
// The bridge grants nothing: an actor without an active, valid assignment is
// denied with the persuasion vocabulary, so the restricted surface keeps
// answering the same 403 to every unauthorized caller. Competence for cases,
// claims and decisions stays in the moderation matrix composed by P49-T07;
// this bridge only answers "may this account read attribution signals".
package moderationpass

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	persuasionapp "github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	persuasiondomain "github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

// Bridge implements the persuasion ModerationAuthorizer port over the
// moderation role store.
type Bridge struct {
	roles application.RoleRepository
}

var _ persuasionapp.ModerationAuthorizer = (*Bridge)(nil)

// New creates the bridge over the moderation role store.
func New(roles application.RoleRepository) *Bridge {
	return &Bridge{roles: roles}
}

// EnsureModerator denies every actor without an active, valid assignment:
// unknown accounts, revoked assignments and stored roles the domain no
// longer recognizes all fail closed with the persuasion denial, so the
// caller never learns which of the three applied.
func (b *Bridge) EnsureModerator(ctx context.Context, actor persuasiondomain.ModeratorID) error {
	assignment, err := b.roles.AssignmentFor(ctx, domain.AccountID(actor.String()))
	if err != nil {
		return err
	}
	if assignment == nil || assignment.Revoked || !assignment.Role.IsValid() {
		return persuasionapp.ErrNotAuthorized
	}
	return nil
}
