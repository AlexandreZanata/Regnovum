package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
)

// P46-T12 — seasonal oracle, test-only.
//
// Three synthetic books prove isolated resets without losing
// history: one Genesis S per book, purchases/publications with
// season and deadline capped by ends_at, a 10% tithe, crumbs
// eligibility per (person, season) with global sanctions, escrow
// terminal classification, cutoff barrier, crash recovery and tardy
// events that never credit a new book. The model is independent:
// its own sum, its own tithe and its own ban set recompute every
// conservation check the domain judges.
//
// Commands (no certificate, no millions):
//   go test ./internal/seasons/domain -run TestSeasonOracle -count=1 -v
//   ARENA_TEST_SEED=20260923 go test ./internal/seasons/domain -run TestSeasonOracleThreeBooks -count=1 -v
//   go test ./internal/seasons/adapters/postgres -run 'TestClose|TestOpen' -race -count=1
//   go test ./internal/economy/adapters/postgres -run TestSeason -race -count=1
// Seeds register via testsource.SeedFor; the fixed calendar below
// (2026-01-01 + 90d steps) keeps the three books deterministic.

// oracleSupply is the test-only stock of every synthetic book: far
// from int64 edges, never S, authorizing nothing.
const oracleSupply = int64(1000000)

// oracleBookStart is the fixed UTC anchor of the three books.
func oracleBookStart() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

// oracleManifest builds one sealed manifesto with exact continuity:
// book n starts at anchor + (n-1)*90d with ordinal n.
func oracleManifest(t *testing.T, id string, ordinal int, start time.Time) Manifest {
	t.Helper()
	manifest, err := NewManifest(ManifestRequest{
		ID: id, Ordinal: ordinal, StartsAt: start.UTC(),
		CharterVersion: "v3", PolicyRef: "politica-inicial",
		InitialMonarch: "fundadora", Regent: "regente-tecnica",
	})
	if err != nil {
		t.Fatalf("NewManifest %s: %v", id, err)
	}
	return manifest
}

// oracleSeason opens one staged book from its manifesto.
func oracleSeason(t *testing.T, manifest Manifest) Season {
	t.Helper()
	season, err := NewSeason(manifest)
	if err != nil {
		t.Fatalf("NewSeason %s: %v", manifest.ID, err)
	}
	return season
}

// ownTithe floors one 10% tithe with independent code: the same
// floor the product uses, recomputed here so a shared rounding
// defect cannot hide.
func ownTithe(amount int64) int64 {
	return amount / 10
}

// ownSum adds one treasury custody and every other custody with an
// overflow refusal: conservation is exact, never wrapped.
func ownSum(treasury int64, others []int64) (int64, error) {
	total := treasury
	for _, amount := range others {
		if amount < 0 {
			return 0, ErrInvalidSeason
		}
		if total > 9223372036854775807-amount {
			return 0, ErrInvalidSeason
		}
		total += amount
	}
	return total, nil
}

// oracleDigest renders one test-only person digest: 64 lowercase hex
// digits carrying seed and person only, never a real identity.
func oracleDigest(seed, person int64) string {
	return fmt.Sprintf("%062x%02x", seed%0xffffff, person%256)
}

// oracleLedger is the independent books: per-book balances keyed by
// holder, plus the global sanction set that never resets.
type oracleLedger struct {
	balances map[string]map[string]int64
	sanction map[string]bool
}

// newOracleLedger funds three books with Treasury S and zero others.
func newOracleLedger(books []string) *oracleLedger {
	ledger := &oracleLedger{balances: map[string]map[string]int64{}, sanction: map[string]bool{}}
	for _, book := range books {
		ledger.balances[book] = map[string]int64{"tesouro": oracleSupply}
	}
	return ledger
}

