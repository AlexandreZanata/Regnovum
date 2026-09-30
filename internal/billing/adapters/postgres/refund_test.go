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

func mustRefundQuantity(t *testing.T, raw int32) domain.Quantity {
	t.Helper()
	quantity, err := domain.NewQuantity(raw)
	if err != nil {
		t.Fatalf("NewQuantity(%d): %v", raw, err)
	}
	return quantity
}

func mustRefundIntent(t *testing.T, ctx context.Context, testDB *dbtest.TestDB, q *platformpg.Queries, repo *postgres.Repository, email, session string) (platformpg.AppAccount, string) {
	t.Helper()
	acc := mustBillingAccount(t, ctx, q, email)
	accountID := domain.AccountID(uuidString(acc.ID))
	customerID, _ := domain.ParseStripeCustomerID("cus_" + email[:4] + "refund1")
	if _, err := repo.RecordStripeCustomer(ctx, application.RecordStripeCustomerRequest{
		AccountID:  accountID,
		CustomerID: customerID,
		Livemode:   false,
	}); err != nil {
		t.Fatalf("RecordStripeCustomer: %v", err)
	}
	pool := testDB.Pool.Pool()
	var intentID string
	var sessionID = session
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.checkout_intents
			(account_id, market, product_id, catalog_version, currency, amount_minor, livemode, status, stripe_checkout_session_id, paid_at)
		VALUES ($1, 'BR', 'ink_10000', 1, 'BRL', 990, false, 'paid', $2, now())
		RETURNING id`, acc.ID, sessionID).Scan(&intentID); err != nil {
		t.Fatalf("seed paid intent: %v", err)
	}
	return acc, intentID
}

func TestRefundJournalIsIdempotentAndAuditable(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	acc, intentID := mustRefundIntent(t, ctx, testDB, q, repo, "refund-journal@arena.example.com", "cs_test_refundjournal1")
	accountID := domain.AccountID(uuidString(acc.ID))

	first, replayed, err := repo.RecordRefund(ctx, application.RecordRefundRequest{
		IntentID:       intentID,
		AccountID:      accountID,
		ProviderRefund: "re_journalaudit1",
		Source:         domain.RefundSourceRefund,
		Status:         domain.RefundStatusApplied,
		ChargedMinor:   990,
		RefundedMinor:  990,
		InkRevoked:     10000,
		NeedsReview:    false,
	})
	if err != nil || replayed {
		t.Fatalf("first RecordRefund = %+v, replayed %v, err %v", first, replayed, err)
	}

	second, replayed, err := repo.RecordRefund(ctx, application.RecordRefundRequest{
		IntentID:       intentID,
		AccountID:      accountID,
		ProviderRefund: "re_journalaudit1",
		Source:         domain.RefundSourceRefund,
		Status:         domain.RefundStatusApplied,
		ChargedMinor:   990,
		RefundedMinor:  990,
		InkRevoked:     1,
		NeedsReview:    false,
	})
	if err != nil || !replayed {
		t.Fatalf("second RecordRefund must replay: %+v %v %v", second, replayed, err)
	}
	if second.InkRevoked != 10000 {
		t.Fatalf("replay rewrote ink_revoked to %d, want original 10000", second.InkRevoked)
	}

	trail, err := repo.ListRefundsByIntent(ctx, intentID)
	if err != nil {
		t.Fatalf("ListRefundsByIntent: %v", err)
	}
	if len(trail) != 1 || trail[0].ProviderRefund != "re_journalaudit1" {
		t.Fatalf("audit trail = %+v, want one row", trail)
	}
	loaded, err := repo.GetRefundByProviderID(ctx, "re_journalaudit1")
	if err != nil || loaded == nil || loaded.InkRevoked != 10000 {
		t.Fatalf("GetRefundByProviderID = %+v, err %v", loaded, err)
	}
	missing, err := repo.GetRefundByProviderID(ctx, "re_missingrefund1")
	if err != nil || missing != nil {
		t.Fatalf("missing refund = %+v, err %v, want nil,nil", missing, err)
	}
}

func TestRefundLedgerDebitsPurchasedOnly(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustBillingAccount(t, ctx, q, "refund-ledger@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	if _, err := pool.Exec(ctx, `INSERT INTO app.wallet_accounts (account_id, balance_free, balance_purchased) VALUES ($1, 5000, 10000) ON CONFLICT (account_id) DO UPDATE SET balance_free = 5000, balance_purchased = 10000`, acc.ID); err != nil {
		t.Fatalf("seed wallet: %v", err)
	}

	ref, _ := domain.ParseReference("refund:re_ledgerdebit1")
	key, _ := domain.ParseIdempotencyKey("refund:re_ledgerdebit1")
	result, err := repo.DebitPurchasedInk(ctx, application.RefundDebitRequest{
		AccountID:   accountID,
		Amount:      10000,
		Reference:   ref,
		Idempotency: key,
	})
	if err != nil || result.Replayed {
		t.Fatalf("first debit = %+v, err %v", result, err)
	}

	wallet, err := q.GetWalletAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("reload wallet: %v", err)
	}
	if wallet.BalancePurchased != 0 || wallet.BalanceFree != 5000 {
		t.Fatalf("balances = %d/%d, want franchise untouched 5000/0", wallet.BalanceFree, wallet.BalancePurchased)
	}

	replay, err := repo.DebitPurchasedInk(ctx, application.RefundDebitRequest{
		AccountID:   accountID,
		Amount:      10000,
		Reference:   ref,
		Idempotency: key,
	})
	if err != nil || !replay.Replayed {
		t.Fatalf("replay debit = %+v, err %v, want replay", replay, err)
	}
	wallet, _ = q.GetWalletAccount(ctx, acc.ID)
	if wallet.BalancePurchased != 0 {
		t.Fatalf("replay debited twice: purchased = %d", wallet.BalancePurchased)
	}

	var operationType, bucket string
	var amount int64
	if err := pool.QueryRow(ctx, `
		SELECT o.operation_type, t.bucket, t.amount
		FROM app.wallet_transactions t
		JOIN app.wallet_operations o ON o.id = t.operation_id
		WHERE o.idempotency_key = 'refund:re_ledgerdebit1'`).Scan(&operationType, &bucket, &amount); err != nil {
		t.Fatalf("load compensating entry: %v", err)
	}
	if operationType != "debit_refund" || bucket != "PURCHASED_INK" || amount != -10000 {
		t.Fatalf("compensating entry = %s/%s/%d, want debit_refund/PURCHASED_INK/-10000", operationType, bucket, amount)
	}
}

func TestRefundPassRevocationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustBillingAccount(t, ctx, q, "refund-passes@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))
	ref, _ := domain.ParseReference("intent-pass-revoke-1")
	granted, err := repo.GrantPassLot(ctx, application.GrantPassLotRequest{
		AccountID: accountID,
		Origin:    domain.OriginPurchase,
		Quantity:  mustRefundQuantity(t, 2),
		Reference: ref,
		GrantedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	lot, err := repo.LotForRefund(ctx, accountID, ref)
	if err != nil || !lot.Found || lot.Remaining != 2 {
		t.Fatalf("LotForRefund = %+v, err %v", lot, err)
	}

	revoked, err := repo.RevokeRemaining(ctx, granted.Lot.ID().String())
	if err != nil || revoked != 2 {
		t.Fatalf("revoke = %d, err %v, want 2", revoked, err)
	}
	stored, err := q.GetArenaPassLot(ctx, mustUUID(t, granted.Lot.ID().String()))
	if err != nil {
		t.Fatalf("reload lot: %v", err)
	}
	if stored.RemainingQuantity != 0 || stored.Quantity != 2 {
		t.Fatalf("lot after revoke = %d/%d, want quantity kept 2 with 0 remaining", stored.Quantity, stored.RemainingQuantity)
	}

	again, err := repo.RevokeRemaining(ctx, granted.Lot.ID().String())
	if err != nil || again != 0 {
		t.Fatalf("second revoke = %d, err %v, want 0 (idempotent)", again, err)
	}
	var consumptions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.arena_pass_consumptions c JOIN app.arena_pass_lots l ON l.id = c.lot_id WHERE l.id = $1`, mustUUID(t, granted.Lot.ID().String())).Scan(&consumptions); err != nil {
		t.Fatalf("count consumptions: %v", err)
	}
	if consumptions != 0 {
		t.Fatalf("revocation created %d consumptions, want 0 (history untouched)", consumptions)
	}
}
