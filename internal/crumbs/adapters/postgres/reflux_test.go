package postgres_test

// P38-T02 — classified regular treasury reflux on real PostgreSQL.
//
// Own services plus tithe minus corresponding refunds, summed by the
// database posted instant inside one [start, end) window: settled
// publications, Treasury credits under liquidating settlements and
// both refund kinds count; sales, seizures, death, corrections,
// vault moves, gifts, holds and unknown transfers never enter,
// fail-closed by construction. An independent UNION ALL oracle
// recomputes every window by transfer instead of by origin. Late
// events land in their posted week, refunds settle in theirs even
// when the cause is older, and refund-only weeks net negative by
// design. The suite runs on a disposable database and writes nothing
// but its fixtures.

import (
	"context"
	"errors"
	"testing"
	"time"

	commercepg "github.com/AlexandreZanata/Regnovum/internal/commerce/adapters/postgres"
	commerceapp "github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	crumbspg "github.com/AlexandreZanata/Regnovum/internal/crumbs/adapters/postgres"
	crumbsdomain "github.com/AlexandreZanata/Regnovum/internal/crumbs/domain"
	economypg "github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	economyapp "github.com/AlexandreZanata/Regnovum/internal/economy/application"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func refluxCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func refluxAccount(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('reflux-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("create account: %v", err)
	}
	return id
}

