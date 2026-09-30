package application_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// retentionBase is the instant the retention scenarios run at. It is the
// same deterministic clock the other profiles tests use.
var retentionBase = testNow

const (
	retentionAccountA = "0a0a0a0a-0000-0000-0000-00000000000a"
	retentionAccountB = "0b0b0b0b-0000-0000-0000-00000000000b"
)

// retentionRow is one governed record in the in-memory store: when it became
// terminal, which account owns it and whether it still carries restricted
// references (the client IP and user agent of a session).
type retentionRow struct {
	terminalAt  time.Time
	accountID   string
	referential bool
}

// retentionStore is a faithful in-memory model of the retention statements:
// the boundary is the cutoff applied to the terminal instant, held accounts
// are excluded, and every run is recorded once per class and instant.
type retentionStore struct {
	tokens     map[string]retentionRow
	sessions   map[string]retentionRow
	exports    map[string]retentionRow
	audit      int32
	billing    int32
	holds      []domain.RetentionHold
	runs       map[string]*application.RetentionRun
	calls      []string
	cutoffs    map[domain.RetentionClass]time.Time
	selections map[domain.RetentionClass]application.RetentionHoldSelection
	failOn     domain.RetentionClass
}

func newRetentionStore() *retentionStore {
	return &retentionStore{
		tokens:     map[string]retentionRow{},
		sessions:   map[string]retentionRow{},
		exports:    map[string]retentionRow{},
		runs:       map[string]*application.RetentionRun{},
		cutoffs:    map[domain.RetentionClass]time.Time{},
		selections: map[domain.RetentionClass]application.RetentionHoldSelection{},
	}
}

// snapshot copies the governed rows and the ledger. The recorded calls are
// an observation channel, not state, so a rollback leaves the evidence of
// which class was attempted.
func (s *retentionStore) snapshot() (map[string]retentionRow, map[string]retentionRow, map[string]retentionRow, map[string]*application.RetentionRun) {
	tokens := make(map[string]retentionRow, len(s.tokens))
	for id, row := range s.tokens {
		tokens[id] = row
	}
	sessions := make(map[string]retentionRow, len(s.sessions))
	for id, row := range s.sessions {
		sessions[id] = row
	}
	exports := make(map[string]retentionRow, len(s.exports))
	for id, row := range s.exports {
		exports[id] = row
	}
	runs := make(map[string]*application.RetentionRun, len(s.runs))
	for key, run := range s.runs {
		runs[key] = run
	}
	return tokens, sessions, exports, runs
}

func (s *retentionStore) restore(tokens, sessions, exports map[string]retentionRow, runs map[string]*application.RetentionRun) {
	s.tokens, s.sessions, s.exports, s.runs = tokens, sessions, exports, runs
}

func (s *retentionStore) held(selection application.RetentionHoldSelection, accountID string) bool {
	if selection.ClassHeld {
		return true
	}
	for _, account := range selection.Accounts {
		if account == accountID {
			return true
		}
	}
	return false
}

// purge applies the class rule to one table: records terminal at or before
// the cutoff are removed unless a hold preserves them; the preserved ones are
// counted.
func (s *retentionStore) purge(rows map[string]retentionRow, cutoff time.Time, selection application.RetentionHoldSelection) application.RetentionCounts {
	counts := application.RetentionCounts{}
	for id, row := range rows {
		if row.terminalAt.After(cutoff) {
			continue
		}
		if s.held(selection, row.accountID) {
			counts.Held++
			continue
		}
		delete(rows, id)
		counts.Purged++
	}
	return counts
}

func (s *retentionStore) ActiveRetentionHolds(_ context.Context) ([]domain.RetentionHold, error) {
	return append([]domain.RetentionHold(nil), s.holds...), nil
}

func (s *retentionStore) PurgeTerminalTokens(_ context.Context, cutoff time.Time, held application.RetentionHoldSelection) (application.RetentionCounts, error) {
	s.calls = append(s.calls, string(domain.RetentionClassTokens))
	s.cutoffs[domain.RetentionClassTokens] = cutoff
	s.selections[domain.RetentionClassTokens] = held
	if s.failOn == domain.RetentionClassTokens {
		return application.RetentionCounts{}, errors.New("store: tokens unavailable")
	}
	return s.purge(s.tokens, cutoff, held), nil
}

