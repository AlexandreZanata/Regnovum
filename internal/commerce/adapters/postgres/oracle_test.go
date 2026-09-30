package postgres_test

// P37-T09 — independent exact-once oracle on real PostgreSQL.
//
// The oracle is a pure in-memory model sharing no settlement code
// with the adapters: settled transfers keyed by payer and token,
// contracts with their lifecycle status, compensated causes keyed by
// contract and refund token, reviews with their status, and the
// expected buyer/provider/Treasury moves with integer division as
// the only pricing rule. Deterministic scripts drive the model and
// the PostgreSQL use cases step by step (3.000 operations across 60
// seeded sequences) over gifts, funding, acceptance, release,
// cancellation, expiry, competent resolution, proportional refunds
// and disguise reviews, under crashes, replays and adversarial
// order; after every step the outcome class and the running balances
// must agree, the supply S must hold and the innocent third party
// must stand untouched. The final check replays the whole database
// against the model: pairings, at-most-once tithe per contract,
// gifts without Treasury legs, linked refunds and owned statuses.
// Injected mutants (rate surcharge, dust floor, misclassified gift,
// legless refund) prove the comparison bites, and no case is
// excluded.
//
// Comparison boundaries, stated once: transfer ids and posted_at are
// excluded because the database mints them; payload hashes are
// excluded because the conflict outcome already proves them; expiry
// margins stay wide around the fixed test clock.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	commercepg "github.com/AlexandreZanata/Regnovum/internal/commerce/adapters/postgres"
	commerceapp "github.com/AlexandreZanata/Regnovum/internal/commerce/application"
	commercedomain "github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
)

// coracleOp is one scripted step over small closed vocabularies so
// every script reproduces from its seed.
type coracleOp struct {
	kind   int
	slot   int
	amount int
}

const (
	copGift = iota
	copFund
	copAccept
	copRelease
	copCancel
	copExpire
	copResolve
	copRefund
	copFlag
	copContest
	copSettleReview
	copCrash
)

// coracleAmounts cover dust, the floor edge and round thousands.
var coracleAmounts = []int64{1, 9, 10, 11, 19, 20, 100, 1000, 5000, 20000}

const (
	coracleBuyerFunds     = int64(50000000)
	coracleBystanderFunds = int64(1000000)
)

func coracleGiftKey(sequence, slot int) string {
	return fmt.Sprintf("og-s%d-g%d", sequence, slot)
}

func coracleContractKey(sequence, slot int) string {
	return fmt.Sprintf("oc-s%d-c%d", sequence, slot)
}

func coracleRefundKey(sequence, slot int, extra bool) string {
	if extra {
		return fmt.Sprintf("or-s%d-c%d-more", sequence, slot)
	}
	return fmt.Sprintf("or-s%d-c%d", sequence, slot)
}

func coracleReviewKey(sequence, slot int) string {
	return fmt.Sprintf("ov-s%d-g%d", sequence, slot)
}

// coracleTransfer is the model side of one settled gift.
type coracleTransfer struct {
	amount int64
}

// coracleContract is the model side of one trade contract.
type coracleContract struct {
	amount         int64
	status         string
	providerLiquid bool
	refunded       int64
	tithePaid      int64
	titheReversed  int64
}

// coracleReview is the model side of one disguise review.
type coracleReview struct {
	status string
}

// coracleModel is the independent ledger: transfers by key,
// contracts by key, compensations by contract and token, reviews by
// key, and the expected moves. Tithe is integer division here,
// never the adapter code.
type coracleModel struct {
	transfers map[string]*coracleTransfer
	contracts map[string]*coracleContract
	refunds   map[string]int64
	reviews   map[string]*coracleReview
	buyer     int64
	provider  int64
	treasury  int64
	legs      int64
}

// coracleHarness drives both sides with fixed clocks and holders.
type coracleHarness struct {
	t         *testing.T
	ctx       context.Context
	db        *dbtest.TestDB
	transfer  *commerceapp.TransferUseCase
	fund      *commerceapp.FundContractUseCase
	accept    *commerceapp.AcceptDeliveryUseCase
	release   *commerceapp.ReleaseContractUseCase
	cancel    *commerceapp.CancelContractUseCase
	expire    *commerceapp.ExpireContractUseCase
	resolve   *commerceapp.ResolveContractUseCase
	refund    *commerceapp.RefundServiceUseCase
	flag      *commerceapp.FlagDisguiseUseCase
	contest   *commerceapp.ContestDisguiseUseCase
	settleRev *commerceapp.ResolveDisguiseUseCase
	now       time.Time
	buyer     string
	provider  string
	bystander string
	model     *coracleModel
}