func refluxFund(t *testing.T, ctx context.Context, db *dbtest.TestDB, account string, funds int64) {
	t.Helper()
	pool := db.Pool.Pool()
	repo := economypg.NewRepository(pool)
	key, err := economydomain.ParseGenesisKey("crumbs-reflux-funding")
	if err != nil {
		t.Fatalf("ParseGenesisKey: %v", err)
	}
	if _, err := repo.RunGenesis(ctx, economyapp.GenesisRequest{Key: key}); err != nil {
		if !errors.Is(err, economydomain.ErrGenesisAlreadyExists) {
			t.Fatalf("seed Genesis: %v", err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('user', $1) ON CONFLICT DO NOTHING`, account); err != nil {
		t.Fatalf("create custody: %v", err)
	}
	amount, err := economydomain.NewMilliInk(funds)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	fromKind, _ := economydomain.ParseCustodyKind("treasury")
	toKind, _ := economydomain.ParseCustodyKind("user")
	if _, err := repo.Transfer(ctx, economyapp.TransferRequest{
		FromKind: fromKind, FromLabel: "main", ToKind: toKind, ToLabel: account, Amount: amount,
	}); err != nil {
		t.Fatalf("fund holder: %v", err)
	}
}

func refluxCustody(t *testing.T, ctx context.Context, db *dbtest.TestDB, kind, label string) string {
	t.Helper()
	var id string
	if _, err := db.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ($1, $2) ON CONFLICT DO NOTHING`, kind, label); err != nil {
		t.Fatalf("provision custody %s/%s: %v", kind, label, err)
	}
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = $1 AND label = $2`, kind, label).Scan(&id); err != nil {
		t.Fatalf("read custody %s/%s: %v", kind, label, err)
	}
	return id
}

func refluxTransfer(t *testing.T, ctx context.Context, db *dbtest.TestDB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("mint transfer: %v", err)
	}
	return id
}

func refluxLeg(t *testing.T, ctx context.Context, db *dbtest.TestDB, transfer, custody, direction string, amount int64) {
	t.Helper()
	if _, err := db.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, $3, $4)`, transfer, custody, direction, amount); err != nil {
		t.Fatalf("record leg: %v", err)
	}
}

// seedRefluxPublication posts one settled service charge in an
// explicit week: the publication row plus its paired legs.
func seedRefluxPublication(t *testing.T, ctx context.Context, db *dbtest.TestDB, key, holder string, amount int64, posted time.Time) {
	t.Helper()
	transfer := refluxTransfer(t, ctx, db)
	if _, err := db.Exec(ctx,
		`INSERT INTO app.metering_publications
		 (intention_key, account_label, service, price_version, units, amount_milli,
		  content_hash, quote_hash, payload_hash, transfer_id, posted_at)
		 VALUES ($1, $2, 'reflux-service', 1, 10, $3, 'content-seal', 'quote-seal', 'payload-seal', $4::uuid, $5)`,
		key, holder, amount, transfer, posted); err != nil {
		t.Fatalf("seed publication: %v", err)
	}
	refluxLeg(t, ctx, db, transfer, refluxCustody(t, ctx, db, "user", holder), "debit", amount)
	refluxLeg(t, ctx, db, transfer, refluxCustody(t, ctx, db, "treasury", "main"), "credit", amount)
}

// seedRefluxRelease posts one liquidated formal payment in an
// explicit week: the contract, its release settlement and the
// floor(10%) split legs.
func seedRefluxRelease(t *testing.T, ctx context.Context, db *dbtest.TestDB, key, buyer, provider string, amount int64, posted time.Time) {
	t.Helper()
	transfer := refluxTransfer(t, ctx, db)
	escrow := refluxTransfer(t, ctx, db)
	var contract string
	if err := db.QueryRow(ctx,
		`INSERT INTO app.commerce_contracts
		 (contract_key, kind, buyer_id, provider_id, object, amount_milli,
		  terms_hash, escrow_transfer_id, expires_at, posted_at)
		 VALUES ($1, 'trade', $2::uuid, $3::uuid, 'serviço com refluxo', $4, 'terms-seal', $5::uuid, $6, $7)
		 RETURNING id::text`,
		key, buyer, provider, amount, escrow, posted.Add(time.Hour), posted).Scan(&contract); err != nil {
		t.Fatalf("seed contract: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO app.commerce_settlements (contract_id, action, transfer_id, posted_at)
		 VALUES ($1::uuid, 'accept', NULL, $2), ($1::uuid, 'release', $3::uuid, $4)`, contract, posted.Add(-time.Hour), transfer, posted); err != nil {
		t.Fatalf("seed settlement: %v", err)
	}
	tithe := amount / 10
	refluxLeg(t, ctx, db, transfer, refluxCustody(t, ctx, db, "escrow", "reflux-"+key), "debit", amount)
	refluxLeg(t, ctx, db, transfer, refluxCustody(t, ctx, db, "user", provider), "credit", amount-tithe)
	refluxLeg(t, ctx, db, transfer, refluxCustody(t, ctx, db, "treasury", "main"), "credit", tithe)
}

// seedRefluxMeteringRefund posts one publication compensation in an
// explicit week, linked to its cause.
func seedRefluxMeteringRefund(t *testing.T, ctx context.Context, db *dbtest.TestDB, key, holder, original string, amount int64, posted time.Time) {
	t.Helper()
	transfer := refluxTransfer(t, ctx, db)
	var cause string
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM app.metering_publications WHERE intention_key = $1`, original).Scan(&cause); err != nil {
		t.Fatalf("read cause: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO app.metering_refunds
		 (refund_key, account_label, original_id, amount_milli, transfer_id, reason, posted_at)
		 VALUES ($1, $2, $3::uuid, $4, $5::uuid, 'refluxo tardio', $6)`,
		key, holder, cause, amount, transfer, posted); err != nil {
		t.Fatalf("seed metering refund: %v", err)
	}
	refluxLeg(t, ctx, db, transfer, refluxCustody(t, ctx, db, "treasury", "main"), "debit", amount)
	refluxLeg(t, ctx, db, transfer, refluxCustody(t, ctx, db, "user", holder), "credit", amount)
}

// seedRefluxServiceRefund posts one service tithe reversal in an
// explicit week, linked to its liquidated cause with the paired
// legs: the provider share and the Treasury reversal return to the
// buyer together.
func seedRefluxServiceRefund(t *testing.T, ctx context.Context, db *dbtest.TestDB, contractKey, refundKey string, amount int64, posted time.Time) {
	t.Helper()
	transfer := refluxTransfer(t, ctx, db)
	var contract, buyer, provider string
	if err := db.QueryRow(ctx,
		`SELECT id::text, buyer_id::text, provider_id::text FROM app.commerce_contracts WHERE contract_key = $1`, contractKey).Scan(&contract, &buyer, &provider); err != nil {
		t.Fatalf("read contract: %v", err)
	}
	tithe := amount / 10
	share := amount - tithe
	if _, err := db.Exec(ctx,
		`INSERT INTO app.commerce_service_refunds
		 (contract_id, refund_key, amount_milli, tithe_reversal_milli, provider_share_milli, transfer_id, posted_at)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7)`,
		contract, refundKey, amount, tithe, share, transfer, posted); err != nil {
		t.Fatalf("seed service refund: %v", err)
	}
	refluxLeg(t, ctx, db, transfer, refluxCustody(t, ctx, db, "user", provider), "debit", share)
	refluxLeg(t, ctx, db, transfer, refluxCustody(t, ctx, db, "treasury", "main"), "debit", tithe)
	refluxLeg(t, ctx, db, transfer, refluxCustody(t, ctx, db, "user", buyer), "credit", amount)
}

// refluxOracle recomputes the net by transfer instead of by origin:
// one UNION ALL over classified transfers with signed amounts. Any
// structural drift between the two paths surfaces here.
func refluxOracle(t *testing.T, ctx context.Context, db *dbtest.TestDB, start, end time.Time) int64 {
	t.Helper()
	var net int64
	if err := db.QueryRow(ctx,
		`WITH classified AS (
		   SELECT p.transfer_id AS tid, p.amount_milli AS amount, 1 AS sign
		   FROM app.metering_publications p WHERE p.posted_at >= $1 AND p.posted_at < $2
		   UNION ALL
		   SELECT s.transfer_id, e.amount_milli, 1
		   FROM app.commerce_settlements s
		   JOIN app.economy_entries e ON e.transfer_id = s.transfer_id
		   JOIN app.economy_custodies c ON c.id = e.custody_id
		   WHERE s.action IN ('release', 'resolve-release')
		     AND s.posted_at >= $1 AND s.posted_at < $2
		     AND c.kind = 'treasury' AND e.direction = 'credit'
		   UNION ALL
		   SELECT r.transfer_id, r.amount_milli, -1
		   FROM app.metering_refunds r WHERE r.posted_at >= $1 AND r.posted_at < $2
		   UNION ALL
		   SELECT r.transfer_id, r.tithe_reversal_milli, -1
		   FROM app.commerce_service_refunds r WHERE r.posted_at >= $1 AND r.posted_at < $2
		 )
		 SELECT COALESCE(SUM(amount * sign), 0) FROM classified`, start, end).Scan(&net); err != nil {
		t.Fatalf("oracle sum: %v", err)
	}
	return net
}

func checkRefluxWindow(t *testing.T, ctx context.Context, db *dbtest.TestDB, repo *crumbspg.RefluxRepository, start, end time.Time, want wantReflux) {
	t.Helper()
	got, err := repo.RegularReflux(ctx, start, end)
	if err != nil {
		t.Fatalf("RegularReflux: %v", err)
	}
	if got.ServiceCharges != want.services || got.Tithe != want.tithe ||
		got.MeteringReversals != want.meteringBack || got.TitheReversals != want.titheBack {
		t.Fatalf("classes = %+v, want %+v", got, want)
	}
	if net := want.services + want.tithe - want.meteringBack - want.titheBack; got.Net != net {
		t.Fatalf("net = %d, want %d", got.Net, net)
	}
	if oracle := refluxOracle(t, ctx, db, start, end); oracle != got.Net {
		t.Fatalf("oracle = %d vs adapter %d: two paths must agree", oracle, got.Net)
	}
}

// wantReflux carries one expected classified net: the four regular
// classes of a window.
type wantReflux struct {
	services     int64
	tithe        int64
	meteringBack int64
	titheBack    int64
}

// TestRegularRefluxNetsPostedWeeks proves the classified net across
// four explicit weeks: an old liquidation, a negative refund-only
// week, a mixed week with a late event, and the live week settled
// through the real use cases beside unknown transfers that never
// count.
func TestRegularRefluxNetsPostedWeeks(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := refluxCtx()
	defer cancel()

	buyer := refluxAccount(t, ctx, db)
	provider := refluxAccount(t, ctx, db)
	bystander := refluxAccount(t, ctx, db)
	refluxFund(t, ctx, db, buyer, 500000)
	refluxFund(t, ctx, db, provider, 500000)
	refluxFund(t, ctx, db, bystander, 500000)

	now := time.Now().UTC()
	epoch := crumbsdomain.EpochOf(now)
	w0start, err := epoch.Start()
	if err != nil {
		t.Fatalf("epoch start: %v", err)
	}
	w0end, err := epoch.End()
	if err != nil {
		t.Fatalf("epoch end: %v", err)
	}
	at := func(weeksBack int, offset time.Duration) time.Time {
		return w0start.AddDate(0, 0, -7*weeksBack).Add(offset)
	}

	repo, err := crumbspg.NewRefluxRepository(db.Pool.Pool())
	if err != nil {
		t.Fatalf("NewRefluxRepository: %v", err)
	}
	if _, err := crumbspg.NewRefluxRepository(nil); err == nil {
		t.Fatal("nil pool must refuse composition")
	}

	// Live week first, through the real use cases: release 20000
	// with tithe 2000, partial service refund 6000 with reversal
	// 600, one gift and one treasury funding that never count.
	escrows, err := commercepg.NewEscrowRepository(db.Pool.Pool())
	if err != nil {
		t.Fatalf("NewEscrowRepository: %v", err)
	}
	fundUC, _ := commerceapp.NewFundContractUseCase(escrows)
	acceptUC, _ := commerceapp.NewAcceptDeliveryUseCase(escrows)
	releaseUC, _ := commerceapp.NewReleaseContractUseCase(escrows)
	// Truncated to the second: the seal binds expiry at nanosecond
	// precision while timestamptz stores microseconds.
	holderNow := time.Now().UTC().Truncate(time.Second)
	if _, err := fundUC.Execute(ctx, commerceapp.FundCommand{
		Key: "reflux-live-1", Object: "serviço com refluxo", Buyer: buyer, Provider: provider,
		AmountMill: 20000, ExpiresAt: holderNow.Add(time.Hour), Now: holderNow,
	}); err != nil {
		t.Fatalf("fund live: %v", err)
	}
	if _, err := acceptUC.Execute(ctx, "reflux-live-1", buyer); err != nil {
		t.Fatalf("accept live: %v", err)
	}
	if _, err := releaseUC.Execute(ctx, "reflux-live-1", buyer); err != nil {
		t.Fatalf("release live: %v", err)
	}
	refundRepo, err := commercepg.NewServiceRefundRepository(db.Pool.Pool())
	if err != nil {
		t.Fatalf("NewServiceRefundRepository: %v", err)
	}
	refundUC, err := commerceapp.NewRefundServiceUseCase(refundRepo)
	if err != nil {
		t.Fatalf("NewRefundServiceUseCase: %v", err)
	}
	if _, err := refundUC.Execute(ctx, commerceapp.ServiceRefundCommand{
		ContractKey: "reflux-live-1", Buyer: buyer, RefundKey: "reflux-live-r1", AmountMill: 6000,
	}); err != nil {
		t.Fatalf("refund live: %v", err)
	}
	transferRepo, err := commercepg.NewRepository(db.Pool.Pool(), commercepg.TransferLimits{MaxAmountMilli: 50000, MaxPerWindow: 1000, WindowMinutes: 60})
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	transferUC, err := commerceapp.NewTransferUseCase(transferRepo)
	if err != nil {
		t.Fatalf("NewTransferUseCase: %v", err)
	}
	if _, err := transferUC.Execute(ctx, commerceapp.TransferCommand{
		Key: "reflux-gift-1", Kind: "gift", Payer: buyer, Payee: provider,
		AmountMilli: 5000, ConsentRef: "consent-reflux-gift-1",
	}); err != nil {
		t.Fatalf("settle gift: %v", err)
	}
	checkRefluxWindow(t, ctx, db, repo, w0start, w0end, wantReflux{tithe: 2000, titheBack: 600})

	// Older weeks land late: insertion order never decides, posted_at
	// does. W-3 liquidates 20000, W-2 reverses 6000 of it, W-1 mixes
	// a 10000 service charge with a 20000 liquidation and a 3000
	// publication reversal.
	seedRefluxRelease(t, ctx, db, "reflux-old-1", buyer, provider, 20000, at(3, 2*time.Hour))
	seedRefluxServiceRefund(t, ctx, db, "reflux-old-1", "reflux-old-r1", 6000, at(2, 3*time.Hour))
	seedRefluxPublication(t, ctx, db, "reflux-pub-1", buyer, 10000, at(1, time.Hour))
	seedRefluxRelease(t, ctx, db, "reflux-mix-1", buyer, provider, 20000, at(1, 2*time.Hour))
	seedRefluxMeteringRefund(t, ctx, db, "reflux-mref-1", buyer, "reflux-pub-1", 3000, at(1, 3*time.Hour))

	checkRefluxWindow(t, ctx, db, repo, w0start, w0end, wantReflux{tithe: 2000, titheBack: 600})
	checkRefluxWindow(t, ctx, db, repo, at(1, 0), at(0, 0), wantReflux{services: 10000, tithe: 2000, meteringBack: 3000})
	checkRefluxWindow(t, ctx, db, repo, at(2, 0), at(1, 0), wantReflux{titheBack: 600})
	checkRefluxWindow(t, ctx, db, repo, at(3, 0), at(2, 0), wantReflux{tithe: 2000})
	checkRefluxWindow(t, ctx, db, repo, w0end, w0end.AddDate(0, 0, 7), wantReflux{})
}

// TestRegularRefluxRefusesBadWindows proves malformed queries settle
// nothing: empty, inverted and zero windows refuse before any read.
func TestRegularRefluxRefusesBadWindows(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := refluxCtx()
	defer cancel()

	repo, err := crumbspg.NewRefluxRepository(db.Pool.Pool())
	if err != nil {
		t.Fatalf("NewRefluxRepository: %v", err)
	}
	now := time.Now().UTC()
	for _, window := range []struct {
		start time.Time
		end   time.Time
	}{
		{now, now},
		{now.Add(time.Hour), now},
		{time.Time{}, now},
		{now, time.Time{}},
	} {
		if _, err := repo.RegularReflux(ctx, window.start, window.end); !errors.Is(err, crumbsdomain.ErrInvalidReflux) {
			t.Fatalf("window %+v = nil, want ErrInvalidReflux", window)
		}
	}
}
