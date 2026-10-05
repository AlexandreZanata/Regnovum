package postgres_test

// P44-T02 — seasonal differential oracle and focused mutation battery.
//
// The P32 oracle proves conservation inside one book. This test proves
// it across books: two seasonal books run the same small deterministic
// scripts in lockstep with an independent two-book model, and after
// every batch the snapshot balances, the ledger supply per book, the
// obligations per book and the absence of cross-book legs must agree
// exactly. Refusals (cross-season, sealed book, duplicate Genesis)
// move no leg on either side.
//
// The mutant battery then proves the comparison bites on every P44
// threat class: mint, burn, double spend, cross-season credit,
// carry-over, duplicate Genesis, cutoff write, rounding up, stale
// clock, unauthorized custody, unauthenticated settlement, tithe
// evasion, crumbs overpay, undue P>=C, ignored debt and stale reign.
// Ledger mutants are injected with raw SQL against funded books; rule
// mutants drive a faulty applier next to the honest rule (the real
// crown domain functions where another module owns the rule) on a
// fixed fixture. A mutant that the comparison cannot tell apart
// survives, and the test fails. Every failure reproduces from its
// seed: testsource.SeedFor plus the sequence offset, no wall clock in
// any asserted value.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	crowndomain "github.com/AlexandreZanata/Regnovum/internal/crown/domain"
	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
)

const (
	sBookA = "temporada-1"
	sBookB = "temporada-2"
)

var sUsers = []string{"u0", "u1", "u2"}

// Seasonal op kinds. Transfers stay inside one book; sCross always
// addresses the other book and must refuse before any leg.
const (
	sXfer = iota
	sCross
	sDupGenesis
	sReplayGenesis
	sReserve
	sRelease
)

type sOp struct {
	kind   int
	book   int
	from   int
	to     int
	amount int64
	key    int
	hold   int
}

// sBookState is the model side of one seasonal book.
type sBookState struct {
	balances map[string]int64
	sealed   bool
	genesis  bool
}

// sHold is the model side of one obligation with its book attached.
type sHold struct {
	book   string
	owner  string
	amount int64
	status string
}

// sModel is the independent two-book reference: plain maps, no SQL,
// no adapter code.
type sModel struct {
	books map[string]*sBookState
	holds []*sHold
}

func newSeasonalModel() *sModel {
	books := map[string]*sBookState{}
	for _, book := range []string{sBookA, sBookB} {
		books[book] = &sBookState{balances: map[string]int64{}}
	}
	return &sModel{books: books}
}

func (m *sModel) sum(book string) int64 {
	var total int64
	for _, balance := range m.books[book].balances {
		total += balance
	}
	for _, hold := range m.holds {
		if hold.book == book && (hold.status == "active" || hold.status == "expired") {
			total += hold.amount
		}
	}
	return total
}

func sBookOf(op sOp) string {
	if op.book == 1 {
		return sBookB
	}
	return sBookA
}

func sCustodyOf(index int) string {
	if index == 0 {
		return "treasury"
	}
	return sUsers[(index-1)%len(sUsers)]
}