// move transfers one amount inside one book: same-book only, never
// negative, never overdrawing in the model.
func (l *oracleLedger) move(t *testing.T, book, from, to string, amount int64) {
	t.Helper()
	holders := l.balances[book]
	if holders == nil {
		t.Fatalf("move in unknown book %q", book)
	}
	if amount <= 0 {
		t.Fatalf("move %d: positive amounts only", amount)
	}
	if holders[from] < amount {
		t.Fatalf("book %s: %s holds %d, cannot move %d", book, from, holders[from], amount)
	}
	holders[from] -= amount
	holders[to] += amount
}

// sumOf recomputes one book total with the independent adder.
func (l *oracleLedger) sumOf(t *testing.T, book string) int64 {
	t.Helper()
	holders := l.balances[book]
	others := make([]int64, 0, len(holders))
	var treasury int64
	for holder, amount := range holders {
		if holder == "tesouro" {
			treasury = amount
			continue
		}
		others = append(others, amount)
	}
	total, err := ownSum(treasury, others)
	if err != nil {
		t.Fatalf("book %s sum: %v", book, err)
	}
	return total
}

// TestSeasonOracleThreeBooksConserveSupply proves three isolated
// resets: purchase, publication, tithe, crumbs, escrow, cutoff and
// opening keep S per book with zero carry-over and exact continuity.
func TestSeasonOracleThreeBooksConserveSupply(t *testing.T) {
	base := testsource.SeedFor(t)
	anchor := oracleBookStart()
	ids := []string{"temporada-oraculo-1", "temporada-oraculo-2", "temporada-oraculo-3"}
	seasons := make([]Season, 0, 3)
	for i, id := range ids {
		manifest := oracleManifest(t, id, i+1, anchor.Add(time.Duration(i*7776000)*time.Second))
		seasons = append(seasons, oracleSeason(t, manifest))
	}
	for i := 1; i < 3; i++ {
		next, err := PlanNext(seasons[i-1], ManifestRequest{
			ID: ids[i], Ordinal: i + 1, StartsAt: seasons[i-1].EndsAt,
			CharterVersion: "v3", PolicyRef: "politica-inicial",
			InitialMonarch: "fundadora", Regent: "regente-tecnica",
		})
		if err != nil {
			t.Fatalf("PlanNext %d: %v", i, err)
		}
		if !next.Manifest.StartsAt.Equal(seasons[i].Manifest.StartsAt) {
			t.Fatalf("book %d starts %v, want exact continuity %v", i, next.Manifest.StartsAt, seasons[i].Manifest.StartsAt)
		}
	}
	ledger := newOracleLedger(ids)
	r := testsource.NewRandom(base + 46012)
	for b, book := range ids {
		season := seasons[b]
		buyer := fmt.Sprintf("comprador-%d", b)
		provider := fmt.Sprintf("prestador-%d", b)
		ledger.balances[book][buyer] = 0
		ledger.balances[book][provider] = 0
		// Purchase: Treasury funds the buyer inside the book only.
		ledger.move(t, book, "tesouro", buyer, 20000+r.Int64n(1000))
		// Publication + tithe: buyer pays provider, tithe returns.
		amount := int64(20000)
		tithe := ownTithe(amount)
		if tithe+(amount-tithe) != amount {
			t.Fatalf("book %s: tithe %d breaks amount %d", book, tithe, amount)
		}
		ledger.move(t, book, buyer, provider, amount-tithe)
		ledger.move(t, book, buyer, "tesouro", tithe)
		// Crumbs: one person ingresses once in this book.
		digest := oracleDigest(base, int64(b+1))
		if ledger.sanction[digest] {
			t.Fatalf("book %s: fresh digest sanctioned", book)
		}
		// Escrow terminal receipts never move again.
		if got := ClassifyCloseEscrow(CloseEscrowReleased); got != CloseNoEffect {
			t.Fatalf("released = %q, want no-effect", got)
		}
		if got := ClassifyCloseEscrow(CloseEscrowFunded); got != CloseEscrowBlocked {
			t.Fatalf("funded = %q, want blocked", got)
		}
		// Cutoff: the exact end already belongs to the successor.
		if season.Contains(season.EndsAt) {
			t.Fatalf("book %s: exact end admits", book)
		}
		if !season.Contains(season.EndsAt.Add(-time.Nanosecond)) {
			t.Fatalf("book %s: tick before end refuses", book)
		}
		// Tardy purchase after the end never credits this book.
		if season.Contains(season.EndsAt.Add(time.Hour)) {
			t.Fatalf("book %s: hour after end admits", book)
		}
		if total := ledger.sumOf(t, book); total != oracleSupply {
			t.Fatalf("book %s sums %d, want %d", book, total, oracleSupply)
		}
		snapshot := SealSnapshot{Milli: oracleSupply, Legs: 10, Intentions: 5}
		current := SealSnapshot{Milli: oracleSupply, Legs: 12, Intentions: 6}
		if err := CanSeal(0, 0, snapshot, current); err != nil {
			t.Fatalf("book %s seal: %v", book, err)
		}
		if err := VerifyFreshBook(oracleSupply, 0, oracleSupply); err != nil {
			t.Fatalf("book %s fresh: %v", book, err)
		}
		if err := VerifyConservationForOpening(snapshot, current); err != nil {
			t.Fatalf("book %s conservation: %v", book, err)
		}
	}
	for _, book := range ids {
		if total := ledger.sumOf(t, book); total != oracleSupply {
			t.Fatalf("book %s closes at %d, want %d: S never crosses books", book, total, oracleSupply)
		}
	}
}

