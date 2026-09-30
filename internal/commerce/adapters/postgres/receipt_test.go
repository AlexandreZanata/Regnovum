package postgres_test

// P37-T07 — private trade receipts and extracts on real PostgreSQL.
//
// One owned formal trade resolves with its gross, its floor(10%)
// tithe, its provider net, its derived escrow status, its instants,
// its identifiers and its compensations, if any. Buyer and provider
// read identical amounts through their own side; strangers and
// unknown ids resolve to absence, and extracts list owned lines
// only. Reads never mutate and never leak. The suite runs on a
// disposable database and activates nothing.

import (
	"testing"

	commercepg "github.com/AlexandreZanata/Regnovum/internal/commerce/adapters/postgres"
	commercedomain "github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func newTradeReceiptsRepo(t *testing.T, db *dbtest.TestDB) *commercepg.TradeReceiptRepository {
	t.Helper()
	repo, err := commercepg.NewTradeReceiptRepository(db.Pool.Pool())
	if err != nil {
		t.Fatalf("NewTradeReceiptRepository: %v", err)
	}
	if _, err := commercepg.NewTradeReceiptRepository(nil); err == nil {
		t.Fatal("nil pool must refuse composition")
	}
	return repo
}

// TestTradeReceiptShowsBothSidesIdenticalAmounts proves buyer and
// provider read the same gross, tithe and net through their own
// side: 20000 releases as 2000 tithe and 18000 net, with the escrow
// status, the instants and the identifiers of the same contract.
func TestTradeReceiptShowsBothSidesIdenticalAmounts(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newServiceRefundKit(t, db, 100000)
	kit.fundRelease(t, ctx, "receipt-both-1", 20000)
	reads := newTradeReceiptsRepo(t, db)

	var contractID string
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM app.commerce_contracts WHERE contract_key = 'receipt-both-1'`).Scan(&contractID); err != nil {
		t.Fatalf("read contract id: %v", err)
	}
	buyerView, err := reads.GetReceipt(ctx, kit.buyer, contractID)
	if err != nil {
		t.Fatalf("buyer receipt: %v", err)
	}
	providerView, err := reads.GetReceipt(ctx, kit.provider, contractID)
	if err != nil {
		t.Fatalf("provider receipt: %v", err)
	}
	if buyerView.GrossMilli != 20000 || providerView.GrossMilli != 20000 {
		t.Fatalf("gross buyer=%d provider=%d, want identical 20000", buyerView.GrossMilli, providerView.GrossMilli)
	}
	if buyerView.TitheMilli != 2000 || providerView.TitheMilli != 2000 {
		t.Fatalf("tithe buyer=%d provider=%d, want identical 2000", buyerView.TitheMilli, providerView.TitheMilli)
	}
	if buyerView.NetMilli != 18000 || providerView.NetMilli != 18000 {
		t.Fatalf("net buyer=%d provider=%d, want identical 18000", buyerView.NetMilli, providerView.NetMilli)
	}
	if buyerView.Status != commercedomain.ContractReleased || providerView.Status != commercedomain.ContractReleased {
		t.Fatalf("status buyer=%q provider=%q, want identical released", buyerView.Status, providerView.Status)
	}
	if buyerView.Role != "buyer" || buyerView.Counterparty != kit.provider {
		t.Fatalf("buyer side = %+v, want role buyer naming the provider", buyerView)
	}
	if providerView.Role != "provider" || providerView.Counterparty != kit.buyer {
		t.Fatalf("provider side = %+v, want role provider naming the buyer", providerView)
	}
	if buyerView.SettledAt == nil || buyerView.SettlementTransferID == nil {
		t.Fatal("released receipt owes the settlement instant and transfer")
	}
	if buyerView.PostedAt.IsZero() || buyerView.ExpiresAt.IsZero() {
		t.Fatal("receipt owes the posting and expiry instants")
	}
}

// TestTradeReceiptRefusesStrangers proves receipts never leak: a
// third party and an unknown id resolve to absence without revealing
// whether the contract exists.
func TestTradeReceiptRefusesStrangers(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newServiceRefundKit(t, db, 100000)
	kit.fundRelease(t, ctx, "receipt-alien-1", 20000)
	reads := newTradeReceiptsRepo(t, db)

	var contractID string
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM app.commerce_contracts WHERE contract_key = 'receipt-alien-1'`).Scan(&contractID); err != nil {
		t.Fatalf("read contract id: %v", err)
	}
	stranger := escrowAccount(t, ctx, db)
	if _, err := reads.GetReceipt(ctx, stranger, contractID); err != commercedomain.ErrContractNotFound {
		t.Fatalf("stranger receipt = %v, want ErrContractNotFound", err)
	}
	if _, err := reads.GetReceipt(ctx, kit.buyer, "00000000-0000-4000-8000-000000000000"); err != commercedomain.ErrContractNotFound {
		t.Fatalf("unknown receipt = %v, want ErrContractNotFound", err)
	}
	if _, err := reads.GetReceipt(ctx, "", contractID); err != commercedomain.ErrContractNotFound {
		t.Fatalf("blank account = %v, want ErrContractNotFound", err)
	}
}