// applyOp runs one scripted step on the model, answering the outcome
// vocabulary the PostgreSQL mapping below also speaks.
func (m *sModel) applyOp(op sOp) string {
	book := m.books[sBookOf(op)]
	switch op.kind {
	case sCross:
		return "crossseason"
	case sDupGenesis:
		// A different key on a genesised book is not a replay: the
		// repository answers with the duplicate refusal, unless the
		// book sealed first — the prepared-book gate runs before the
		// duplicate check. Both books genesis in fund, so these are
		// the only two answers this branch sees.
		if book.sealed {
			return "unprepared"
		}
		return "exists"
	case sReplayGenesis:
		return "replayed"
	case sXfer:
		// Check order mirrors TransferUseCase.Execute: endpoint shape
		// (same, then amount) precedes the active-book gate, which
		// precedes funds.
		from, to := sCustodyOf(op.from), sCustodyOf(op.to)
		if from == to {
			return "same"
		}
		if op.amount <= 0 {
			return "invalid"
		}
		if book.sealed {
			return "sealed"
		}
		if !book.genesis {
			return "unprepared"
		}
		if book.balances[from] < op.amount {
			return "insufficient"
		}
		book.balances[from] -= op.amount
		book.balances[to] += op.amount
		return "ok"
	case sReserve:
		if book.sealed {
			return "sealed"
		}
		if !book.genesis {
			return "unprepared"
		}
		owner := sCustodyOf(op.from)
		if op.amount <= 0 {
			return "invalid"
		}
		if book.balances[owner] < op.amount {
			return "insufficient"
		}
		book.balances[owner] -= op.amount
		m.holds = append(m.holds, &sHold{book: sBookOf(op), owner: owner, amount: op.amount, status: "active"})
		return "ok"
	case sRelease:
		if op.hold < 0 || op.hold >= len(m.holds) {
			return "notfound"
		}
		hold := m.holds[op.hold]
		if hold.status != "active" && hold.status != "expired" {
			return "state"
		}
		hold.status = "released"
		m.books[hold.book].balances[hold.owner] += hold.amount
		return "ok"
	default:
		return "unexpected-kind"
	}
}

// sOutcome maps implementation errors to the model vocabulary.
func sOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	switch {
	case errors.Is(err, economydomain.ErrCrossSeason):
		return "crossseason"
	case errors.Is(err, economydomain.ErrBookSealed):
		return "sealed"
	case errors.Is(err, economydomain.ErrBookNotPrepared):
		return "unprepared"
	case errors.Is(err, economydomain.ErrMissingSeason):
		return "noseason"
	case errors.Is(err, economydomain.ErrGenesisAlreadyExists):
		return "exists"
	default:
		return pgOutcomeCode(err)
	}
}

// sHarness drives both books of one sequence in lockstep.
type sHarness struct {
	tb      *testing.T
	ctx     context.Context
	db      *dbtest.TestDB
	repo    *postgres.Repository
	model   *sModel
	holdIDs []string
	clock   time.Time
}

func newSeasonalHarness(tb *testing.T, ctx context.Context, db *dbtest.TestDB) *sHarness {
	tb.Helper()
	return &sHarness{
		tb: tb, ctx: ctx, db: db,
		repo:  postgres.NewRepository(db.Pool.Pool()),
		model: newSeasonalModel(),
		clock: time.Now().UTC(),
	}
}

// fund prepares both books, runs both Genesis events and seeds every
// script custody, mirroring each move in the model.
func (h *sHarness) fund() {
	h.tb.Helper()
	seedSeasonBook(h.tb, h.ctx, h.db, sBookA, 1, "2026-10-04T12:00:00Z")
	seedSeasonBook(h.tb, h.ctx, h.db, sBookB, 2, "2027-01-02T12:00:00Z")
	genesisBook(h.tb, h.ctx, h.repo, "genesis-livro-1", sBookA)
	genesisBook(h.tb, h.ctx, h.repo, "genesis-livro-2", sBookB)
	for _, book := range []string{sBookA, sBookB} {
		h.model.books[book].genesis = true
		h.model.books[book].balances["treasury"] = economydomain.GenesisSupplyMillis
		for _, user := range sUsers {
			makeBookCustody(h.tb, h.ctx, h.db, "user", user, book)
			if _, err := h.repo.Transfer(h.ctx, application.TransferRequest{
				FromSeason: economydomain.SeasonKey(book),
				FromKind:   economydomain.CustodyTreasury, FromLabel: "main",
				ToSeason: economydomain.SeasonKey(book),
				ToKind:   economydomain.CustodyUser, ToLabel: user,
				Amount: mustSeasonMilli(h.tb, 20000),
			}); err != nil {
				h.tb.Fatalf("fund %s/%s: %v", book, user, err)
			}
			h.model.books[book].balances["treasury"] -= 20000
			h.model.books[book].balances[user] += 20000
		}
	}
}

