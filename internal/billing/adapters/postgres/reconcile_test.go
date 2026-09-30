package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

func TestReconcileStoreRoundTripWithoutCorrecting(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustBillingAccount(t, ctx, q, "reconcile-store@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))
	customerID, _ := domain.ParseStripeCustomerID("cus_reconcile1")
	if _, err := repo.RecordStripeCustomer(ctx, application.RecordStripeCustomerRequest{
		AccountID:  accountID,
		CustomerID: customerID,
		Livemode:   false,
	}); err != nil {
		t.Fatalf("RecordStripeCustomer: %v", err)
	}

	windowStart := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	intentID := "cs_test_reconcile1"
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.checkout_intents
			(account_id, market, product_id, catalog_version, currency, amount_minor, livemode, status, stripe_checkout_session_id, created_at, paid_at)
		VALUES ($1, 'BR', 'ink_10000', 1, 'BRL', 990, false, 'paid', $2, $3, $3)`,
		acc.ID, intentID, windowStart.Add(time.Hour)); err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	intents, err := repo.ListIntents(ctx, windowStart, windowEnd, false)
	if err != nil {
		t.Fatalf("ListIntents: %v", err)
	}
	if len(intents) != 1 || intents[0].SessionID.String() != intentID {
		t.Fatalf("intents = %+v, want one %s", intents, intentID)
	}

	runID, err := repo.CreateRun(ctx, false, windowStart, windowEnd)
	if err != nil || runID == "" {
		t.Fatalf("CreateRun: %v %q", err, runID)
	}
	if err := repo.RecordFinding(ctx, application.RecordFindingRequest{
		RunID:     runID,
		AccountID: &accountID,
		Kind:      domain.ReconciliationMissingRemote,
		Reference: intentID,
		Details:   "probe finding",
	}); err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}
	if err := repo.FinishRun(ctx, runID, 1, 1, false); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	findings, err := q.ListReconciliationFindingsByRun(ctx, mustUUID(t, runID))
	if err != nil {
		t.Fatalf("ListReconciliationFindingsByRun: %v", err)
	}
	if len(findings) != 1 || findings[0].Kind != "missing_remote" {
		t.Fatalf("findings = %+v, want one missing_remote", findings)
	}

	// The job never corrects: the intent row is byte-identical afterwards.
	var status string
	var amount int64
	if err := pool.QueryRow(ctx, `SELECT status, amount_minor FROM app.checkout_intents WHERE stripe_checkout_session_id = $1`, intentID).Scan(&status, &amount); err != nil {
		t.Fatalf("reload intent: %v", err)
	}
	if status != "paid" || amount != 990 {
		t.Fatalf("intent mutated to %s/%d, want paid/990", status, amount)
	}
}
