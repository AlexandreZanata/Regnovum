package postgres_test

// P36-T08 — receipts and extracts read settled evidence on real
// PostgreSQL.
//
// One receipt binds the sealed publication to its legs and its
// compensation, when compensated; the extract lists the owner lines
// in reverse posting order beside the journal-derived balance.
// Another owner's rows and unknown ids resolve to absence, never to
// a leak. The suite runs on a disposable database and activates
// nothing.

import (
	"errors"
	"testing"
	"time"

	meteringpg "github.com/AlexandreZanata/Regnovum/internal/metering/adapters/postgres"
	meteringapp "github.com/AlexandreZanata/Regnovum/internal/metering/application"
	meteringdomain "github.com/AlexandreZanata/Regnovum/internal/metering/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func refundCommand(key, account, original string) meteringapp.RefundCommand {
	return meteringapp.RefundCommand{Key: key, Account: account, OriginalKey: original, Reason: "publication-error"}
}

func TestReceiptBindsLegsAndCompensation(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := publishFixtureTime()
	citizen := "citizen-receipt"
	seedLedger(t, ctx, db, citizen, 100000)
	clock := publishClock{now: accepted.Add(time.Minute)}
	content, price, quote := publishTerms(t, accepted, citizen)
	repo, err := meteringpg.NewRepository(pool, clock, approvedCatalog(t, price))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	settled := settleCharge(t, ctx, db, citizen, "pub-receipt-1", clock)

	receipt, err := repo.GetReceipt(ctx, citizen, settled.PublicationID)
	if err != nil {
		t.Fatalf("GetReceipt: %v", err)
	}
	if receipt.Publication.AmountMilli != quote.TotalMilli || receipt.Publication.QuoteHash != quote.Hash {
		t.Fatal("receipt must carry the sealed terms")
	}
	if len(receipt.Legs) != 2 || receipt.Refund != nil {
		t.Fatalf("receipt legs/refund = %d/%v, want one pair and no compensation yet", len(receipt.Legs), receipt.Refund)
	}

	refundUC, _ := refundUseCase(t, db, publishClock{now: accepted.Add(2 * time.Hour)})
	if _, err := refundUC.Execute(ctx, refundCommand("refund-receipt-1", citizen, "pub-receipt-1")); err != nil {
		t.Fatalf("refund: %v", err)
	}
	compensated, err := repo.GetReceipt(ctx, citizen, settled.PublicationID)
	if err != nil {
		t.Fatalf("GetReceipt after refund: %v", err)
	}
	if compensated.Refund == nil || compensated.Refund.Amount != quote.TotalMilli {
		t.Fatal("compensated receipt must name the full compensation")
	}
	_ = content
}

func TestReceiptRefusesForeignAndUnknown(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := publishFixtureTime()
	citizen := "citizen-receipt-owner"
	seedLedger(t, ctx, db, citizen, 100000)
	clock := publishClock{now: accepted.Add(time.Minute)}
	content, price, quote := publishTerms(t, accepted, citizen)
	repo, _ := meteringpg.NewRepository(pool, clock, approvedCatalog(t, price))
	settled := settleCharge(t, ctx, db, citizen, "pub-receipt-2", clock)

	if _, err := repo.GetReceipt(ctx, "someone-else", settled.PublicationID); !errors.Is(err, meteringdomain.ErrUnknownPublication) {
		t.Fatalf("foreign receipt = %v, want ErrUnknownPublication", err)
	}
	if _, err := repo.GetReceipt(ctx, citizen, "00000000-0000-4000-8000-000000000000"); !errors.Is(err, meteringdomain.ErrUnknownPublication) {
		t.Fatalf("unknown receipt = %v, want ErrUnknownPublication", err)
	}
	if _, err := repo.GetReceipt(ctx, citizen, "not-a-uuid"); !errors.Is(err, meteringdomain.ErrUnknownPublication) {
		t.Fatalf("malformed receipt = %v, want ErrUnknownPublication", err)
	}
	_, _, _ = content, price, quote
}

func TestStatementListsOwnedLinesWithBalance(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := publishFixtureTime()
	citizen := "citizen-statement"
	const funds = 100000
	seedLedger(t, ctx, db, citizen, funds)
	clock := publishClock{now: accepted.Add(time.Minute)}
	content, price, quote := publishTerms(t, accepted, citizen)
	repo, _ := meteringpg.NewRepository(pool, clock, approvedCatalog(t, price))
	first := settleCharge(t, ctx, db, citizen, "pub-stmt-1", clock)
	second := settleCharge(t, ctx, db, citizen, "pub-stmt-2", clock)

	statement, err := repo.Statement(ctx, citizen, 10)
	if err != nil {
		t.Fatalf("Statement: %v", err)
	}
	if len(statement.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(statement.Entries))
	}
	if statement.Entries[0].Publication.ID != second.PublicationID || statement.Entries[1].Publication.ID != first.PublicationID {
		t.Fatal("extract must list reverse posting order")
	}
	if statement.Entries[0].Refunded || statement.Entries[1].Refunded {
		t.Fatal("uncompensated lines must not read refunded")
	}
	if statement.BalanceMilli != funds-2*quote.TotalMilli {
		t.Fatalf("balance = %d, want journal-derived %d", statement.BalanceMilli, funds-2*quote.TotalMilli)
	}
	_ = content

	refundUC, _ := refundUseCase(t, db, publishClock{now: accepted.Add(2 * time.Hour)})
	if _, err := refundUC.Execute(ctx, refundCommand("refund-stmt-1", citizen, "pub-stmt-1")); err != nil {
		t.Fatalf("refund: %v", err)
	}
	rebuilt, err := repo.Statement(ctx, citizen, 10)
	if err != nil {
		t.Fatalf("Statement after refund: %v", err)
	}
	for _, entry := range rebuilt.Entries {
		if entry.Publication.ID == first.PublicationID && !entry.Refunded {
			t.Fatal("compensated line must read refunded")
		}
	}
	if rebuilt.BalanceMilli != funds-quote.TotalMilli {
		t.Fatalf("balance after refund = %d, want %d", rebuilt.BalanceMilli, funds-quote.TotalMilli)
	}

	empty, err := repo.Statement(ctx, "ghost-owner", 10)
	if err != nil || len(empty.Entries) != 0 || empty.BalanceMilli != 0 {
		t.Fatalf("unknown owner = %+v,%v, want empty extract with zero balance", empty, err)
	}
	if _, err := repo.Statement(ctx, citizen, 0); err == nil {
		t.Fatal("non-positive limit must refuse")
	}
}
