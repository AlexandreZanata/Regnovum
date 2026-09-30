package postgres_test

// P36-T07 — publication errors settle as current compensations on
// real PostgreSQL.
//
// A refund reverses the original legs under a new transfer and links
// a new row to the untouched cause: the citizen returns whole, the
// Treasury returns to pre-charge, the original keeps its amount,
// hashes and posted instant, and the compensation stamps the current
// database clock, never a backdate. Replays resolve, doubled
// compensations and unknown causes refuse, all with journal and
// registries unchanged where required. The suite runs on a
// disposable database and activates nothing.

import (
	"context"
	"errors"
	"testing"
	"time"

	meteringpg "github.com/AlexandreZanata/Regnovum/internal/metering/adapters/postgres"
	meteringapp "github.com/AlexandreZanata/Regnovum/internal/metering/application"
	meteringdomain "github.com/AlexandreZanata/Regnovum/internal/metering/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func settleCharge(t *testing.T, ctx context.Context, db *dbtest.TestDB, citizen, key string, clock publishClock) meteringapp.PublishResult {
	t.Helper()
	accepted := publishFixtureTime()
	content, price, quote := publishTerms(t, accepted, citizen)
	repo, err := meteringpg.NewRepository(db.Pool.Pool(), clock, approvedCatalog(t, price))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	uc, _ := meteringapp.NewPublishUseCase(repo)
	result, err := uc.Execute(ctx, publishCommand(key, citizen, content, price, quote, clock.now))
	if err != nil {
		t.Fatalf("settle charge: %v", err)
	}
	return *result
}

func refundUseCase(t *testing.T, db *dbtest.TestDB, clock publishClock) (*meteringapp.RefundUseCase, *meteringpg.Repository) {
	t.Helper()
	repo, err := meteringpg.NewRepository(db.Pool.Pool(), clock, meteringdomain.Catalog{})
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	uc, err := meteringapp.NewRefundUseCase(repo)
	if err != nil {
		t.Fatalf("NewRefundUseCase: %v", err)
	}
	return uc, repo
}

