package postgres_test

// P33-T06 — refusal exit and legacy refunds on real PostgreSQL.
//
// A refused charter never converts: the holder leaves with a contractual
// fiat reconciliation and a legacy-only revocation of the still-unused
// purchased benefit. The tests prove on a disposable database: full and
// partial refunds with exact proration, contested charges waiting for a
// human, consumed benefit capped without implicit debt, export preserved
// with a fifteen-day answer, replays without duplication, innocent third
// parties untouched, unrefused exits refused and the economy journal
// byte-identical with S conserved.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func refundCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func refundHolder(t *testing.T, ctx context.Context, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ($1, 'active') RETURNING id::text`,
		email).Scan(&id); err != nil {
		t.Fatalf("create holder: %v", err)
	}
	return id
}

func refuseCharter(t *testing.T, ctx context.Context, repo *postgres.Repository, holder, charter string) time.Time {
	t.Helper()
	consents := application.NewConsentUseCase(repo)
	view, err := consents.Execute(ctx, application.ConsentCommand{AccountID: holder, Charter: charter, Decision: "refused"})
	if err != nil {
		t.Fatalf("refuse: %v", err)
	}
	return view.DecidedAt
}

func settleRefusal(t *testing.T, ctx context.Context, repo *postgres.Repository, cmd application.RefusalRefundCommand) *application.RefusalRefundResult {
	t.Helper()
	useCase := application.NewRefusalRefundUseCase(repo)
	result, err := useCase.Execute(ctx, cmd)
	if err != nil {
		t.Fatalf("settle refusal: %v", err)
	}
	return result
}

func refusalCmd(holder, charter string, paid, refunded int64, source, provider string) application.RefusalRefundCommand {
	return application.RefusalRefundCommand{
		AccountID: holder, Charter: charter,
		PaidMinor: paid, RefundedMinor: refunded,
		Source: source, ProviderRefund: provider,
	}
}

func consumePurchased(t *testing.T, ctx context.Context, pool *pgxpool.Pool, holder string, units int64) {
	t.Helper()
	var operation string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference)
		 VALUES ($1::uuid, 'debit_argument', $2, $3) RETURNING id::text`,
		holder, "consume-"+holder, "consume").Scan(&operation); err != nil {
		t.Fatalf("consume operation: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.wallet_transactions (operation_id, bucket, amount) VALUES ($1::uuid, 'PURCHASED_INK', $2)`,
		operation, -units); err != nil {
		t.Fatalf("consume leg: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE app.wallet_accounts SET balance_purchased = balance_purchased - $2 WHERE account_id = $1::uuid`,
		holder, units); err != nil {
		t.Fatalf("rewrite projection: %v", err)
	}
}