func newCoracleHarness(t *testing.T, ctx context.Context, db *dbtest.TestDB) *coracleHarness {
	t.Helper()
	pool := db.Pool.Pool()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	transferRepo, err := commercepg.NewRepository(pool, commercepg.TransferLimits{MaxAmountMilli: 50000, MaxPerWindow: 100000, WindowMinutes: 60})
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	transferUC, err := commerceapp.NewTransferUseCase(transferRepo)
	if err != nil {
		t.Fatalf("NewTransferUseCase: %v", err)
	}
	escrows, err := commercepg.NewEscrowRepository(pool)
	if err != nil {
		t.Fatalf("NewEscrowRepository: %v", err)
	}
	fundUC, err := commerceapp.NewFundContractUseCase(escrows)
	if err != nil {
		t.Fatalf("NewFundContractUseCase: %v", err)
	}
	acceptUC, err := commerceapp.NewAcceptDeliveryUseCase(escrows)
	if err != nil {
		t.Fatalf("NewAcceptDeliveryUseCase: %v", err)
	}
	releaseUC, err := commerceapp.NewReleaseContractUseCase(escrows)
	if err != nil {
		t.Fatalf("NewReleaseContractUseCase: %v", err)
	}
	cancelUC, err := commerceapp.NewCancelContractUseCase(escrows)
	if err != nil {
		t.Fatalf("NewCancelContractUseCase: %v", err)
	}
	expireUC, err := commerceapp.NewExpireContractUseCase(escrows)
	if err != nil {
		t.Fatalf("NewExpireContractUseCase: %v", err)
	}
	resolveUC, err := commerceapp.NewResolveContractUseCase(escrows)
	if err != nil {
		t.Fatalf("NewResolveContractUseCase: %v", err)
	}
	refundRepo, err := commercepg.NewServiceRefundRepository(pool)
	if err != nil {
		t.Fatalf("NewServiceRefundRepository: %v", err)
	}
	refundUC, err := commerceapp.NewRefundServiceUseCase(refundRepo)
	if err != nil {
		t.Fatalf("NewRefundServiceUseCase: %v", err)
	}
	reviewRepo, err := commercepg.NewDisguiseReviewRepository(pool)
	if err != nil {
		t.Fatalf("NewDisguiseReviewRepository: %v", err)
	}
	flagUC, err := commerceapp.NewFlagDisguiseUseCase(reviewRepo)
	if err != nil {
		t.Fatalf("NewFlagDisguiseUseCase: %v", err)
	}
	contestUC, err := commerceapp.NewContestDisguiseUseCase(reviewRepo)
	if err != nil {
		t.Fatalf("NewContestDisguiseUseCase: %v", err)
	}
	settleRevUC, err := commerceapp.NewResolveDisguiseUseCase(reviewRepo)
	if err != nil {
		t.Fatalf("NewResolveDisguiseUseCase: %v", err)
	}
	buyer := escrowAccount(t, ctx, db)
	provider := escrowAccount(t, ctx, db)
	bystander := escrowAccount(t, ctx, db)
	fundCommerceHolder(t, ctx, db, buyer, coracleBuyerFunds)
	fundCommerceHolder(t, ctx, db, bystander, coracleBystanderFunds)
	provisionCustody(t, ctx, db, provider)
	supply := economydomain.GenesisSupplyMillis
	// The model counts journal legs from here: genesis and holder
	// funding already wrote, so the baseline is measured, never
	// assumed.
	var baseline int64
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries`).Scan(&baseline); err != nil {
		t.Fatalf("count baseline legs: %v", err)
	}
	return &coracleHarness{
		t: t, ctx: ctx, db: db,
		transfer: transferUC, fund: fundUC, accept: acceptUC, release: releaseUC,
		cancel: cancelUC, expire: expireUC, resolve: resolveUC, refund: refundUC,
		flag: flagUC, contest: contestUC, settleRev: settleRevUC, now: now,
		buyer: buyer, provider: provider, bystander: bystander,
		model: &coracleModel{
			transfers: map[string]*coracleTransfer{},
			contracts: map[string]*coracleContract{},
			refunds:   map[string]int64{},
			reviews:   map[string]*coracleReview{},
			buyer:     coracleBuyerFunds, provider: 0,
			treasury: supply - coracleBuyerFunds - coracleBystanderFunds,
			legs:     baseline,
		},
	}
}

// genCoracleScript renders one deterministic operation sequence.
func genCoracleScript(rng *testsource.Random, steps int) []coracleOp {
	script := make([]coracleOp, 0, steps)
	for i := 0; i < steps; i++ {
		script = append(script, coracleOp{
			kind:   int(rng.Int64n(12)),
			slot:   int(rng.Int64n(4)),
			amount: int(rng.Int64n(int64(len(coracleAmounts)))),
		})
	}
	return script
}

// coracleBalances reads the four holder balances, the journal size
// and the total supply.
func coracleBalances(t *testing.T, ctx context.Context, db *dbtest.TestDB, h *coracleHarness) (buyer, provider, treasury, bystander int64, legs int64, supply int64) {
	t.Helper()
	buyer = escrowBalance(t, ctx, db, h.buyer)
	provider = escrowBalance(t, ctx, db, h.provider)
	bystander = escrowBalance(t, ctx, db, h.bystander)
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = 'treasury' AND c.label = 'main'`).Scan(&treasury); err != nil {
		t.Fatalf("read treasury: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries`).Scan(&legs); err != nil {
		t.Fatalf("count legs: %v", err)
	}
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
		 FROM app.economy_entries`).Scan(&supply); err != nil {
		t.Fatalf("read supply: %v", err)
	}
	return buyer, provider, treasury, bystander, legs, supply
}

// checkCoracleStep demands the running balances, the journal size,
// the supply and the innocent third party.
func checkCoracleStep(t *testing.T, ctx context.Context, db *dbtest.TestDB, h *coracleHarness) {
	t.Helper()
	buyer, provider, treasury, bystander, legs, supply := coracleBalances(t, ctx, db, h)
	model := h.model
	if buyer != model.buyer || provider != model.provider || treasury != model.treasury {
		t.Fatalf("balances buyer=%d provider=%d treasury=%d, want model %d/%d/%d",
			buyer, provider, treasury, model.buyer, model.provider, model.treasury)
	}
	if legs != model.legs {
		t.Fatalf("legs = %d, want model %d: reviews and false positives write no legs", legs, model.legs)
	}
	if supply != economydomain.GenesisSupplyMillis {
		t.Fatalf("supply = %d, want S %d", supply, economydomain.GenesisSupplyMillis)
	}
	if bystander != coracleBystanderFunds {
		t.Fatalf("bystander = %d, want untouched %d", bystander, coracleBystanderFunds)
	}
}

// apply runs one scripted step on both sides and demands agreement
// on the outcome class: fresh settlement, idempotent replay or
// fail-closed refusal. The want class comes from the model before
// the call; any divergence fails.
func (h *coracleHarness) apply(op coracleOp, sequence int) {
	t := h.t
	amount := coracleAmounts[op.amount]
	giftKey := coracleGiftKey(sequence, op.slot)
	contractKey := coracleContractKey(sequence, op.slot)

	switch op.kind {
	case copGift:
		h.applyGift(giftKey, amount)
	case copFund:
		h.applyFund(contractKey, amount)
	case copAccept:
		h.applyAccept(contractKey)
	case copRelease:
		h.applyRelease(contractKey)
	case copCancel:
		h.applyCancel(contractKey)
	case copExpire:
		h.applyExpire(contractKey)
	case copResolve:
		h.applyResolve(op, contractKey)
	case copRefund:
		h.applyRefund(op, sequence, contractKey, amount)
	case copFlag:
		h.applyFlag(op, sequence, giftKey)
	case copContest:
		h.applyContest(op, sequence, giftKey)
	case copSettleReview:
		h.applySettleReview(op, sequence, giftKey)
	case copCrash:
		h.applyCrash(giftKey, amount)
	}
	checkCoracleStep(t, h.ctx, h.db, h)
}

// applyGift settles one voluntary gift: fresh, replayed or conflicted.
func (h *coracleHarness) applyGift(giftKey string, amount int64) {
	t := h.t
	model := h.model
	if stored, ok := model.transfers[giftKey]; ok {
		got, err := h.transfer.Execute(h.ctx, transferCmd(giftKey, "gift", h.buyer, h.provider, amount))
		if stored.amount == amount {
			if err != nil || !got.Replayed {
				t.Fatalf("gift replay %s: err=%v result=%+v, want replayed", giftKey, err, got)
			}
			return
		}
		if !errors.Is(err, commercedomain.ErrIntentionConflict) {
			t.Fatalf("gift conflict %s: err=%v, want ErrIntentionConflict", giftKey, err)
		}
		return
	}
	got, err := h.transfer.Execute(h.ctx, transferCmd(giftKey, "gift", h.buyer, h.provider, amount))
	if err != nil {
		t.Fatalf("gift %s (%d): %v", giftKey, amount, err)
	}
	if got.Replayed {
		t.Fatalf("gift %s reported replay on first settlement", giftKey)
	}
	model.transfers[giftKey] = &coracleTransfer{amount: amount}
	model.buyer -= amount
	model.provider += amount
	model.legs += 2
}

// applyFund locks one trade contract: fresh, replayed or conflicted.
func (h *coracleHarness) applyFund(contractKey string, amount int64) {
	t := h.t
	model := h.model
	cmd := commerceapp.FundCommand{
		Key: contractKey, Object: "serviço oracle", Buyer: h.buyer, Provider: h.provider,
		AmountMill: amount, ExpiresAt: h.now.Add(time.Hour), Now: h.now,
	}
	if stored, ok := model.contracts[contractKey]; ok {
		view, err := h.fund.Execute(h.ctx, cmd)
		if stored.amount == amount {
			if err != nil || view == nil {
				t.Fatalf("fund replay %s: err=%v, want the original contract", contractKey, err)
			}
			return
		}
		if !errors.Is(err, commercedomain.ErrIntentionConflict) {
			t.Fatalf("fund conflict %s: err=%v, want ErrIntentionConflict", contractKey, err)
		}
		return
	}
	if _, err := h.fund.Execute(h.ctx, cmd); err != nil {
		t.Fatalf("fund %s (%d): %v", contractKey, amount, err)
	}
	model.contracts[contractKey] = &coracleContract{amount: amount, status: "funded"}
	model.buyer -= amount
	model.legs += 2
}

// applyAccept records one buyer acceptance: ghosts refuse, repeats
// replay the current view.
func (h *coracleHarness) applyAccept(contractKey string) {
	t := h.t
	model := h.model
	stored, ok := model.contracts[contractKey]
	_, err := h.accept.Execute(h.ctx, contractKey, h.buyer)
	if !ok {
		if err == nil {
			t.Fatalf("accept ghost %s settled", contractKey)
		}
		return
	}
	if err != nil {
		t.Fatalf("accept %s in %s: %v, want the current view", contractKey, stored.status, err)
	}
	if stored.status == "funded" {
		stored.status = "accepted"
	}
}

// applyRelease pays one accepted escrow: ghosts and crossed states
// refuse, repeats replay the single payment.
func (h *coracleHarness) applyRelease(contractKey string) {
	t := h.t
	model := h.model
	stored, ok := model.contracts[contractKey]
	_, err := h.release.Execute(h.ctx, contractKey, h.buyer)
	if !ok {
		if err == nil {
			t.Fatalf("release ghost %s settled", contractKey)
		}
		return
	}
	switch stored.status {
	case "accepted":
		if err != nil {
			t.Fatalf("release %s: %v", contractKey, err)
		}
		tithe := stored.amount / 10
		model.provider += stored.amount - tithe
		model.treasury += tithe
		stored.status = "released"
		stored.providerLiquid = true
		stored.tithePaid = tithe
		model.legs += 2
		if tithe > 0 {
			model.legs++
		}
	case "released":
		if err != nil {
			t.Fatalf("release replay %s: %v, want the single payment", contractKey, err)
		}
	default:
		if err == nil {
			t.Fatalf("release %s in %s settled, want refusal", contractKey, stored.status)
		}
	}
}

// applyFlag signals one settled gift: ghosts refuse, repeats replay.
func (h *coracleHarness) applyFlag(op coracleOp, sequence int, giftKey string) {
	t := h.t
	model := h.model
	reviewKey := coracleReviewKey(sequence, op.slot)
	rkey := giftKey + "\x00" + reviewKey
	cmd := commerceapp.FlagDisguiseCommand{
		ReviewKey: reviewKey, TransferKey: giftKey, Payer: h.buyer,
		Reason: "contract", EvidenceHash: disguiseEvidenceA, Reporter: "watcher",
	}
	if _, ok := model.reviews[rkey]; ok {
		view, err := h.flag.Execute(h.ctx, cmd)
		if err != nil || !view.Replayed {
			t.Fatalf("flag replay %s: err=%v view=%+v, want replayed", rkey, err, view)
		}
		return
	}
	view, err := h.flag.Execute(h.ctx, cmd)
	if _, ok := model.transfers[giftKey]; !ok {
		if err == nil {
			t.Fatalf("flag ghost %s settled", giftKey)
		}
		return
	}
	if err != nil {
		t.Fatalf("flag %s: %v", rkey, err)
	}
	if view.Replayed {
		t.Fatalf("flag %s reported replay on first signal", rkey)
	}
	model.reviews[rkey] = &coracleReview{status: "flagged"}
}

// applyContest records one challenge: strangers, ghosts and terminal
// reviews refuse.
func (h *coracleHarness) applyContest(op coracleOp, sequence int, giftKey string) {
	t := h.t
	model := h.model
	reviewKey := coracleReviewKey(sequence, op.slot)
	rkey := giftKey + "\x00" + reviewKey
	by := []string{h.buyer, h.provider, h.bystander}[op.amount%3]
	_, err := h.contest.Execute(h.ctx, commerceapp.ContestDisguiseCommand{
		ReviewKey: reviewKey, TransferKey: giftKey, Payer: h.buyer, By: by,
	})
	stored, ok := model.reviews[rkey]
	if !ok || stored.status != "flagged" || (by != h.buyer && by != h.provider) {
		if err == nil {
			t.Fatalf("contest %s settled, want refusal", rkey)
		}
		return
	}
	if err != nil {
		t.Fatalf("contest %s: %v", rkey, err)
	}
	stored.status = "contested"
}

// applySettleReview settles one flagged review exactly once, without
// moving value: dismissals clear, confirmations record the floor
// charge owed.
func (h *coracleHarness) applySettleReview(op coracleOp, sequence int, giftKey string) {
	t := h.t
	model := h.model
	reviewKey := coracleReviewKey(sequence, op.slot)
	rkey := giftKey + "\x00" + reviewKey
	decision := "dismiss"
	if op.amount%2 == 1 {
		decision = "confirm"
	}
	cmd := commerceapp.ResolveDisguiseCommand{
		ReviewKey: reviewKey, TransferKey: giftKey, Payer: h.buyer,
		Decision: decision, Now: h.now, AppealUntil: h.now.Add(72 * time.Hour),
	}
	if decision == "confirm" {
		cmd.Basis = "contrato oracle"
		cmd.TrailHash = disguiseEvidenceB
	}
	view, err := h.settleRev.Execute(h.ctx, cmd)
	stored, ok := model.reviews[rkey]
	if !ok || (stored.status != "flagged" && stored.status != "contested") {
		if err == nil {
			t.Fatalf("settle review %s settled, want refusal", rkey)
		}
		return
	}
	if err != nil {
		t.Fatalf("settle review %s: %v", rkey, err)
	}
	if decision == "dismiss" {
		stored.status = "dismissed"
	} else {
		stored.status = "confirmed"
		if view.ChargeMill != model.transfers[giftKey].amount/10 {
			t.Fatalf("confirmed charge = %d, want floor of the flagged gift", view.ChargeMill)
		}
	}
}

// applyCrash proves cancelled contexts write nothing: any error is
// accepted, but the model and the journal must stand still.
func (h *coracleHarness) applyCrash(giftKey string, amount int64) {
	t := h.t
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := h.transfer.Execute(cancelled, transferCmd(giftKey, "gift", h.buyer, h.provider, amount)); err == nil {
		t.Fatal("cancelled gift settled: crashes must write nothing")
	}
}
func (h *coracleHarness) applyRefund(op coracleOp, sequence int, contractKey string, amount int64) {
	t := h.t
	model := h.model
	extra := op.slot%2 == 1
	refundKey := coracleRefundKey(sequence, op.slot, extra)
	rkey := contractKey + "\x00" + refundKey
	got, err := h.refund.Execute(h.ctx, commerceapp.ServiceRefundCommand{
		ContractKey: contractKey, Buyer: h.buyer, RefundKey: refundKey, AmountMill: amount,
	})
	if paid, ok := model.refunds[rkey]; ok {
		if err != nil || !got.Replayed || got.AmountMill != paid {
			t.Fatalf("refund replay %s: err=%v result=%+v, want %d replayed", rkey, err, got, paid)
		}
		return
	}
	stored, ok := model.contracts[contractKey]
	if !ok || !stored.providerLiquid {
		if err == nil {
			t.Fatalf("refund %s without liquidation settled", contractKey)
		}
		return
	}
	if stored.refunded+amount > stored.amount {
		if !errors.Is(err, commercedomain.ErrRefundExceedsOriginal) {
			t.Fatalf("refund excess %s: err=%v, want ErrRefundExceedsOriginal", contractKey, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("refund %s (%d): %v", contractKey, amount, err)
	}
	if got.Replayed {
		t.Fatalf("refund %s reported replay on first compensation", contractKey)
	}
	tithe := amount / 10
	share := amount - tithe
	if got.TitheReversal != tithe || got.ProviderShare != share || got.AmountMill != amount {
		t.Fatalf("refund %s = %+v, want split %d/%d of %d", contractKey, got, tithe, share, amount)
	}
	model.provider -= share
	model.treasury -= tithe
	model.buyer += amount
	stored.refunded += amount
	stored.titheReversed += tithe
	model.refunds[rkey] = amount
	model.legs += 2
	if tithe > 0 {
		model.legs++
	}
}
func (h *coracleHarness) applyExpire(contractKey string) {
	t := h.t
	model := h.model
	stored, ok := model.contracts[contractKey]
	_, err := h.expire.Execute(h.ctx, contractKey, h.buyer, h.now.Add(2*time.Hour))
	if !ok {
		if err == nil {
			t.Fatalf("expire ghost %s settled", contractKey)
		}
		return
	}
	if err != nil {
		t.Fatalf("expire %s in %s: %v, want the current view", contractKey, stored.status, err)
	}
	if stored.status == "funded" || stored.status == "accepted" {
		stored.status = "expired"
	}
}

// applyResolve settles one expired contract by competent decision:
// ghosts and moved states refuse, repeats replay the resolution.
func (h *coracleHarness) applyResolve(op coracleOp, contractKey string) {
	t := h.t
	model := h.model
	stored, ok := model.contracts[contractKey]
	decision := "release"
	if op.slot%2 == 1 {
		decision = "refund"
	}
	_, err := h.resolve.Execute(h.ctx, contractKey, h.buyer, "arbiter-oracle", decision)
	if !ok {
		if err == nil {
			t.Fatalf("resolve ghost %s settled", contractKey)
		}
		return
	}
	switch stored.status {
	case "expired":
		if err != nil {
			t.Fatalf("resolve %s: %v", contractKey, err)
		}
		if decision == "release" {
			tithe := stored.amount / 10
			model.provider += stored.amount - tithe
			model.treasury += tithe
			stored.providerLiquid = true
			stored.tithePaid = tithe
			model.legs += 2
			if tithe > 0 {
				model.legs++
			}
		} else {
			model.buyer += stored.amount
			model.legs += 2
		}
		stored.status = "resolved"
	case "resolved":
		if err != nil {
			t.Fatalf("resolve replay %s: %v, want the single resolution", contractKey, err)
		}
	default:
		if err == nil {
			t.Fatalf("resolve %s in %s settled, want refusal", contractKey, stored.status)
		}
	}
}
func (h *coracleHarness) applyCancel(contractKey string) {
	t := h.t
	model := h.model
	stored, ok := model.contracts[contractKey]
	_, err := h.cancel.Execute(h.ctx, contractKey, h.buyer)
	if !ok {
		if err == nil {
			t.Fatalf("cancel ghost %s settled", contractKey)
		}
		return
	}
	switch stored.status {
	case "funded":
		if err != nil {
			t.Fatalf("cancel %s: %v", contractKey, err)
		}
		model.buyer += stored.amount
		model.legs += 2
		stored.status = "refunded"
	case "refunded":
		if err != nil {
			t.Fatalf("cancel replay %s: %v, want the single refund", contractKey, err)
		}
	default:
		if err == nil {
			t.Fatalf("cancel %s in %s settled, want refusal", contractKey, stored.status)
		}
	}
}

// checkCoracleSound replays the oracle state against the database and
// reports every divergence: pairings, at-most-once tithe per
// contract, gifts without Treasury legs, linked refunds, owned
// statuses and counts. Empty findings mean exact-once holds.
func checkCoracleSound(t *testing.T, ctx context.Context, db *dbtest.TestDB, h *coracleHarness) []string {
	t.Helper()
	findings := []string{}
	model := h.model
	// Running balances still match the model after the fault: any
	// extra or missing leg moves a holder.
	buyer, provider, treasury, bystander, legs, _ := coracleBalances(t, ctx, db, h)
	if buyer != model.buyer || provider != model.provider || treasury != model.treasury {
		findings = append(findings, fmt.Sprintf("balances buyer=%d provider=%d treasury=%d, want model %d/%d/%d",
			buyer, provider, treasury, model.buyer, model.provider, model.treasury))
	}
	if legs != model.legs {
		findings = append(findings, fmt.Sprintf("legs = %d, want model %d", legs, model.legs))
	}
	if bystander != coracleBystanderFunds {
		findings = append(findings, fmt.Sprintf("bystander = %d, want untouched %d", bystander, coracleBystanderFunds))
	}
	// Every transfer pairs debits with credits. The lawful exception
	// is Genesis itself: the single unpaired credit of exactly S,
	// which the check names instead of ignoring.
	rows, err := db.Query(ctx,
		`SELECT transfer_id::text,
		  COALESCE(SUM(amount_milli) FILTER (WHERE direction = 'debit'), 0),
		  COALESCE(SUM(amount_milli) FILTER (WHERE direction = 'credit'), 0)
		 FROM app.economy_entries GROUP BY transfer_id`)
	if err != nil {
		t.Fatalf("read pairings: %v", err)
	}
	genesisSeen := false
	for rows.Next() {
		var transfer string
		var debited, credited int64
		if err := rows.Scan(&transfer, &debited, &credited); err != nil {
			t.Fatalf("scan pairing: %v", err)
		}
		if debited == 0 && credited == economydomain.GenesisSupplyMillis {
			genesisSeen = true
			continue
		}
		if debited != credited {
			findings = append(findings, fmt.Sprintf("transfer %s nets %d/%d, want balanced legs", transfer, debited, credited))
		}
	}
	rows.Close()
	if !genesisSeen {
		findings = append(findings, "genesis credit missing: the lawful unpaired credit must exist exactly once")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pairings: %v", err)
	}
	// At most one tithe per contract, exact floor when paid: every
	// Treasury credit under a settlement transfer of one contract
	// sums to amount/10 paid once.
	contractRows, err := db.Query(ctx, `SELECT id::text, contract_key, amount_milli FROM app.commerce_contracts`)
	if err != nil {
		t.Fatalf("read contracts: %v", err)
	}
	contracts := 0
	for contractRows.Next() {
		var id, key string
		var amount int64
		if err := contractRows.Scan(&id, &key, &amount); err != nil {
			t.Fatalf("scan contract: %v", err)
		}
		contracts++
		stored, ok := model.contracts[key]
		if !ok {
			findings = append(findings, fmt.Sprintf("contract %s settled beyond the model", key))
			continue
		}
		var tithe int64
		var terminals int
		if err := db.QueryRow(ctx,
			`SELECT COALESCE(SUM(e.amount_milli) FILTER (WHERE c.kind = 'treasury'), 0),
			  COUNT(DISTINCT s.id) FILTER (WHERE s.action IN ('release', 'resolve-release'))
			 FROM app.commerce_settlements s
			 LEFT JOIN app.economy_entries e ON e.transfer_id = s.transfer_id
			 LEFT JOIN app.economy_custodies c ON c.id = e.custody_id AND e.direction = 'credit'
			 WHERE s.contract_id = $1::uuid`, id).Scan(&tithe, &terminals); err != nil {
			t.Fatalf("read contract tithe: %v", err)
		}
		if terminals > 1 {
			findings = append(findings, fmt.Sprintf("contract %s tithes %d times, want at most once", key, terminals))
		}
		if tithe != stored.tithePaid {
			findings = append(findings, fmt.Sprintf("contract %s tithe = %d, want model %d", key, tithe, stored.tithePaid))
		}
		var refunded int64
		if err := db.QueryRow(ctx,
			`SELECT COALESCE(SUM(amount_milli), 0) FROM app.commerce_service_refunds WHERE contract_id = $1::uuid`, id).Scan(&refunded); err != nil {
			t.Fatalf("read refunded: %v", err)
		}
		if refunded != stored.refunded {
			findings = append(findings, fmt.Sprintf("contract %s refunded = %d, want model %d", key, refunded, stored.refunded))
		}
	}
	contractRows.Close()
	if contracts != len(model.contracts) {
		findings = append(findings, fmt.Sprintf("contracts = %d, want model %d", contracts, len(model.contracts)))
	}
	// Gifts never bear tithe: no gift transfer funds a Treasury leg.
	var giftTithe int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM app.commerce_transfers t
		 JOIN app.economy_entries e ON e.transfer_id = t.transfer_id
		 JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE t.kind = 'gift' AND c.kind = 'treasury'`).Scan(&giftTithe); err != nil {
		t.Fatalf("read gift tithe: %v", err)
	}
	if giftTithe != 0 {
		findings = append(findings, fmt.Sprintf("gift treasury legs = %d, want none: presents never bear tithe", giftTithe))
	}
	// Every refund links its cause with legs summing the refunded
	// value, unless dust with a broke provider left a NULL transfer
	// with the full share owed explicitly.
	refundRows, err := db.Query(ctx,
		`SELECT id::text, amount_milli, tithe_reversal_milli, provider_share_milli,
		  obligation_milli, transfer_id::text FROM app.commerce_service_refunds`)
	if err != nil {
		t.Fatalf("read refunds: %v", err)
	}
	for refundRows.Next() {
		var id string
		var amount, tithe, share, obligation int64
		var transfer *string
		if err := refundRows.Scan(&id, &amount, &tithe, &share, &obligation, &transfer); err != nil {
			t.Fatalf("scan refund: %v", err)
		}
		if tithe != amount/10 || share != amount-tithe {
			findings = append(findings, fmt.Sprintf("refund %s splits %d/%d of %d, want floor", id, tithe, share, amount))
		}
		if transfer == nil {
			if obligation != share {
				findings = append(findings, fmt.Sprintf("refund %s without transfer owes %d, want the full share %d", id, obligation, share))
			}
			continue
		}
		var legs int64
		if err := db.QueryRow(ctx,
			`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
			 FROM app.economy_entries WHERE transfer_id = $1::uuid`, *transfer).Scan(&legs); err != nil {
			t.Fatalf("read refund legs: %v", err)
		}
		if legs != 0 {
			findings = append(findings, fmt.Sprintf("refund %s legs net %d, want zero", id, legs))
		}
		var credited int64
		if err := db.QueryRow(ctx,
			`SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries
			 WHERE transfer_id = $1::uuid AND direction = 'credit'`, *transfer).Scan(&credited); err != nil {
			t.Fatalf("read refund credited: %v", err)
		}
		if credited != amount {
			findings = append(findings, fmt.Sprintf("refund %s moved %d, want %d", id, credited, amount))
		}
	}
	refundRows.Close()
	if err := refundRows.Err(); err != nil {
		t.Fatalf("iterate refunds: %v", err)
	}
	return findings
}

