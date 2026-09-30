// Package auditbridge records the moderation module's administrative
// assignment facts in the audit trail (P19-T09).
//
// The bridge exists because the two modules must not know each other: the
// moderation module declares the fact it produces in its own vocabulary, the
// audit module owns the trail's vocabulary, and this adapter is the only place
// where both are visible — the same shape the identity second factor, the
// operational retry and the wallet adjustment already use.
//
// Two facts are recorded here, and they are the two halves of one capability:
// an operator became an administrator, and an operator stopped being one. Both
// name the account as the target, because the capability belongs to the account;
// the role and the reason travel as metadata, where the trail's allowlist keeps
// them codes and refuses everything else. There is no key for an address, a
// session, a secret or a free sentence, so a bootstrap cannot smuggle any of
// them into history.
//
// The acting account is the promoted account itself. A command that runs on the
// host has no operator account to attribute the act to, and the schema requires
// one: inventing an identity that never acted would be a worse record than the
// honest one, which says the installation's own account was the subject and the
// reason code says a local bootstrap did it.
package auditbridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	auditapp "github.com/AlexandreZanata/Regnovum/internal/audit/application"
	auditdomain "github.com/AlexandreZanata/Regnovum/internal/audit/domain"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
)

// The trail's vocabulary for the two facts. The action names are stable and
// greppable; the target is the account the assignment belongs to.
const (
	actionRoleGranted = "administration.role_granted"
	actionRoleRevoked = "administration.role_revoked"

	// accountTargetType is the trail target type of a capability held by an
	// account.
	accountTargetType = "account"

	// reasonRoleGranted and reasonRoleRevoked are the stable codes of the
	// transition, so a reviewer filters on the reason column without reading
	// metadata.
	reasonRoleGranted = "administrative_bootstrap"
	reasonRoleRevoked = "administrative_demotion"

	// The allowlisted metadata keys of the trail that carry the transition.
	previousStatusMetadataKey = "previous_status"
	newStatusMetadataKey      = "new_status"
	ruleMetadataKey           = "rule"

	// statusRevoked is the state a demotion leaves behind, in the same
	// vocabulary the grant uses for "the account held a revoked assignment".
	statusRevoked = "revoked"
)

// Recorder implements the moderation administrative audit port over the trail.
type Recorder struct {
	recorder auditapp.Recorder
}

var _ moderationapp.RoleAudit = (*Recorder)(nil)

// NewRecorder wires the bridge. A nil trail fails at construction: a promotion
// or a demotion that cannot be recorded must not be possible, and failing here
// is how that becomes true at composition time instead of at the first
// command.
func NewRecorder(recorder auditapp.Recorder) (*Recorder, error) {
	if recorder == nil {
		return nil, errors.New("moderation: administrative audit bridge needs the trail recorder")
	}
	return &Recorder{recorder: recorder}, nil
}

// RecordAdministrativeGrant implements moderationapp.RoleAudit.
func (r *Recorder) RecordAdministrativeGrant(ctx context.Context, event moderationapp.AdministrativeGrantEvent) error {
	return r.record(ctx, actionRoleGranted, reasonRoleGranted, event.AccountID, event.Role.String(), event.PreviousStatus, event.Rule, event.OccurredAt)
}

// RecordAdministrativeRevocation implements moderationapp.RoleAudit.
func (r *Recorder) RecordAdministrativeRevocation(ctx context.Context, event moderationapp.AdministrativeRevocationEvent) error {
	return r.record(ctx, actionRoleRevoked, reasonRoleRevoked, event.AccountID, statusRevoked, event.Role.String(), event.Rule, event.OccurredAt)
}

// record writes one fact. The instant comes from the use case, so the trail and
// the assignment row agree on when the transition happened; a zero instant is
// refused by the trail rather than replaced here, because a fact whose time
// nobody stated is not worth recording.
func (r *Recorder) record(ctx context.Context, action, reason, accountID, newStatus, previousStatus, rule string, occurredAt time.Time) error {
	if r == nil || r.recorder == nil {
		return errors.New("moderation: administrative audit bridge is not wired")
	}
	if ctx == nil {
		return errors.New("moderation: administrative audit bridge received a nil context")
	}

	_, err := r.recorder.Record(ctx, auditdomain.AuditEvent{
		// See the package comment: the local command has no operator
		// account, and the account the assignment belongs to is the only
		// honest actor available.
		Actor:      accountID,
		Action:     action,
		TargetType: accountTargetType,
		TargetID:   accountID,
		ReasonCode: reason,
		Metadata: map[string]string{
			previousStatusMetadataKey: previousStatus,
			newStatusMetadataKey:      newStatus,
			ruleMetadataKey:           rule,
		},
		OccurredAt: occurredAt.UTC(),
	})
	if err != nil {
		return fmt.Errorf("record the administrative assignment: %w", err)
	}
	return nil
}