func economyOnly(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (legs, sum int64) {
	t.Helper()
	if err := pool.QueryRow(ctx, `SELECT count(*), COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0) FROM app.economy_entries`).Scan(&legs, &sum); err != nil {
		t.Fatalf("fingerprint economy: %v", err)
	}
	return legs, sum
}

// TestRefusalRefundSettlesFullRefund proves the happy path: 100 purchased
// units refunded in full revoke exactly 100, FREE stays untouched, S is
// conserved and the economy journal is identical before and after.
func TestRefusalRefundSettlesFullRefund(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := refundCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	holder := refundHolder(t, ctx, pool, "refund-1@invalid.example")
	fundLegacy(t, ctx, pool, holder, 20, 100)
	decidedAt := refuseCharter(t, ctx, repo, holder, "v1")

	beforeLegs, beforeSum := economyOnly(t, ctx, pool)
	result := settleRefusal(t, ctx, repo, refusalCmd(holder, "v1", 990, 990, "refund", "re_full001"))
	if result.RevokeUnits != 100 || result.NeedsReview || result.Replayed {
		t.Fatalf("full = %+v, want 100 fresh without review", result)
	}
	if result.FiatMinor != 990 {
		t.Fatalf("fiat = %d, want 990", result.FiatMinor)
	}
	if !result.RespondBy.Equal(domain.RefusalResponseDue(decidedAt)) {
		t.Fatalf("deadline = %v, want verdict + 15d", result.RespondBy)
	}
	if free, purchased := legacySums(t, ctx, pool, holder); free != 20 || purchased != 0 {
		t.Fatalf("legacy remainder = %d/%d, want 20/0", free, purchased)
	}
	replayed := settleRefusal(t, ctx, repo, refusalCmd(holder, "v1", 990, 990, "refund", "re_full001"))
	if !replayed.Replayed || replayed.RevokeUnits != 100 {
		t.Fatalf("replay = %+v, want 100 replayed", replayed)
	}
	if free, purchased := legacySums(t, ctx, pool, holder); free != 20 || purchased != 0 {
		t.Fatalf("replay moved legacy: %d/%d", free, purchased)
	}
	afterLegs, afterSum := economyOnly(t, ctx, pool)
	if afterLegs != beforeLegs || afterSum != beforeSum {
		t.Fatalf("economy moved on refusal refund")
	}
	economySupply(t, ctx, pool)
}

// TestRefusalRefundProratesPartialAndCapsConsumed proves proportional
// fiat reconciliation with integer floor, and that consumed benefit caps
// without implicit debt: 100 granted with 30 left and half the price back
// revokes 30 with review, while 100 left and half back revokes 50 clean.
func TestRefusalRefundProratesPartialAndCapsConsumed(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := refundCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)

	capped := refundHolder(t, ctx, pool, "refund-2a@invalid.example")
	fundLegacy(t, ctx, pool, capped, 0, 100)
	consumePurchased(t, ctx, pool, capped, 70)
	refuseCharter(t, ctx, repo, capped, "v1")
	beforeLegs, beforeSum := economyOnly(t, ctx, pool)
	cappedResult := settleRefusal(t, ctx, repo, refusalCmd(capped, "v1", 1000, 500, "refund", "re_part001"))
	if cappedResult.RevokeUnits != 30 || !cappedResult.NeedsReview {
		t.Fatalf("capped = %+v, want 30 with review", cappedResult)
	}
	if cappedResult.ReviewReason != application.RefusalReviewAlreadyConsumed {
		t.Fatalf("reason = %q, want already_consumed", cappedResult.ReviewReason)
	}
	if _, purchased := legacySums(t, ctx, pool, capped); purchased != 0 {
		t.Fatalf("capped remainder = %d, want 0", purchased)
	}
	if afterLegs, afterSum := economyOnly(t, ctx, pool); afterLegs != beforeLegs || afterSum != beforeSum {
		t.Fatalf("economy moved on capped refund")
	}

	clean := refundHolder(t, ctx, pool, "refund-2b@invalid.example")
	fundLegacy(t, ctx, pool, clean, 0, 100)
	refuseCharter(t, ctx, repo, clean, "v1")
	cleanResult := settleRefusal(t, ctx, repo, refusalCmd(clean, "v1", 1000, 500, "refund", "re_part002"))
	if cleanResult.RevokeUnits != 50 || cleanResult.NeedsReview {
		t.Fatalf("partial = %+v, want 50 without review", cleanResult)
	}
	economySupply(t, ctx, pool)
}

// TestRefusalRefundDisputeAlwaysReviews proves a contested charge waits
// for a human even when the whole remainder covers it.
func TestRefusalRefundDisputeAlwaysReviews(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := refundCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	holder := refundHolder(t, ctx, pool, "refund-3@invalid.example")
	fundLegacy(t, ctx, pool, holder, 0, 100)
	refuseCharter(t, ctx, repo, holder, "v1")

	beforeLegs, beforeSum := economyOnly(t, ctx, pool)
	result := settleRefusal(t, ctx, repo, refusalCmd(holder, "v1", 990, 990, "dispute", "dp_contest001"))
	if result.RevokeUnits != 100 || !result.NeedsReview {
		t.Fatalf("dispute = %+v, want 100 with review", result)
	}
	if result.ReviewReason != application.RefusalReviewChargeback {
		t.Fatalf("reason = %q, want chargeback", result.ReviewReason)
	}
	if afterLegs, afterSum := economyOnly(t, ctx, pool); afterLegs != beforeLegs || afterSum != beforeSum {
		t.Fatalf("economy moved on dispute refund")
	}
	economySupply(t, ctx, pool)
}

// TestRefusalRefundPreservesExport proves the refusal exit keeps the
// holder's export alive: the verdict still carries the export right and
// the export row survives the refund with the fifteen-day answer.
func TestRefusalRefundPreservesExport(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := refundCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	holder := refundHolder(t, ctx, pool, "refund-4@invalid.example")
	fundLegacy(t, ctx, pool, holder, 0, 10)
	decidedAt := refuseCharter(t, ctx, repo, holder, "v1")
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.data_exports (account_id, download_token_hash)
		 VALUES ($1::uuid, decode('000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f', 'hex'))`,
		holder); err != nil {
		t.Fatalf("seed export: %v", err)
	}

	result := settleRefusal(t, ctx, repo, refusalCmd(holder, "v1", 100, 100, "refund", "re_export001"))
	if result.RevokeUnits != 10 {
		t.Fatalf("revoke = %d, want 10", result.RevokeUnits)
	}
	var exports int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.data_exports WHERE account_id = $1::uuid`, holder).Scan(&exports); err != nil {
		t.Fatalf("count exports: %v", err)
	}
	if exports != 1 {
		t.Fatalf("exports = %d, want 1 preserved", exports)
	}
	kept := map[string]bool{}
	for _, right := range domain.PreservedRights(domain.ConsentRefused) {
		kept[right] = true
	}
	if !kept["export"] {
		t.Fatalf("refusal drops export")
	}
	if !result.RespondBy.Equal(domain.RefusalResponseDue(decidedAt)) {
		t.Fatalf("deadline = %v, want verdict + 15d", result.RespondBy)
	}
	economySupply(t, ctx, pool)
}