// TestCoracleAgreesAcrossSequences runs 3.000 scripted operations
// across 60 seeded sequences on one disposable database, comparing
// outcome class and state after every step.
func TestCoracleAgreesAcrossSequences(t *testing.T) {
	const sequences = 60
	const steps = 50
	testDB := dbtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	harness := newCoracleHarness(t, ctx, testDB)
	applied := 0
	for sequence := 0; sequence < sequences; sequence++ {
		rng := testsource.NewRandom(testsource.SeedFor(t) + int64(sequence)*1000003)
		for _, op := range genCoracleScript(rng, steps) {
			harness.apply(op, sequence)
			applied++
		}
	}
	if applied != sequences*steps {
		t.Fatalf("applied %d operations, want %d", applied, sequences*steps)
	}
	if findings := checkCoracleSound(t, ctx, testDB, harness); len(findings) > 0 {
		t.Fatalf("oracle diverged:\n%s", strings.Join(findings, "\n"))
	}
}

// TestCoracleKillsMutants injects one fault per class with raw SQL
// and proves the comparison bites every time: rate surcharge, dust
// floor, misclassified gift and legless refund are all detected,
// with no case excluded.
func TestCoracleKillsMutants(t *testing.T) {
	// providerCustody resolves one holder custody for raw-SQL mutants.
	providerCustody := func(t *testing.T, ctx context.Context, db *dbtest.TestDB, h *coracleHarness, kind, label string) string {
		t.Helper()
		var custody string
		if err := db.QueryRow(ctx,
			`SELECT id::text FROM app.economy_custodies WHERE kind = $1 AND label = $2`, kind, label).Scan(&custody); err != nil {
			t.Fatalf("read %s/%s custody: %v", kind, label, err)
		}
		return custody
	}
	// surcharge moves one milliINK from the provider to the Treasury
	// under a fresh transfer: balances diverge and the contract
	// tithe exceeds its floor.
	surcharge := func(t *testing.T, ctx context.Context, db *dbtest.TestDB, h *coracleHarness) {
		t.Helper()
		provider := providerCustody(t, ctx, db, h, "user", h.provider)
		treasury := providerCustody(t, ctx, db, h, "treasury", "main")
		if _, err := db.Exec(ctx,
			`WITH fresh AS (SELECT gen_random_uuid() AS id)
			 INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
			 VALUES ((SELECT id FROM fresh), $1::uuid, 'debit', 1),
			        ((SELECT id FROM fresh), $2::uuid, 'credit', 1)`, provider, treasury); err != nil {
			t.Fatalf("inject surcharge: %v", err)
		}
	}
	mutants := []struct {
		name      string
		amountIdx int
		inject    func(t *testing.T, ctx context.Context, db *dbtest.TestDB, h *coracleHarness)
	}{
		{"rate-surcharge", 9, surcharge},
		{"floor-dust-tithe", 1, surcharge},
		{"misclassified-gift", 9, func(t *testing.T, ctx context.Context, db *dbtest.TestDB, h *coracleHarness) {
			t.Helper()
			var transfer string
			if err := db.QueryRow(ctx,
				`SELECT transfer_id::text FROM app.commerce_transfers WHERE kind = 'gift' LIMIT 1`).Scan(&transfer); err != nil {
				t.Fatalf("read settled gift: %v", err)
			}
			var payee string
			if err := db.QueryRow(ctx,
				`SELECT custody_id::text FROM app.economy_entries WHERE transfer_id = $1::uuid AND direction = 'credit' LIMIT 1`, transfer).Scan(&payee); err != nil {
				t.Fatalf("read gift payee: %v", err)
			}
			treasury := providerCustody(t, ctx, db, h, "treasury", "main")
			// The surcharge rides the gift transfer itself: a present
			// taxed as trade, pairing-balanced but classified wrong.
			if _, err := db.Exec(ctx,
				`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
				 VALUES ($1::uuid, $2::uuid, 'debit', 100),
				        ($1::uuid, $3::uuid, 'credit', 100)`, transfer, payee, treasury); err != nil {
				t.Fatalf("inject gift tithe: %v", err)
			}
		}},
		{"legless-refund", 9, func(t *testing.T, ctx context.Context, db *dbtest.TestDB, h *coracleHarness) {
			t.Helper()
			var contract string
			if err := db.QueryRow(ctx, `SELECT id::text FROM app.commerce_contracts LIMIT 1`).Scan(&contract); err != nil {
				t.Fatalf("read settled cause: %v", err)
			}
			if _, err := db.Exec(ctx,
				`INSERT INTO app.commerce_service_refunds
				 (contract_id, refund_key, amount_milli, tithe_reversal_milli, provider_share_milli, obligation_milli, transfer_id)
				 VALUES ($1::uuid, 'mutant-legless', 20000, 2000, 18000, 0, NULL)`, contract); err != nil {
				t.Fatalf("inject legless refund: %v", err)
			}
		}},
	}
	for _, mutant := range mutants {
		t.Run(mutant.name, func(t *testing.T) {
			testDB := dbtest.New(t)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			harness := newCoracleHarness(t, ctx, testDB)
			harness.apply(coracleOp{kind: copGift, slot: 0, amount: mutant.amountIdx}, 0)
			harness.apply(coracleOp{kind: copFund, slot: 0, amount: mutant.amountIdx}, 0)
			harness.apply(coracleOp{kind: copAccept, slot: 0}, 0)
			harness.apply(coracleOp{kind: copRelease, slot: 0}, 0)
			mutant.inject(t, ctx, testDB, harness)
			if findings := checkCoracleSound(t, ctx, testDB, harness); len(findings) == 0 {
				t.Fatalf("mutant %q survived: the comparison saw nothing", mutant.name)
			}
		})
	}
}

