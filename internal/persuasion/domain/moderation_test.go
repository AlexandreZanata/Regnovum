package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

const (
	testAttributionRaw = "018f6b2a-0000-7000-8000-000000000009"
	testModeratorRaw   = "018f6b2a-0000-7000-8000-00000000000b"
	testChangeRaw      = "018f6b2a-0000-7000-8000-000000000001"
	testAccountRaw     = "018f6b2a-0000-7000-8000-000000000003"
)

func mustDecision(t *testing.T, action domain.ModerationAction, mutate func(*domain.ModerationDecision)) domain.ModerationDecision {
	t.Helper()
	actor, err := domain.ParseModeratorID(testModeratorRaw)
	if err != nil {
		t.Fatalf("ParseModeratorID: %v", err)
	}
	reason, err := domain.ParseReason("atribuição fraudulenta confirmada no caso 42")
	if err != nil {
		t.Fatalf("ParseReason: %v", err)
	}
	decision := domain.ModerationDecision{
		Action:    action,
		Actor:     actor,
		Reason:    reason,
		DecidedAt: testInstant,
	}
	if mutate != nil {
		mutate(&decision)
	}
	return decision
}

func mustAttribution(t *testing.T, status domain.AttributionStatus) domain.Attribution {
	t.Helper()
	id, err := domain.ParseAttributionID(testAttributionRaw)
	if err != nil {
		t.Fatalf("ParseAttributionID: %v", err)
	}
	changeID, err := domain.ParseChangeID(testChangeRaw)
	if err != nil {
		t.Fatalf("ParseChangeID: %v", err)
	}
	argumentID, err := domain.ParseArgumentID("018f6b2a-0000-7000-8000-00000000000a")
	if err != nil {
		t.Fatalf("ParseArgumentID: %v", err)
	}
	attributorID, err := domain.ParseAttributorID(testAccountRaw)
	if err != nil {
		t.Fatalf("ParseAttributorID: %v", err)
	}
	return domain.Attribution{
		ID:           id,
		ChangeID:     changeID,
		ArgumentID:   argumentID,
		AttributorID: attributorID,
		Status:       status,
		CreatedAt:    testInstant.Add(-time.Hour),
	}
}

func TestParseAttributionStatus(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"valid", "invalid"} {
		status, err := domain.ParseAttributionStatus(raw)
		if err != nil {
			t.Fatalf("ParseAttributionStatus(%q) error = %v", raw, err)
		}
		if status.String() != raw || !status.IsValid() {
			t.Fatalf("ParseAttributionStatus(%q) = %v", raw, status)
		}
	}

	for _, raw := range []string{"", "VALID", "maybe", "invalidate", "valid "} {
		if _, err := domain.ParseAttributionStatus(raw); !errors.Is(err, domain.ErrInvalidAttributionStatus) {
			t.Fatalf("ParseAttributionStatus(%q) error = %v, want ErrInvalidAttributionStatus", raw, err)
		}
	}
}

func TestModerationActionTargetStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		action domain.ModerationAction
		want   domain.AttributionStatus
		valid  bool
	}{
		{action: domain.ModerationActionInvalidate, want: domain.AttributionStatusInvalid, valid: true},
		{action: domain.ModerationActionRestore, want: domain.AttributionStatusValid, valid: true},
		{action: "delete", want: "", valid: false},
		{action: "", want: "", valid: false},
	}
	for _, test := range cases {
		if got := test.action.TargetStatus(); got != test.want {
			t.Errorf("%q.TargetStatus() = %q, want %q", test.action, got, test.want)
		}
		if got := test.action.IsValid(); got != test.valid {
			t.Errorf("%q.IsValid() = %v, want %v", test.action, got, test.valid)
		}
	}
}

func TestParseReason(t *testing.T) {
	t.Parallel()

	reason, err := domain.ParseReason("  atribuição fraudulenta  ")
	if err != nil {
		t.Fatalf("ParseReason() error = %v", err)
	}
	if reason.String() != "atribuição fraudulenta" {
		t.Fatalf("ParseReason() = %q, want the trimmed reason", reason.String())
	}

	cases := []struct {
		name string
		raw  string
		want error
	}{
		{name: "empty", raw: "", want: domain.ErrEmptyReason},
		{name: "blank", raw: "   \t ", want: domain.ErrEmptyReason},
		{name: "too long", raw: strings.Repeat("a", 501), want: domain.ErrReasonTooLong},
		{name: "newline", raw: "linha\noutra", want: domain.ErrInvalidReason},
		{name: "null byte", raw: "causa\x00oculta", want: domain.ErrInvalidReason},
		{name: "bidi override", raw: "fraude \u202Eemitir", want: domain.ErrInvalidReason},
		{name: "isolate", raw: "fraude \u2067emitir", want: domain.ErrInvalidReason},
		{name: "invalid utf8", raw: "fraude \xff", want: domain.ErrInvalidReason},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := domain.ParseReason(test.raw); !errors.Is(err, test.want) {
				t.Fatalf("ParseReason() error = %v, want %v", err, test.want)
			}
		})
	}

	// The bound is measured in runes, not bytes: 500 accented characters fit.
	if _, err := domain.ParseReason(strings.Repeat("ç", 500)); err != nil {
		t.Fatalf("ParseReason(500 runes) error = %v", err)
	}
}

