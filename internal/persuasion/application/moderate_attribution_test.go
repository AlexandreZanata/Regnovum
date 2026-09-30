package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

const (
	moderationAttributionRaw = "018f6b2a-0000-7000-8000-000000000009"
	moderationArgumentRaw    = "018f6b2a-0000-7000-8000-00000000000a"
	moderationModeratorRaw   = "018f6b2a-0000-7000-8000-00000000000b"
	moderationAdminRaw       = "018f6b2a-0000-7000-8000-00000000000c"
)

// moderationTxKey marks the context of the fake transaction, so the fakes can
// prove that every state read, write and audit record happens inside it.
type moderationTxKey struct{}

type moderationUnitOfWork struct {
	calls int
	err   error
}

func (u *moderationUnitOfWork) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	u.calls++
	if u.err != nil {
		return u.err
	}
	return fn(context.WithValue(ctx, moderationTxKey{}, true))
}

func inModerationTx(ctx context.Context) bool {
	marked, _ := ctx.Value(moderationTxKey{}).(bool)
	return marked
}

type moderationAuthorizer struct {
	deny     bool
	failWith error
	calls    int
}

func (a *moderationAuthorizer) EnsureModerator(_ context.Context, _ domain.ModeratorID) error {
	a.calls++
	if a.failWith != nil {
		return a.failWith
	}
	if a.deny {
		return application.ErrNotAuthorized
	}
	return nil
}

type moderationAudit struct {
	err       error
	events    []application.AttributionModerationEvent
	outsideTx int
}

func (a *moderationAudit) RecordAttributionModeration(ctx context.Context, event application.AttributionModerationEvent) error {
	if !inModerationTx(ctx) {
		a.outsideTx++
	}
	if a.err != nil {
		return a.err
	}
	a.events = append(a.events, event)
	return nil
}

type moderationRepo struct {
	attributions map[string]domain.Attribution

	lockCalls     int
	applyCalls    int
	outsideTx     int
	lockErr       error
	applyErr      error
	appliedIDs    []string
	appliedAction []domain.ModerationAction
}

func newModerationRepo(t *testing.T, status domain.AttributionStatus) *moderationRepo {
	t.Helper()
	attributionID, err := domain.ParseAttributionID(moderationAttributionRaw)
	if err != nil {
		t.Fatalf("ParseAttributionID: %v", err)
	}
	changeID, err := domain.ParseChangeID(testChangeRaw)
	if err != nil {
		t.Fatalf("ParseChangeID: %v", err)
	}
	argumentID, err := domain.ParseArgumentID(moderationArgumentRaw)
	if err != nil {
		t.Fatalf("ParseArgumentID: %v", err)
	}
	attributorID, err := domain.ParseAttributorID(testAccountRaw)
	if err != nil {
		t.Fatalf("ParseAttributorID: %v", err)
	}
	attribution := domain.Attribution{
		ID:           attributionID,
		ChangeID:     changeID,
		ArgumentID:   argumentID,
		AttributorID: attributorID,
		Status:       status,
		CreatedAt:    testInstant.Add(-time.Hour),
	}
	if status == domain.AttributionStatusInvalid {
		reason, _ := domain.ParseReason("invalidação anterior registrada")
		actor, _ := domain.ParseModeratorID(moderationModeratorRaw)
		attribution.Decision = domain.ModerationDecision{
			Action:    domain.ModerationActionInvalidate,
			Actor:     actor,
			Reason:    reason,
			DecidedAt: testInstant.Add(-time.Minute),
		}
	}
	return &moderationRepo{attributions: map[string]domain.Attribution{moderationAttributionRaw: attribution}}
}

func (r *moderationRepo) LockAttributionForModeration(ctx context.Context, attributionID domain.AttributionID) (*domain.Attribution, error) {
	r.lockCalls++
	if !inModerationTx(ctx) {
		r.outsideTx++
	}
	if r.lockErr != nil {
		return nil, r.lockErr
	}
	attribution, ok := r.attributions[attributionID.String()]
	if !ok {
		return nil, application.ErrAttributionNotFound
	}
	return &attribution, nil
}

