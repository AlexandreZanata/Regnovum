package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/audit/domain"
)

func validAuditEvent() domain.AuditEvent {
	return domain.AuditEvent{
		Actor:      "018f6b2a-0000-7000-8000-000000000001",
		Action:     "moderation.decide",
		TargetType: "case",
		TargetID:   "018f6b2a-0000-7000-8000-000000000002",
		ReasonCode: "MOD-3:spam",
		Metadata: map[string]string{
			"case_id": "018f6b2a-0000-7000-8000-000000000002",
			"rule":    "MOD-3:spam",
		},
		OccurredAt: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
	}
}

func TestAuditEventValidation(t *testing.T) {
	t.Parallel()

	if err := validAuditEvent().Validate(); err != nil {
		t.Fatalf("valid event: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*domain.AuditEvent)
	}{
		{name: "empty actor", mutate: func(e *domain.AuditEvent) { e.Actor = "   " }},
		{name: "action without namespace", mutate: func(e *domain.AuditEvent) { e.Action = "decide" }},
		{name: "action uppercase", mutate: func(e *domain.AuditEvent) { e.Action = "Moderation.Decide" }},
		{name: "action double dot", mutate: func(e *domain.AuditEvent) { e.Action = "a.b.c" }},
		{name: "unknown target type", mutate: func(e *domain.AuditEvent) { e.TargetType = "comment" }},
		{name: "blank target id", mutate: func(e *domain.AuditEvent) { e.TargetID = "" }},
		{name: "blank reason code", mutate: func(e *domain.AuditEvent) { e.ReasonCode = "  " }},
		{name: "zero instant", mutate: func(e *domain.AuditEvent) { e.OccurredAt = time.Time{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			event := validAuditEvent()
			tc.mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatal("incoherent event must fail validation")
			}
		})
	}
}

func TestAuditMetadataAllowlist(t *testing.T) {
	t.Parallel()

	for key := range domain.MetadataAllowlist {
		event := validAuditEvent()
		event.Metadata = map[string]string{key: "probe-value"}
		if err := event.Validate(); err != nil {
			t.Fatalf("allowlisted key %q rejected: %v", key, err)
		}
	}

	for _, key := range []string{"email", "token", "session", "password", "payload", "context", "justification", "ip", "fingerprint", "prompt"} {
		event := validAuditEvent()
		event.Metadata = map[string]string{key: "probe-value"}
		if err := event.Validate(); err == nil {
			t.Fatalf("forbidden metadata key %q accepted", key)
		}
	}

	oversized := validAuditEvent()
	oversized.Metadata = map[string]string{"rule": strings.Repeat("r", 501)}
	if err := oversized.Validate(); err == nil {
		t.Fatal("oversized metadata value must fail")
	}
}
