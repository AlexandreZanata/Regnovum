package postgres_test

// P36-T06 — institutional service fees reuse the publication port
// while bonds stay reservations on real PostgreSQL.
//
// Approved fee tables settle citizen-to-Treasury charges through the
// same measure, quote and publish path as publications: one
// deployment catalog prices each service, and the adapter re-prices
// every request against it. Refundable bonds travel the holds
// machinery instead: locked value can never feed a charge, and a
// release returns it whole. Services outside the approved table stay
// unavailable even with a self-sealed quote. The suite runs on a
// disposable database and activates nothing.

import (
	"errors"
	"testing"
	"time"

	economypg "github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	economyapp "github.com/AlexandreZanata/Regnovum/internal/economy/application"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	meteringpg "github.com/AlexandreZanata/Regnovum/internal/metering/adapters/postgres"
	meteringapp "github.com/AlexandreZanata/Regnovum/internal/metering/application"
	meteringdomain "github.com/AlexandreZanata/Regnovum/internal/metering/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

func feePrice(t *testing.T, service string, priceMilli int64, accepted time.Time) meteringdomain.PriceEntry {
	t.Helper()
	id, err := meteringdomain.ParseServiceID(service)
	if err != nil {
		t.Fatalf("ParseServiceID(%q): %v", service, err)
	}
	entry, err := meteringdomain.NewPriceEntry(meteringdomain.PriceRequest{
		Service: id, Version: 1,
		ValidFrom: accepted.Add(-time.Hour), ValidUntil: accepted.Add(time.Hour),
		PriceMilli: priceMilli, Unit: meteringdomain.UnitGraphemeCluster, Authority: "test-authority",
	})
	if err != nil {
		t.Fatalf("NewPriceEntry(%q): %v", service, err)
	}
	return entry
}

func feeQuote(t *testing.T, account, service string, priceMilli int64, accepted time.Time) (meteringdomain.MeasuredContent, meteringdomain.PriceEntry, meteringdomain.PublicationQuote) {
	t.Helper()
	content, err := meteringdomain.ParseMeasuredContent("texto final da taxa", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("ParseMeasuredContent: %v", err)
	}
	price := feePrice(t, service, priceMilli, accepted)
	quote, err := meteringdomain.AcceptPublicationQuote(meteringdomain.QuoteRequest{
		Account: account, Content: content, Price: price, AcceptedAt: accepted, TTL: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("AcceptPublicationQuote(%q): %v", service, err)
	}
	return content, price, quote
}

// TestInstitutionalFeesSettleToTreasury proves approved challenge,
// mediation and certificate fees settle through the publication port
// with exact amounts: each service prices from the same deployment
// catalog, and every milliINK lands in the Treasury.
func TestInstitutionalFeesSettleToTreasury(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := publishFixtureTime()
	citizen := "citizen-fees"
	services := []struct {
		name       string
		priceMilli int64
	}{
		{name: "challenge-fee", priceMilli: 100},
		{name: "mediation-fee", priceMilli: 250},
		{name: "certificate-fee", priceMilli: 500},
	}
	var catalog meteringdomain.Catalog
	for _, service := range services {
		if err := catalog.Add(feePrice(t, service.name, service.priceMilli, accepted)); err != nil {
			t.Fatalf("approve %q: %v", service.name, err)
		}
	}
	seedLedger(t, ctx, db, citizen, 1000000)
	beforeTreasury := custodyMillis(t, ctx, db, "treasury", "main")
	clock := publishClock{now: accepted.Add(time.Minute)}
	repo, err := meteringpg.NewRepository(pool, clock, catalog)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	uc, _ := meteringapp.NewPublishUseCase(repo)

	var paid int64
	for _, service := range services {
		content, price, quote := feeQuote(t, citizen, service.name, service.priceMilli, accepted)
		result, err := uc.Execute(ctx, meteringapp.PublishCommand{
			Key: "fee-" + service.name, Account: citizen, Content: content, Price: price, Quote: quote,
			FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main",
			Now: clock.now,
		})
		if err != nil {
			t.Fatalf("fee %q: %v", service.name, err)
		}
		if result.TotalMilli != quote.TotalMilli {
			t.Fatalf("fee %q total = %d, want quoted %d", service.name, result.TotalMilli, quote.TotalMilli)
		}
		paid += quote.TotalMilli
	}
	if got := custodyMillis(t, ctx, db, "treasury", "main"); got != beforeTreasury+paid {
		t.Fatalf("treasury = %d, want %d", got, beforeTreasury+paid)
	}
	if countPublications(t, ctx, db) != len(services) {
		t.Fatalf("publications = %d, want one per fee", countPublications(t, ctx, db))
	}
}

// TestUnapprovedServiceStaysUnavailable proves a service outside the
// approved table settles nothing even with a self-sealed quote, and
// that terms diverging from the approved entry refuse at the
// adapter: every request is re-priced server-side before any lock is
// taken or leg written.
func TestUnapprovedServiceStaysUnavailable(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := publishFixtureTime()
	citizen := "citizen-unapproved"
	seedLedger(t, ctx, db, citizen, 1000000)
	var catalog meteringdomain.Catalog
	if err := catalog.Add(feePrice(t, "challenge-fee", 100, accepted)); err != nil {
		t.Fatalf("approve challenge-fee: %v", err)
	}
	clock := publishClock{now: accepted.Add(time.Minute)}
	repo, err := meteringpg.NewRepository(pool, clock, catalog)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	uc, _ := meteringapp.NewPublishUseCase(repo)

	content, forgedPrice, forgedQuote := feeQuote(t, citizen, "casino-bet", 100, accepted)
	if _, err := uc.Execute(ctx, meteringapp.PublishCommand{
		Key: "unapproved-1", Account: citizen, Content: content, Price: forgedPrice, Quote: forgedQuote,
		FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main", Now: clock.now,
	}); !errors.Is(err, meteringdomain.ErrPriceNotFound) {
		t.Fatalf("unapproved service = %v, want ErrPriceNotFound", err)
	}

	roguePrice := feePrice(t, "challenge-fee", 100, accepted)
	roguePrice.Version = 9
	rogueContent, err := meteringdomain.ParseMeasuredContent("texto final da taxa", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("rogue content: %v", err)
	}
	rogueQuote, err := meteringdomain.AcceptPublicationQuote(meteringdomain.QuoteRequest{
		Account: citizen, Content: rogueContent, Price: roguePrice, AcceptedAt: accepted, TTL: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("rogue quote: %v", err)
	}
	if _, err := uc.Execute(ctx, meteringapp.PublishCommand{
		Key: "unapproved-2", Account: citizen, Content: rogueContent, Price: roguePrice, Quote: rogueQuote,
		FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main", Now: clock.now,
	}); !errors.Is(err, meteringdomain.ErrInvalidQuote) {
		t.Fatalf("stale terms = %v, want ErrInvalidQuote", err)
	}

	lapsedContent, lapsedPrice, _ := feeQuote(t, citizen, "challenge-fee", 100, accepted.Add(-time.Hour))
	lapsedQuote, err := meteringdomain.AcceptPublicationQuote(meteringdomain.QuoteRequest{
		Account: citizen, Content: lapsedContent, Price: lapsedPrice, AcceptedAt: accepted.Add(-time.Hour), TTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("lapsed quote: %v", err)
	}
	if _, err := uc.Execute(ctx, meteringapp.PublishCommand{
		Key: "unapproved-3", Account: citizen, Content: lapsedContent, Price: lapsedPrice, Quote: lapsedQuote,
		FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main", Now: clock.now,
	}); !errors.Is(err, meteringdomain.ErrQuoteExpired) {
		t.Fatalf("lapsed acceptance = %v, want ErrQuoteExpired", err)
	}

	if countPublications(t, ctx, db) != 0 {
		t.Fatal("refused services must leave the registry empty")
	}
}

// TestBondStaysReservationNeverFee proves refundable bonds live in
// holds, never in charges: locked value cannot feed a publication,
// and a release returns it whole to the owner with the Treasury
// untouched.
func TestBondStaysReservationNeverFee(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := publishFixtureTime()
	citizen := "citizen-bond"
	const funds = 100000
	const bond = 20000
	seedLedger(t, ctx, db, citizen, funds)
	beforeTreasury := custodyMillis(t, ctx, db, "treasury", "main")

	economyRepo := economypg.NewRepository(pool)
	clock := publishClock{now: accepted.Add(time.Minute)}
	reserveUC := economyapp.NewReserveUseCase(economyRepo, clock)
	hold, err := reserveUC.Execute(ctx, economyapp.ReserveCommand{
		OwnerKind: "user", OwnerLabel: citizen, Purpose: "challenge-bond",
		Millis: bond, ExpiresAt: accepted.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("reserve bond: %v", err)
	}
	if got := custodyMillis(t, ctx, db, "user", citizen); got != funds-bond {
		t.Fatalf("citizen after reserve = %d, want locked out %d", got, funds-bond)
	}

	var holdLabel string
	if err := db.QueryRow(ctx,
		`SELECT label FROM app.economy_custodies WHERE id = $1::uuid`, hold.HoldCustodyID).Scan(&holdLabel); err != nil {
		t.Fatalf("read hold custody: %v", err)
	}
	bondContent, err := meteringdomain.ParseMeasuredContent("texto da caucao", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("bond content: %v", err)
	}
	bondPrice := feePrice(t, "challenge-fee", 100, accepted)
	bondQuote, err := meteringdomain.AcceptPublicationQuote(meteringdomain.QuoteRequest{
		Account: holdLabel, Content: bondContent, Price: bondPrice, AcceptedAt: accepted, TTL: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("bond quote: %v", err)
	}
	var catalog meteringdomain.Catalog
	if err := catalog.Add(bondPrice); err != nil {
		t.Fatalf("approve challenge-fee: %v", err)
	}
	feeRepo, err := meteringpg.NewRepository(pool, clock, catalog)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	feeUC, _ := meteringapp.NewPublishUseCase(feeRepo)
	if _, err := feeUC.Execute(ctx, meteringapp.PublishCommand{
		Key: "bond-as-fee", Account: holdLabel, Content: bondContent, Price: bondPrice, Quote: bondQuote,
		FromKind: "escrow", FromLabel: holdLabel, ToKind: "treasury", ToLabel: "main", Now: clock.now,
	}); !errors.Is(err, economydomain.ErrUnauthorizedCustody) {
		t.Fatalf("bond as fee = %v, want ErrUnauthorizedCustody", err)
	}

	releaseUC := economyapp.NewReleaseUseCase(economyRepo)
	if _, err := releaseUC.Execute(ctx, economyapp.SettleCommand{HoldID: hold.HoldID}); err != nil {
		t.Fatalf("release bond: %v", err)
	}
	if got := custodyMillis(t, ctx, db, "user", citizen); got != funds {
		t.Fatalf("citizen after release = %d, want whole %d", got, funds)
	}
	if got := custodyMillis(t, ctx, db, "treasury", "main"); got != beforeTreasury {
		t.Fatalf("treasury moved on a bond: %d vs %d", got, beforeTreasury)
	}
	if countPublications(t, ctx, db) != 0 {
		t.Fatal("a bond must never settle as a publication")
	}
}
