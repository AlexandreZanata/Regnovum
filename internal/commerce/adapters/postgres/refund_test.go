package postgres_test

// P37-T05 — proportional service refunds with explicit shortfall
// obligations on real PostgreSQL.
//
// A liquidated formal payment is compensated in whole or in part
// by a new current transfer reversing the provider share and the
// tithe, linked to its untouched cause: the provider answers up to
// its balance with the shortfall owed explicitly to the buyer,
// never as a hidden negative balance, and the Treasury returns
// exactly the tithe reversal. The tests prove on a disposable
// database: the full reversal with linked cause, replay without
// duplication, accumulated partials refusing beyond the original,
// the broke provider settling tithe plus obligation with S
// conserved, and cancelled refunds rolling back. The suite moves no
// money outside its own ledger.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	commercepg "github.com/AlexandreZanata/Regnovum/internal/commerce/adapters/postgres"
	commerceapp "github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	commercedomain "github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

type serviceRefundKit struct {
	*escrowKit
	refund *commerceapp.RefundServiceUseCase
}

func newServiceRefundKit(t *testing.T, db *dbtest.TestDB, funds int64) *serviceRefundKit {
	t.Helper()
	kit := newEscrowKit(t, db, funds)
	repo, err := commercepg.NewServiceRefundRepository(db.Pool.Pool())
	if err != nil {
		t.Fatalf("NewServiceRefundRepository: %v", err)
	}
	uc, err := commerceapp.NewRefundServiceUseCase(repo)
	if err != nil {
		t.Fatalf("NewRefundServiceUseCase: %v", err)
	}
	return &serviceRefundKit{escrowKit: kit, refund: uc}
}

func (k *serviceRefundKit) fundRelease(t *testing.T, ctx context.Context, key string, amount int64) {
	t.Helper()
	view, err := k.fund.Execute(ctx, commerceapp.FundCommand{
		Key: key, Object: "serviço com reembolso", Buyer: k.buyer, Provider: k.provider,
		AmountMill: amount, ExpiresAt: k.now.Add(time.Hour), Now: k.now,
	})
	if err != nil {
		t.Fatalf("fund %s: %v", key, err)
	}
	_ = view
	if _, err := k.accept.Execute(ctx, key, k.buyer); err != nil {
		t.Fatalf("accept %s: %v", key, err)
	}
	if _, err := k.release.Execute(ctx, key, k.buyer); err != nil {
		t.Fatalf("release %s: %v", key, err)
	}
}

func (k *serviceRefundKit) serviceRefund(t *testing.T, ctx context.Context, contractKey, refundKey string, amount int64) *commerceapp.ServiceRefundResult {
	t.Helper()
	result, err := k.refund.Execute(ctx, commerceapp.ServiceRefundCommand{
		ContractKey: contractKey, Buyer: k.buyer, RefundKey: refundKey, AmountMill: amount,
	})
	if err != nil {
		t.Fatalf("refund %s/%s (%d): %v", contractKey, refundKey, amount, err)
	}
	return result
}