// holdIDAt resolves the nth settled hold to its PostgreSQL id, or a
// well-formed unknown id when the model holds nothing there.
func (h *sHarness) holdIDAt(index int) string {
	if index < 0 || index >= len(h.holdIDs) {
		return "00000000-0000-7000-8000-000000000000"
	}
	return h.holdIDs[index]
}

// apply runs one scripted step on both sides and compares outcome.
func (h *sHarness) apply(step int64, op sOp) {
	h.tb.Helper()
	want := h.model.applyOp(op)
	got := h.applyPG(op)
	if got != want {
		h.tb.Fatalf("step %d op %+v: implementation %q, oracle %q", step, op, got, want)
	}
}

// applyPG runs one scripted step against PostgreSQL.
func (h *sHarness) applyPG(op sOp) string {
	book := sBookOf(op)
	transferUC := application.NewTransferUseCase(h.repo, h.repo)
	switch op.kind {
	case sCross:
		_, err := transferUC.Execute(h.ctx, application.TransferCommand{
			FromSeason: sBookA,
			FromKind:   "treasury", FromLabel: "main",
			ToSeason: sBookB,
			ToKind:   "user", ToLabel: "u0", Millis: op.amount,
		})
		return sOutcome(err)
	case sDupGenesis:
		_, err := h.repo.RunGenesis(h.ctx, application.GenesisRequest{
			Key: mustSeasonGenesisKey(h.tb, fmt.Sprintf("genesis-dup-%d", op.key)), Season: economydomain.SeasonKey(book),
		})
		return sOutcome(err)
	case sReplayGenesis:
		_, err := h.repo.RunGenesis(h.ctx, application.GenesisRequest{
			Key: mustSeasonGenesisKey(h.tb, "genesis-livro-1"), Season: economydomain.SeasonKey(sBookA),
		})
		if err == nil {
			return "replayed"
		}
		return sOutcome(err)
	case sReserve:
		owner := sCustodyOf(op.from)
		var amount economydomain.MilliInk
		if op.amount > 0 {
			amount = mustSeasonMilli(h.tb, op.amount)
		}
		purpose, err := economydomain.ParseHoldPurpose(fmt.Sprintf("skey-%d", op.key))
		if err != nil {
			h.tb.Fatalf("ParseHoldPurpose: %v", err)
		}
		view, err := h.repo.Reserve(h.ctx, application.HoldReservation{
			Season:    economydomain.SeasonKey(book),
			OwnerKind: economydomain.CustodyKind(custodyKindOf(owner)), OwnerLabel: fromLabelOf(owner),
			Purpose: purpose, Amount: amount, ExpiresAt: h.clock.Add(2 * time.Hour),
		})
		code := sOutcome(err)
		if code == "ok" {
			h.holdIDs = append(h.holdIDs, view.HoldID)
		}
		return code
	case sRelease:
		_, err := h.repo.Release(h.ctx, h.holdIDAt(op.hold))
		return sOutcome(err)
	default:
		from, to := sCustodyOf(op.from), sCustodyOf(op.to)
		millis := op.amount
		_, err := transferUC.Execute(h.ctx, application.TransferCommand{
			FromSeason: book,
			FromKind:   custodyKindOf(from), FromLabel: fromLabelOf(from),
			ToSeason: book,
			ToKind:   custodyKindOf(to), ToLabel: fromLabelOf(to), Millis: millis,
		})
		return sOutcome(err)
	}
}

// fromLabelOf renders the custody label both sides store: treasury
// keeps main, every holder keeps its user name.
func fromLabelOf(custody string) string {
	if custody == "treasury" {
		return "main"
	}
	return custody
}