func (s *retentionStore) PurgeTerminalSessions(_ context.Context, cutoff time.Time, held application.RetentionHoldSelection) (application.RetentionCounts, error) {
	s.calls = append(s.calls, string(domain.RetentionClassSessions))
	s.cutoffs[domain.RetentionClassSessions] = cutoff
	s.selections[domain.RetentionClassSessions] = held
	if s.failOn == domain.RetentionClassSessions {
		return application.RetentionCounts{}, errors.New("store: sessions unavailable")
	}
	return s.purge(s.sessions, cutoff, held), nil
}

func (s *retentionStore) AnonymizeTerminalSessionReferentials(_ context.Context, cutoff time.Time, held application.RetentionHoldSelection) (application.RetentionCounts, error) {
	s.calls = append(s.calls, string(domain.RetentionClassAbuseSignals))
	s.cutoffs[domain.RetentionClassAbuseSignals] = cutoff
	s.selections[domain.RetentionClassAbuseSignals] = held
	if s.failOn == domain.RetentionClassAbuseSignals {
		return application.RetentionCounts{}, errors.New("store: sessions unavailable")
	}
	counts := application.RetentionCounts{}
	for id, row := range s.sessions {
		if row.terminalAt.After(cutoff) || !row.referential {
			continue
		}
		if s.held(held, row.accountID) {
			counts.Held++
			continue
		}
		row.referential = false
		s.sessions[id] = row
		counts.Anonymized++
	}
	return counts, nil
}

func (s *retentionStore) PurgeExpiredExports(_ context.Context, cutoff, _ time.Time, held application.RetentionHoldSelection) (application.RetentionCounts, error) {
	s.calls = append(s.calls, string(domain.RetentionClassExports))
	s.cutoffs[domain.RetentionClassExports] = cutoff
	s.selections[domain.RetentionClassExports] = held
	if s.failOn == domain.RetentionClassExports {
		return application.RetentionCounts{}, errors.New("store: exports unavailable")
	}
	return s.purge(s.exports, cutoff, held), nil
}

func (s *retentionStore) CountRetainedAuditEvents(_ context.Context) (int32, error) {
	s.calls = append(s.calls, string(domain.RetentionClassReferentialLogs))
	if s.failOn == domain.RetentionClassReferentialLogs {
		return 0, errors.New("store: audit unavailable")
	}
	return s.audit, nil
}

func (s *retentionStore) CountRetainedBillingRows(_ context.Context) (int32, error) {
	s.calls = append(s.calls, string(domain.RetentionClassBilling))
	if s.failOn == domain.RetentionClassBilling {
		return 0, errors.New("store: billing unavailable")
	}
	return s.billing, nil
}

func (s *retentionStore) RecordRetentionRun(_ context.Context, record application.RetentionRunRecord) (*application.RetentionRun, error) {
	key := string(record.Class) + "|" + record.ExecutedAt.Format(time.RFC3339Nano)
	if existing, ok := s.runs[key]; ok {
		replay := *existing
		replay.Replayed = true
		return &replay, nil
	}
	run := &application.RetentionRun{
		ID:         "run-" + key,
		Class:      record.Class,
		ExecutedAt: record.ExecutedAt,
		CutoffAt:   record.CutoffAt,
		Counts:     record.Counts,
	}
	s.runs[key] = run
	return run, nil
}

// retentionUow restores the store snapshot when the function fails, so a
// rollback is observable in the unit test exactly like the database does it.
type retentionUow struct {
	store *retentionStore
}

func (u retentionUow) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	tokens, sessions, exports, runs := u.store.snapshot()
	if err := fn(ctx); err != nil {
		u.store.restore(tokens, sessions, exports, runs)
		return err
	}
	return nil
}

func newRetentionHarness(t *testing.T, store *retentionStore, now time.Time) *application.EnforceRetentionUseCase {
	t.Helper()
	useCase, err := application.NewEnforceRetentionUseCase(store, retentionUow{store: store}, fixedClock{now: now})
	if err != nil {
		t.Fatalf("NewEnforceRetentionUseCase: %v", err)
	}
	return useCase
}

