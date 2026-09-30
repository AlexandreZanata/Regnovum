// Package auditbridge records the jobs module's administrative facts in the
// audit trail (P15-T06).
//
// The bridge exists because the two modules must not know each other: jobs
// declares the fact it produces in its own vocabulary, the audit module owns
// the trail's vocabulary, and this adapter is the only place where both are
// visible. The codebase already uses the same shape where one module needs
// another's capability (the arenas pass consumption, the notifications outbox).
//
// The target type of an operational retry is `operation`, which is the type
// the trail reserves for work the platform performed or re-performed. The
// trail's metadata allowlist is what keeps the fact complete and minimal: the
// transition, the reference and the operator's reason are allowlisted keys,
// and there is no key for a payload or a secret, so a retry cannot smuggle the
// job's arguments into the trail.
//
// The operator's justification is the one piece of prose the fact carries, and
// it travels as metadata because the trail's reason column is a stable code:
// the column stays greppable, the sentence stays attributable.
package auditbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	auditapp "github.com/AlexandreZanata/Regnovum/internal/audit/application"
	auditdomain "github.com/AlexandreZanata/Regnovum/internal/audit/domain"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

// operationTargetType is the trail target type of an operational action.
const operationTargetType = "operation"

// reasonMetadataKey is the allowlisted trail key that carries an operator's
// stated justification. The trail's reason column is a stable code and never
// prose, so the prose has to travel as metadata — the same shape the wallet
// adjustment, the arena moderation and the attribution moderation already use.
const reasonMetadataKey = "reason"

// Recorder implements the jobs administrative audit port over the audit trail.
type Recorder struct {
	recorder auditapp.Recorder
}

var _ jobsapp.AdminAudit = (*Recorder)(nil)

// NewRecorder wires the bridge. A nil trail fails at construction: an
// operational action that cannot be recorded must not be possible, and failing
// here is how that becomes true at composition time instead of at the first
// retry.
func NewRecorder(recorder auditapp.Recorder) (*Recorder, error) {
	if recorder == nil {
		return nil, errors.New("jobs: audit bridge needs the trail recorder")
	}
	return &Recorder{recorder: recorder}, nil
}

// Record writes one administrative fact.
//
// The fact is validated by the audit domain before it is stored, so an
// unallowlisted metadata key or an empty actor fails the caller's transaction —
// which is what keeps the operator's action and its record atomic.
func (r *Recorder) Record(ctx context.Context, fact jobsapp.AdminFact) error {
	if r == nil || r.recorder == nil {
		return errors.New("jobs: audit bridge is not wired")
	}
	if ctx == nil {
		return errors.New("jobs: nil context")
	}
	if !isOperationalAction(fact.Action) {
		return fmt.Errorf("jobs: action %q is not an operational action", fact.Action)
	}
	if _, err := r.recorder.Record(ctx, auditdomain.AuditEvent{
		Actor:      fact.Actor,
		Action:     fact.Action,
		TargetType: operationTargetType,
		TargetID:   fact.TargetID,
		ReasonCode: fact.ReasonCode,
		Metadata:   metadataWithReason(fact),
		OccurredAt: fact.OccurredAt,
	}); err != nil {
		return fmt.Errorf("jobs: record audit fact: %w", err)
	}
	return nil
}

// metadataWithReason composes the trail's metadata from the fact. The
// destination map is always a fresh one, so the caller's map is never mutated
// and the fact cannot gain a key by being recorded.
//
// An unallowlisted key the caller added is not dropped here: the trail's domain
// validates the object and rejects it, which fails the operator's transaction
// and rolls the retry back with it. Failing closed is the only acceptable
// outcome — recording a partial fact would report a transition the trail
// cannot fully explain.
func metadataWithReason(fact jobsapp.AdminFact) map[string]string {
	metadata := make(map[string]string, len(fact.Metadata)+1)
	for key, value := range fact.Metadata {
		metadata[key] = value
	}
	if reason := strings.TrimSpace(fact.Reason); reason != "" {
		metadata[reasonMetadataKey] = fact.Reason
	}
	return metadata
}

// isOperationalAction keeps the bridge from becoming a general-purpose writer
// of arbitrary audit actions: it records the actions of this module, and a new
// one is an explicit edit here.
func isOperationalAction(action string) bool {
	return action == domain.ActionRetryDeadJob
}