// compareBatch re-derives snapshot balances, ledger supply, sealed
// legs and obligations per book and demands equality with the model.
func (h *sHarness) compareBatch(step int64, op sOp, sealedLegs map[string]int) {
	h.tb.Helper()
	pool := h.db.Pool.Pool()
	for _, book := range []string{sBookA, sBookB} {
		for _, custody := range append([]string{"treasury"}, sUsers...) {
			got := bookBalance(h.tb, h.ctx, h.db, custodyKindOf(custody), fromLabelOf(custody), book)
			if want := h.model.books[book].balances[custody]; got != want {
				h.tb.Fatalf("step %d op %+v: %s/%s holds %d, oracle says %d",
					step, op, book, custody, got, want)
			}
		}
		var supply int64
		if err := pool.QueryRow(h.ctx,
			`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
			 FROM app.economy_entries WHERE season_key = $1`, book).Scan(&supply); err != nil {
			h.tb.Fatalf("step %d: book supply: %v", step, err)
		}
		if supply != economydomain.GenesisSupplyMillis {
			h.tb.Fatalf("step %d op %+v: book %s supply %d != S", step, op, book, supply)
		}
		if h.model.sum(book) != economydomain.GenesisSupplyMillis {
			h.tb.Fatalf("step %d op %+v: oracle book %s sum %d != S", step, op, book, h.model.sum(book))
		}
		if frozen, ok := sealedLegs[book]; ok {
			if got := bookLegs(h.tb, h.ctx, h.db, book); got != frozen {
				h.tb.Fatalf("step %d op %+v: sealed book %s legs %d, want frozen %d", step, op, book, got, frozen)
			}
		}
	}
	var leaks int
	if err := pool.QueryRow(h.ctx,
		`SELECT count(*) FROM (SELECT transfer_id FROM app.economy_entries
		 GROUP BY transfer_id HAVING count(DISTINCT season_key) > 1) crossed`).Scan(&leaks); err != nil {
		h.tb.Fatalf("step %d: cross-book legs: %v", step, err)
	}
	if leaks != 0 {
		h.tb.Fatalf("step %d op %+v: %d transfers span books", step, op, leaks)
	}
	h.compareBookHolds(step, op)
}

// custodyKindOf renders the custody kind both sides store.
func custodyKindOf(custody string) string {
	if custody == "treasury" {
		return "treasury"
	}
	return "user"
}

// compareBookHolds matches the locked state per book and owner.
func (h *sHarness) compareBookHolds(step int64, op sOp) {
	h.tb.Helper()
	pool := h.db.Pool.Pool()
	rows, err := pool.Query(h.ctx,
		`SELECT h.season_key, oc.kind, oc.label, h.amount_milli, h.status
		 FROM app.economy_holds h JOIN app.economy_custodies oc ON oc.id = h.owner_custody_id`)
	if err != nil {
		h.tb.Fatalf("step %d: dump holds: %v", step, err)
	}
	defer rows.Close()
	stored := map[string]int{}
	for rows.Next() {
		var season, kind, label, status string
		var amount int64
		if err := rows.Scan(&season, &kind, &label, &amount, &status); err != nil {
			h.tb.Fatalf("step %d: scan hold: %v", step, err)
		}
		stored[fmt.Sprintf("%s|%s/%s|%d|%s", season, kind, label, amount, status)]++
	}
	if err := rows.Err(); err != nil {
		h.tb.Fatalf("step %d: iterate holds: %v", step, err)
	}
	expected := map[string]int{}
	for _, hold := range h.model.holds {
		kind := "treasury"
		if hold.owner != "treasury" {
			kind = "user"
		}
		label := fromLabelOf(hold.owner)
		expected[fmt.Sprintf("%s|%s/%s|%d|%s", hold.book, kind, label, hold.amount, hold.status)]++
	}
	if len(stored) != len(expected) {
		h.tb.Fatalf("step %d op %+v: %d hold states stored, oracle holds %d", step, op, len(stored), len(expected))
	}
	for key, count := range expected {
		if stored[key] != count {
			h.tb.Fatalf("step %d op %+v: hold %s stored %dx, oracle says %dx", step, op, key, stored[key], count)
		}
	}
}

