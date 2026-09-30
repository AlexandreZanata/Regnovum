package postgres_test

// P37-T06 — disguised-trade review without automatic seizure on real
// PostgreSQL.
//
// A settled personal gift is signalled with a closed reason and a
// minimized evidence digest, contested by its parties and settled
// once by dismissal (false positive, no movement) or by confirmation
// (explicit act with contractual basis, trail, floor(10%) charge owed
// and appeal window, still no debit). The tests prove on a disposable
// database: the false-positive path moves nothing, the confirmed act
// records basis, trail, charge and appeal without seizing, replays
// resolve, conflicts refuse, strangers never speak, non-gifts never
// flag and concurrent flags collapse to one. The suite moves no money
// outside its own ledger.

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

const disguiseEvidenceA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const disguiseEvidenceB = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

type disguiseKit struct {
	transfer *commerceapp.TransferUseCase
	flag     *commerceapp.FlagDisguiseUseCase
	contest  *commerceapp.ContestDisguiseUseCase
	resolve  *commerceapp.ResolveDisguiseUseCase
	payer    string
	payee    string
}

func newDisguiseKit(t *testing.T, db *dbtest.TestDB, funds int64) *disguiseKit {
	t.Helper()
	transferRepo, err := commercepg.NewRepository(db.Pool.Pool(), commercepg.TransferLimits{MaxAmountMilli: 50000, MaxPerWindow: 1000, WindowMinutes: 60})
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	transferUC, err := commerceapp.NewTransferUseCase(transferRepo)
	if err != nil {
		t.Fatalf("NewTransferUseCase: %v", err)
	}
	reviewRepo, err := commercepg.NewDisguiseReviewRepository(db.Pool.Pool())
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
	resolveUC, err := commerceapp.NewResolveDisguiseUseCase(reviewRepo)
	if err != nil {
		t.Fatalf("NewResolveDisguiseUseCase: %v", err)
	}
	ctx, cancel := escrowCtx()
	defer cancel()
	payer := escrowAccount(t, ctx, db)
	payee := escrowAccount(t, ctx, db)
	fundCommerceHolder(t, ctx, db, payer, funds)
	provisionCustody(t, ctx, db, payee)
	return &disguiseKit{transfer: transferUC, flag: flagUC, contest: contestUC, resolve: resolveUC, payer: payer, payee: payee}
}

func (k *disguiseKit) settleGift(t *testing.T, ctx context.Context, key string, amount int64) {
	t.Helper()
	if _, err := k.transfer.Execute(ctx, commerceapp.TransferCommand{
		Key: key, Kind: "gift", Payer: k.payer, Payee: k.payee,
		AmountMilli: amount, ConsentRef: "consent-" + key,
	}); err != nil {
		t.Fatalf("settle gift %s (%d): %v", key, amount, err)
	}
}

func (k *disguiseKit) flagGift(t *testing.T, ctx context.Context, transferKey, reviewKey, reason string) *commerceapp.DisguiseReviewView {
	t.Helper()
	view, err := k.flag.Execute(ctx, commerceapp.FlagDisguiseCommand{
		ReviewKey: reviewKey, TransferKey: transferKey, Payer: k.payer,
		Reason: reason, EvidenceHash: disguiseEvidenceA, Reporter: "watcher",
	})
	if err != nil {
		t.Fatalf("flag %s/%s: %v", transferKey, reviewKey, err)
	}
	return view
}