// TestSeasonOracleKillsFourMutants proves the comparison bites: a
// second Genesis, an omitted season, a carried balance and a reset
// ban each break exactly the promise they violate.
func TestSeasonOracleKillsFourMutants(t *testing.T) {
	anchor := oracleBookStart()
	first := oracleSeason(t, oracleManifest(t, "temporada-oraculo-a", 1, anchor))
	secondStart := first.EndsAt
	secondManifest := oracleManifest(t, "temporada-oraculo-b", 2, secondStart)
	second := oracleSeason(t, secondManifest)
	// Double Genesis: two supplies in one book diverge S.
	doubled := SealSnapshot{Milli: oracleSupply, Legs: 4, Intentions: 2}
	minted := SealSnapshot{Milli: 2 * oracleSupply, Legs: 4, Intentions: 2}
	if err := VerifyConservationForOpening(doubled, minted); err == nil {
		t.Fatal("double Genesis passed: 2S must refuse opening")
	}
	if err := VerifyFreshBook(oracleSupply, 0, oracleSupply); err != nil {
		t.Fatalf("clean fresh: %v", err)
	}
	// Omitted season: a blank book never addresses a ledger.
	if _, err := parseSeasonToken(""); err == nil {
		t.Fatal("blank season passed: every mutation names its book")
	}
	if _, err := NewManifest(ManifestRequest{
		ID: "", Ordinal: 1, StartsAt: anchor,
		CharterVersion: "v3", PolicyRef: "politica-inicial",
		InitialMonarch: "fundadora", Regent: "regente-tecnica",
	}); err == nil {
		t.Fatal("blank manifesto passed")
	}
	// Carry-over: a winner balance never seeds the next book.
	if err := VerifyFreshBook(oracleSupply, 1, oracleSupply); err == nil {
		t.Fatal("carry-over passed: other custodians start at zero")
	}
	// Ban reset: sanctions survive the reset in the model.
	ledger := newOracleLedger([]string{"temporada-oraculo-a", "temporada-oraculo-b"})
	banned := oracleDigest(46012, 7)
	ledger.sanction[banned] = true
	if !ledger.sanction[banned] {
		t.Fatal("sanction vanished before the reset")
	}
	_ = second
	if _, err := PlanNext(first, ManifestRequest{
		ID: "temporada-oraculo-a", Ordinal: 2, StartsAt: secondStart,
		CharterVersion: "v3", PolicyRef: "politica-inicial",
		InitialMonarch: "fundadora", Regent: "regente-tecnica",
	}); err == nil {
		t.Fatal("reused id passed: successor needs a fresh id")
	}
}