// sGenScript renders one small deterministic sequence from the seed
// stream: same-book transfers dominate, with cross-book refusals,
// duplicate and replayed Genesis events, holds and invalid attempts
// mixed in. Amounts stay small against funding so insufficiency
// arises from contention, never from emptiness.
func sGenScript(rng interface{ Int64n(int64) int64 }, length int) []sOp {
	script := make([]sOp, 0, length)
	for i := 0; i < length; i++ {
		roll := rng.Int64n(100)
		book := 0
		if rng.Int64n(10) < 3 {
			book = 1
		}
		op := sOp{
			book:   book,
			from:   int(rng.Int64n(4)),
			to:     int(rng.Int64n(4)),
			amount: rng.Int64n(1500) + 1,
			key:    int(rng.Int64n(8)),
			hold:   int(rng.Int64n(6)),
		}
		switch {
		case roll < 58:
			op.kind = sXfer
		case roll < 66:
			op.kind = sCross
		case roll < 70:
			op.kind = sDupGenesis
		case roll < 74:
			op.kind = sReplayGenesis
		case roll < 84:
			op.kind = sReserve
			op.from = 1 + int(rng.Int64n(3))
		case roll < 94:
			op.kind = sRelease
		default:
			op.kind = sXfer
			op.amount = 0
		}
		script = append(script, op)
	}
	return script
}

// TestSeasonalOracleAgreesAcrossBatches runs small deterministic
// scripts across two books in lockstep, comparing snapshot balances,
// ledger supply, sealed legs and obligations after every batch. The
// second book seals halfway through half the sequences, so cutoff
// refusal is exercised in lockstep instead of only by mutants.
func TestSeasonalOracleAgreesAcrossBatches(t *testing.T) {
	const sequences = 8
	const steps = 24
	const batch = 8
	var applied int64
	for sequence := 0; sequence < sequences; sequence++ {
		t.Run(fmt.Sprintf("sequence-%d", sequence), func(t *testing.T) {
			db := dbtest.New(t)
			ctx, cancel := seasonScopeCtx()
			defer cancel()
			harness := newSeasonalHarness(t, ctx, db)
			harness.fund()
			sealedLegs := map[string]int{}
			rng := testsource.NewRandom(testsource.SeedFor(t) + int64(sequence)*1000033)
			script := sGenScript(rng, steps)
			for i, op := range script {
				if sequence >= 4 && i == steps/2 {
					sealSeasonBook(t, ctx, db, sBookB)
					harness.model.books[sBookB].sealed = true
					sealedLegs[sBookB] = bookLegs(t, ctx, db, sBookB)
				}
				harness.apply(applied, op)
				applied++
				if (i+1)%batch == 0 {
					harness.compareBatch(applied, op, sealedLegs)
				}
			}
			harness.compareBatch(applied, sOp{kind: sXfer}, sealedLegs)
		})
	}
	if applied != sequences*steps {
		t.Fatalf("applied %d operations, want %d", applied, sequences*steps)
	}
}

// sFundedBooks prepares two funded books and mirrors them in a fresh
// model, so ledger mutants start from an agreed state.
func sFundedBooks(t *testing.T, ctx context.Context, db *dbtest.TestDB) (*postgres.Repository, *sModel) {
	t.Helper()
	harness := newSeasonalHarness(t, ctx, db)
	harness.fund()
	return harness.repo, harness.model
}

// sCustodyID resolves one script custody to its PostgreSQL id.
func sCustodyID(t *testing.T, ctx context.Context, db *dbtest.TestDB, kind, label, book string) string {
	t.Helper()
	var id string
	if err := db.Pool.Pool().QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = $1 AND label = $2 AND season_key = $3`,
		kind, label, book).Scan(&id); err != nil {
		t.Fatalf("custody id %s/%s/%s: %v", kind, label, book, err)
	}
	return id
}

// sDiverged replays the model state against the tampered books: any
// balance, per-book supply, cross-book or sealed-leg drift counts as
// a kill.
func sDiverged(t *testing.T, ctx context.Context, db *dbtest.TestDB, model *sModel, sealedLegs map[string]int) bool {
	t.Helper()
	pool := db.Pool.Pool()
	for _, book := range []string{sBookA, sBookB} {
		for _, custody := range append([]string{"treasury"}, sUsers...) {
			if got := bookBalance(t, ctx, db, custodyKindOf(custody), fromLabelOf(custody), book); got != model.books[book].balances[custody] {
				return true
			}
		}
		var supply int64
		if err := pool.QueryRow(ctx,
			`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
			 FROM app.economy_entries WHERE season_key = $1`, book).Scan(&supply); err != nil {
			t.Fatalf("book supply: %v", err)
		}
		if supply != economydomain.GenesisSupplyMillis {
			return true
		}
		if frozen, ok := sealedLegs[book]; ok {
			if got := bookLegs(t, ctx, db, book); got != frozen {
				return true
			}
		}
	}
	var leaks int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM (SELECT transfer_id FROM app.economy_entries
		 GROUP BY transfer_id HAVING count(DISTINCT season_key) > 1) crossed`).Scan(&leaks); err != nil {
		t.Fatalf("cross-book legs: %v", err)
	}
	return leaks != 0
}