func (r *moderationRepo) ApplyModerationDecision(ctx context.Context, attributionID domain.AttributionID, decision domain.ModerationDecision) (*domain.Attribution, error) {
	r.applyCalls++
	if !inModerationTx(ctx) {
		r.outsideTx++
	}
	if r.applyErr != nil {
		return nil, r.applyErr
	}
	attribution, ok := r.attributions[attributionID.String()]
	if !ok {
		return nil, application.ErrAttributionNotFound
	}
	target, err := applyForFake(attribution, decision)
	if err != nil {
		return nil, err
	}
	r.attributions[attributionID.String()] = target
	r.appliedIDs = append(r.appliedIDs, attributionID.String())
	r.appliedAction = append(r.appliedAction, decision.Action)
	return &target, nil
}

func applyForFake(current domain.Attribution, decision domain.ModerationDecision) (domain.Attribution, error) {
	switch decision.Action {
	case domain.ModerationActionInvalidate:
		return current.Invalidate(decision)
	case domain.ModerationActionRestore:
		return current.Restore(decision)
	default:
		return domain.Attribution{}, domain.ErrInvalidModerationAction
	}
}

func moderationCommand() application.ModerateAttributionCommand {
	return application.ModerateAttributionCommand{
		ActorAccountID: moderationModeratorRaw,
		AttributionID:  moderationAttributionRaw,
		Reason:         "atribuição fraudulenta confirmada no caso 42",
	}
}

type moderationHarness struct {
	repo       *moderationRepo
	authorizer *moderationAuthorizer
	audit      *moderationAudit
	uow        *moderationUnitOfWork
	invalidate *application.InvalidateAttributionUseCase
	restore    *application.RestoreAttributionUseCase
}

func newModerationHarness(t *testing.T, status domain.AttributionStatus) *moderationHarness {
	t.Helper()
	repo := newModerationRepo(t, status)
	authorizer := &moderationAuthorizer{}
	audit := &moderationAudit{}
	uow := &moderationUnitOfWork{}
	clock := fixedClock{instant: testInstant}
	harness := &moderationHarness{
		repo:       repo,
		authorizer: authorizer,
		audit:      audit,
		uow:        uow,
		invalidate: application.NewInvalidateAttributionUseCase(repo, authorizer, audit, clock, uow),
		restore:    application.NewRestoreAttributionUseCase(repo, authorizer, audit, clock, uow),
	}
	return harness
}

type fixedClock struct{ instant time.Time }

func (c fixedClock) Now() time.Time { return c.instant }