func refundedTotal(t *testing.T, ctx context.Context, db *dbtest.TestDB, contractKey, buyer string) int64 {
	t.Helper()
	var total int64
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(r.amount_milli), 0) FROM app.commerce_service_refunds r
		 JOIN app.commerce_contracts c ON c.id = r.contract_id
		 WHERE c.contract_key = $1 AND c.buyer_id = $2::uuid`, contractKey, buyer).Scan(&total); err != nil {
		t.Fatalf("sum refunded: %v", err)
	}
	return total
}

func obligationOf(t *testing.T, ctx context.Context, db *dbtest.TestDB, refundID string) (int64, bool) {
	t.Helper()
	var amount int64
	err := db.QueryRow(ctx,
		`SELECT amount_milli FROM app.commerce_refund_obligations WHERE refund_id = $1::uuid`, refundID).Scan(&amount)
	if err != nil {
		return 0, false
	}
	return amount, true
}

// TestServiceRefundReversesFullPayment proves the full reversal of
// a 20000 release: the buyer recovers 20000 (18000 provider plus
// 2000 tithe) under one transfer linked to the untouched release,
// with outputs summing to the refunded value.
func TestServiceRefundReversesFullPayment(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newServiceRefundKit(t, db, 100000)
	beforeBuyer := escrowBalance(t, ctx, db, kit.buyer)
	beforeTreasury := titheTreasury(t, ctx, db)
	kit.fundRelease(t, ctx, "refund-full-1", 20000)

	result := kit.serviceRefund(t, ctx, "refund-full-1", "refund-full-1-a", 20000)
	if result.Replayed || result.TitheReversal != 2000 || result.ProviderShare != 18000 || result.ObligationMill != 0 {
		t.Fatalf("result = %+v, want fresh 20000/2000/18000/0", result)
	}
	if got := escrowBalance(t, ctx, db, kit.buyer); got != beforeBuyer {
		t.Fatalf("buyer = %d, want whole %d after full reversal", got, beforeBuyer)
	}
	if got := escrowBalance(t, ctx, db, kit.provider); got != 0 {
		t.Fatalf("provider = %d, want 0 after returning its net", got)
	}
	if got := titheTreasury(t, ctx, db); got != beforeTreasury {
		t.Fatalf("treasury = %d, want %d after returning exactly its tithe", got, beforeTreasury)
	}
	if result.TitheReversal+result.ProviderShare != result.AmountMill {
		t.Fatal("reversal outputs do not sum to the refunded value")
	}
	if refundedTotal(t, ctx, db, "refund-full-1", kit.buyer) != 20000 {
		t.Fatal("linked refunds must sum to the full payment")
	}
}

// TestServiceRefundReplaysAndRefusesOverTotal proves the same key
// and amount replays the original compensation while partials
// accumulate against the original: 6000 plus 14000 settles, any
// further unit refuses without writing.
func TestServiceRefundReplaysAndRefusesOverTotal(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newServiceRefundKit(t, db, 100000)
	kit.fundRelease(t, ctx, "refund-parts-1", 20000)

	first := kit.serviceRefund(t, ctx, "refund-parts-1", "refund-parts-1-a", 6000)
	again, err := kit.refund.Execute(ctx, commerceapp.ServiceRefundCommand{
		ContractKey: "refund-parts-1", Buyer: kit.buyer, RefundKey: "refund-parts-1-a", AmountMill: 6000,
	})
	if err != nil {
		t.Fatalf("replay refund: %v", err)
	}
	if !again.Replayed || again.RefundID != first.RefundID {
		t.Fatal("same key and amount must replay the original compensation")
	}
	divergent := commerceapp.ServiceRefundCommand{
		ContractKey: "refund-parts-1", Buyer: kit.buyer, RefundKey: "refund-parts-1-a", AmountMill: 6001,
	}
	if _, err := kit.refund.Execute(ctx, divergent); !errors.Is(err, commercedomain.ErrIntentionConflict) {
		t.Fatalf("divergent amount = %v, want ErrIntentionConflict", err)
	}
	kit.serviceRefund(t, ctx, "refund-parts-1", "refund-parts-1-b", 14000)
	if refundedTotal(t, ctx, db, "refund-parts-1", kit.buyer) != 20000 {
		t.Fatal("partials 6000+14000 must accumulate to the full payment")
	}
	over := commerceapp.ServiceRefundCommand{
		ContractKey: "refund-parts-1", Buyer: kit.buyer, RefundKey: "refund-parts-1-c", AmountMill: 1,
	}
	if _, err := kit.refund.Execute(ctx, over); !errors.Is(err, commercedomain.ErrRefundExceedsOriginal) {
		t.Fatalf("over total = %v, want ErrRefundExceedsOriginal", err)
	}
	if refundedTotal(t, ctx, db, "refund-parts-1", kit.buyer) != 20000 {
		t.Fatal("refused over-refund must leave the accumulation untouched")
	}
	if _, err := kit.refund.Execute(ctx, commerceapp.ServiceRefundCommand{
		ContractKey: "refund-ghost", Buyer: kit.buyer, RefundKey: "refund-ghost-a", AmountMill: 100,
	}); !errors.Is(err, commercedomain.ErrContractNotFound) {
		t.Fatalf("unknown contract = %v, want ErrContractNotFound", err)
	}
	funded, err := kit.fund.Execute(ctx, commerceapp.FundCommand{
		Key: "refund-unliquidated-1", Object: "sem liquidação", Buyer: kit.buyer, Provider: kit.provider,
		AmountMill: 5000, ExpiresAt: kit.now.Add(time.Hour), Now: kit.now,
	})
	if err != nil {
		t.Fatalf("fund unliquidated: %v", err)
	}
	_ = funded
	if _, err := kit.refund.Execute(ctx, commerceapp.ServiceRefundCommand{
		ContractKey: "refund-unliquidated-1", Buyer: kit.buyer, RefundKey: "refund-unliquidated-1-a", AmountMill: 5000,
	}); !errors.Is(err, commercedomain.ErrContractState) {
		t.Fatalf("unliquidated refund = %v, want ErrContractState", err)
	}
}

// TestServiceRefundWithBrokeProviderRecordsObligation proves the
// broke provider settles what it holds without going negative:
// the Treasury returns its tithe in full, the buyer recovers the
// moved share plus tithe, and the shortfall lands as one explicit
// obligation with S conserved.
func TestServiceRefundWithBrokeProviderRecordsObligation(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newServiceRefundKit(t, db, 100000)
	beforeTreasury := titheTreasury(t, ctx, db)
	kit.fundRelease(t, ctx, "refund-broke-1", 20000)

	stranger := escrowAccount(t, ctx, db)
	provisionCustody(t, ctx, db, stranger)
	limits := commercepg.TransferLimits{MaxAmountMilli: 50000, MaxPerWindow: 1000, WindowMinutes: 60}
	transferRepo, err := commercepg.NewRepository(db.Pool.Pool(), limits)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	transferUC, err := commerceapp.NewTransferUseCase(transferRepo)
	if err != nil {
		t.Fatalf("NewTransferUseCase: %v", err)
	}
	if _, err := transferUC.Execute(ctx, commerceapp.TransferCommand{
		Key: "drain-provider-1", Kind: "gift", Payer: kit.provider, Payee: stranger,
		AmountMilli: 18000, ConsentRef: "consent-drain-1",
	}); err != nil {
		t.Fatalf("drain provider: %v", err)
	}
	if got := escrowBalance(t, ctx, db, kit.provider); got != 0 {
		t.Fatalf("provider = %d, want broke 0 before refund", got)
	}

	result := kit.serviceRefund(t, ctx, "refund-broke-1", "refund-broke-1-a", 20000)
	if result.ObligationMill != 18000 || result.ProviderShare != 18000 || result.TitheReversal != 2000 {
		t.Fatalf("result = %+v, want 20000/2000/18000/18000 owed", result)
	}
	if got := escrowBalance(t, ctx, db, kit.provider); got != 0 {
		t.Fatalf("provider = %d, want 0: shortfall never negativates", got)
	}
	if got := escrowBalance(t, ctx, db, kit.buyer); got != 82000 {
		t.Fatalf("buyer = %d, want 82000 (80000 after funding plus 2000 tithe back)", got)
	}
	if got := titheTreasury(t, ctx, db); got != beforeTreasury {
		t.Fatalf("treasury = %d, want %d after returning exactly its tithe", got, beforeTreasury)
	}
	owed, found := obligationOf(t, ctx, db, result.RefundID)
	if !found || owed != 18000 {
		t.Fatalf("obligation = %d/%v, want explicit 18000 owed to the buyer", owed, found)
	}
	var debtor, creditor string
	if err := db.QueryRow(ctx,
		`SELECT debtor_id::text, creditor_id::text FROM app.commerce_refund_obligations WHERE refund_id = $1::uuid`,
		result.RefundID).Scan(&debtor, &creditor); err != nil {
		t.Fatalf("read obligation parties: %v", err)
	}
	if debtor != kit.provider || creditor != kit.buyer {
		t.Fatal("obligation must name the provider debtor and the buyer creditor")
	}
}

// TestServiceRefundCancelledRollsBack proves a cancelled refund
// settles nothing: no row, no legs, no obligation, balances intact.
func TestServiceRefundCancelledRollsBack(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newServiceRefundKit(t, db, 100000)
	kit.fundRelease(t, ctx, "refund-cancel-1", 20000)
	beforeBuyer := escrowBalance(t, ctx, db, kit.buyer)
	beforeProvider := escrowBalance(t, ctx, db, kit.provider)
	beforeTreasury := titheTreasury(t, ctx, db)

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := kit.refund.Execute(cancelled, commerceapp.ServiceRefundCommand{
		ContractKey: "refund-cancel-1", Buyer: kit.buyer, RefundKey: "refund-cancel-1-a", AmountMill: 5000,
	}); err == nil {
		t.Fatal("cancelled refund must fail, never settle")
	}
	if refundedTotal(t, ctx, db, "refund-cancel-1", kit.buyer) != 0 {
		t.Fatal("cancelled refund must leave no row")
	}
	if escrowBalance(t, ctx, db, kit.buyer) != beforeBuyer ||
		escrowBalance(t, ctx, db, kit.provider) != beforeProvider ||
		titheTreasury(t, ctx, db) != beforeTreasury {
		t.Fatal("cancelled refund must leave all balances untouched")
	}
	var obligations int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.commerce_refund_obligations`).Scan(&obligations); err != nil {
		t.Fatalf("count obligations: %v", err)
	}
	if obligations != 0 {
		t.Fatalf("obligations = %d, want none after rollback", obligations)
	}
}