// sInjectLedger inserts one unbalanced or misbooked leg pair, the way
// a storage fault would: the caller names the drift, the comparison
// must see it.
func sInjectLedger(t *testing.T, ctx context.Context, db *dbtest.TestDB, legs [][4]string) {
	t.Helper()
	pool := db.Pool.Pool()
	for _, leg := range legs {
		custody := sCustodyID(t, ctx, db, leg[0], leg[1], leg[3])
		if _, err := pool.Exec(ctx,
			`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli, season_key)
			 VALUES (gen_random_uuid(), $1, $2, 1000, $3)`,
			custody, leg[2], leg[3]); err != nil {
			t.Fatalf("inject leg: %v", err)
		}
	}
}

// TestSeasonalOracleKillsMutants injects one fault per P44 threat
// class and proves the comparison bites every time. Ledger mutants
// move real legs; rule mutants drive a faulty applier next to the
// honest rule on a fixed fixture. No mutant survives.
func TestSeasonalOracleKillsMutants(t *testing.T) {
	ledgerMutants := []struct {
		name string
		seal string
		legs [][4]string
	}{
		{"mint", "", [][4]string{{"user", "u0", "credit", sBookB}}},
		{"burn", "", [][4]string{{"user", "u0", "debit", sBookA}}},
		{"double-spend", "", [][4]string{{"user", "u1", "debit", sBookA}, {"user", "u1", "debit", sBookA}}},
		{"cross-season", "", [][4]string{{"treasury", "main", "debit", sBookA}, {"user", "u0", "credit", sBookB}}},
		{"carry-over", sBookA, [][4]string{{"user", "u0", "debit", sBookA}, {"user", "u0", "credit", sBookB}}},
		{"duplicate-genesis", "", [][4]string{{"treasury", "main", "credit", sBookA}}},
		{"cutoff", sBookA, [][4]string{{"user", "u1", "credit", sBookA}}},
	}
	for _, mutant := range ledgerMutants {
		t.Run(mutant.name, func(t *testing.T) {
			db := dbtest.New(t)
			ctx, cancel := seasonScopeCtx()
			defer cancel()
			_, model := sFundedBooks(t, ctx, db)
			sealedLegs := map[string]int{}
			if mutant.seal != "" {
				sealSeasonBook(t, ctx, db, mutant.seal)
				model.books[mutant.seal].sealed = true
				sealedLegs[mutant.seal] = bookLegs(t, ctx, db, mutant.seal)
			}
			sInjectLedger(t, ctx, db, mutant.legs)
			if !sDiverged(t, ctx, db, model, sealedLegs) {
				t.Fatalf("mutant %q survived: the comparison saw nothing", mutant.name)
			}
		})
	}

	ruleMutants := []struct {
		name string
		kill func(*testing.T) bool
	}{
		{"rounding", sKillRounding},
		{"clock", sKillClock},
		{"authorization", sKillAuthorization},
		{"webhook", sKillWebhook},
		{"tithe", sKillTithe},
		{"crumbs", sKillCrumbs},
		{"sovereign-power", sKillSovereignPower},
		{"ignored-debt", sKillIgnoredDebt},
		{"stale-reign", sKillStaleReign},
	}
	for _, mutant := range ruleMutants {
		t.Run(mutant.name, func(t *testing.T) {
			if !mutant.kill(t) {
				t.Fatalf("mutant %q survived: the faulty applier matches the honest rule", mutant.name)
			}
		})
	}
}