func TestInvalidateAttributionRecordsDecisionAndAudit(t *testing.T) {
	t.Parallel()

	harness := newModerationHarness(t, domain.AttributionStatusValid)
	result, err := harness.invalidate.Execute(context.Background(), moderationCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed {
		t.Fatal("a fresh invalidation must not be reported as a replay")
	}
	if result.Attribution.Status != domain.AttributionStatusInvalid || result.Attribution.IsValid() {
		t.Fatalf("status = %q, want invalid", result.Attribution.Status)
	}
	decision, ok := result.Attribution.DecisionOf()
	if !ok || decision.Action != domain.ModerationActionInvalidate || !decision.Actor.Equals(mustModerator(t)) {
		t.Fatalf("recorded decision = %+v", decision)
	}
	if !decision.DecidedAt.Equal(testInstant) {
		t.Fatalf("decision instant = %s, want the injected clock instant", decision.DecidedAt)
	}
	if harness.repo.applyCalls != 1 || harness.uow.calls != 1 || harness.authorizer.calls != 1 {
		t.Fatalf("calls = apply:%d uow:%d authorizer:%d, want one each", harness.repo.applyCalls, harness.uow.calls, harness.authorizer.calls)
	}

	// The audit event carries the decision facts and never the private
	// attributor identity.
	if len(harness.audit.events) != 1 {
		t.Fatalf("audit events = %d, want exactly one", len(harness.audit.events))
	}
	event := harness.audit.events[0]
	if event.Action != domain.ModerationActionInvalidate ||
		event.AttributionID.String() != moderationAttributionRaw ||
		event.ArgumentID.String() != moderationArgumentRaw ||
		!event.ActorAccountID.Equals(mustModerator(t)) ||
		!event.Reason.Equals(decision.Reason) ||
		!event.OccurredAt.Equal(testInstant) {
		t.Fatalf("audit event = %+v", event)
	}
	if event.ActorAccountID.String() == testAccountRaw {
		t.Fatal("the audit event must not carry the attributor identity")
	}

	// Every port call happened inside the shared transaction.
	if harness.repo.outsideTx != 0 || harness.audit.outsideTx != 0 {
		t.Fatalf("reads/writes outside the transaction: repo=%d audit=%d", harness.repo.outsideTx, harness.audit.outsideTx)
	}
}

func TestInvalidateAttributionReplaysRecordedDecision(t *testing.T) {
	t.Parallel()

	harness := newModerationHarness(t, domain.AttributionStatusInvalid)
	result, err := harness.invalidate.Execute(context.Background(), moderationCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Replayed {
		t.Fatal("reapplying an existing invalidation must resolve as a replay")
	}
	if harness.repo.applyCalls != 0 {
		t.Fatalf("apply calls = %d, want none on replay", harness.repo.applyCalls)
	}
	if len(harness.audit.events) != 0 {
		t.Fatalf("audit events = %d, want none: a replay is not a new decision", len(harness.audit.events))
	}
	// The original decision facts and instant stay untouched.
	decision, ok := result.Attribution.DecisionOf()
	if !ok || !decision.DecidedAt.Equal(testInstant.Add(-time.Minute)) {
		t.Fatalf("replayed decision = %+v, want the recorded one", decision)
	}
}

func TestRestoreAttributionReversesInvalidation(t *testing.T) {
	t.Parallel()

	harness := newModerationHarness(t, domain.AttributionStatusInvalid)
	result, err := harness.restore.Execute(context.Background(), moderationCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed || result.Attribution.Status != domain.AttributionStatusValid {
		t.Fatalf("result = %+v, want a fresh restoration", result)
	}
	if decision, ok := result.Attribution.DecisionOf(); !ok || decision.Action != domain.ModerationActionRestore {
		t.Fatalf("restoration decision = %+v", decision)
	}
	if len(harness.audit.events) != 1 || harness.audit.events[0].Action != domain.ModerationActionRestore {
		t.Fatalf("audit events = %+v, want one restoration event", harness.audit.events)
	}

	// Restoring an already valid attribution resolves as a replay.
	again := newModerationHarness(t, domain.AttributionStatusValid)
	replayed, err := again.restore.Execute(context.Background(), moderationCommand())
	if err != nil {
		t.Fatalf("restore of a valid attribution error = %v", err)
	}
	if !replayed.Replayed || again.repo.applyCalls != 0 || len(again.audit.events) != 0 {
		t.Fatalf("replay = %+v, apply=%d, events=%d", replayed, again.repo.applyCalls, len(again.audit.events))
	}
}

func TestModerationDeniedByAuthorizer(t *testing.T) {
	t.Parallel()

	harness := newModerationHarness(t, domain.AttributionStatusValid)
	harness.authorizer.deny = true

	if _, err := harness.invalidate.Execute(context.Background(), moderationCommand()); !errors.Is(err, application.ErrNotAuthorized) {
		t.Fatalf("Execute() error = %v, want ErrNotAuthorized", err)
	}
	// Nothing is read, written, audited or even opened in a transaction.
	if harness.repo.lockCalls != 0 || harness.repo.applyCalls != 0 || harness.uow.calls != 0 || len(harness.audit.events) != 0 {
		t.Fatalf("denied moderation touched state: lock=%d apply=%d uow=%d events=%d",
			harness.repo.lockCalls, harness.repo.applyCalls, harness.uow.calls, len(harness.audit.events))
	}

	// A failing authorizer propagates its own error.
	denied := newModerationHarness(t, domain.AttributionStatusValid)
	denied.authorizer.failWith = application.ErrNotAuthorized
	if _, err := denied.restore.Execute(context.Background(), moderationCommand()); !errors.Is(err, application.ErrNotAuthorized) {
		t.Fatalf("Execute() error = %v, want the authorizer error", err)
	}
}

func TestModerationRejectsIncompleteCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*application.ModerateAttributionCommand)
		want   error
	}{
		{name: "missing actor", mutate: func(c *application.ModerateAttributionCommand) { c.ActorAccountID = "" }, want: domain.ErrEmptyModeratorID},
		{name: "missing attribution", mutate: func(c *application.ModerateAttributionCommand) { c.AttributionID = "" }, want: domain.ErrEmptyAttributionID},
		{name: "missing reason", mutate: func(c *application.ModerateAttributionCommand) { c.Reason = "" }, want: domain.ErrEmptyReason},
		{name: "blank reason", mutate: func(c *application.ModerateAttributionCommand) { c.Reason = "   " }, want: domain.ErrEmptyReason},
		{name: "control characters", mutate: func(c *application.ModerateAttributionCommand) { c.Reason = "fraude\nnova linha" }, want: domain.ErrInvalidReason},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newModerationHarness(t, domain.AttributionStatusValid)
			command := moderationCommand()
			test.mutate(&command)
			if _, err := harness.invalidate.Execute(context.Background(), command); !errors.Is(err, test.want) {
				t.Fatalf("Execute() error = %v, want %v", err, test.want)
			}
			if harness.uow.calls != 0 || harness.authorizer.calls != 0 {
				t.Fatalf("invalid command reached ports: uow=%d authorizer=%d", harness.uow.calls, harness.authorizer.calls)
			}
		})
	}
}

