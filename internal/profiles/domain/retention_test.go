package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

var retentionTerminal = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func retentionSchedule(t *testing.T, class domain.RetentionClass) domain.RetentionSchedule {
	t.Helper()
	schedule, known := domain.RetentionScheduleFor(class)
	if !known {
		t.Fatalf("RetentionScheduleFor(%q) reported no schedule", class)
	}
	return schedule
}

// TestRetentionVocabularyIsClosed pins the class and action vocabularies
// against the strings the ledger and the migration store, so a rename cannot
// silently split policy from storage.
func TestRetentionVocabularyIsClosed(t *testing.T) {
	classes := map[domain.RetentionClass]string{
		domain.RetentionClassTokens:          "tokens",
		domain.RetentionClassSessions:        "sessions",
		domain.RetentionClassReferentialLogs: "referential_logs",
		domain.RetentionClassExports:         "exports",
		domain.RetentionClassAbuseSignals:    "abuse_signals",
		domain.RetentionClassBilling:         "billing",
	}
	for class, want := range classes {
		if string(class) != want {
			t.Errorf("class %q rendered as %q, want %q", class, string(class), want)
		}
		parsed, known := domain.ParseRetentionClass(want)
		if !known || parsed != class {
			t.Errorf("ParseRetentionClass(%q) = (%q, %v), want (%q, true)", want, parsed, known, class)
		}
	}

	if len(domain.RetentionSchedules()) != len(classes) {
		t.Fatalf("policy has %d classes, want %d", len(domain.RetentionSchedules()), len(classes))
	}

	for _, unknown := range []string{"", "TOKENS", "tokens ", " token", "log", "audit_events", "wallet"} {
		if _, known := domain.ParseRetentionClass(unknown); known {
			t.Errorf("ParseRetentionClass(%q) accepted an unknown class", unknown)
		}
	}

	if string(domain.RetentionActionPurge) != "purge" ||
		string(domain.RetentionActionAnonymize) != "anonymize" ||
		string(domain.RetentionActionRetain) != "retain" {
		t.Fatalf("action vocabulary drifted: %q/%q/%q",
			domain.RetentionActionPurge, domain.RetentionActionAnonymize, domain.RetentionActionRetain)
	}
}

// TestRetentionPolicyAssignsTheDeclaredAction proves each governed class has
// exactly the action the policy publishes, in a deterministic order.
func TestRetentionPolicyAssignsTheDeclaredAction(t *testing.T) {
	wantOrder := []domain.RetentionClass{
		domain.RetentionClassTokens,
		domain.RetentionClassSessions,
		domain.RetentionClassReferentialLogs,
		domain.RetentionClassExports,
		domain.RetentionClassAbuseSignals,
		domain.RetentionClassBilling,
	}
	wantActions := map[domain.RetentionClass]domain.RetentionAction{
		domain.RetentionClassTokens:          domain.RetentionActionPurge,
		domain.RetentionClassSessions:        domain.RetentionActionPurge,
		domain.RetentionClassReferentialLogs: domain.RetentionActionRetain,
		domain.RetentionClassExports:         domain.RetentionActionPurge,
		domain.RetentionClassAbuseSignals:    domain.RetentionActionAnonymize,
		domain.RetentionClassBilling:         domain.RetentionActionRetain,
	}

	schedules := domain.RetentionSchedules()
	if len(schedules) != len(wantOrder) {
		t.Fatalf("policy has %d classes, want %d", len(schedules), len(wantOrder))
	}
	for i, schedule := range schedules {
		if schedule.Class != wantOrder[i] {
			t.Fatalf("policy[%d].Class = %q, want %q", i, schedule.Class, wantOrder[i])
		}
		if schedule.Action != wantActions[schedule.Class] {
			t.Errorf("%s action = %q, want %q", schedule.Class, schedule.Action, wantActions[schedule.Class])
		}
		if err := schedule.IsValid(); err != nil {
			t.Errorf("%s schedule is invalid: %v", schedule.Class, err)
		}
		if schedule.ReasonCode == "" {
			t.Errorf("%s schedule has no reason code", schedule.Class)
		}
	}

	// The retained classes have no boundary; the others must have one.
	if !retentionSchedule(t, domain.RetentionClassReferentialLogs).Indefinite ||
		!retentionSchedule(t, domain.RetentionClassBilling).Indefinite {
		t.Fatal("retained classes must be marked indefinite")
	}
	for _, class := range []domain.RetentionClass{
		domain.RetentionClassTokens, domain.RetentionClassSessions,
		domain.RetentionClassExports, domain.RetentionClassAbuseSignals,
	} {
		if retentionSchedule(t, class).HasCutoff() != true {
			t.Errorf("%s must have a cutoff", class)
		}
	}
}

// TestRetentionSchedulesAreCopies proves the enforced policy cannot be
// mutated by a caller that received it.
func TestRetentionSchedulesAreCopies(t *testing.T) {
	first := domain.RetentionSchedules()
	first[0] = domain.RetentionSchedule{}

	second := domain.RetentionSchedules()
	if second[0].Class != domain.RetentionClassTokens || second[0].Action != domain.RetentionActionPurge {
		t.Fatalf("policy was mutated through the returned slice: %+v", second[0])
	}
}

