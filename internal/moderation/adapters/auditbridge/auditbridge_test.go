package auditbridge_test

// The bridge that records the administrative assignment facts in the trail
// (P19-T09). The fake recorder validates with the audit domain's own rules,
// which is the point: the trail's allowlist is what decides whether a fact is
// complete and minimal, so a bridge that invented a key or left a value empty
// fails here exactly as it would fail against PostgreSQL.

import (
	"context"
	"errors"
	"testing"
	"time"

	auditapp "github.com/AlexandreZanata/Regnovum/internal/audit/application"
	auditdomain "github.com/AlexandreZanata/Regnovum/internal/audit/domain"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/auditbridge"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

// validatingRecorder stores the events the audit domain accepts and surfaces
// the refusals, which is what the real adapter does before the insert.
type validatingRecorder struct {
	events []auditdomain.AuditEvent
	err    error
}

func (r *validatingRecorder) Record(_ context.Context, event auditdomain.AuditEvent) (*auditapp.RecordResult, error) {
	if r.err != nil {
		return nil, r.err
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	r.events = append(r.events, event)
	return &auditapp.RecordResult{ID: "0192f3a0-0000-7000-8000-00000000evt1"}, nil
}

const bridgeAccount = "0192f3a0-0000-7000-8000-00000000adm1"

func TestRecordAdministrativeGrantWritesTheTransition(t *testing.T) {
	recorder := &validatingRecorder{}
	bridge, err := auditbridge.NewRecorder(recorder)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	instant := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	if err := bridge.RecordAdministrativeGrant(context.Background(), moderationapp.AdministrativeGrantEvent{
		AccountID:      bridgeAccount,
		Role:           domain.RoleAdmin,
		PreviousStatus: moderationapp.StatusNone,
		Rule:           moderationapp.RuleFirstAdministrator,
		OccurredAt:     instant,
	}); err != nil {
		t.Fatalf("RecordAdministrativeGrant: %v", err)
	}

	if len(recorder.events) != 1 {
		t.Fatalf("events = %d, want one", len(recorder.events))
	}
	event := recorder.events[0]
	if event.Action != "administration.role_granted" {
		t.Fatalf("Action = %q, want the greppable action name", event.Action)
	}
	if event.TargetType != "account" || event.TargetID != bridgeAccount {
		t.Fatalf("target = %s %s, want the account the assignment belongs to", event.TargetType, event.TargetID)
	}
	if event.Actor != bridgeAccount {
		t.Fatalf("Actor = %q, want the promoted account: the local command has no operator account", event.Actor)
	}
	if event.ReasonCode != "administrative_bootstrap" {
		t.Fatalf("ReasonCode = %q, want a stable code", event.ReasonCode)
	}
	if event.Metadata["previous_status"] != moderationapp.StatusNone {
		t.Fatalf("previous_status = %q, want %q", event.Metadata["previous_status"], moderationapp.StatusNone)
	}
	if event.Metadata["new_status"] != domain.RoleAdmin.String() {
		t.Fatalf("new_status = %q, want %q", event.Metadata["new_status"], domain.RoleAdmin)
	}
	if event.Metadata["rule"] != moderationapp.RuleFirstAdministrator {
		t.Fatalf("rule = %q, want %q", event.Metadata["rule"], moderationapp.RuleFirstAdministrator)
	}
	if !event.OccurredAt.Equal(instant) {
		t.Fatalf("OccurredAt = %s, want the instant the write used", event.OccurredAt)
	}
}

func TestRecordAdministrativeRevocationWritesTheTransition(t *testing.T) {
	recorder := &validatingRecorder{}
	bridge, err := auditbridge.NewRecorder(recorder)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	instant := time.Date(2026, 9, 22, 13, 0, 0, 0, time.UTC)
	if err := bridge.RecordAdministrativeRevocation(context.Background(), moderationapp.AdministrativeRevocationEvent{
		AccountID:  bridgeAccount,
		Role:       domain.RoleAdmin,
		Rule:       moderationapp.RuleLocalDemotion,
		OccurredAt: instant,
	}); err != nil {
		t.Fatalf("RecordAdministrativeRevocation: %v", err)
	}

	event := recorder.events[0]
	if event.Action != "administration.role_revoked" {
		t.Fatalf("Action = %q, want the greppable action name", event.Action)
	}
	if event.Metadata["previous_status"] != domain.RoleAdmin.String() {
		t.Fatalf("previous_status = %q, want the role that was held", event.Metadata["previous_status"])
	}
	if event.Metadata["new_status"] != "revoked" {
		t.Fatalf("new_status = %q, want %q", event.Metadata["new_status"], "revoked")
	}
	if event.ReasonCode != "administrative_demotion" {
		t.Fatalf("ReasonCode = %q, want a stable code", event.ReasonCode)
	}
}

func TestTheBridgeNeverCarriesAnAddress(t *testing.T) {
	recorder := &validatingRecorder{}
	bridge, err := auditbridge.NewRecorder(recorder)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	// The event types have no field an address could travel in, and the
	// trail's allowlist has no key for one. This test states the property
	// the types enforce: a promotion is recorded about an identifier, never
	// about the person's address.
	if err := bridge.RecordAdministrativeGrant(context.Background(), moderationapp.AdministrativeGrantEvent{
		AccountID:      bridgeAccount,
		Role:           domain.RoleAdmin,
		PreviousStatus: moderationapp.StatusRevoked,
		Rule:           moderationapp.RuleFirstAdministrator,
		OccurredAt:     time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("RecordAdministrativeGrant: %v", err)
	}

	for key, value := range recorder.events[0].Metadata {
		if !auditdomain.MetadataAllowlist[key] {
			t.Fatalf("metadata key %q is not allowlisted by the trail", key)
		}
		if value == "" {
			t.Fatalf("metadata key %q is empty, which the trail refuses", key)
		}
	}
}

func TestTheBridgeSurfacesTheTrailRefusal(t *testing.T) {
	failure := errors.New("the trail is unavailable")
	bridge, err := auditbridge.NewRecorder(&validatingRecorder{err: failure})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	err = bridge.RecordAdministrativeGrant(context.Background(), moderationapp.AdministrativeGrantEvent{
		AccountID:      bridgeAccount,
		Role:           domain.RoleAdmin,
		PreviousStatus: moderationapp.StatusNone,
		Rule:           moderationapp.RuleFirstAdministrator,
		OccurredAt:     time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want the trail failure", err)
	}
}

func TestNewRecorderFailsClosedWithoutTheTrail(t *testing.T) {
	if _, err := auditbridge.NewRecorder(nil); err == nil {
		t.Fatal("expected a nil trail to be refused: an unrecorded promotion is not a promotion")
	}
}
