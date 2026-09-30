// Package auditbridge records the identity module's second-factor facts in the
// audit trail (P16-T05).
//
// The bridge exists because the two modules must not know each other: identity
// declares the fact it produces in its own vocabulary, the audit module owns
// the trail's vocabulary, and this adapter is the only place where both are
// visible. The same shape already carries the arenas moderation, the wallet
// adjustment and the operational retries.
//
// Only two facts are recorded here, and both are administrative: an operator
// gaining a second factor, and an operator recovering access with a one-time
// code. The second is the one that matters — a recovery is the rare event a
// reviewer needs to find later — and the trail's metadata allowlist keeps it
// minimal: the outcome and the account, never a code, a secret or a session.
package auditbridge

import (
	"context"
	"errors"
	"time"

	auditapp "github.com/AlexandreZanata/Regnovum/internal/audit/application"
	auditdomain "github.com/AlexandreZanata/Regnovum/internal/audit/domain"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
)

// The trail's vocabulary for the two facts. The action names are stable and
// greppable; the target is the account the factor belongs to.
const (
	actionMFAEnrolled        = "mfa.enrolled"
	actionMFABackupCodeUsed  = "mfa.backup_code_used"
	accountTargetType        = "account"
	reasonMFAEnrolled        = "mfa_enrollment_confirmed"
	reasonMFABackupCodeUsed  = "mfa_backup_code_recovery"
	outcomeMetadataKey       = "outcome"
	outcomeMFAEnrolled       = "confirmed"
	outcomeMFABackupCodeUsed = "backup_code_used"
)

// Recorder implements the identity audit port over the trail.
type Recorder struct {
	recorder auditapp.Recorder
	clock    identityapp.Clock
}

var _ identityapp.MFAAudit = (*Recorder)(nil)

// NewRecorder wires the bridge. A nil trail or clock fails at construction: a
// recovery that cannot be recorded must not be possible, and failing here is
// how that becomes true at composition time instead of at the first recovery.
func NewRecorder(recorder auditapp.Recorder, clock identityapp.Clock) (*Recorder, error) {
	if recorder == nil {
		return nil, errors.New("identity: the second factor audit bridge needs the trail recorder")
	}
	if clock == nil {
		return nil, errors.New("identity: the second factor audit bridge needs a clock")
	}
	return &Recorder{recorder: recorder, clock: clock}, nil
}

// RecordMFAEnrolled implements identityapp.MFAAudit.
func (bridge *Recorder) RecordMFAEnrolled(ctx context.Context, accountID string, occurredAt time.Time) error {
	return bridge.record(ctx, actionMFAEnrolled, reasonMFAEnrolled, outcomeMFAEnrolled, accountID, occurredAt)
}

// RecordMFABackupCodeUsed implements identityapp.MFAAudit.
func (bridge *Recorder) RecordMFABackupCodeUsed(ctx context.Context, accountID string, occurredAt time.Time) error {
	return bridge.record(ctx, actionMFABackupCodeUsed, reasonMFABackupCodeUsed, outcomeMFABackupCodeUsed, accountID, occurredAt)
}

// record writes one fact. The instant comes from the use case, so the trail
// and the session elevation agree on when it happened; the clock is only the
// fallback for a caller that did not state one.
func (bridge *Recorder) record(ctx context.Context, action, reason, outcome, accountID string, occurredAt time.Time) error {
	if occurredAt.IsZero() {
		occurredAt = bridge.clock.Now().UTC()
	}

	_, err := bridge.recorder.Record(ctx, auditdomain.AuditEvent{
		// The actor is the account itself: a second-factor change is the
		// operator acting on their own factor, not an administrator acting on
		// somebody else's.
		Actor:      accountID,
		Action:     action,
		TargetType: accountTargetType,
		TargetID:   accountID,
		ReasonCode: reason,
		Metadata:   map[string]string{outcomeMetadataKey: outcome},
		OccurredAt: occurredAt.UTC(),
	})
	return err
}
