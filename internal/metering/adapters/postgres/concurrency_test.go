package postgres_test

// P36-T05 — one intention key under fifty simultaneous publications
// settles exactly once on real PostgreSQL.
//
// Fifty retries of one key share a single settlement: one executes
// the publication with its debit, forty-nine replay it, and the
// journal holds one paired transfer. A divergent payload under the
// same key fails instead of charging twice, even mid-race. The suite
// runs on a disposable database with `-race` green and moves exactly
// one charge.

import (
	"errors"
	"sync"
	"testing"
	"time"

	meteringpg "github.com/AlexandreZanata/Regnovum/internal/metering/adapters/postgres"
	meteringapp "github.com/AlexandreZanata/Regnovum/internal/metering/application"
	meteringdomain "github.com/AlexandreZanata/Regnovum/internal/metering/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

func racePublishUseCase(t *testing.T, repo *meteringpg.Repository) *meteringapp.PublishUseCase {
	t.Helper()
	uc, err := meteringapp.NewPublishUseCase(repo)
	if err != nil {
		t.Fatalf("NewPublishUseCase: %v", err)
	}
	return uc
}

// TestPublishRacesCollapseToOne proves fifty simultaneous retries of
// one key settle a single publication: one executes, forty-nine
// replay. Funds cover exactly one charge, so losers take the
// short-balance path back to re-lookup instead of mistaking a won
// race for an empty balance.
func TestPublishRacesCollapseToOne(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(60, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := publishFixtureTime()
	citizen := "citizen-race-50"
	content, price, quote := publishTerms(t, accepted, citizen)
	seedLedger(t, ctx, testDB, citizen, quote.TotalMilli)
	clock := publishClock{now: accepted.Add(time.Minute)}
	repo, err := meteringpg.NewRepository(pool, clock)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}

	const runners = 50
	var wg sync.WaitGroup
	results := make([]*meteringapp.PublishResult, runners)
	errs := make([]error, runners)
	start := make(chan struct{})
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = racePublishUseCase(t, repo).Execute(ctx,
				publishCommand("pub-race-50", citizen, content, price, quote, clock.now))
		}(i)
	}
	close(start)
	wg.Wait()

	var transferID, publicationID string
	founded, replayed := 0, 0
	for i := range runners {
		if errs[i] != nil {
			t.Fatalf("runner %d: %v", i, errs[i])
		}
		if results[i].Replayed {
			replayed++
		} else {
			founded++
		}
		if transferID == "" {
			transferID, publicationID = results[i].TransferID, results[i].PublicationID
		} else if results[i].TransferID != transferID || results[i].PublicationID != publicationID {
			t.Fatal("runners resolved different settlements for one key")
		}
		if results[i].TotalMilli != quote.TotalMilli {
			t.Fatalf("runner %d total = %d, want quoted %d", i, results[i].TotalMilli, quote.TotalMilli)
		}
	}
	if founded != 1 || replayed != runners-1 {
		t.Fatalf("founded = %d, replayed = %d; want 1 and %d", founded, replayed, runners-1)
	}
	if countPublications(t, ctx, testDB) != 1 {
		t.Fatal("race settled more than one publication")
	}
	if debits, credits := countLegs(t, ctx, testDB, transferID); debits != 1 || credits != 1 {
		t.Fatalf("legs = %d/%d, want one debit and one credit", debits, credits)
	}
	if got := custodyMillis(t, ctx, testDB, "user", citizen); got != 0 {
		t.Fatalf("citizen = %d, want exactly spent", got)
	}
}

// TestPublishRaceDivergentPayloadFails proves a different payload
// under the hot key fails even mid-race: the winner keeps the single
// settlement and every divergent run conflicts without new rows or
// legs.
func TestPublishRaceDivergentPayloadFails(t *testing.T) {
	testDB := dbtest.New(t, dbtest.WithPoolLimits(60, 1))
	pool := testDB.Pool.Pool()
	ctx, cancel := publishCtx()
	defer cancel()

	accepted := publishFixtureTime()
	citizen := "citizen-race-mixed"
	content, price, quote := publishTerms(t, accepted, citizen)
	seedLedger(t, ctx, testDB, citizen, 10*quote.TotalMilli)
	clock := publishClock{now: accepted.Add(time.Minute)}
	repo, err := meteringpg.NewRepository(pool, clock)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}

	edited, err := meteringdomain.ParseMeasuredContent("texto adulterado na corrida", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("edited: %v", err)
	}
	editedQuote, err := meteringdomain.AcceptPublicationQuote(meteringdomain.QuoteRequest{
		Account: citizen, Content: edited, Price: price, AcceptedAt: accepted, TTL: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("edited quote: %v", err)
	}

	const runners = 50
	var wg sync.WaitGroup
	type outcome struct {
		result *meteringapp.PublishResult
		err    error
	}
	outcomes := make([]outcome, runners)
	start := make(chan struct{})
	for i := range runners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			cmd := publishCommand("pub-race-mixed", citizen, content, price, quote, clock.now)
			if i%2 == 1 {
				cmd = publishCommand("pub-race-mixed", citizen, edited, price, editedQuote, clock.now)
			}
			outcomes[i].result, outcomes[i].err = racePublishUseCase(t, repo).Execute(ctx, cmd)
		}(i)
	}
	close(start)
	wg.Wait()

	var transferID string
	charged := 0
	for i, o := range outcomes {
		if o.err == nil {
			charged++
			if transferID == "" {
				transferID = o.result.TransferID
			} else if o.result.TransferID != transferID {
				t.Fatal("one key settled two transfers")
			}
			continue
		}
		if !errors.Is(o.err, meteringdomain.ErrPublishConflict) {
			t.Fatalf("runner %d: %v, want ErrPublishConflict", i, o.err)
		}
	}
	if charged == 0 {
		t.Fatal("no runner settled: one payload must win")
	}
	if countPublications(t, ctx, testDB) != 1 {
		t.Fatal("mixed race settled more than one publication")
	}
	if debits, credits := countLegs(t, ctx, testDB, transferID); debits != 1 || credits != 1 {
		t.Fatalf("legs = %d/%d, want one debit and one credit", debits, credits)
	}
}
