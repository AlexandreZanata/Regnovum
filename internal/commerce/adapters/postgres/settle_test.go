package postgres_test

// P37-T03 — contractual escrow settles exactly once on real
// PostgreSQL.
//
// One transaction funds the contract locking the buyer amount in
// exclusive escrow; acceptance, provider payment, buyer refund,
// lapse marking and competent resolution advance the lifecycle
// without ever rewriting history. The tests prove on a disposable
// database: the funded hold with locked balance, no unilateral
// release, conflicting releases paying once, expiry delivering
// nothing, resolution by decision either way, replay without
// duplication and unknown contracts refused. The suite moves no
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

func escrowCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func escrowNow() time.Time {
	return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
}

func escrowAccount(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('escrow-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("create account: %v", err)
	}
	return id
}

type escrowKit struct {
	repo     *commercepg.EscrowRepository
	fund     *commerceapp.FundContractUseCase
	accept   *commerceapp.AcceptDeliveryUseCase
	release  *commerceapp.ReleaseContractUseCase
	cancel   *commerceapp.CancelContractUseCase
	expire   *commerceapp.ExpireContractUseCase
	resolve  *commerceapp.ResolveContractUseCase
	buyer    string
	provider string
	now      time.Time
}

func newEscrowKit(t *testing.T, db *dbtest.TestDB, funds int64) *escrowKit {
	t.Helper()
	repo, err := commercepg.NewEscrowRepository(db.Pool.Pool())
	if err != nil {
		t.Fatalf("NewEscrowRepository: %v", err)
	}
	must := func(uc any, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("wire use case: %v", err)
		}
		_ = uc
	}
	fund, err := commerceapp.NewFundContractUseCase(repo)
	must(fund, err)
	accept, err := commerceapp.NewAcceptDeliveryUseCase(repo)
	must(accept, err)
	release, err := commerceapp.NewReleaseContractUseCase(repo)
	must(release, err)
	cancelUC, err := commerceapp.NewCancelContractUseCase(repo)
	must(cancelUC, err)
	expire, err := commerceapp.NewExpireContractUseCase(repo)
	must(expire, err)
	resolve, err := commerceapp.NewResolveContractUseCase(repo)
	must(resolve, err)
	ctx, cancel := escrowCtx()
	defer cancel()
	buyer := escrowAccount(t, ctx, db)
	provider := escrowAccount(t, ctx, db)
	fundCommerceHolder(t, ctx, db, buyer, funds)
	provisionCustody(t, ctx, db, provider)
	return &escrowKit{
		repo: repo, fund: fund, accept: accept, release: release,
		cancel: cancelUC, expire: expire, resolve: resolve,
		buyer: buyer, provider: provider, now: escrowNow(),
	}
}

func (k *escrowKit) fundContract(t *testing.T, ctx context.Context, key string) *commerceapp.ContractView {
	t.Helper()
	view, err := k.fund.Execute(ctx, commerceapp.FundCommand{
		Key: key, Object: "revisão de contrato", Buyer: k.buyer, Provider: k.provider,
		AmountMill: 20000, ExpiresAt: k.now.Add(time.Hour), Now: k.now,
	})
	if err != nil {
		t.Fatalf("fund %s: %v", key, err)
	}
	if view.Status != commercedomain.ContractFunded {
		t.Fatalf("status = %q, want funded", view.Status)
	}
	return view
}

func provisionCustody(t *testing.T, ctx context.Context, db *dbtest.TestDB, account string) {
	t.Helper()
	if _, err := db.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('user', $1) ON CONFLICT DO NOTHING`, account); err != nil {
		t.Fatalf("provision custody: %v", err)
	}
}

func escrowBalance(t *testing.T, ctx context.Context, db *dbtest.TestDB, account string) int64 {
	t.Helper()
	var balance int64
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = 'user' AND c.label = $1`, account).Scan(&balance); err != nil {
		t.Fatalf("balance %s: %v", account, err)
	}
	return balance
}

// TestEscrowFundAcceptRelease proves the funded hold with the locked
// balance, the buyer acceptance and the exact provider payment with
// paired legs.
func TestEscrowFundAcceptRelease(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newEscrowKit(t, db, 100000)
	funded := kit.fundContract(t, ctx, "escrow-journey-1")
	if escrowBalance(t, ctx, db, kit.buyer) != 80000 {
		t.Fatal("funding must debit the buyer into escrow")
	}
	accepted, err := kit.accept.Execute(ctx, "escrow-journey-1", kit.buyer)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted.Status != commercedomain.ContractAccepted {
		t.Fatalf("status = %q, want accepted", accepted.Status)
	}
	if escrowBalance(t, ctx, db, kit.buyer) != 80000 {
		t.Fatal("acceptance moves no legs")
	}
	released, err := kit.release.Execute(ctx, "escrow-journey-1", kit.buyer)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if released.Status != commercedomain.ContractReleased {
		t.Fatalf("status = %q, want released", released.Status)
	}
	if escrowBalance(t, ctx, db, kit.provider) != 20000 {
		t.Fatal("release must pay the provider exactly")
	}
	_ = funded
}

