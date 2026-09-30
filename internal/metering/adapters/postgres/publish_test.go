package postgres_test

// P36-T04 — publication and INK charge commit together on real
// PostgreSQL.
//
// One transaction writes the publication row and the
// citizen-to-Treasury legs moving the exact quoted cost: the database
// clock stamps the posting inside the writing transaction, so the row
// is only visible after commit. The tests prove on a disposable
// database: exact pairing of legs and row under one intention,
// post-commit replay without duplication, divergent payload conflict,
// and every failure stage (unknown custody, short balance, frozen
// kinds, cancelled context) with journal and registry unchanged.
// Statement and content always point at the same transfer id.
//
// Fault staging is per failure point: this adapter owns its
// transaction like the economy and billing adapters, so each case
// dies at its own stage instead of through the shared TxManager
// port. The frozen book is exercised by the economy suite; this
// adapter replicates the same mode read.

import (
	"context"
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

type publishClock struct{ now time.Time }

func (c publishClock) Now() time.Time { return c.now }

func publishCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func publishFixtureTime() time.Time {
	return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
}

func seedLedger(t *testing.T, ctx context.Context, db *dbtest.TestDB, citizen string, funds int64) (economyRepo *economypg.Repository) {
	t.Helper()
	pool := db.Pool.Pool()
	repo := economypg.NewRepository(pool)
	key, err := economydomain.ParseGenesisKey("publish-funding-seed")
	if err != nil {
		t.Fatalf("ParseGenesisKey: %v", err)
	}
	if _, err := repo.RunGenesis(ctx, economyapp.GenesisRequest{Key: key, Season: economydomain.SeasonKey(economydomain.CompatSeasonKey)}); err != nil {
		t.Fatalf("seed Genesis: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('user', $1)`, citizen); err != nil {
		t.Fatalf("create citizen custody: %v", err)
	}
	amount, err := economydomain.NewMilliInk(funds)
	if err != nil {
		t.Fatalf("NewMilliInk: %v", err)
	}
	fromKind, _ := economydomain.ParseCustodyKind("treasury")
	toKind, _ := economydomain.ParseCustodyKind("user")
	if _, err := repo.Transfer(ctx, economyapp.TransferRequest{
		FromSeason: economydomain.SeasonKey(economydomain.CompatSeasonKey),
		FromKind:   fromKind, FromLabel: "main",
		ToSeason: economydomain.SeasonKey(economydomain.CompatSeasonKey),
		ToKind:   toKind, ToLabel: citizen, Amount: amount,
	}); err != nil {
		t.Fatalf("fund citizen: %v", err)
	}
	return repo
}

func publishTerms(t *testing.T, accepted time.Time, citizen string) (meteringdomain.MeasuredContent, meteringdomain.PriceEntry, meteringdomain.PublicationQuote) {
	t.Helper()
	content, err := meteringdomain.ParseMeasuredContent("texto final", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("ParseMeasuredContent: %v", err)
	}
	service, err := meteringdomain.ParseServiceID("argument-publish")
	if err != nil {
		t.Fatalf("ParseServiceID: %v", err)
	}
	price, err := meteringdomain.NewPriceEntry(meteringdomain.PriceRequest{
		Service: service, Version: 2,
		ValidFrom: accepted.Add(-time.Hour), ValidUntil: accepted.Add(time.Hour),
		PriceMilli: 250, Unit: meteringdomain.UnitGraphemeCluster, Authority: "test-authority",
	})
	if err != nil {
		t.Fatalf("NewPriceEntry: %v", err)
	}
	quote, err := meteringdomain.AcceptPublicationQuote(meteringdomain.QuoteRequest{
		Account: citizen, Content: content, Price: price, AcceptedAt: accepted, TTL: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("AcceptPublicationQuote: %v", err)
	}
	return content, price, quote
}

func publishCommand(key, citizen string, content meteringdomain.MeasuredContent, price meteringdomain.PriceEntry, quote meteringdomain.PublicationQuote, now time.Time) meteringapp.PublishCommand {
	return meteringapp.PublishCommand{
		Key: key, Account: citizen, Content: content, Price: price, Quote: quote,
		FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main", Now: now,
	}
}

func countPublications(t *testing.T, ctx context.Context, db *dbtest.TestDB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.metering_publications`).Scan(&count); err != nil {
		t.Fatalf("count publications: %v", err)
	}
	return count
}

// approvedCatalog builds the deployment-approved price table for
// tests: only the supplied entries settle, every other service stays
// unavailable.
func approvedCatalog(t *testing.T, prices ...meteringdomain.PriceEntry) meteringdomain.Catalog {
	t.Helper()
	var catalog meteringdomain.Catalog
	for _, price := range prices {
		if err := catalog.Add(price); err != nil {
			t.Fatalf("Add approved price: %v", err)
		}
	}
	return catalog
}

func countLegs(t *testing.T, ctx context.Context, db *dbtest.TestDB, transfer string) (debits, credits int64) {
	t.Helper()
	if err := db.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE direction = 'debit'), count(*) FILTER (WHERE direction = 'credit')
		 FROM app.economy_entries WHERE transfer_id = $1::uuid`, transfer).Scan(&debits, &credits); err != nil {
		t.Fatalf("count legs: %v", err)
	}
	return debits, credits
}

func custodyMillis(t *testing.T, ctx context.Context, db *dbtest.TestDB, kind, label string) int64 {
	t.Helper()
	var balance int64
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = $1 AND c.label = $2`, kind, label).Scan(&balance); err != nil {
		t.Fatalf("balance %s/%s: %v", kind, label, err)
	}
	return balance
}

// TestPublishChargesAtomically proves one settlement writes exactly
// one publication row and one paired debit/credit under the same
// transfer: the statement and the content point at one intention,
// the database clock stamps the posting, and balances move the exact
// quoted cost.
func TestPublishChargesAtomically(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := publishFixtureTime()
	citizen := "citizen-atomic"
	seedLedger(t, ctx, db, citizen, 100000)
	beforeCitizen := custodyMillis(t, ctx, db, "user", citizen)
	beforeTreasury := custodyMillis(t, ctx, db, "treasury", "main")

	content, price, quote := publishTerms(t, accepted, citizen)
	clock := publishClock{now: accepted.Add(time.Minute)}
	repo, err := meteringpg.NewRepository(pool, clock, approvedCatalog(t, price))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	uc, err := meteringapp.NewPublishUseCase(repo)
	if err != nil {
		t.Fatalf("NewPublishUseCase: %v", err)
	}
	result, err := uc.Execute(ctx, publishCommand("pub-atomic-1", citizen, content, price, quote, clock.now))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Replayed {
		t.Fatal("first settlement must not replay")
	}
	if result.TotalMilli != quote.TotalMilli {
		t.Fatalf("total = %d, want quoted %d", result.TotalMilli, quote.TotalMilli)
	}
	if result.PostedAt.IsZero() {
		t.Fatal("posted_at must come from the database clock, never zero")
	}
	if countPublications(t, ctx, db) != 1 {
		t.Fatal("exactly one publication row must exist")
	}
	if debits, credits := countLegs(t, ctx, db, result.TransferID); debits != 1 || credits != 1 {
		t.Fatalf("legs = %d/%d, want one debit and one credit", debits, credits)
	}
	var storedKey, storedAccount, storedTransfer, storedContent, storedQuote string
	var storedAmount int64
	var storedPosted time.Time
	if err := db.QueryRow(ctx,
		`SELECT intention_key, account_label, transfer_id::text, content_hash, quote_hash, amount_milli, posted_at
		 FROM app.metering_publications WHERE id = $1::uuid`, result.PublicationID).Scan(
		&storedKey, &storedAccount, &storedTransfer, &storedContent, &storedQuote, &storedAmount, &storedPosted); err != nil {
		t.Fatalf("read publication: %v", err)
	}
	if storedKey != "pub-atomic-1" || storedAccount != citizen || storedTransfer != result.TransferID {
		t.Fatalf("row points at %+v, want key/account/transfer of this intention", storedKey)
	}
	if storedContent != quote.ContentHash.String() || storedQuote != quote.Hash || storedAmount != quote.TotalMilli {
		t.Fatal("row must carry the sealed quote terms untouched")
	}
	if !storedPosted.Equal(result.PostedAt) {
		t.Fatal("returned posted_at must be the stored database instant")
	}
	var legAmount int64
	var legKind, legLabel, legDirection string
	rows, err := db.Query(ctx,
		`SELECT e.amount_milli, c.kind, c.label, e.direction FROM app.economy_entries e
		 JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE e.transfer_id = $1::uuid ORDER BY e.direction`, result.TransferID)
	if err != nil {
		t.Fatalf("read legs: %v", err)
	}
	defer rows.Close()
	seen := map[string]string{}
	for rows.Next() {
		if err := rows.Scan(&legAmount, &legKind, &legLabel, &legDirection); err != nil {
			t.Fatalf("scan leg: %v", err)
		}
		if legAmount != quote.TotalMilli {
			t.Fatalf("leg amount = %d, want quoted %d", legAmount, quote.TotalMilli)
		}
		seen[legDirection] = legKind + "/" + legLabel
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate legs: %v", err)
	}
	if seen["debit"] != "user/"+citizen || seen["credit"] != "treasury/main" {
		t.Fatalf("legs = %v, want citizen debit and Treasury credit", seen)
	}
	if got := custodyMillis(t, ctx, db, "user", citizen); got != beforeCitizen-quote.TotalMilli {
		t.Fatalf("citizen = %d, want %d", got, beforeCitizen-quote.TotalMilli)
	}
	if got := custodyMillis(t, ctx, db, "treasury", "main"); got != beforeTreasury+quote.TotalMilli {
		t.Fatalf("treasury = %d, want %d", got, beforeTreasury+quote.TotalMilli)
	}
}

// TestPublishReplayAfterCommit proves a retried intention after a
// post-commit timeout resolves the original settlement: same row,
// same transfer, no new legs.
func TestPublishReplayAfterCommit(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := publishFixtureTime()
	citizen := "citizen-replay"
	seedLedger(t, ctx, db, citizen, 100000)
	content, price, quote := publishTerms(t, accepted, citizen)
	clock := publishClock{now: accepted.Add(time.Minute)}
	repo, err := meteringpg.NewRepository(pool, clock, approvedCatalog(t, price))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	uc, _ := meteringapp.NewPublishUseCase(repo)
	first, err := uc.Execute(ctx, publishCommand("pub-replay-1", citizen, content, price, quote, clock.now))
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	second, err := uc.Execute(ctx, publishCommand("pub-replay-1", citizen, content, price, quote, clock.now))
	if err != nil {
		t.Fatalf("replay Execute: %v", err)
	}
	if !second.Replayed {
		t.Fatal("same key and payload must replay")
	}
	if second.PublicationID != first.PublicationID || second.TransferID != first.TransferID {
		t.Fatal("replay must resolve the original row and transfer")
	}
	if countPublications(t, ctx, db) != 1 {
		t.Fatal("replay must write no new publication")
	}
	if debits, credits := countLegs(t, ctx, db, first.TransferID); debits != 1 || credits != 1 {
		t.Fatalf("legs after replay = %d/%d, want still one pair", debits, credits)
	}
}

// TestPublishConflictOnDivergentPayload proves the same key with
// edited terms conflicts instead of charging twice: no new row, no
// new legs.
func TestPublishConflictOnDivergentPayload(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := publishFixtureTime()
	citizen := "citizen-conflict"
	seedLedger(t, ctx, db, citizen, 100000)
	content, price, quote := publishTerms(t, accepted, citizen)
	clock := publishClock{now: accepted.Add(time.Minute)}
	repo, _ := meteringpg.NewRepository(pool, clock, approvedCatalog(t, price))
	uc, _ := meteringapp.NewPublishUseCase(repo)
	if _, err := uc.Execute(ctx, publishCommand("pub-conflict-1", citizen, content, price, quote, clock.now)); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	edited, err := meteringdomain.ParseMeasuredContent("texto adulterado", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("edited: %v", err)
	}
	editedPrice := price
	editedPrice.PriceMilli = price.PriceMilli
	editedQuote, err := meteringdomain.AcceptPublicationQuote(meteringdomain.QuoteRequest{
		Account: citizen, Content: edited, Price: editedPrice, AcceptedAt: accepted, TTL: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("edited quote: %v", err)
	}
	_, err = repo.Publish(ctx, meteringapp.PublishRequest{
		Key: mustPublishKey(t, "pub-conflict-1"), Account: citizen, Content: edited,
		Price: editedPrice, Quote: editedQuote,
		FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main",
		At: clock.now,
		PayloadHash: meteringdomain.PublishPayloadHash(meteringdomain.PublishPayload{
			Account: citizen, Service: editedQuote.Service.String(), Version: editedQuote.Version,
			Units: editedQuote.Units, AmountMilli: editedQuote.TotalMilli,
			ContentHash: editedQuote.ContentHash.String(), QuoteHash: editedQuote.Hash,
			FromKind: "user", FromLabel: citizen, ToKind: "treasury", ToLabel: "main",
		}),
	})
	if !errors.Is(err, meteringdomain.ErrPublishConflict) {
		t.Fatalf("divergent payload = %v, want ErrPublishConflict", err)
	}
	if countPublications(t, ctx, db) != 1 {
		t.Fatal("conflict must write no new publication")
	}
}

func mustPublishKey(t *testing.T, raw string) meteringdomain.PublishKey {
	t.Helper()
	key, err := meteringdomain.ParsePublishKey(raw)
	if err != nil {
		t.Fatalf("ParsePublishKey: %v", err)
	}
	return key
}

// TestPublishRefusesWithoutWriting proves every failure stage dies
// with journal and registry unchanged: unknown custody, short
// balance, locked source kind and cancelled context write nothing.
func TestPublishRefusesWithoutWriting(t *testing.T) {
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := publishFixtureTime()
	citizen := "citizen-refused"
	seedLedger(t, ctx, db, citizen, 100000)
	content, price, quote := publishTerms(t, accepted, citizen)
	clock := publishClock{now: accepted.Add(time.Minute)}
	repo, _ := meteringpg.NewRepository(pool, clock, approvedCatalog(t, price))
	uc, _ := meteringapp.NewPublishUseCase(repo)
	beforeCitizen := custodyMillis(t, ctx, db, "user", citizen)

	poor := "citizen-poor"
	if _, err := pool.Exec(ctx, `INSERT INTO app.economy_custodies (kind, label) VALUES ('user', $1)`, poor); err != nil {
		t.Fatalf("create poor custody: %v", err)
	}
	poorContent, poorPrice, poorQuote := publishTerms(t, accepted, poor)

	if _, err := pool.Exec(ctx, `INSERT INTO app.economy_custodies (kind, label) VALUES ('escrow', 'locked-1')`); err != nil {
		t.Fatalf("create escrow custody: %v", err)
	}

	cases := []struct {
		name string
		cmd  meteringapp.PublishCommand
		want error
	}{
		{name: "unknown custody", cmd: publishCommand("pub-fail-1", "ghost", content, price, quote, clock.now), want: economydomain.ErrUnknownCustody},
		{name: "short balance", cmd: publishCommand("pub-fail-2", poor, poorContent, poorPrice, poorQuote, clock.now), want: economydomain.ErrInsufficientMilliInk},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "unknown custody" {
				quoteGhost, err := meteringdomain.AcceptPublicationQuote(meteringdomain.QuoteRequest{
					Account: "ghost", Content: content, Price: price, AcceptedAt: accepted, TTL: 30 * time.Minute,
				})
				if err != nil {
					t.Fatalf("ghost quote: %v", err)
				}
				tc.cmd.Quote = quoteGhost
			}
			if _, err := uc.Execute(ctx, tc.cmd); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}

	lockedQuote, err := meteringdomain.AcceptPublicationQuote(meteringdomain.QuoteRequest{
		Account: "locked-1", Content: content, Price: price, AcceptedAt: accepted, TTL: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("locked quote: %v", err)
	}
	lockedCmd := publishCommand("pub-fail-3", "locked-1", content, price, lockedQuote, clock.now)
	lockedCmd.FromKind = "escrow"
	lockedCmd.FromLabel = "locked-1"
	if _, err := uc.Execute(ctx, lockedCmd); !errors.Is(err, economydomain.ErrUnauthorizedCustody) {
		t.Fatalf("locked source = %v, want ErrUnauthorizedCustody", err)
	}

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := uc.Execute(cancelled, publishCommand("pub-fail-4", citizen, content, price, quote, clock.now)); err == nil {
		t.Fatal("cancelled context must fail, never settle")
	}

	if countPublications(t, ctx, db) != 0 {
		t.Fatal("refused settlements must leave the registry empty")
	}
	if got := custodyMillis(t, ctx, db, "user", citizen); got != beforeCitizen {
		t.Fatalf("citizen = %d, want untouched %d", got, beforeCitizen)
	}
}