// TestRefusalRefundLeavesInnocentThirdPartyUntouched proves scoped
// settlement: the neighbor's purchased and free balances never move, a
// divergent reuse of one provider object conflicts, and an accepted
// holder cannot exit through a refund.
func TestRefusalRefundLeavesInnocentThirdPartyUntouched(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := refundCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	ana := refundHolder(t, ctx, pool, "refund-5a@invalid.example")
	bia := refundHolder(t, ctx, pool, "refund-5b@invalid.example")
	fundLegacy(t, ctx, pool, ana, 5, 40)
	fundLegacy(t, ctx, pool, bia, 7, 60)
	refuseCharter(t, ctx, repo, ana, "v1")
	refuseCharter(t, ctx, repo, bia, "v1")

	biaFreeBefore, biaPurchasedBefore := legacySums(t, ctx, pool, bia)
	beforeLegs, beforeSum := economyOnly(t, ctx, pool)
	result := settleRefusal(t, ctx, repo, refusalCmd(ana, "v1", 400, 400, "refund", "re_innocent001"))
	if result.RevokeUnits != 40 {
		t.Fatalf("revoke = %d, want 40", result.RevokeUnits)
	}
	if free, purchased := legacySums(t, ctx, pool, bia); free != biaFreeBefore || purchased != biaPurchasedBefore {
		t.Fatalf("innocent moved: %d/%d, want %d/%d", free, purchased, biaFreeBefore, biaPurchasedBefore)
	}
	if afterLegs, afterSum := economyOnly(t, ctx, pool); afterLegs != beforeLegs || afterSum != beforeSum {
		t.Fatalf("economy moved on scoped refund")
	}

	useCase := application.NewRefusalRefundUseCase(repo)
	if _, err := useCase.Execute(ctx, application.RefusalRefundCommand{
		AccountID: bia, Charter: "v1",
		PaidMinor: 400, RefundedMinor: 400,
		Source: "refund", ProviderRefund: "re_innocent001",
	}); !errors.Is(err, domain.ErrIntentionConflict) {
		t.Fatalf("third-party replay = %v, want ErrIntentionConflict", err)
	}
	if _, err := useCase.Execute(ctx, application.RefusalRefundCommand{
		AccountID: ana, Charter: "v1",
		PaidMinor: 400, RefundedMinor: 200,
		Source: "refund", ProviderRefund: "re_innocent001",
	}); !errors.Is(err, domain.ErrIntentionConflict) {
		t.Fatalf("divergent reuse = %v, want ErrIntentionConflict", err)
	}

	accepted := refundHolder(t, ctx, pool, "refund-5c@invalid.example")
	fundLegacy(t, ctx, pool, accepted, 0, 10)
	consents := application.NewConsentUseCase(repo)
	if _, err := consents.Execute(ctx, application.ConsentCommand{AccountID: accepted, Charter: "v1", Decision: "accepted"}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, err := useCase.Execute(ctx, application.RefusalRefundCommand{
		AccountID: accepted, Charter: "v1",
		PaidMinor: 100, RefundedMinor: 100,
		Source: "refund", ProviderRefund: "re_accepted001",
	}); !errors.Is(err, domain.ErrConsentRequired) {
		t.Fatalf("accepted exit = %v, want ErrConsentRequired", err)
	}
	economySupply(t, ctx, pool)
}

// TestRefusalRefundFrozenRefuses proves the frozen book refuses the legacy
// compensation while reads continue.
func TestRefusalRefundFrozenRefuses(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := refundCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	holder := refundHolder(t, ctx, pool, "refund-6@invalid.example")
	fundLegacy(t, ctx, pool, holder, 0, 20)
	refuseCharter(t, ctx, repo, holder, "v1")
	makeCustody(t, ctx, pool, "user", holder)
	freezeWithOrphan(t, ctx, pool, holder)
	if _, err := repo.Reconcile(ctx, domain.SeasonKey(domain.CompatSeasonKey)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	useCase := application.NewRefusalRefundUseCase(repo)
	if _, err := useCase.Execute(ctx, application.RefusalRefundCommand{
		AccountID: holder, Charter: "v1",
		PaidMinor: 200, RefundedMinor: 200,
		Source: "refund", ProviderRefund: "re_frozen001",
	}); !errors.Is(err, domain.ErrEconomyFrozen) {
		t.Fatalf("frozen refund = %v, want ErrEconomyFrozen", err)
	}
}