func retentionRunFor(t *testing.T, summary *application.RetentionSummary, class domain.RetentionClass) application.RetentionRun {
	t.Helper()
	for _, run := range summary.Runs {
		if run.Class == class {
			return run
		}
	}
	t.Fatalf("summary has no run for %s", class)
	return application.RetentionRun{}
}

// TestRetentionEnforcesEveryClassInPolicyOrder proves the job walks the whole
// policy exactly once, in the declared order, and records one ledger entry per
// class with the boundary it applied.
func TestRetentionEnforcesEveryClassInPolicyOrder(t *testing.T) {
	store := newRetentionStore()
	store.tokens["token-1"] = retentionRow{terminalAt: retentionBase.Add(-31 * 24 * time.Hour), accountID: retentionAccountA}
	store.sessions["session-1"] = retentionRow{terminalAt: retentionBase.Add(-31 * 24 * time.Hour), accountID: retentionAccountA}
	store.exports["export-1"] = retentionRow{terminalAt: retentionBase.Add(-48 * time.Hour), accountID: retentionAccountA}
	store.audit = 7
	store.billing = 11

	summary, err := newRetentionHarness(t, store, retentionBase).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	wantCalls := []string{"tokens", "sessions", "referential_logs", "exports", "abuse_signals", "billing"}
	if strings.Join(store.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("enforced classes = %v, want %v", store.calls, wantCalls)
	}
	if len(summary.Runs) != len(wantCalls) {
		t.Fatalf("summary has %d runs, want %d", len(summary.Runs), len(wantCalls))
	}
	for i, class := range wantCalls {
		if string(summary.Runs[i].Class) != class {
			t.Fatalf("run[%d].Class = %q, want %q", i, summary.Runs[i].Class, class)
		}
	}

	if run := retentionRunFor(t, summary, domain.RetentionClassTokens); run.Counts.Purged != 1 {
		t.Errorf("tokens purged = %d, want 1", run.Counts.Purged)
	}
	if run := retentionRunFor(t, summary, domain.RetentionClassSessions); run.Counts.Purged != 1 {
		t.Errorf("sessions purged = %d, want 1", run.Counts.Purged)
	}
	if run := retentionRunFor(t, summary, domain.RetentionClassExports); run.Counts.Purged != 1 {
		t.Errorf("exports purged = %d, want 1", run.Counts.Purged)
	}
	if run := retentionRunFor(t, summary, domain.RetentionClassReferentialLogs); run.Counts.Retained != 7 {
		t.Errorf("referential logs retained = %d, want 7", run.Counts.Retained)
	}
	if run := retentionRunFor(t, summary, domain.RetentionClassBilling); run.Counts.Retained != 11 {
		t.Errorf("billing retained = %d, want 11", run.Counts.Retained)
	}

	// The boundary travels to the ledger: purge and anonymize classes date
	// the cutoff they applied, retained classes have none.
	for _, run := range summary.Runs {
		schedule, _ := domain.RetentionScheduleFor(run.Class)
		if schedule.HasCutoff() {
			if run.CutoffAt == nil {
				t.Fatalf("%s run has no cutoff", run.Class)
			}
			if want := retentionBase.Add(-schedule.Window); !run.CutoffAt.Equal(want) {
				t.Errorf("%s cutoff = %s, want %s", run.Class, run.CutoffAt, want)
			}
			continue
		}
		if run.CutoffAt != nil {
			t.Errorf("retained class %s must not date a cutoff", run.Class)
		}
	}
}