// TestTradeReceiptBeforeReleaseCarriesNoSplit proves unreleased escrow
// never splits: funded contracts carry zero tithe and net with the
// funded status and no settlement transfer.
func TestTradeReceiptBeforeReleaseCarriesNoSplit(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newServiceRefundKit(t, db, 100000)
	funded := kit.fundContract(t, ctx, "receipt-locked-1")
	reads := newTradeReceiptsRepo(t, db)
	receipt, err := reads.GetReceipt(ctx, kit.buyer, funded.ID)
	if err != nil {
		t.Fatalf("locked receipt: %v", err)
	}
	if receipt.GrossMilli != 20000 || receipt.TitheMilli != 0 || receipt.NetMilli != 0 {
		t.Fatalf("locked receipt = %+v, want gross 20000 with zero split", receipt)
	}
	if receipt.Status != commercedomain.ContractFunded || receipt.SettledAt != nil || receipt.SettlementTransferID != nil {
		t.Fatalf("locked receipt = %+v, want funded with no settlement", receipt)
	}
}

// TestTradeStatementListsOwnedLinesOnly proves extracts list owned
// contracts in reverse posting order with identical amounts, while
// strangers read an empty extract.
func TestTradeStatementListsOwnedLinesOnly(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newServiceRefundKit(t, db, 100000)
	kit.fundRelease(t, ctx, "receipt-stmt-1", 20000)
	kit.fundRelease(t, ctx, "receipt-stmt-2", 20000)
	reads := newTradeReceiptsRepo(t, db)

	buyerStmt, err := reads.Statement(ctx, kit.buyer, 50)
	if err != nil {
		t.Fatalf("buyer statement: %v", err)
	}
	if len(buyerStmt.Entries) != 2 {
		t.Fatalf("buyer entries = %d, want the two owned lines", len(buyerStmt.Entries))
	}
	if buyerStmt.Entries[0].ContractKey != "receipt-stmt-2" {
		t.Fatalf("newest first = %q, want reverse posting order", buyerStmt.Entries[0].ContractKey)
	}
	for _, entry := range buyerStmt.Entries {
		if entry.Role != "buyer" || entry.GrossMilli != 20000 || entry.Status != commercedomain.ContractReleased {
			t.Fatalf("buyer entry = %+v, want owned line with identical amounts", entry)
		}
	}
	providerStmt, err := reads.Statement(ctx, kit.provider, 50)
	if err != nil {
		t.Fatalf("provider statement: %v", err)
	}
	if len(providerStmt.Entries) != 2 || providerStmt.Entries[0].Role != "provider" {
		t.Fatalf("provider statement = %+v, want the same two lines through the provider side", providerStmt)
	}
	stranger := escrowAccount(t, ctx, db)
	alienStmt, err := reads.Statement(ctx, stranger, 50)
	if err != nil {
		t.Fatalf("stranger statement: %v", err)
	}
	if len(alienStmt.Entries) != 0 {
		t.Fatalf("stranger entries = %d, want none: extracts never leak", len(alienStmt.Entries))
	}
	if _, err := reads.Statement(ctx, kit.buyer, 0); err != commercedomain.ErrInvalidReceipt {
		t.Fatalf("zero limit = %v, want ErrInvalidReceipt", err)
	}
}

// TestTradeReceiptBindsRefunds proves compensations travel with the
// receipt: the refunded total and the lines behind it.
func TestTradeReceiptBindsRefunds(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newServiceRefundKit(t, db, 100000)
	kit.fundRelease(t, ctx, "receipt-refund-1", 20000)
	kit.serviceRefund(t, ctx, "receipt-refund-1", "receipt-refund-1-a", 6000)
	reads := newTradeReceiptsRepo(t, db)

	var contractID string
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM app.commerce_contracts WHERE contract_key = 'receipt-refund-1'`).Scan(&contractID); err != nil {
		t.Fatalf("read contract id: %v", err)
	}
	receipt, err := reads.GetReceipt(ctx, kit.buyer, contractID)
	if err != nil {
		t.Fatalf("receipt: %v", err)
	}
	if receipt.RefundedMilli != 6000 || len(receipt.Refunds) != 1 {
		t.Fatalf("refunds = %+v, want the 6000 compensation line", receipt)
	}
	line := receipt.Refunds[0]
	if line.AmountMill != 6000 || line.TitheReversal != 600 || line.ProviderShare != 5400 {
		t.Fatalf("refund line = %+v, want the proportional 6000/600/5400 split", line)
	}
}
