package auditbridge_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	auditapp "github.com/AlexandreZanata/Regnovum/internal/audit/application"
	auditdomain "github.com/AlexandreZanata/Regnovum/internal/audit/domain"
	"github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/auditbridge"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

// trailStub behaves like the trail's own recorder: it validates the event
// before accepting it, which is what makes these tests measure the agreement
// between the two modules instead of the stub's tolerance.
type trailStub struct {
	events []auditdomain.AuditEvent
	err    error
}

func (s *trailStub) Record(_ context.Context, event auditdomain.AuditEvent) (*auditapp.RecordResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	s.events = append(s.events, event)
	return &auditapp.RecordResult{ID: "audit-1"}, nil
}

// retryFact is the fact the retry use case produces for one operator action.
func retryFact() jobsapp.AdminFact {
	return jobsapp.AdminFact{
		Actor:      "0191f0e0-0000-7000-8000-0000000000aa",
		Action:     domain.ActionRetryDeadJob,
		TargetID:   "0191f0e0-0000-7000-8000-0000000000bb",
		ReasonCode: domain.ReasonCodeDeadJobRetry,
		Reason:     "provider outage resolved",
		Metadata: map[string]string{
			"previous_status": string(domain.StateDead),
			"new_status":      string(domain.StateQueued),
			"reference":       "0191f0e0-0000-7000-8000-0000000000bb",
		},
		OccurredAt: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
	}
}

// TestRecordTranslatesTheOperationalFact is the two-module agreement: the fact
// jobs produces is a fact the trail accepts, unchanged in meaning.
func TestRecordTranslatesTheOperationalFact(t *testing.T) {
	trail := &trailStub{}
	recorder, err := auditbridge.NewRecorder(trail)
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	fact := retryFact()
	if err := recorder.Record(context.Background(), fact); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if len(trail.events) != 1 {
		t.Fatalf("events = %d, want one", len(trail.events))
	}
	event := trail.events[0]
	if event.Actor != fact.Actor || event.Action != domain.ActionRetryDeadJob {
		t.Errorf("event = %+v, want the operator and the action", event)
	}
	if event.TargetType != "operation" || event.TargetID != fact.TargetID {
		t.Errorf("target = %s/%s, want the operation the fact is about", event.TargetType, event.TargetID)
	}
	// The column carries the module's stable code and never the sentence.
	if event.ReasonCode != domain.ReasonCodeDeadJobRetry {
		t.Errorf("reason code = %q, want the stable code", event.ReasonCode)
	}
	if strings.Contains(event.ReasonCode, " ") {
		t.Errorf("reason code = %q, want no prose in the column", event.ReasonCode)
	}
	// The sentence survives as allowlisted metadata, so the operator stays
	// accountable for what they claimed.
	if event.Metadata["reason"] != fact.Reason {
		t.Errorf("metadata reason = %q, want the operator's justification", event.Metadata["reason"])
	}
	for key, value := range event.Metadata {
		if !auditdomain.MetadataAllowlist[key] {
			t.Errorf("metadata key %q is not allowlisted by the trail", key)
		}
		if value != fact.Metadata[key] && key != "reason" {
			t.Errorf("metadata %q = %q, want it forwarded unchanged", key, value)
		}
	}
	if event.Metadata["previous_status"] != string(domain.StateDead) || event.Metadata["new_status"] != string(domain.StateQueued) {
		t.Errorf("metadata = %v, want the recorded transition", event.Metadata)
	}
	// Forgetting to state a reason cannot invent one.
	fact.Reason = ""
	if err := recorder.Record(context.Background(), fact); err != nil {
		t.Fatalf("Record() without a reason error = %v", err)
	}
	if _, present := trail.events[1].Metadata["reason"]; present {
		t.Error("an empty reason produced a metadata key")
	}
}

// TestRecordDoesNotMutateTheCallersFact keeps the bridge from being a channel
// through which the trail's vocabulary leaks back into the module.
func TestRecordDoesNotMutateTheCallersFact(t *testing.T) {
	trail := &trailStub{}
	recorder, err := auditbridge.NewRecorder(trail)
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	fact := retryFact()
	before := len(fact.Metadata)
	if err := recorder.Record(context.Background(), fact); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if len(fact.Metadata) != before {
		t.Errorf("caller metadata grew to %d keys, want %d", len(fact.Metadata), before)
	}
	if _, present := fact.Metadata["reason"]; present {
		t.Error("the bridge wrote the reason into the caller's map")
	}
}

// TestRecordRefusesAnActionOutsideItsScope stops the bridge from becoming a
// general-purpose writer of arbitrary audit actions.
func TestRecordRefusesAnActionOutsideItsScope(t *testing.T) {
	trail := &trailStub{}
	recorder, err := auditbridge.NewRecorder(trail)
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	fact := retryFact()
	fact.Action = "accounts.ban"
	if err := recorder.Record(context.Background(), fact); err == nil {
		t.Fatal("Record() error = nil, want a foreign action refused")
	}
	if len(trail.events) != 0 {
		t.Errorf("events = %d, want nothing recorded", len(trail.events))
	}
}

// TestRecordPropagatesTheTrailFailure is what makes the operator's action
// atomic: the failure has to reach the caller's transaction.
func TestRecordPropagatesTheTrailFailure(t *testing.T) {
	failure := errors.New("audit: unavailable")
	trail := &trailStub{err: failure}
	recorder, err := auditbridge.NewRecorder(trail)
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	if err := recorder.Record(context.Background(), retryFact()); !errors.Is(err, failure) {
		t.Errorf("Record() error = %v, want the trail failure", err)
	}
}

// TestRecorderRequiresTheTrail: an operational action that cannot be recorded
// must not be possible, so construction fails and an unwired bridge refuses.
func TestRecorderRequiresTheTrail(t *testing.T) {
	if _, err := auditbridge.NewRecorder(nil); err == nil {
		t.Fatal("NewRecorder(nil) error = nil, want a failure")
	}
	var unwired *auditbridge.Recorder
	if err := unwired.Record(context.Background(), retryFact()); err == nil {
		t.Error("Record() on an unwired bridge error = nil, want a failure")
	}
}