// TestServiceRefundRacesCollapseToOne proves two simultaneous runs
// of one key settle a single compensation: one executes, one
// replays, with one refund row under -race.
func TestServiceRefundRacesCollapseToOne(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newServiceRefundKit(t, testDB, 100000)
	kit.fundRelease(t, ctx, "refund-race-1", 20000)

	var wg sync.WaitGroup
	type outcome struct {
		result *commerceapp.ServiceRefundResult
		err    error
	}
	outcomes := make([]outcome, 2)
	for i := range outcomes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcomes[i].result, outcomes[i].err = kit.refund.Execute(ctx, commerceapp.ServiceRefundCommand{
				ContractKey: "refund-race-1", Buyer: kit.buyer, RefundKey: "refund-race-1-a", AmountMill: 5000,
			})
		}(i)
	}
	wg.Wait()
	for _, o := range outcomes {
		if o.err != nil {
			t.Fatalf("concurrent refund: %v", o.err)
		}
	}
	if outcomes[0].result.RefundID != outcomes[1].result.RefundID {
		t.Fatal("runners resolved different compensations for one key")
	}
	var rows int
	if err := testDB.QueryRow(ctx,
		`SELECT count(*) FROM app.commerce_service_refunds r
		 JOIN app.commerce_contracts c ON c.id = r.contract_id
		 WHERE c.contract_key = 'refund-race-1'`).Scan(&rows); err != nil {
		t.Fatalf("count refunds: %v", err)
	}
	if rows != 1 {
		t.Fatalf("refund rows = %d, want exactly one compensation", rows)
	}
	if refundedTotal(t, ctx, testDB, "refund-race-1", kit.buyer) != 5000 {
		t.Fatal("race must compensate exactly once")
	}
}