func TestModerationPropagatesFailures(t *testing.T) {
	t.Parallel()

	t.Run("unknown attribution", func(t *testing.T) {
		harness := newModerationHarness(t, domain.AttributionStatusValid)
		harness.repo.lockErr = application.ErrAttributionNotFound
		if _, err := harness.invalidate.Execute(context.Background(), moderationCommand()); !errors.Is(err, application.ErrAttributionNotFound) {
			t.Fatalf("Execute() error = %v, want ErrAttributionNotFound", err)
		}
		if harness.repo.applyCalls != 0 || len(harness.audit.events) != 0 {
			t.Fatalf("missing attribution wrote: apply=%d events=%d", harness.repo.applyCalls, len(harness.audit.events))
		}
	})

	t.Run("conflicting decision", func(t *testing.T) {
		harness := newModerationHarness(t, domain.AttributionStatusValid)
		harness.repo.applyErr = application.ErrModerationConflict
		if _, err := harness.invalidate.Execute(context.Background(), moderationCommand()); !errors.Is(err, application.ErrModerationConflict) {
			t.Fatalf("Execute() error = %v, want ErrModerationConflict", err)
		}
		if len(harness.audit.events) != 0 {
			t.Fatalf("audit events = %d, want none after a conflict", len(harness.audit.events))
		}
	})

	t.Run("audit failure aborts the decision", func(t *testing.T) {
		harness := newModerationHarness(t, domain.AttributionStatusValid)
		harness.audit.err = errors.New("audit trail unavailable")
		result, err := harness.invalidate.Execute(context.Background(), moderationCommand())
		if err == nil || result != nil {
			t.Fatalf("Execute() = %+v, %v, want a propagated audit failure", result, err)
		}
		if !errors.Is(err, harness.audit.err) {
			t.Fatalf("Execute() error = %v, want the recorder error", err)
		}
	})

	t.Run("transaction failure", func(t *testing.T) {
		harness := newModerationHarness(t, domain.AttributionStatusValid)
		harness.uow.err = errors.New("begin failed")
		if _, err := harness.invalidate.Execute(context.Background(), moderationCommand()); err == nil {
			t.Fatal("Execute() error = nil, want the transaction failure")
		}
		if harness.repo.lockCalls != 0 || len(harness.audit.events) != 0 {
			t.Fatal("a failed transaction must not read or record")
		}
	})
}

func mustModerator(t *testing.T) domain.ModeratorID {
	t.Helper()
	actor, err := domain.ParseModeratorID(moderationModeratorRaw)
	if err != nil {
		t.Fatalf("ParseModeratorID: %v", err)
	}
	return actor
}