func TestModerationDecisionValidate(t *testing.T) {
	t.Parallel()

	if err := mustDecision(t, domain.ModerationActionInvalidate, nil).Validate(); err != nil {
		t.Fatalf("valid decision error = %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*domain.ModerationDecision)
		want   error
	}{
		{name: "unknown action", mutate: func(d *domain.ModerationDecision) { d.Action = "ban" }, want: domain.ErrInvalidModerationAction},
		{name: "missing action", mutate: func(d *domain.ModerationDecision) { d.Action = "" }, want: domain.ErrInvalidModerationAction},
		{name: "missing actor", mutate: func(d *domain.ModerationDecision) { d.Actor = domain.ModeratorID{} }, want: domain.ErrEmptyModeratorID},
		{name: "missing reason", mutate: func(d *domain.ModerationDecision) { d.Reason = domain.Reason{} }, want: domain.ErrEmptyReason},
		{name: "missing instant", mutate: func(d *domain.ModerationDecision) { d.DecidedAt = time.Time{} }, want: domain.ErrInvalidInstant},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			decision := mustDecision(t, domain.ModerationActionInvalidate, test.mutate)
			if err := decision.Validate(); !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestAttributionInvalidateAndRestore(t *testing.T) {
	t.Parallel()

	attribution := mustAttribution(t, domain.AttributionStatusValid)
	if attribution.HasDecision() || !attribution.IsValid() {
		t.Fatalf("fresh attribution = %+v, want valid without decision", attribution)
	}

	invalidate := mustDecision(t, domain.ModerationActionInvalidate, nil)
	invalidated, err := attribution.Invalidate(invalidate)
	if err != nil {
		t.Fatalf("Invalidate() error = %v", err)
	}
	if invalidated.IsValid() || invalidated.Status != domain.AttributionStatusInvalid {
		t.Fatalf("invalidated status = %q, want invalid", invalidated.Status)
	}
	if !invalidated.HasDecision() {
		t.Fatal("invalidation must record the decision")
	}
	recorded, ok := invalidated.DecisionOf()
	if !ok || recorded.Action != domain.ModerationActionInvalidate || !recorded.Reason.Equals(invalidate.Reason) {
		t.Fatalf("recorded decision = %+v", recorded)
	}
	// The links and the private attributor never change.
	if !invalidated.ID.Equals(attribution.ID) || !invalidated.ChangeID.Equals(attribution.ChangeID) ||
		!invalidated.ArgumentID.Equals(attribution.ArgumentID) || !invalidated.AttributorID.Equals(attribution.AttributorID) ||
		!invalidated.CreatedAt.Equal(attribution.CreatedAt) {
		t.Fatalf("invalidation altered attribution identity: %+v", invalidated)
	}

	// Invalidating again is refused by the domain: the use case resolves it
	// as a replay before reaching this rule.
	if _, err := invalidated.Invalidate(invalidate); !errors.Is(err, domain.ErrAttributionNotModeratable) {
		t.Fatalf("second Invalidate() error = %v, want ErrAttributionNotModeratable", err)
	}

	restore := mustDecision(t, domain.ModerationActionRestore, nil)
	restored, err := invalidated.Restore(restore)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if !restored.IsValid() || restored.Status != domain.AttributionStatusValid {
		t.Fatalf("restored status = %q, want valid", restored.Status)
	}
	if recorded, ok := restored.DecisionOf(); !ok || recorded.Action != domain.ModerationActionRestore {
		t.Fatalf("restore must keep a decision record: %+v", restored)
	}
	if _, err := restored.Restore(restore); !errors.Is(err, domain.ErrAttributionNotModeratable) {
		t.Fatalf("second Restore() error = %v, want ErrAttributionNotModeratable", err)
	}

	// A full cycle is repeatable: restore → invalidate → restore.
	again, err := restored.Invalidate(mustDecision(t, domain.ModerationActionInvalidate, func(d *domain.ModerationDecision) {
		d.DecidedAt = testInstant.Add(2 * time.Hour)
	}))
	if err != nil {
		t.Fatalf("second cycle Invalidate() error = %v", err)
	}
	if _, err := again.Restore(mustDecision(t, domain.ModerationActionRestore, func(d *domain.ModerationDecision) {
		d.DecidedAt = testInstant.Add(3 * time.Hour)
	})); err != nil {
		t.Fatalf("second cycle Restore() error = %v", err)
	}
}

func TestAttributionRejectsMismatchedAction(t *testing.T) {
	t.Parallel()

	valid := mustAttribution(t, domain.AttributionStatusValid)
	invalid := mustAttribution(t, domain.AttributionStatusInvalid)

	if _, err := valid.Invalidate(mustDecision(t, domain.ModerationActionRestore, nil)); !errors.Is(err, domain.ErrInvalidModerationAction) {
		t.Fatalf("Invalidate(restore) error = %v, want ErrInvalidModerationAction", err)
	}
	if _, err := invalid.Restore(mustDecision(t, domain.ModerationActionInvalidate, nil)); !errors.Is(err, domain.ErrInvalidModerationAction) {
		t.Fatalf("Restore(invalidate) error = %v, want ErrInvalidModerationAction", err)
	}

	// An incomplete decision never reaches the transition.
	incomplete := mustDecision(t, domain.ModerationActionInvalidate, func(d *domain.ModerationDecision) { d.Reason = domain.Reason{} })
	if _, err := valid.Invalidate(incomplete); !errors.Is(err, domain.ErrEmptyReason) {
		t.Fatalf("Invalidate(incomplete) error = %v, want ErrEmptyReason", err)
	}
}