// TestRetentionWindowsArePinned fixes the published policy decision: how long
// after the terminal instant each class acts. Changing a constant is a policy
// change and must be a conscious commit.
func TestRetentionWindowsArePinned(t *testing.T) {
	cases := []struct {
		class  domain.RetentionClass
		window time.Duration
	}{
		{domain.RetentionClassTokens, 30 * 24 * time.Hour},
		{domain.RetentionClassSessions, 30 * 24 * time.Hour},
		{domain.RetentionClassExports, domain.ExportTTL},
		{domain.RetentionClassAbuseSignals, 7 * 24 * time.Hour},
	}
	for _, testCase := range cases {
		if got := retentionSchedule(t, testCase.class).Window; got != testCase.window {
			t.Errorf("%s window = %s, want %s", testCase.class, got, testCase.window)
		}
	}
	if domain.RetentionExportsWindow != domain.ExportTTL {
		t.Errorf("exports window = %s, want the export lifetime %s", domain.RetentionExportsWindow, domain.ExportTTL)
	}
	if domain.RetentionAbuseSignalsWindow >= domain.RetentionSessionsWindow {
		t.Error("prevention references must be anonymized before the session row is purged")
	}
}

// TestRetentionDueBoundary proves the window rule at its exact edges: the
// boundary instant is due, one second earlier is not, and a record that is
// not terminal yet is never due.
func TestRetentionDueBoundary(t *testing.T) {
	schedule := retentionSchedule(t, domain.RetentionClassTokens)

	dueAt, hasDue := schedule.DueAt(retentionTerminal)
	if !hasDue {
		t.Fatal("tokens class must have a due instant")
	}
	if want := retentionTerminal.Add(30 * 24 * time.Hour); !dueAt.Equal(want) {
		t.Fatalf("DueAt = %s, want %s", dueAt, want)
	}

	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"one second before the window", dueAt.Add(-time.Second), false},
		{"exactly at the boundary", dueAt, true},
		{"one second after the boundary", dueAt.Add(time.Second), true},
		{"far past the boundary", dueAt.Add(365 * 24 * time.Hour), true},
	}
	for _, testCase := range cases {
		if got := schedule.Due(retentionTerminal, testCase.now); got != testCase.want {
			t.Errorf("%s: Due(%s) = %v, want %v", testCase.name, testCase.now, got, testCase.want)
		}
	}

	// The comparison is done in UTC: a terminal instant in another zone
	// cannot shift the boundary.
	offsetTerminal := retentionTerminal.In(time.FixedZone("UTC-3", -3*60*60))
	if !schedule.Due(offsetTerminal, dueAt) {
		t.Error("a terminal instant in another zone must be due at the same absolute boundary")
	}

	// A refused class never yields a boundary nor a due instant.
	if _, hasDue := retentionSchedule(t, domain.RetentionClassBilling).DueAt(retentionTerminal); hasDue {
		t.Error("a retained class must not have a due instant")
	}
	if retentionSchedule(t, domain.RetentionClassBilling).Due(retentionTerminal, dueAt.Add(100*365*24*time.Hour)) {
		t.Error("a retained class must never be due")
	}
}

// TestRetentionCutoffIsInclusive proves the boundary a run applies: records
// terminal at or before the cutoff are inside the window, and the cutoff is
// exactly now minus the class window.
func TestRetentionCutoffIsInclusive(t *testing.T) {
	schedule := retentionSchedule(t, domain.RetentionClassAbuseSignals)
	now := retentionTerminal.Add(10 * 24 * time.Hour)

	cutoff, hasCutoff := schedule.Cutoff(now)
	if !hasCutoff {
		t.Fatal("abuse signals must have a cutoff")
	}
	if want := now.Add(-7 * 24 * time.Hour); !cutoff.Equal(want) {
		t.Fatalf("Cutoff = %s, want %s", cutoff, want)
	}

	// A record terminal exactly at the cutoff is inside; one second later is
	// not.
	if !schedule.Due(cutoff, now) {
		t.Error("a record terminal exactly at the cutoff must be due")
	}
	if schedule.Due(cutoff.Add(time.Second), now) {
		t.Error("a record terminal after the cutoff must not be due")
	}

	if _, hasCutoff := retentionSchedule(t, domain.RetentionClassReferentialLogs).Cutoff(now); hasCutoff {
		t.Error("a retained class must not yield a cutoff")
	}
}

