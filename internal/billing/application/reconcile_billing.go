package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// ReconciliationGateway is the minimal provider surface the job needs: read
// the current remote state of one session and one subscription. Failures use
// the gateway vocabulary: unavailable/timeout abort the run (unknown
// outcome), rejected means the remote object is gone (missing_remote),
// misconfigured aborts like a bug.
type ReconciliationGateway interface {
	GetCheckoutSession(ctx context.Context, id domain.StripeCheckoutSessionID) (CheckoutSession, error)
	GetSubscription(ctx context.Context, id domain.StripeSubscriptionID) (Subscription, error)
}

// ReconciliationIntent is one local checkout intent inside the window.
type ReconciliationIntent struct {
	Record CheckoutIntentRecord
}

// ReconciliationSubscription is one local subscription mirror inside the
// window.
type ReconciliationSubscription struct {
	Record SubscriptionRecord
}

// ReconciliationEvent is one verified webhook event in the window that never
// reached a terminal outcome.
type ReconciliationEvent struct {
	EventID   domain.WebhookEventID
	EventType domain.WebhookEventType
	AccountID *domain.AccountID
}

// ReconciliationStore persists runs and findings. It never updates intents,
// subscriptions or events: findings are the only write besides the run row.
type ReconciliationStore interface {
	ListIntents(ctx context.Context, start, end time.Time, livemode bool) ([]CheckoutIntentRecord, error)
	ListSubscriptions(ctx context.Context, start, end time.Time, livemode bool) ([]SubscriptionRecord, error)
	ListUnprocessedEvents(ctx context.Context, start, end time.Time, livemode bool) ([]WebhookEventRecord, error)
	CreateRun(ctx context.Context, livemode bool, start, end time.Time) (string, error)
	RecordFinding(ctx context.Context, request RecordFindingRequest) error
	FinishRun(ctx context.Context, runID string, scanned, findings int, failed bool) error
}

// RecordFindingRequest is one immutable divergence.
type RecordFindingRequest struct {
	RunID     string
	AccountID *domain.AccountID
	Kind      domain.ReconciliationKind
	Reference string
	Details   string
}

// ReconcileBillingWindowCommand is one window to inspect.
type ReconcileBillingWindowCommand struct {
	Window   domain.ReconciliationWindow
	Livemode bool
}

// ReconcileBillingWindowResult is the explicit outcome: counters only.
// Findings live in the journal, never in this return.
type ReconcileBillingWindowResult struct {
	RunID    string
	Scanned  int
	Findings int
}

// ReconcileBillingDependencies groups everything the job needs.
type ReconcileBillingDependencies struct {
	Gateway PaymentGateway
	Intents CheckoutIntentRepository
	Subs    SubscriptionRepository
	Events  WebhookEventRepository
	Runs    ReconciliationStore
	Clock   Clock
}

// ReconcileBillingWindowUseCase compares the local mirrors in the window
// against the provider and records every divergence as a finding. It never
// corrects anything: intents, subscriptions and events are read-only here,
// so a bug in the job cannot rewrite money.
type ReconcileBillingWindowUseCase struct {
	deps ReconcileBillingDependencies
}

// NewReconcileBillingWindowUseCase builds the job, refusing incomplete
// composition.
func NewReconcileBillingWindowUseCase(deps ReconcileBillingDependencies) (*ReconcileBillingWindowUseCase, error) {
	if deps.Gateway == nil || deps.Intents == nil || deps.Subs == nil ||
		deps.Events == nil || deps.Runs == nil || deps.Clock == nil {
		return nil, ErrInvalidReconciliationConfig
	}
	return &ReconcileBillingWindowUseCase{deps: deps}, nil
}

// Execute inspects one window.
func (uc *ReconcileBillingWindowUseCase) Execute(ctx context.Context, cmd ReconcileBillingWindowCommand) (*ReconcileBillingWindowResult, error) {
	if cmd.Window.IsZero() {
		return nil, domain.ErrInvalidReconciliationWindow
	}

	runID, err := uc.deps.Runs.CreateRun(ctx, cmd.Livemode, cmd.Window.Start(), cmd.Window.End())
	if err != nil {
		return nil, fmt.Errorf("reconcile create run: %w", err)
	}
	scanned := 0
	findings := 0
	failed := false
	defer func() {
		_ = uc.deps.Runs.FinishRun(ctx, runID, scanned, findings, failed)
	}()

	intents, err := uc.deps.Runs.ListIntents(ctx, cmd.Window.Start(), cmd.Window.End(), cmd.Livemode)
	if err != nil {
		failed = true
		return nil, fmt.Errorf("reconcile list intents: %w", err)
	}
	for _, intent := range intents {
		scanned++
		count, err := uc.reconcileIntent(ctx, runID, intent)
		if err != nil {
			if IsRetryablePaymentGatewayError(err) {
				failed = true
				return nil, fmt.Errorf("reconcile intent %s: %w", intent.ID, err)
			}
			// Misconfiguration aborts the run: the integration is broken
			// and every further comparison would be suspect.
			if errors.Is(err, ErrPaymentGatewayMisconfigured) {
				failed = true
				return nil, fmt.Errorf("reconcile intent %s: %w", intent.ID, err)
			}
			return nil, err
		}
		findings += count
	}

	subs, err := uc.deps.Runs.ListSubscriptions(ctx, cmd.Window.Start(), cmd.Window.End(), cmd.Livemode)
	if err != nil {
		failed = true
		return nil, fmt.Errorf("reconcile list subscriptions: %w", err)
	}
	for _, sub := range subs {
		scanned++
		count, err := uc.reconcileSubscription(ctx, runID, sub)
		if err != nil {
			if IsRetryablePaymentGatewayError(err) || errors.Is(err, ErrPaymentGatewayMisconfigured) {
				failed = true
				return nil, fmt.Errorf("reconcile subscription %s: %w", sub.ID, err)
			}
			return nil, err
		}
		findings += count
	}

	events, err := uc.deps.Runs.ListUnprocessedEvents(ctx, cmd.Window.Start(), cmd.Window.End(), cmd.Livemode)
	if err != nil {
		failed = true
		return nil, fmt.Errorf("reconcile list events: %w", err)
	}
	for _, event := range events {
		scanned++
		if err := uc.deps.Runs.RecordFinding(ctx, RecordFindingRequest{
			RunID:     runID,
			AccountID: nil,
			Kind:      domain.ReconciliationUnprocessedEvent,
			Reference: event.EventID.String(),
			Details:   "webhook event never reached a terminal outcome in the window",
		}); err != nil {
			failed = true
			return nil, fmt.Errorf("reconcile record unprocessed event: %w", err)
		}
		findings++
	}

	return &ReconcileBillingWindowResult{RunID: runID, Scanned: scanned, Findings: findings}, nil
}