func countRefunds(t *testing.T, ctx context.Context, db *dbtest.TestDB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.metering_refunds`).Scan(&count); err != nil {
		t.Fatalf("count refunds: %v", err)
	}
	return count
}

// TestRefundCompensatesCurrentWithoutBackdate proves an erroneous
// charge returns whole through a new transfer: reversed legs, linked
// row, current database posting, and the original amount, hashes and
// posted instant intact.
func TestRefundCompensatesCurrentWithoutBackdate(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	_ = pool
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := publishFixtureTime()
	citizen := "citizen-refund"
	const funds = 100000
	seedLedger(t, ctx, db, citizen, funds)
	beforeTreasury := custodyMillis(t, ctx, db, "treasury", "main")
	clock := publishClock{now: accepted.Add(time.Minute)}
	charge := settleCharge(t, ctx, db, citizen, "pub-refund-1", clock)

	var originalPosted time.Time
	var originalAmount int64
	var originalQuote string
	if err := db.QueryRow(ctx,
		`SELECT posted_at, amount_milli, quote_hash FROM app.metering_publications WHERE id = $1::uuid`,
		charge.PublicationID).Scan(&originalPosted, &originalAmount, &originalQuote); err != nil {
		t.Fatalf("read original: %v", err)
	}

	beforeRefund := time.Now().UTC()
	uc, _ := refundUseCase(t, db, publishClock{now: accepted.Add(2 * time.Hour)})
	refund, err := uc.Execute(ctx, meteringapp.RefundCommand{
		Key: "refund-1", Account: citizen, OriginalKey: "pub-refund-1", Reason: "publication-error",
	})
	if err != nil {
		t.Fatalf("Execute refund: %v", err)
	}
	if refund.Replayed {
		t.Fatal("first compensation must not replay")
	}
	if refund.AmountMilli != charge.TotalMilli {
		t.Fatalf("refund = %d, want original charge %d", refund.AmountMilli, charge.TotalMilli)
	}
	if refund.TransferID == charge.TransferID {
		t.Fatal("compensation must move under a new transfer, never reuse the charge")
	}
	if refund.PostedAt.Before(beforeRefund.Add(-time.Minute)) {
		t.Fatalf("refund posted_at = %v, want the current database clock", refund.PostedAt)
	}
	if !refund.PostedAt.After(originalPosted) && !refund.PostedAt.Equal(originalPosted) {
		t.Fatalf("refund posted_at = %v, want at or after original %v", refund.PostedAt, originalPosted)
	}

	if debits, credits := countLegs(t, ctx, db, refund.TransferID); debits != 1 || credits != 1 {
		t.Fatalf("refund legs = %d/%d, want one reversed pair", debits, credits)
	}
	rows, err := db.Query(ctx,
		`SELECT c.kind, c.label, e.direction FROM app.economy_entries e
		 JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE e.transfer_id = $1::uuid`, refund.TransferID)
	if err != nil {
		t.Fatalf("read refund legs: %v", err)
	}
	defer rows.Close()
	seen := map[string]string{}
	for rows.Next() {
		var kind, label, direction string
		if err := rows.Scan(&kind, &label, &direction); err != nil {
			t.Fatalf("scan refund leg: %v", err)
		}
		seen[direction] = kind + "/" + label
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate refund legs: %v", err)
	}
	if seen["debit"] != "treasury/main" || seen["credit"] != "user/"+citizen {
		t.Fatalf("refund legs = %v, want reversed Treasury debit and citizen credit", seen)
	}

	var afterPosted time.Time
	var afterAmount int64
	var afterQuote string
	if err := db.QueryRow(ctx,
		`SELECT posted_at, amount_milli, quote_hash FROM app.metering_publications WHERE id = $1::uuid`,
		charge.PublicationID).Scan(&afterPosted, &afterAmount, &afterQuote); err != nil {
		t.Fatalf("reread original: %v", err)
	}
	if !afterPosted.Equal(originalPosted) || afterAmount != originalAmount || afterQuote != originalQuote {
		t.Fatal("compensation must never rewrite its cause")
	}
	if got := custodyMillis(t, ctx, db, "user", citizen); got != funds {
		t.Fatalf("citizen = %d, want whole %d", got, funds)
	}
	if got := custodyMillis(t, ctx, db, "treasury", "main"); got != beforeTreasury {
		t.Fatalf("treasury = %d, want pre-charge %d", got, beforeTreasury)
	}
}

// TestRefundReplayAndDuplicate proves the same refund key replays
// the original compensation, a second key for one cause refuses,
// and unknown or foreign causes refuse, all without new legs.
func TestRefundReplayAndDuplicate(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := publishFixtureTime()
	citizen := "citizen-refund-twice"
	seedLedger(t, ctx, db, citizen, 100000)
	clock := publishClock{now: accepted.Add(time.Minute)}
	charge := settleCharge(t, ctx, db, citizen, "pub-refund-2", clock)
	uc, _ := refundUseCase(t, db, publishClock{now: accepted.Add(2 * time.Hour)})

	first, err := uc.Execute(ctx, meteringapp.RefundCommand{
		Key: "refund-2", Account: citizen, OriginalKey: "pub-refund-2", Reason: "publication-error",
	})
	if err != nil {
		t.Fatalf("first refund: %v", err)
	}
	second, err := uc.Execute(ctx, meteringapp.RefundCommand{
		Key: "refund-2", Account: citizen, OriginalKey: "pub-refund-2", Reason: "publication-error",
	})
	if err != nil {
		t.Fatalf("replay refund: %v", err)
	}
	if !second.Replayed || second.RefundID != first.RefundID || second.TransferID != first.TransferID {
		t.Fatal("same key must replay the original compensation")
	}
	if _, err := uc.Execute(ctx, meteringapp.RefundCommand{
		Key: "refund-3", Account: citizen, OriginalKey: "pub-refund-2", Reason: "publication-error",
	}); !errors.Is(err, meteringdomain.ErrRefundDuplicate) {
		t.Fatalf("doubled compensation = %v, want ErrRefundDuplicate", err)
	}
	if _, err := uc.Execute(ctx, meteringapp.RefundCommand{
		Key: "refund-4", Account: citizen, OriginalKey: "pub-ghost", Reason: "publication-error",
	}); !errors.Is(err, meteringdomain.ErrUnknownPublication) {
		t.Fatalf("unknown cause = %v, want ErrUnknownPublication", err)
	}
	if _, err := uc.Execute(ctx, meteringapp.RefundCommand{
		Key: "refund-5", Account: "someone-else", OriginalKey: "pub-refund-2", Reason: "publication-error",
	}); !errors.Is(err, meteringdomain.ErrUnknownPublication) {
		t.Fatalf("foreign cause = %v, want ErrUnknownPublication", err)
	}
	if countRefunds(t, ctx, db) != 1 {
		t.Fatal("exactly one compensation row must exist")
	}
	if debits, credits := countLegs(t, ctx, db, first.TransferID); debits != 1 || credits != 1 {
		t.Fatalf("refund legs = %d/%d, want still one pair", debits, credits)
	}
	_ = charge
}