// TestRetentionBoundariesAreExact proves the window rule at its edges: a
// record terminal exactly at the cutoff is inside, one second later is not,
// and the in-memory rule agrees with the domain boundary for every class.
func TestRetentionBoundariesAreExact(t *testing.T) {
	store := newRetentionStore()

	tokensWindow := domain.RetentionTokensWindow
	tokenBoundary := retentionBase.Add(-tokensWindow)
	store.tokens["inside"] = retentionRow{terminalAt: tokenBoundary, accountID: retentionAccountA}
	store.tokens["outside"] = retentionRow{terminalAt: tokenBoundary.Add(time.Second), accountID: retentionAccountA}
	store.tokens["well-inside"] = retentionRow{terminalAt: tokenBoundary.Add(-time.Hour), accountID: retentionAccountB}

	abuseWindow := domain.RetentionAbuseSignalsWindow
	abuseBoundary := retentionBase.Add(-abuseWindow)
	store.sessions["anonymize-inside"] = retentionRow{terminalAt: abuseBoundary, accountID: retentionAccountA, referential: true}
	store.sessions["anonymize-outside"] = retentionRow{terminalAt: abuseBoundary.Add(time.Second), accountID: retentionAccountA, referential: true}

	store.exports["expired-boundary"] = retentionRow{terminalAt: retentionBase.Add(-domain.RetentionExportsWindow), accountID: retentionAccountA}
	store.exports["expired-after"] = retentionRow{terminalAt: retentionBase.Add(-domain.RetentionExportsWindow).Add(time.Second), accountID: retentionAccountA}

	summary, err := newRetentionHarness(t, store, retentionBase).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if run := retentionRunFor(t, summary, domain.RetentionClassTokens); run.Counts.Purged != 2 || run.Counts.Held != 0 {
		t.Fatalf("tokens purged = %d (held %d), want 2 (0)", run.Counts.Purged, run.Counts.Held)
	}
	if _, kept := store.tokens["outside"]; !kept {
		t.Error("a token terminal one second after the cutoff must survive")
	}
	if run := retentionRunFor(t, summary, domain.RetentionClassAbuseSignals); run.Counts.Anonymized != 1 {
		t.Fatalf("prevention references anonymized = %d, want 1", run.Counts.Anonymized)
	}
	if store.sessions["anonymize-outside"].referential == false {
		t.Error("a session inside the prevention window must keep its references")
	}
	if run := retentionRunFor(t, summary, domain.RetentionClassExports); run.Counts.Purged != 1 {
		t.Fatalf("exports purged = %d, want 1", run.Counts.Purged)
	}

	// The in-memory rule and the domain boundary must agree: a record is in
	// scope exactly when the schedule says it is due.
	tokens, _ := domain.RetentionScheduleFor(domain.RetentionClassTokens)
	for _, terminal := range []time.Time{tokenBoundary.Add(-time.Second), tokenBoundary, tokenBoundary.Add(time.Second)} {
		if (terminal.After(store.cutoffs[domain.RetentionClassTokens]) == false) != tokens.Due(terminal, retentionBase) {
			t.Fatalf("boundary disagreement at %s: store and domain rules diverge", terminal)
		}
	}
}

// TestRetentionLegalHoldPreservesHeldRows proves a hold is enforced where the
// data lives: an account hold keeps that account's due records, a class-wide
// hold keeps every record of the class, and the preserved count reaches the
// ledger.
func TestRetentionLegalHoldPreservesHeldRows(t *testing.T) {
	store := newRetentionStore()
	store.tokens["held"] = retentionRow{terminalAt: retentionBase.Add(-60 * 24 * time.Hour), accountID: retentionAccountA}
	store.tokens["free"] = retentionRow{terminalAt: retentionBase.Add(-60 * 24 * time.Hour), accountID: retentionAccountB}
	store.sessions["litigation-a"] = retentionRow{terminalAt: retentionBase.Add(-60 * 24 * time.Hour), accountID: retentionAccountA, referential: true}
	store.sessions["unrelated"] = retentionRow{terminalAt: retentionBase.Add(-60 * 24 * time.Hour), accountID: retentionAccountB}
	store.holds = []domain.RetentionHold{
		{ID: "hold-tokens", Class: domain.RetentionClassTokens, AccountID: retentionAccountA, ReasonCode: "litigation", PlacedAt: retentionBase.Add(-time.Hour)},
		{ID: "hold-sessions", Class: domain.RetentionClassSessions, ReasonCode: "regulator_request", PlacedAt: retentionBase.Add(-time.Hour)},
	}

	summary, err := newRetentionHarness(t, store, retentionBase).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	tokensRun := retentionRunFor(t, summary, domain.RetentionClassTokens)
	if tokensRun.Counts.Purged != 1 || tokensRun.Counts.Held != 1 {
		t.Fatalf("tokens purged = %d held = %d, want 1 and 1", tokensRun.Counts.Purged, tokensRun.Counts.Held)
	}
	if _, kept := store.tokens["held"]; !kept {
		t.Fatal("a held token must survive the purge")
	}
	if _, kept := store.tokens["free"]; kept {
		t.Fatal("an unheld token must be purged")
	}

	sessionsRun := retentionRunFor(t, summary, domain.RetentionClassSessions)
	if sessionsRun.Counts.Purged != 0 || sessionsRun.Counts.Held != 2 {
		t.Fatalf("sessions purged = %d held = %d, want 0 and 2", sessionsRun.Counts.Purged, sessionsRun.Counts.Held)
	}
	if len(store.sessions) != 2 {
		t.Fatalf("a class-wide hold must keep every session, %d left", len(store.sessions))
	}
	// The anonymize class is a different class: it keeps working while the
	// sessions class is held, because the hold names one class.
	if run := retentionRunFor(t, summary, domain.RetentionClassAbuseSignals); run.Counts.Anonymized != 1 {
		t.Fatalf("prevention references anonymized = %d, want 1", run.Counts.Anonymized)
	}

	selection := store.selections[domain.RetentionClassTokens]
	if selection.ClassHeld || len(selection.Accounts) != 1 || selection.Accounts[0] != retentionAccountA {
		t.Fatalf("tokens selection = %+v, want one held account", selection)
	}
	if !store.selections[domain.RetentionClassSessions].ClassHeld {
		t.Fatal("a class-wide hold must reach the store as a class-wide selection")
	}
}