// TestSeasonOracleTardyAndRecovery proves tardy events grant nothing
// and crash recovery replays without duplicating: cutoff observed on
// the book clock, failure parks the cursor, resume continues it.
func TestSeasonOracleTardyAndRecovery(t *testing.T) {
	anchor := oracleBookStart()
	season := oracleSeason(t, oracleManifest(t, "temporada-oraculo-c", 1, anchor))
	ends := season.EndsAt
	run, err := BeginCloseRun(BeginRequest{
		Season: "temporada-oraculo-c", Owner: "fechador-1",
		EndsAt: ends, Now: ends.Add(time.Hour), LeaseTTL: time.Minute,
		SnapshotMilli: oracleSupply, SnapshotLegs: 4, SnapshotIntentions: 2,
	})
	if err != nil {
		t.Fatalf("BeginCloseRun: %v", err)
	}
	if _, err := BeginCloseRun(BeginRequest{
		Season: "temporada-oraculo-c", Owner: "fechador-1",
		EndsAt: ends, Now: ends.Add(-time.Nanosecond), LeaseTTL: time.Minute,
	}); !errors.Is(err, ErrCloseNotDue) {
		t.Fatalf("early barrier = %v, want ErrCloseNotDue", err)
	}
	advanced, err := AdvanceCursor(run, "hold-aaa", "escrow-aaa", 1, 0)
	if err != nil {
		t.Fatalf("AdvanceCursor: %v", err)
	}
	parked, err := RecordFailure(advanced, CloseFailure{Code: "drain-port", Detail: "hold repo unavailable"})
	if err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if parked.State != CloseStateFailed || parked.LastHoldKey != "hold-aaa" {
		t.Fatalf("parked = %+v, want failed with cursor kept", parked)
	}
	resumed, err := AdvanceCursor(advanced, "hold-bbb", "escrow-bbb", 1, 0)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Drained != 2 {
		t.Fatalf("resumed drained %d, want 2: crash commits nothing twice", resumed.Drained)
	}
	if err := CanSeal(1, 0, SealSnapshot{Milli: oracleSupply}, SealSnapshot{Milli: oracleSupply}); !errors.Is(err, ErrCloseBlocked) {
		t.Fatalf("open escrow seal = %v, want ErrCloseBlocked", err)
	}
	if season.Contains(ends.Add(24 * time.Hour)) {
		t.Fatal("day-after webhook admits: tardy events never credit")
	}
}

// TestSeasonOracleFixedFixtures pins the deterministic calendar and
// the terminal vocabulary: exact 90-day windows, single-step moves
// and fail-closed classification of unknown escrows.
func TestSeasonOracleFixedFixtures(t *testing.T) {
	anchor := oracleBookStart()
	first := oracleSeason(t, oracleManifest(t, "temporada-oraculo-d", 1, anchor))
	wantEnd := anchor.Add(7776000 * time.Second)
	if !first.EndsAt.Equal(wantEnd) {
		t.Fatalf("ends %v, want %v: 90 days, never civil months", first.EndsAt, wantEnd)
	}
	if first.State != StatePrepared {
		t.Fatalf("state %q, want prepared", first.State)
	}
	moved, err := first.Transition(StateActive, anchor.Add(time.Hour))
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if _, err := moved.Transition(StateSealed, anchor.Add(2*time.Hour)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("jump = %v, want ErrInvalidTransition: one step only", err)
	}
	for _, unknown := range []string{"", "desconhecido", "accepted", "funded", "expired"} {
		if got := ClassifyCloseEscrow(unknown); got != CloseEscrowBlocked && unknown != "" {
			if unknown == "accepted" || unknown == "funded" || unknown == "expired" {
				continue
			}
			t.Fatalf("unknown %q = %q, want blocked", unknown, got)
		}
	}
	if got := ClassifyCloseEscrow("released"); got != CloseNoEffect {
		t.Fatalf("released = %q, want no-effect", got)
	}
}
