package postgres_test

// P36-T07 — contract time survives price changes and calendar
// boundaries on real PostgreSQL.
//
// A quote accepted under v1 settles at v1 even after v2 takes
// effect: the accepted version protects the valid intention, while
// liveness still gates settlement. The settlement row keeps the
// acceptance terms with the database posted instant beside them, so
// accepted_at and posted_at never conflate across month boundaries.
// The suite runs on a disposable database and activates nothing.

import (
	"testing"
	"time"

	meteringpg "github.com/AlexandreZanata/Regnovum/internal/metering/adapters/postgres"
	meteringapp "github.com/AlexandreZanata/Regnovum/internal/metering/application"
	meteringdomain "github.com/AlexandreZanata/Regnovum/internal/metering/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

func windowPrice(t *testing.T, service string, version int, priceMilli int64, from, until time.Time) meteringdomain.PriceEntry {
	t.Helper()
	id, err := meteringdomain.ParseServiceID(service)
	if err != nil {
		t.Fatalf("ParseServiceID: %v", err)
	}
	entry, err := meteringdomain.NewPriceEntry(meteringdomain.PriceRequest{
		Service: id, Version: version, ValidFrom: from, ValidUntil: until,
		PriceMilli: priceMilli, Unit: meteringdomain.UnitGraphemeCluster, Authority: "test-authority",
	})
	if err != nil {
		t.Fatalf("NewPriceEntry v%d: %v", version, err)
	}
	return entry
}

// TestPriceChangeMidWayKeepsAcceptedTerms proves a settlement
// delayed past a price change still charges the accepted version:
// v1 accepted, v2 live at settlement, v1 amount moves and the row
// records v1.
func TestPriceChangeMidWayKeepsAcceptedTerms(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	change := time.Date(2026, 9, 29, 12, 30, 0, 0, time.UTC)
	v1 := windowPrice(t, "argument-publish", 1, 250, accepted.Add(-time.Hour), change)
	v2 := windowPrice(t, "argument-publish", 2, 400, change, change.Add(time.Hour))
	var catalog meteringdomain.Catalog
	if err := catalog.Add(v1); err != nil {
		t.Fatalf("approve v1: %v", err)
	}
	if err := catalog.Add(v2); err != nil {
		t.Fatalf("approve v2: %v", err)
	}

	citizen := "citizen-price-change"
	content, err := meteringdomain.ParseMeasuredContent("texto final", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("ParseMeasuredContent: %v", err)
	}
	quote, err := meteringdomain.AcceptPublicationQuote(meteringdomain.QuoteRequest{
		Account: citizen, Content: content, Price: v1, AcceptedAt: accepted, TTL: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("AcceptPublicationQuote: %v", err)
	}
	seedLedger(t, ctx, db, citizen, 100000)
	settledAt := change.Add(30 * time.Minute)
	clock := publishClock{now: settledAt}
	repo, err := meteringpg.NewRepository(pool, clock, catalog)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	var dbNow time.Time
	if err := db.QueryRow(ctx, `SELECT now()`).Scan(&dbNow); err != nil {
		t.Fatalf("read database clock: %v", err)
	}
	uc, _ := meteringapp.NewPublishUseCase(repo)
	result, err := uc.Execute(ctx, meteringapp.PublishCommand{
		Key: "pub-price-change", Account: citizen, Content: content, Price: v1, Quote: quote,
		FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main", Now: settledAt,
	})
	if err != nil {
		t.Fatalf("delayed settlement: %v", err)
	}
	wantTotal, err := meteringdomain.TotalFor(content.Units(), 250)
	if err != nil {
		t.Fatalf("TotalFor: %v", err)
	}
	if result.TotalMilli != wantTotal {
		t.Fatalf("total = %d, want accepted v1 %d", result.TotalMilli, wantTotal)
	}
	var version int
	var amount int64
	var posted time.Time
	if err := db.QueryRow(ctx,
		`SELECT price_version, amount_milli, posted_at FROM app.metering_publications WHERE id = $1::uuid`,
		result.PublicationID).Scan(&version, &amount, &posted); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if version != 1 || amount != wantTotal {
		t.Fatalf("row = v%d/%d, want v1/%d", version, amount, wantTotal)
	}
	// posted_at runs on the real database clock, not on the
	// fictional acceptance timeline: it must be current.
	if posted.Before(dbNow) {
		t.Fatalf("posted_at = %v, want at or after the pre-call database clock %v", posted, dbNow)
	}
}

// TestAcceptedAtStaysDistinctFromPostedAt proves the settlement row
// carries both instants without conflating them: the acceptance
// terms seal accepted_at, the database stamps posted_at, and a
// settlement crossing a month boundary lands current.
func TestAcceptedAtStaysDistinctFromPostedAt(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := time.Date(2026, 1, 31, 23, 50, 0, 0, time.UTC)
	v1 := windowPrice(t, "argument-publish", 1, 250, accepted.Add(-time.Hour), accepted.Add(2*time.Hour))
	var catalog meteringdomain.Catalog
	if err := catalog.Add(v1); err != nil {
		t.Fatalf("approve v1: %v", err)
	}
	citizen := "citizen-month-edge"
	content, err := meteringdomain.ParseMeasuredContent("texto final", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("ParseMeasuredContent: %v", err)
	}
	quote, err := meteringdomain.AcceptPublicationQuote(meteringdomain.QuoteRequest{
		Account: citizen, Content: content, Price: v1, AcceptedAt: accepted, TTL: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("AcceptPublicationQuote: %v", err)
	}
	seedLedger(t, ctx, db, citizen, 100000)
	settledAt := time.Date(2026, 2, 1, 0, 30, 0, 0, time.UTC)
	clock := publishClock{now: settledAt}
	repo, err := meteringpg.NewRepository(pool, clock, catalog)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	var dbNow time.Time
	if err := db.QueryRow(ctx, `SELECT now()`).Scan(&dbNow); err != nil {
		t.Fatalf("read database clock: %v", err)
	}
	uc, _ := meteringapp.NewPublishUseCase(repo)
	result, err := uc.Execute(ctx, meteringapp.PublishCommand{
		Key: "pub-month-edge", Account: citizen, Content: content, Price: v1, Quote: quote,
		FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main", Now: settledAt,
	})
	if err != nil {
		t.Fatalf("month-edge settlement: %v", err)
	}
	var storedHash string
	var storedVersion int
	var posted time.Time
	if err := db.QueryRow(ctx,
		`SELECT quote_hash, price_version, posted_at FROM app.metering_publications WHERE id = $1::uuid`,
		result.PublicationID).Scan(&storedHash, &storedVersion, &posted); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if storedHash != quote.Hash || storedVersion != 1 {
		t.Fatal("cross-month settlement must keep the accepted terms")
	}
	// The acceptance lives in the January scenario timeline while
	// the posting runs on the real database clock: two instants
	// from two clocks, never conflated.
	if posted.Before(dbNow) {
		t.Fatalf("posted_at = %v, want at or after the pre-call database clock %v", posted, dbNow)
	}
	if posted.Equal(quote.AcceptedAt) {
		t.Fatal("posted_at must be the database instant, never a copy of accepted_at")
	}
	if quote.AcceptedAt.Month() != time.January {
		t.Fatalf("accepted_at = %v, want the January acceptance", quote.AcceptedAt)
	}
}