// TestRetentionStripsReferencesBeforeTheSessionWindowEnds proves the two
// session classes are independent: a session past the prevention window but
// inside the session window survives with its references stripped, while a
// session past the session window is purged outright (the references go with
// the row).
func TestRetentionStripsReferencesBeforeTheSessionWindowEnds(t *testing.T) {
	store := newRetentionStore()
	store.sessions["stale"] = retentionRow{terminalAt: retentionBase.Add(-8 * 24 * time.Hour), accountID: retentionAccountA, referential: true}
	store.sessions["ancient"] = retentionRow{terminalAt: retentionBase.Add(-31 * 24 * time.Hour), accountID: retentionAccountB, referential: true}

	summary, err := newRetentionHarness(t, store, retentionBase).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// The sessions class runs first, so the 31-day session is already gone
	// when the prevention class looks at terminal sessions: only the 8-day
	// row still needs its references stripped.
	if run := retentionRunFor(t, summary, domain.RetentionClassSessions); run.Counts.Purged != 1 {
		t.Fatalf("sessions purged = %d, want 1", run.Counts.Purged)
	}
	if run := retentionRunFor(t, summary, domain.RetentionClassAbuseSignals); run.Counts.Anonymized != 1 {
		t.Fatalf("prevention references anonymized = %d, want 1", run.Counts.Anonymized)
	}
	if len(store.sessions) != 1 || store.sessions["stale"].referential {
		t.Fatalf("the surviving session must be the recent one without references: %+v", store.sessions)
	}
}

// TestRetentionIsIdempotent proves a replayed run does not duplicate the
// ledger and that a second run at a later instant finds nothing to purge.
func TestRetentionIsIdempotent(t *testing.T) {
	store := newRetentionStore()
	store.tokens["token-1"] = retentionRow{terminalAt: retentionBase.Add(-31 * 24 * time.Hour), accountID: retentionAccountA}
	store.sessions["session-1"] = retentionRow{terminalAt: retentionBase.Add(-31 * 24 * time.Hour), accountID: retentionAccountA}
	store.audit, store.billing = 3, 5

	first, err := newRetentionHarness(t, store, retentionBase).Execute(context.Background())
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if len(store.runs) != 6 {
		t.Fatalf("ledger has %d runs after the first execution, want 6", len(store.runs))
	}

	second, err := newRetentionHarness(t, store, retentionBase).Execute(context.Background())
	if err != nil {
		t.Fatalf("replayed Execute: %v", err)
	}
	if len(store.runs) != 6 {
		t.Fatalf("a replayed run duplicated the ledger: %d rows", len(store.runs))
	}
	for _, run := range second.Runs {
		if !run.Replayed {
			t.Fatalf("%s run was not reported as replayed", run.Class)
		}
	}
	firstTokens := retentionRunFor(t, first, domain.RetentionClassTokens)
	secondTokens := retentionRunFor(t, second, domain.RetentionClassTokens)
	if secondTokens.Counts != firstTokens.Counts || secondTokens.ID != firstTokens.ID {
		t.Fatalf("a replay must report the recorded outcome: %+v vs %+v", secondTokens.Counts, firstTokens.Counts)
	}

	third, err := newRetentionHarness(t, store, retentionBase.Add(time.Second)).Execute(context.Background())
	if err != nil {
		t.Fatalf("third Execute: %v", err)
	}
	if len(store.runs) != 12 {
		t.Fatalf("a later run must add one row per class: %d rows", len(store.runs))
	}
	for _, class := range []domain.RetentionClass{
		domain.RetentionClassTokens, domain.RetentionClassSessions, domain.RetentionClassExports,
	} {
		run := retentionRunFor(t, third, class)
		if run.Replayed {
			t.Errorf("%s reported a replay at a later instant", class)
		}
		if run.Counts.Purged != 0 {
			t.Errorf("%s purged %d records again, want 0", class, run.Counts.Purged)
		}
	}
	if run := retentionRunFor(t, third, domain.RetentionClassBilling); run.Counts.Retained != 5 {
		t.Errorf("billing retained = %d, want 5", run.Counts.Retained)
	}
}