func disguiseEntries(t *testing.T, ctx context.Context, db *dbtest.TestDB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries`).Scan(&count); err != nil {
		t.Fatalf("count entries: %v", err)
	}
	return count
}

func disguiseNow() time.Time {
	return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
}

// TestDisguiseFalsePositiveMovesNothing proves the contested
// dismissal moves no value: flag, contest and dismiss leave payer,
// payee and journal identical, with the terminal status and the
// appeal window recorded and no act.
func TestDisguiseFalsePositiveMovesNothing(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newDisguiseKit(t, db, 100000)
	kit.settleGift(t, ctx, "disguise-fp-1", 20000)
	beforePayer := escrowBalance(t, ctx, db, kit.payer)
	beforePayee := escrowBalance(t, ctx, db, kit.payee)
	beforeEntries := disguiseEntries(t, ctx, db)

	flagged := kit.flagGift(t, ctx, "disguise-fp-1", "disguise-fp-1-r1", "recurrence")
	if flagged.Status != commercedomain.DisguiseFlagged || flagged.Replayed {
		t.Fatalf("flag = %+v, want fresh flagged", flagged)
	}
	if escrowBalance(t, ctx, db, kit.payer) != beforePayer || escrowBalance(t, ctx, db, kit.payee) != beforePayee {
		t.Fatal("flagging a present must never move INK")
	}
	contested, err := kit.contest.Execute(ctx, commerceapp.ContestDisguiseCommand{
		ReviewKey: "disguise-fp-1-r1", TransferKey: "disguise-fp-1", Payer: kit.payer, By: kit.payee,
	})
	if err != nil {
		t.Fatalf("contest: %v", err)
	}
	if contested.Status != commercedomain.DisguiseContested {
		t.Fatalf("contest status = %q, want contested", contested.Status)
	}
	now := disguiseNow()
	dismissed, err := kit.resolve.Execute(ctx, commerceapp.ResolveDisguiseCommand{
		ReviewKey: "disguise-fp-1-r1", TransferKey: "disguise-fp-1", Payer: kit.payer,
		Decision: "dismiss", Now: now, AppealUntil: now.Add(72 * time.Hour),
	})
	if err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if dismissed.Status != commercedomain.DisguiseDismissed || dismissed.Basis != "" || dismissed.ChargeMill != 0 {
		t.Fatalf("dismiss = %+v, want dismissed with no act and no charge", dismissed)
	}
	if dismissed.AppealUntil.IsZero() {
		t.Fatal("dismissal owes the parties an appeal window")
	}
	if escrowBalance(t, ctx, db, kit.payer) != beforePayer || escrowBalance(t, ctx, db, kit.payee) != beforePayee {
		t.Fatal("dismissed false positive must leave all balances untouched")
	}
	if disguiseEntries(t, ctx, db) != beforeEntries {
		t.Fatal("review steps must write no ledger legs")
	}
}

// TestDisguiseConfirmRecordsActWithoutSeizure proves a confirmed
// fraud records the act with contractual basis, trail, explicit
// floor(10%) charge and appeal window, still with no debit: the
// charge is owed, never seized.
func TestDisguiseConfirmRecordsActWithoutSeizure(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newDisguiseKit(t, db, 100000)
	kit.settleGift(t, ctx, "disguise-cf-1", 20000)
	beforePayer := escrowBalance(t, ctx, db, kit.payer)
	beforePayee := escrowBalance(t, ctx, db, kit.payee)
	beforeEntries := disguiseEntries(t, ctx, db)

	kit.flagGift(t, ctx, "disguise-cf-1", "disguise-cf-1-r1", "contract")
	now := disguiseNow()
	confirmed, err := kit.resolve.Execute(ctx, commerceapp.ResolveDisguiseCommand{
		ReviewKey: "disguise-cf-1-r1", TransferKey: "disguise-cf-1", Payer: kit.payer,
		Decision: "confirm", Basis: "contrato de serviço nº 7",
		TrailHash: disguiseEvidenceB, Now: now, AppealUntil: now.Add(72 * time.Hour),
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.Status != commercedomain.DisguiseConfirmed {
		t.Fatalf("confirm status = %q, want confirmed", confirmed.Status)
	}
	if confirmed.ChargeMill != 2000 || confirmed.Basis != "contrato de serviço nº 7" || confirmed.TrailHash != disguiseEvidenceB {
		t.Fatalf("confirm = %+v, want act 2000 with basis, trail and appeal", confirmed)
	}
	if confirmed.AppealUntil.IsZero() {
		t.Fatal("confirmation owes the parties an appeal window")
	}
	if escrowBalance(t, ctx, db, kit.payer) != beforePayer || escrowBalance(t, ctx, db, kit.payee) != beforePayee {
		t.Fatal("confirmation records the charge owed, never seizes: balances stay identical")
	}
	if disguiseEntries(t, ctx, db) != beforeEntries {
		t.Fatal("confirmation must write no ledger legs")
	}
	var steps int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM app.commerce_disguise_steps s
		 JOIN app.commerce_disguise_reviews r ON r.id = s.review_id
		 WHERE r.review_key = 'disguise-cf-1-r1'`).Scan(&steps); err != nil {
		t.Fatalf("count steps: %v", err)
	}
	if steps != 1 {
		t.Fatalf("steps = %d, want exactly the confirming act (no auto-tithe, no seizure)", steps)
	}
}