// TestTitheFloorBattery proves the floor prices dust and thousands
// exactly on real PostgreSQL: every release settles provider and
// Treasury to amount/10 with truncation, and legs sum the payment.
func TestTitheFloorBattery(t *testing.T) {
	testDB := dbtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	harness := newCoracleHarness(t, ctx, testDB)
	for i, amount := range append(append([]int64{}, coracleAmounts...), 1000) {
		key := fmt.Sprintf("floor-%d", i)
		if _, err := harness.fund.Execute(harness.ctx, commerceapp.FundCommand{
			Key: key, Object: "piso exato", Buyer: harness.buyer, Provider: harness.provider,
			AmountMill: amount, ExpiresAt: harness.now.Add(time.Hour), Now: harness.now,
		}); err != nil {
			t.Fatalf("fund %d: %v", amount, err)
		}
		beforeProvider := escrowBalance(t, ctx, testDB, harness.provider)
		var beforeTreasury int64
		if err := testDB.QueryRow(ctx,
			`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
			 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
			 WHERE c.kind = 'treasury' AND c.label = 'main'`).Scan(&beforeTreasury); err != nil {
			t.Fatalf("read treasury: %v", err)
		}
		if _, err := harness.accept.Execute(harness.ctx, key, harness.buyer); err != nil {
			t.Fatalf("accept %d: %v", amount, err)
		}
		if _, err := harness.release.Execute(harness.ctx, key, harness.buyer); err != nil {
			t.Fatalf("release %d: %v", amount, err)
		}
		wantTithe := amount / 10
		if got := escrowBalance(t, ctx, testDB, harness.provider) - beforeProvider; got != amount-wantTithe {
			t.Fatalf("provider net for %d = %d, want %d", amount, got, amount-wantTithe)
		}
		var afterTreasury int64
		if err := testDB.QueryRow(ctx,
			`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
			 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
			 WHERE c.kind = 'treasury' AND c.label = 'main'`).Scan(&afterTreasury); err != nil {
			t.Fatalf("read treasury: %v", err)
		}
		if afterTreasury-beforeTreasury != wantTithe {
			t.Fatalf("treasury tithe for %d = %d, want %d", amount, afterTreasury-beforeTreasury, wantTithe)
		}
	}
}