// TestRetentionFailsClosedAndRollsBack proves a failing class stops the run,
// leaves the class transaction rolled back and does not enforce the classes
// that come after it.
func TestRetentionFailsClosedAndRollsBack(t *testing.T) {
	store := newRetentionStore()
	store.tokens["token-1"] = retentionRow{terminalAt: retentionBase.Add(-31 * 24 * time.Hour), accountID: retentionAccountA}
	store.sessions["session-1"] = retentionRow{terminalAt: retentionBase.Add(-31 * 24 * time.Hour), accountID: retentionAccountA}
	store.exports["export-1"] = retentionRow{terminalAt: retentionBase.Add(-48 * time.Hour), accountID: retentionAccountA}
	store.failOn = domain.RetentionClassSessions

	_, err := newRetentionHarness(t, store, retentionBase).Execute(context.Background())
	if err == nil {
		t.Fatal("Execute must fail when a class fails")
	}
	if !strings.Contains(err.Error(), string(domain.RetentionClassSessions)) {
		t.Fatalf("error must name the failing class: %v", err)
	}
	if !strings.Contains(err.Error(), "store: sessions unavailable") {
		t.Fatalf("error must carry the cause: %v", err)
	}

	wantCalls := []string{"tokens", "sessions"}
	if strings.Join(store.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("enforced classes = %v, want %v", store.calls, wantCalls)
	}
	// The tokens class already committed: its purge and its ledger entry
	// survive. The sessions class rolled back: its rows and its ledger entry
	// are untouched.
	if _, kept := store.tokens["token-1"]; kept {
		t.Error("the committed purge must not be undone by a later failure")
	}
	recordedTokens := false
	for key := range store.runs {
		if strings.HasPrefix(key, string(domain.RetentionClassTokens)) {
			recordedTokens = true
		}
	}
	if !recordedTokens {
		t.Error("the committed class must stay recorded")
	}
	if _, kept := store.sessions["session-1"]; !kept {
		t.Error("the failing class must roll back its work")
	}
	for key := range store.runs {
		if strings.HasPrefix(key, string(domain.RetentionClassSessions)) {
			t.Fatalf("a failed class must not be recorded: %s", key)
		}
	}
	if _, kept := store.exports["export-1"]; !kept {
		t.Error("classes after the failure must not be enforced")
	}

	// A retry after the failure completes the whole policy.
	store.failOn = ""
	summary, err := newRetentionHarness(t, store, retentionBase.Add(time.Minute)).Execute(context.Background())
	if err != nil {
		t.Fatalf("retry Execute: %v", err)
	}
	if len(summary.Runs) != 6 {
		t.Fatalf("retry enforced %d classes, want 6", len(summary.Runs))
	}
	if run := retentionRunFor(t, summary, domain.RetentionClassReferentialLogs); run.Counts.Retained != 0 {
		t.Errorf("empty audit trail retained = %d, want 0", run.Counts.Retained)
	}
}

