package postgres

import (
	"context"
	"fmt"
	"strings"

	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	auditdomain "github.com/AlexandreZanata/Regnovum/internal/audit/domain"
	persuasionapp "github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	profilesapp "github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
)

var (
	_ walletapp.AdminAuditRecorder           = (*Repository)(nil)
	_ arenasapp.ModerationAuditRecorder      = (*Repository)(nil)
	_ persuasionapp.AttributionAuditRecorder = (*Repository)(nil)
	_ profilesapp.DeletionAuditRecorder      = (*Repository)(nil)
)

// RecordAccountDeletion implements the profiles deletion audit port: one
// workflow transition becomes one trail fact, idempotent by account and
// action. The cancel reason stays in the restricted deletion record; the
// trail carries the stable transition only.
func (r *Repository) RecordAccountDeletion(ctx context.Context, event profilesapp.DeletionAuditEvent) error {
	_, err := r.Record(ctx, auditdomain.AuditEvent{
		Actor:      event.AccountID,
		Action:     event.Action,
		TargetType: "account",
		TargetID:   event.AccountID,
		ReasonCode: event.ReasonCode,
		Metadata: map[string]string{
			"target_account_id": event.AccountID,
			"new_status":        strings.TrimPrefix(event.Action, "account.deletion_"),
		},
		IdempotencyKey: fmt.Sprintf("account-deletion:%s:%s", event.AccountID, event.Action),
		OccurredAt:     event.OccurredAt,
	})
	return err
}

// RecordAdminAdjustment implements the wallet audit port: one adjustment
// becomes one trail fact, idempotent by the ledger idempotency key. The
// admin prose travels as allowlisted metadata, never as a code.
func (r *Repository) RecordAdminAdjustment(ctx context.Context, event walletapp.AdminAdjustmentEvent) error {
	_, err := r.Record(ctx, auditdomain.AuditEvent{
		Actor:      event.ActorAccountID.String(),
		Action:     "wallet.adjust",
		TargetType: "operation",
		TargetID:   event.OperationID,
		ReasonCode: "admin_adjustment",
		Metadata: map[string]string{
			"operation_id":    event.OperationID,
			"reference":       event.Reference.String(),
			"idempotency_key": event.IdempotencyKey.String(),
			"replayed":        boolString(event.Replayed),
			"reason":          event.Reason.String(),
		},
		IdempotencyKey: event.IdempotencyKey.String(),
		OccurredAt:     event.OccurredAt,
	})
	return err
}

// RecordArenaModeration implements the arenas audit port: one moderation
// becomes one trail fact, idempotent by arena and action. The moderator
// prose travels as allowlisted metadata under a stable reason code.
func (r *Repository) RecordArenaModeration(ctx context.Context, event arenasapp.ModerationEvent) error {
	action := "arena." + strings.ToLower(string(event.Action))
	_, err := r.Record(ctx, auditdomain.AuditEvent{
		Actor:      event.ActorAccountID.String(),
		Action:     action,
		TargetType: "arena",
		TargetID:   event.ArenaID.String(),
		ReasonCode: "arena_moderation",
		Metadata: map[string]string{
			"arena_id": event.ArenaID.String(),
			"replayed": boolString(event.Replayed),
			"reason":   event.Reason.String(),
		},
		IdempotencyKey: fmt.Sprintf("arena:%s:%s", event.ArenaID, event.Action),
		OccurredAt:     event.OccurredAt,
	})
	return err
}

// RecordAttributionModeration implements the persuasion audit port: one
// applied decision becomes one trail fact. Only applied decisions are
// reported (replays produce no second event), and the write joins the
// caller transaction when there is one, so the decision and its audit
// record commit or roll back together. The attributor identity is private
// by default and never crosses this port.
func (r *Repository) RecordAttributionModeration(ctx context.Context, event persuasionapp.AttributionModerationEvent) error {
	action := "attribution." + strings.ToLower(string(event.Action))
	_, err := r.Record(ctx, auditdomain.AuditEvent{
		Actor:      event.ActorAccountID.String(),
		Action:     action,
		TargetType: "attribution",
		TargetID:   event.AttributionID.String(),
		ReasonCode: "attribution_moderation",
		Metadata: map[string]string{
			"attribution_id": event.AttributionID.String(),
			"argument_id":    event.ArgumentID.String(),
			"reason":         event.Reason.String(),
		},
		OccurredAt: event.OccurredAt,
	})
	return err
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