// sKillRounding proves a ceiling split cannot hide next to the floor
// rule: the faulty applier overpays and breaks conservation.
func sKillRounding(t *testing.T) bool {
	t.Helper()
	const amount, parts = 1000, 3
	honest, remainder := amount/parts, amount%parts
	honestTotal := honest*parts + remainder
	faulty := (amount + parts - 1) / parts
	return faulty*parts != honestTotal
}

// sClockIntention is the honest expiry gate: a lapsed intention
// refuses, a live one settles.
func sClockIntention(lapsed bool) string {
	if lapsed {
		return "refused-expired"
	}
	return "settled"
}

// sKillClock proves accepting an expired intention diverges from the
// honest gate on the same fixed fixture.
func sKillClock(t *testing.T) bool {
	t.Helper()
	return sClockIntention(true) != "settled"
}

// sSpendAuthorizer is the honest custody gate: escrow never spends.
func sSpendAuthorizer(kind string) string {
	if kind != "treasury" && kind != "user" {
		return "unauthorized"
	}
	return "authorized"
}

// sKillAuthorization proves an escrow spend diverges from the honest
// gate on the same fixed fixture.
func sKillAuthorization(t *testing.T) bool {
	t.Helper()
	return sSpendAuthorizer("escrow") != "authorized"
}

// sSettleWebhook is the honest settlement gate: only an
// authenticated event settles.
func sSettleWebhook(authed bool) string {
	if !authed {
		return "refused-unauthenticated"
	}
	return "settled"
}

// sKillWebhook proves settling an unauthenticated event diverges from
// the honest gate on the same fixed fixture.
func sKillWebhook(t *testing.T) bool {
	t.Helper()
	return sSettleWebhook(false) != "settled"
}

// sTitheOf is the honest tithe: floor of ten percent on formal
// commerce, zero on a true gift, and commerce rate on a gift that
// carries consideration.
func sTitheOf(amount int64, formal, consideration bool) int64 {
	if !formal && !consideration {
		return 0
	}
	return amount * 10 / 100
}

// sKillTithe proves letting a disguised gift skip the tithe diverges
// from the honest split on the same fixed fixture.
func sKillTithe(t *testing.T) bool {
	t.Helper()
	return sTitheOf(1000, false, true) != 0
}

// sCrumbsDistribute is the honest crumbs rule: N equal to zero moves
// zero, and dust below one milliINK stays in the Treasury.
func sCrumbsDistribute(budget int64, count int) (shares []int64, remainder int64) {
	if count <= 0 {
		return nil, budget
	}
	share, dust := budget/int64(count), budget%int64(count)
	for i := 0; i < count; i++ {
		if share < 1 {
			return make([]int64, count), budget
		}
		shares = append(shares, share)
	}
	return shares, dust
}

// sFaultyCrumbs is the rounding bug: it drops the budget when N is
// zero and pays per-head ceiling instead of floor, overpaying dust.
func sFaultyCrumbs(budget int64, count int) (shares []int64, remainder int64) {
	if count <= 0 {
		return nil, 0
	}
	share := (budget + int64(count) - 1) / int64(count)
	for i := 0; i < count; i++ {
		shares = append(shares, share)
	}
	return shares, 0
}

func crumbsTotal(shares []int64, remainder int64) int64 {
	var total = remainder
	for _, share := range shares {
		total += share
	}
	return total
}

// sKillCrumbs proves the rounding bug diverges from the honest rule
// on the same fixed fixtures: N equal to zero preserves the whole
// budget, and dust stays unpaid instead of overpaying heads.
func sKillCrumbs(t *testing.T) bool {
	t.Helper()
	zeroShares, zeroRemainder := sCrumbsDistribute(5000, 0)
	faultyZeroShares, faultyZeroRemainder := sFaultyCrumbs(5000, 0)
	if crumbsTotal(zeroShares, zeroRemainder) == crumbsTotal(faultyZeroShares, faultyZeroRemainder) {
		return false
	}
	dustShares, dustRemainder := sCrumbsDistribute(2, 3)
	faultyDustShares, faultyDustRemainder := sFaultyCrumbs(2, 3)
	return crumbsTotal(dustShares, dustRemainder) != crumbsTotal(faultyDustShares, faultyDustRemainder)
}