// TestEscrowRefusesUnilateralRelease proves releases without buyer
// acceptance, by strangers and twice all refuse with the escrow
// intact.
func TestEscrowRefusesUnilateralRelease(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newEscrowKit(t, db, 100000)
	kit.fundContract(t, ctx, "escrow-unilateral-1")
	if _, err := kit.release.Execute(ctx, "escrow-unilateral-1", kit.buyer); !errors.Is(err, commercedomain.ErrContractState) {
		t.Fatalf("release before acceptance = %v, want ErrContractState", err)
	}
	if _, err := kit.accept.Execute(ctx, "escrow-unilateral-1", kit.provider); !errors.Is(err, commercedomain.ErrContractNotFound) {
		t.Fatalf("provider self-acceptance = %v, want ErrContractNotFound: foreign contracts stay unaddressable", err)
	}
	stranger := escrowAccount(t, ctx, db)
	if _, err := kit.accept.Execute(ctx, "escrow-unilateral-1", stranger); !errors.Is(err, commercedomain.ErrContractNotFound) {
		t.Fatalf("stranger acceptance = %v, want ErrContractNotFound", err)
	}
	if _, err := kit.accept.Execute(ctx, "escrow-unilateral-1", kit.buyer); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, err := kit.release.Execute(ctx, "escrow-unilateral-1", kit.buyer); err != nil {
		t.Fatalf("release: %v", err)
	}
	replayed, err := kit.release.Execute(ctx, "escrow-unilateral-1", kit.buyer)
	if err != nil {
		t.Fatalf("repeated release = %v, want idempotent replay of the single payment", err)
	}
	if replayed.Status != commercedomain.ContractReleased {
		t.Fatalf("replay status = %q, want released", replayed.Status)
	}
	if escrowBalance(t, ctx, db, kit.provider) != 20000 {
		t.Fatal("exactly one provider payment must exist")
	}
	var releases int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM app.commerce_settlements s
		 JOIN app.commerce_contracts c ON c.id = s.contract_id
		 WHERE c.contract_key = 'escrow-unilateral-1' AND s.action = 'release'`).Scan(&releases); err != nil {
		t.Fatalf("count releases: %v", err)
	}
	if releases != 1 {
		t.Fatalf("release rows = %d, want exactly one payment", releases)
	}
}

// TestEscrowConflictingOrdersSettleOnce proves two simultaneous
// release orders resolve the single payment without paying twice,
// while a cancellation racing an acceptance loses on the moved
// state: release needs acceptance, cancellation needs funding, so
// the machine never arms both at once.
func TestEscrowConflictingOrdersSettleOnce(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newEscrowKit(t, testDB, 100000)
	kit.fundContract(t, ctx, "escrow-race-1")
	if _, err := kit.accept.Execute(ctx, "escrow-race-1", kit.buyer); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, err := kit.cancel.Execute(ctx, "escrow-race-1", kit.buyer); !errors.Is(err, commercedomain.ErrContractState) {
		t.Fatalf("cancel after acceptance = %v, want ErrContractState", err)
	}
	var wg sync.WaitGroup
	type outcome struct {
		view *commerceapp.ContractView
		err  error
	}
	outcomes := make([]outcome, 2)
	for i := range outcomes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcomes[i].view, outcomes[i].err = kit.release.Execute(ctx, "escrow-race-1", kit.buyer)
		}(i)
	}
	wg.Wait()
	for _, o := range outcomes {
		if o.err != nil {
			t.Fatalf("repeated release = %v, want idempotent replay of the single payment", o.err)
		}
	}
	if escrowBalance(t, ctx, testDB, kit.provider) != 20000 {
		t.Fatal("conflicting orders must pay exactly once")
	}
	var releases int
	if err := testDB.QueryRow(ctx,
		`SELECT count(*) FROM app.commerce_settlements s
		 JOIN app.commerce_contracts c ON c.id = s.contract_id
		 WHERE c.contract_key = 'escrow-race-1' AND s.action = 'release'`).Scan(&releases); err != nil {
		t.Fatalf("count releases: %v", err)
	}
	if releases != 1 {
		t.Fatalf("release rows = %d, want exactly one payment", releases)
	}
}

// TestEscrowCancelRefundsBuyer proves buyer cancellation before
// acceptance returns the full amount with paired legs.
func TestEscrowCancelRefundsBuyer(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newEscrowKit(t, db, 100000)
	kit.fundContract(t, ctx, "escrow-cancel-1")
	refunded, err := kit.cancel.Execute(ctx, "escrow-cancel-1", kit.buyer)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if refunded.Status != commercedomain.ContractRefunded {
		t.Fatalf("status = %q, want refunded", refunded.Status)
	}
	if escrowBalance(t, ctx, db, kit.buyer) != 100000 {
		t.Fatal("cancellation must return the buyer whole")
	}
}

// TestEscrowExpiryNeedsDecision proves lapse marking moves nothing
// and only a competent resolution settles afterwards, either way.
func TestEscrowExpiryNeedsDecision(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newEscrowKit(t, db, 100000)
	kit.fundContract(t, ctx, "escrow-expire-1")
	lapsedAt := kit.now.Add(2 * time.Hour)
	expired, err := kit.expire.Execute(ctx, "escrow-expire-1", kit.buyer, lapsedAt)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if expired.Status != commercedomain.ContractExpired {
		t.Fatalf("status = %q, want expired", expired.Status)
	}
	if escrowBalance(t, ctx, db, kit.buyer) != 80000 {
		t.Fatal("expiry must not deliver by itself")
	}
	if _, err := kit.release.Execute(ctx, "escrow-expire-1", kit.buyer); !errors.Is(err, commercedomain.ErrContractState) {
		t.Fatalf("release after expiry = %v, want ErrContractState", err)
	}
	resolved, err := kit.resolve.Execute(ctx, "escrow-expire-1", kit.buyer, "arbiter-1", "release")
	if err != nil {
		t.Fatalf("resolve release: %v", err)
	}
	if resolved.Status != commercedomain.ContractResolved {
		t.Fatalf("status = %q, want resolved", resolved.Status)
	}
	if escrowBalance(t, ctx, db, kit.provider) != 20000 {
		t.Fatal("competent release must pay the provider")
	}

	kit.fundContract(t, ctx, "escrow-expire-2")
	if _, err := kit.expire.Execute(ctx, "escrow-expire-2", kit.buyer, lapsedAt); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if _, err := kit.resolve.Execute(ctx, "escrow-expire-2", kit.buyer, "arbiter-1", "refund"); err != nil {
		t.Fatalf("resolve refund: %v", err)
	}
	if escrowBalance(t, ctx, db, kit.buyer) != 80000 {
		t.Fatalf("buyer = %d, want 80000 after one release and one refund", escrowBalance(t, ctx, db, kit.buyer))
	}
}

// TestEscrowFundReplaysWithoutDuplication proves the same funding
// key resolves the original contract while divergent terms
// conflict, and unknown contracts refuse on every transition.
func TestEscrowFundReplaysWithoutDuplication(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newEscrowKit(t, db, 100000)
	first := kit.fundContract(t, ctx, "escrow-replay-1")
	again, err := kit.fund.Execute(ctx, kitFundCommand(kit, "escrow-replay-1"))
	if err != nil {
		t.Fatalf("replay fund: %v", err)
	}
	if again.ID != first.ID {
		t.Fatal("same key and terms must replay the original contract")
	}
	divergent := kitFundCommand(kit, "escrow-replay-1")
	divergent.AmountMill = 1
	if _, err := kit.fund.Execute(ctx, divergent); !errors.Is(err, commercedomain.ErrIntentionConflict) {
		t.Fatalf("divergent fund = %v, want ErrIntentionConflict", err)
	}
	for _, op := range []func() error{
		func() error { _, err := kit.accept.Execute(ctx, "escrow-ghost", kit.buyer); return err },
		func() error { _, err := kit.release.Execute(ctx, "escrow-ghost", kit.buyer); return err },
		func() error { _, err := kit.cancel.Execute(ctx, "escrow-ghost", kit.buyer); return err },
	} {
		if err := op(); !errors.Is(err, commercedomain.ErrContractNotFound) {
			t.Fatalf("unknown contract = %v, want ErrContractNotFound", err)
		}
	}
}

func kitFundCommand(kit *escrowKit, key string) commerceapp.FundCommand {
	return commerceapp.FundCommand{
		Key: key, Object: "revisão de contrato", Buyer: kit.buyer, Provider: kit.provider,
		AmountMill: 20000, ExpiresAt: kit.now.Add(time.Hour), Now: kit.now,
	}
}