// TestDisguiseReplayAndConflict proves one key flags once: the retry
// replays untouched and divergent terms under one key conflict.
func TestDisguiseReplayAndConflict(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newDisguiseKit(t, db, 100000)
	kit.settleGift(t, ctx, "disguise-rp-1", 5000)
	first := kit.flagGift(t, ctx, "disguise-rp-1", "disguise-rp-1-r1", "delivery")
	replayed, err := kit.flag.Execute(ctx, commerceapp.FlagDisguiseCommand{
		ReviewKey: "disguise-rp-1-r1", TransferKey: "disguise-rp-1", Payer: kit.payer,
		Reason: "delivery", EvidenceHash: disguiseEvidenceA, Reporter: "watcher",
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replayed.Replayed || replayed.ReviewID != first.ReviewID {
		t.Fatalf("replay = %+v, want same review replayed", replayed)
	}
	if _, err := kit.flag.Execute(ctx, commerceapp.FlagDisguiseCommand{
		ReviewKey: "disguise-rp-1-r1", TransferKey: "disguise-rp-1", Payer: kit.payer,
		Reason: "contract", EvidenceHash: disguiseEvidenceA, Reporter: "watcher",
	}); !errors.Is(err, commercedomain.ErrIntentionConflict) {
		t.Fatalf("divergent reason = %v, want ErrIntentionConflict, never a silent reclassification", err)
	}
}

// TestDisguiseRefusals proves the fail-closed edges: trades never
// flag, unknown gifts never flag, strangers never contest and
// terminal reviews never reopen.
func TestDisguiseRefusals(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newDisguiseKit(t, db, 100000)
	if _, err := kit.transfer.Execute(ctx, commerceapp.TransferCommand{
		Key: "disguise-trade-1", Kind: "trade", Payer: kit.payer, Payee: kit.payee,
		AmountMilli: 5000, ConsentRef: "consent-disguise-trade-1",
	}); err != nil {
		t.Fatalf("settle trade: %v", err)
	}
	if _, err := kit.flag.Execute(ctx, commerceapp.FlagDisguiseCommand{
		ReviewKey: "disguise-trade-1-r1", TransferKey: "disguise-trade-1", Payer: kit.payer,
		Reason: "contract", EvidenceHash: disguiseEvidenceA, Reporter: "watcher",
	}); !errors.Is(err, commercedomain.ErrInvalidDisguise) {
		t.Fatalf("trade flag = %v, want ErrInvalidDisguise (trades bear tithe already)", err)
	}
	if _, err := kit.flag.Execute(ctx, commerceapp.FlagDisguiseCommand{
		ReviewKey: "disguise-ghost-1-r1", TransferKey: "disguise-ghost-1", Payer: kit.payer,
		Reason: "contract", EvidenceHash: disguiseEvidenceA, Reporter: "watcher",
	}); !errors.Is(err, commercedomain.ErrContractNotFound) {
		t.Fatalf("ghost flag = %v, want ErrContractNotFound", err)
	}
	kit.settleGift(t, ctx, "disguise-rf-1", 8000)
	kit.flagGift(t, ctx, "disguise-rf-1", "disguise-rf-1-r1", "announcement")
	stranger := escrowAccount(t, ctx, db)
	if _, err := kit.contest.Execute(ctx, commerceapp.ContestDisguiseCommand{
		ReviewKey: "disguise-rf-1-r1", TransferKey: "disguise-rf-1", Payer: kit.payer, By: stranger,
	}); !errors.Is(err, commercedomain.ErrDisguiseNotParty) {
		t.Fatalf("stranger contest = %v, want ErrDisguiseNotParty", err)
	}
	now := disguiseNow()
	if _, err := kit.resolve.Execute(ctx, commerceapp.ResolveDisguiseCommand{
		ReviewKey: "disguise-rf-1-r1", TransferKey: "disguise-rf-1", Payer: kit.payer,
		Decision: "dismiss", Now: now, AppealUntil: now.Add(72 * time.Hour),
	}); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if _, err := kit.contest.Execute(ctx, commerceapp.ContestDisguiseCommand{
		ReviewKey: "disguise-rf-1-r1", TransferKey: "disguise-rf-1", Payer: kit.payer, By: kit.payer,
	}); !errors.Is(err, commercedomain.ErrDisguiseState) {
		t.Fatalf("contest after terminal = %v, want ErrDisguiseState", err)
	}
	if _, err := kit.resolve.Execute(ctx, commerceapp.ResolveDisguiseCommand{
		ReviewKey: "disguise-rf-1-r1", TransferKey: "disguise-rf-1", Payer: kit.payer,
		Decision: "confirm", Basis: "contrato", TrailHash: disguiseEvidenceB,
		Now: now, AppealUntil: now.Add(72 * time.Hour),
	}); !errors.Is(err, commercedomain.ErrDisguiseState) {
		t.Fatalf("second terminal = %v, want ErrDisguiseState (terminal never reopens)", err)
	}
}

// TestDisguiseCancelledRollsBack proves a cancelled flag settles
// nothing: no row, no step, balances intact.
func TestDisguiseCancelledRollsBack(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newDisguiseKit(t, db, 100000)
	kit.settleGift(t, ctx, "disguise-cc-1", 7000)
	beforePayer := escrowBalance(t, ctx, db, kit.payer)

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := kit.flag.Execute(cancelled, commerceapp.FlagDisguiseCommand{
		ReviewKey: "disguise-cc-1-r1", TransferKey: "disguise-cc-1", Payer: kit.payer,
		Reason: "contract", EvidenceHash: disguiseEvidenceA, Reporter: "watcher",
	}); err == nil {
		t.Fatal("cancelled flag must fail, never settle")
	}
	var rows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.commerce_disguise_reviews`).Scan(&rows); err != nil {
		t.Fatalf("count reviews: %v", err)
	}
	if rows != 0 {
		t.Fatalf("reviews = %d, want none after rollback", rows)
	}
	if escrowBalance(t, ctx, db, kit.payer) != beforePayer {
		t.Fatal("cancelled flag must leave balances untouched")
	}
}

// TestDisguiseRacesCollapseToOne proves two simultaneous flags of one
// key settle a single review: one executes, one replays, with one
// review row under -race.
func TestDisguiseRacesCollapseToOne(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	ctx, cancel := escrowCtx()
	defer cancel()

	kit := newDisguiseKit(t, testDB, 100000)
	kit.settleGift(t, ctx, "disguise-race-1", 9000)

	var wg sync.WaitGroup
	type outcome struct {
		view *commerceapp.DisguiseReviewView
		err  error
	}
	outcomes := make([]outcome, 2)
	for i := range outcomes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcomes[i].view, outcomes[i].err = kit.flag.Execute(ctx, commerceapp.FlagDisguiseCommand{
				ReviewKey: "disguise-race-1-r1", TransferKey: "disguise-race-1", Payer: kit.payer,
				Reason: "recurrence", EvidenceHash: disguiseEvidenceA, Reporter: "watcher",
			})
		}(i)
	}
	wg.Wait()
	for _, o := range outcomes {
		if o.err != nil {
			t.Fatalf("concurrent flag: %v", o.err)
		}
	}
	if outcomes[0].view.ReviewID != outcomes[1].view.ReviewID {
		t.Fatal("runners resolved different reviews for one key")
	}
	var rows int
	if err := testDB.QueryRow(ctx, `SELECT count(*) FROM app.commerce_disguise_reviews`).Scan(&rows); err != nil {
		t.Fatalf("count reviews: %v", err)
	}
	if rows != 1 {
		t.Fatalf("review rows = %d, want exactly one flag", rows)
	}
}