// TestRetentionScheduleValidationRefusesIncoherentPolicies proves a schedule
// that cannot be enforced exactly is refused instead of being applied
// approximately.
func TestRetentionScheduleValidationRefusesIncoherentPolicies(t *testing.T) {
	valid := retentionSchedule(t, domain.RetentionClassTokens)

	cases := []struct {
		name     string
		schedule domain.RetentionSchedule
		wantErr  error
	}{
		{
			name:     "unknown class",
			schedule: domain.RetentionSchedule{Class: "wallet", Action: domain.RetentionActionPurge, ReasonCode: "x"},
			wantErr:  domain.ErrUnknownRetentionClass,
		},
		{
			name:     "unknown action",
			schedule: domain.RetentionSchedule{Class: domain.RetentionClassTokens, Action: "forget", ReasonCode: "x"},
			wantErr:  domain.ErrInvalidRetentionSchedule,
		},
		{
			name:     "negative window",
			schedule: domain.RetentionSchedule{Class: domain.RetentionClassTokens, Action: domain.RetentionActionPurge, Window: -time.Second, ReasonCode: "x"},
			wantErr:  domain.ErrInvalidRetentionSchedule,
		},
		{
			name:     "empty reason code",
			schedule: domain.RetentionSchedule{Class: domain.RetentionClassTokens, Action: domain.RetentionActionPurge, ReasonCode: "  "},
			wantErr:  domain.ErrInvalidRetentionSchedule,
		},
		{
			name:     "retained class with a boundary",
			schedule: domain.RetentionSchedule{Class: domain.RetentionClassBilling, Action: domain.RetentionActionRetain, Window: time.Hour, ReasonCode: "x"},
			wantErr:  domain.ErrInvalidRetentionSchedule,
		},
		{
			name:     "retain action without the indefinite mark",
			schedule: domain.RetentionSchedule{Class: domain.RetentionClassBilling, Action: domain.RetentionActionRetain, ReasonCode: "x"},
			wantErr:  domain.ErrInvalidRetentionSchedule,
		},
		{
			name:     "purge marked indefinite",
			schedule: domain.RetentionSchedule{Class: domain.RetentionClassTokens, Action: domain.RetentionActionPurge, Indefinite: true, ReasonCode: "x"},
			wantErr:  domain.ErrInvalidRetentionSchedule,
		},
	}
	for _, testCase := range cases {
		err := testCase.schedule.IsValid()
		if !errors.Is(err, testCase.wantErr) {
			t.Errorf("%s: IsValid() = %v, want %v", testCase.name, err, testCase.wantErr)
		}
	}

	if err := valid.IsValid(); err != nil {
		t.Fatalf("the declared tokens schedule must be valid: %v", err)
	}
}

// TestRetentionHoldShape proves the hold value object: whole-class holds,
// account holds and the refusals.
func TestRetentionHoldShape(t *testing.T) {
	classWide := domain.RetentionHold{
		ID:         "hold-1",
		Class:      domain.RetentionClassSessions,
		ReasonCode: "litigation_hold",
		PlacedAt:   retentionTerminal,
	}
	if !classWide.IsClassWide() {
		t.Error("a hold without an account must cover the whole class")
	}
	if err := classWide.Validate(); err != nil {
		t.Fatalf("class-wide hold must be valid: %v", err)
	}

	accountHold := classWide
	accountHold.AccountID = "0b0b0b0b-1111-2222-3333-444455556666"
	if accountHold.IsClassWide() {
		t.Error("a hold with an account must not be class-wide")
	}
	if err := accountHold.Validate(); err != nil {
		t.Fatalf("account hold must be valid: %v", err)
	}
	if !(domain.RetentionHold{Class: domain.RetentionClassBilling, AccountID: "  ", ReasonCode: "x", PlacedAt: retentionTerminal}).IsClassWide() {
		t.Error("a blank account identifier must not address an account")
	}

	invalid := []struct {
		name string
		hold domain.RetentionHold
	}{
		{"unknown class", domain.RetentionHold{Class: "wallet", ReasonCode: "x", PlacedAt: retentionTerminal}},
		{"empty reason", domain.RetentionHold{Class: domain.RetentionClassTokens, ReasonCode: " ", PlacedAt: retentionTerminal}},
		{"reason too long", domain.RetentionHold{Class: domain.RetentionClassTokens, ReasonCode: string(make([]byte, 101)), PlacedAt: retentionTerminal}},
		{"placement instant missing", domain.RetentionHold{Class: domain.RetentionClassTokens, ReasonCode: "x"}},
	}
	for _, testCase := range invalid {
		if err := testCase.hold.Validate(); err == nil {
			t.Errorf("%s: Validate() accepted an incoherent hold", testCase.name)
		}
	}

	// The reason bound matches the migration CHECK.
	bound := domain.RetentionHold{Class: domain.RetentionClassTokens, ReasonCode: string(make([]byte, 100)), PlacedAt: retentionTerminal}
	for i := range bound.ReasonCode {
		bound.ReasonCode = bound.ReasonCode[:i] + "a" + bound.ReasonCode[i+1:]
	}
	if err := bound.Validate(); err != nil {
		t.Errorf("a 100-character reason must be accepted: %v", err)
	}
	if err := (domain.RetentionHold{Class: domain.RetentionClassTokens, ReasonCode: "reason", PlacedAt: retentionTerminal.UTC()}).Validate(); err != nil {
		t.Errorf("a UTC placement instant must be accepted: %v", err)
	}
}