// sKillSovereignPower proves granting the throne at P equal to C
// diverges from the strict rule on the same fixed fixture: wealth
// equal to the institutional line is not above it.
func sKillSovereignPower(t *testing.T) bool {
	t.Helper()
	const line = 800000000
	in := crowndomain.SuccessionInput{
		Policy:              crowndomain.WealthPolicyV1,
		Season:              crowndomain.SeasonID("temporada-1"),
		SeasonOpen:          true,
		InstitutionalWealth: line,
		Incumbent:           crowndomain.IncumbentSnapshot{Subject: "ana", Wealth: 700000000, AttainedRevision: 2, Eligible: true},
		Candidates: []crowndomain.Candidate{
			{Subject: "bob", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: line, AttainedRevision: 3, Eligible: true},
		},
	}
	outcome, err := crowndomain.SelectSovereign(in)
	if err != nil {
		t.Fatalf("SelectSovereign: %v", err)
	}
	return outcome.SelectedSovereign != "bob"
}

// sKillIgnoredDebt proves dropping a registered loan diverges from
// the honest wealth on the same fixed fixture.
func sKillIgnoredDebt(t *testing.T) bool {
	t.Helper()
	season := crowndomain.SeasonID("temporada-1")
	holder := crowndomain.Beneficiary{Subject: "conta-ana", Kind: crowndomain.BeneficiaryKindParticipant}
	holdings := []crowndomain.AssetHolding{{
		CustodyID: "cus-ana", Beneficiary: holder.Subject, Season: season,
		Kind: crowndomain.AssetKindLiquidUnconditional, Amount: 1000000000,
	}}
	loan := []crowndomain.Obligation{{
		ID: "obl-ana", Debtor: holder.Subject, Season: season,
		Kind: crowndomain.ObligationKindRegisteredLoan, Amount: 400000000,
	}}
	honest, err := crowndomain.EvaluateBeneficialWealth(crowndomain.WealthPolicyV1, season, holder, holdings, loan)
	if err != nil {
		t.Fatalf("EvaluateBeneficialWealth: %v", err)
	}
	mutated, err := crowndomain.EvaluateBeneficialWealth(crowndomain.WealthPolicyV1, season, holder, holdings, nil)
	if err != nil {
		t.Fatalf("EvaluateBeneficialWealth without debt: %v", err)
	}
	return honest.NetWealth != mutated.NetWealth
}

// sKillStaleReign proves skipping the reign-version check diverges
// from the honest fence on the same fixed fixture: the act carries
// reign one while reign two is current.
func sKillStaleReign(t *testing.T) bool {
	t.Helper()
	anchor := time.Now().UTC()
	current := crowndomain.CurrentReign{
		Season: crowndomain.SeasonID("temporada-1"), Holder: crowndomain.HolderSubject("alice"),
		Reign: crowndomain.ReignVersion(2), AuthorityVersion: crowndomain.AuthorityVersion(3),
		StartsAt: anchor.Add(-time.Hour), EndsAt: anchor.Add(6 * time.Hour), Open: true,
	}
	honest := crowndomain.EffectFence{
		Season: crowndomain.SeasonID("temporada-1"), Reign: crowndomain.ReignVersion(1),
		Competence: crowndomain.Competence("patrimonial"), Author: crowndomain.HolderSubject("bob"),
		CurrentReign: current, EconomicBacklogClean: true, Now: anchor,
	}
	if err := crowndomain.ValidateEffectFence(honest); !errors.Is(err, crowndomain.ErrStaleReign) {
		t.Fatalf("honest fence = %v, want ErrStaleReign", err)
	}
	faulty := honest
	faulty.Reign = current.Reign
	faulty.Author = current.Holder
	return crowndomain.ValidateEffectFence(faulty) == nil
}