func (uc *ReconcileBillingWindowUseCase) reconcileIntent(ctx context.Context, runID string, intent CheckoutIntentRecord) (int, error) {
	// Intents without a provider session have no remote object yet (created
	// without session, or failed before the provider answered).
	if intent.SessionID.IsZero() {
		return 0, nil
	}
	remote, err := uc.deps.Gateway.GetCheckoutSession(ctx, intent.SessionID)
	if err != nil {
		if errors.Is(err, ErrPaymentGatewayRejected) {
			if err := uc.deps.Runs.RecordFinding(ctx, RecordFindingRequest{
				RunID:     runID,
				AccountID: &intent.AccountID,
				Kind:      domain.ReconciliationMissingRemote,
				Reference: intent.SessionID.String(),
				Details:   "local intent names a checkout session the provider does not know",
			}); err != nil {
				return 0, err
			}
			return 1, nil
		}
		return 0, err
	}

	count := 0
	if remote.AmountMinor != intent.Amount.MinorUnits() {
		if err := uc.deps.Runs.RecordFinding(ctx, RecordFindingRequest{
			RunID:     runID,
			AccountID: &intent.AccountID,
			Kind:      domain.ReconciliationAmountMismatch,
			Reference: intent.SessionID.String(),
			Details:   "provider total differs from the server-authoritative intent",
		}); err != nil {
			return 0, err
		}
		count++
	}
	if remote.Currency != intent.Amount.Currency() {
		if err := uc.deps.Runs.RecordFinding(ctx, RecordFindingRequest{
			RunID:     runID,
			AccountID: &intent.AccountID,
			Kind:      domain.ReconciliationCurrencyMismatch,
			Reference: intent.SessionID.String(),
			Details:   "provider currency differs from the intent currency",
		}); err != nil {
			return 0, err
		}
		count++
	}
	if intentStatusMismatch(intent.Status, remote) {
		if err := uc.deps.Runs.RecordFinding(ctx, RecordFindingRequest{
			RunID:     runID,
			AccountID: &intent.AccountID,
			Kind:      domain.ReconciliationStatusMismatch,
			Reference: intent.SessionID.String(),
			Details:   "local intent lifecycle disagrees with the provider session",
		}); err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}

// intentStatusMismatch maps the out-of-order cases the webhooks of T05-T08
// can leave behind: a paid intent whose session was never settled remotely,
// an open intent whose session already expired, and an expired intent whose
// session is still payable.
func intentStatusMismatch(local domain.CheckoutIntentStatus, remote CheckoutSession) bool {
	switch local {
	case domain.CheckoutIntentPaid:
		return !remote.PaymentStatus.IsSettled()
	case domain.CheckoutIntentOpen:
		return remote.Status == domain.CheckoutStatusExpired
	case domain.CheckoutIntentExpired:
		return remote.Status == domain.CheckoutStatusOpen ||
			(remote.Status == domain.CheckoutStatusComplete && remote.PaymentStatus.IsSettled())
	default:
		return false
	}
}

func (uc *ReconcileBillingWindowUseCase) reconcileSubscription(ctx context.Context, runID string, sub SubscriptionRecord) (int, error) {
	remote, err := uc.deps.Gateway.GetSubscription(ctx, sub.StripeSubscriptionID)
	if err != nil {
		if errors.Is(err, ErrPaymentGatewayRejected) {
			if err := uc.deps.Runs.RecordFinding(ctx, RecordFindingRequest{
				RunID:     runID,
				AccountID: &sub.AccountID,
				Kind:      domain.ReconciliationMissingRemote,
				Reference: sub.StripeSubscriptionID.String(),
				Details:   "local subscription names a provider object the provider does not know",
			}); err != nil {
				return 0, err
			}
			return 1, nil
		}
		return 0, err
	}

	count := 0
	if remote.Status != sub.Status {
		if err := uc.deps.Runs.RecordFinding(ctx, RecordFindingRequest{
			RunID:     runID,
			AccountID: &sub.AccountID,
			Kind:      domain.ReconciliationStatusMismatch,
			Reference: sub.StripeSubscriptionID.String(),
			Details:   "local subscription status disagrees with the provider",
		}); err != nil {
			return 0, err
		}
		count++
	}
	if remote.PriceID.String() != "" && remote.PriceID != sub.StripePriceID {
		if err := uc.deps.Runs.RecordFinding(ctx, RecordFindingRequest{
			RunID:     runID,
			AccountID: &sub.AccountID,
			Kind:      domain.ReconciliationAmountMismatch,
			Reference: sub.StripeSubscriptionID.String(),
			Details:   "provider price differs from the mirrored subscription price",
		}); err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}