// TestRetentionRefusesIncompleteComposition proves the use case cannot be
// built without a repository, a transaction manager and a clock.
func TestRetentionRefusesIncompleteComposition(t *testing.T) {
	store := newRetentionStore()
	uow := retentionUow{store: store}
	clock := fixedClock{now: retentionBase}

	cases := []struct {
		name string
		repo application.RetentionRepository
		uow  application.UnitOfWork
		clk  application.Clock
	}{
		{"no repository", nil, uow, clock},
		{"no unit of work", store, nil, clock},
		{"no clock", store, uow, nil},
	}
	for _, testCase := range cases {
		if _, err := application.NewEnforceRetentionUseCase(testCase.repo, testCase.uow, testCase.clk); !errors.Is(err, application.ErrInvalidRetentionConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidRetentionConfig", testCase.name, err)
		}
	}
}

// TestRetentionHoldSetShape proves the active holds are validated before any
// purge and resolved into the selection the store applies.
func TestRetentionHoldSetShape(t *testing.T) {
	placedAt := retentionBase.Add(-time.Hour)
	set, err := application.NewRetentionHoldSet([]domain.RetentionHold{
		{ID: "a", Class: domain.RetentionClassSessions, AccountID: retentionAccountB, ReasonCode: "litigation", PlacedAt: placedAt},
		{ID: "b", Class: domain.RetentionClassSessions, AccountID: retentionAccountA, ReasonCode: "litigation", PlacedAt: placedAt},
		{ID: "c", Class: domain.RetentionClassSessions, AccountID: retentionAccountA, ReasonCode: "litigation", PlacedAt: placedAt},
		{ID: "d", Class: domain.RetentionClassExports, ReasonCode: "regulator_request", PlacedAt: placedAt},
	})
	if err != nil {
		t.Fatalf("NewRetentionHoldSet: %v", err)
	}
	if set.Len() != 4 {
		t.Fatalf("Len = %d, want 4", set.Len())
	}
	accounts := set.HeldAccounts(domain.RetentionClassSessions)
	if len(accounts) != 2 || accounts[0] != retentionAccountA || accounts[1] != retentionAccountB {
		t.Fatalf("HeldAccounts = %v, want the two accounts sorted", accounts)
	}
	if !sort.StringsAreSorted(accounts) {
		t.Fatal("held accounts must be deterministic")
	}
	if set.ClassHeld(domain.RetentionClassSessions) {
		t.Error("an account hold is not a class-wide hold")
	}
	if !set.ClassHeld(domain.RetentionClassExports) {
		t.Error("a hold without an account must cover the whole class")
	}
	if set.ClassHeld(domain.RetentionClassTokens) {
		t.Error("an unhled class must not report a hold")
	}
	if len(set.HeldAccounts(domain.RetentionClassTokens)) != 0 {
		t.Error("an unhled class has no held accounts")
	}

	refused := []struct {
		name string
		hold domain.RetentionHold
		want error
	}{
		{
			name: "unknown class",
			hold: domain.RetentionHold{ID: "x", Class: "wallet", ReasonCode: "litigation", PlacedAt: placedAt},
			want: domain.ErrUnknownRetentionClass,
		},
		{
			name: "empty reason",
			hold: domain.RetentionHold{ID: "y", Class: domain.RetentionClassTokens, ReasonCode: " ", PlacedAt: placedAt},
			want: domain.ErrInvalidRetentionHold,
		},
		{
			name: "missing instant",
			hold: domain.RetentionHold{ID: "z", Class: domain.RetentionClassTokens, ReasonCode: "litigation"},
			want: domain.ErrInvalidRetentionHold,
		},
	}
	for _, testCase := range refused {
		if _, err := application.NewRetentionHoldSet([]domain.RetentionHold{testCase.hold}); !errors.Is(err, testCase.want) {
			t.Errorf("%s: err = %v, want %v", testCase.name, err, testCase.want)
		}
	}

	// A nil set is the empty policy: nothing is held.
	var empty *application.RetentionHoldSet
	if empty.ClassHeld(domain.RetentionClassTokens) || empty.Len() != 0 || len(empty.HeldAccounts(domain.RetentionClassTokens)) != 0 {
		t.Error("a nil hold set must hold nothing")
	}
}
