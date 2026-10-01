package postgres_test

// P32-T09 — independent conservation oracle on real PostgreSQL.
//
// The oracle is a pure in-memory model sharing no code with the
// adapters: balances per custody, holds with their closed machine,
// intentions keyed by triple, and the S invariant. Deterministic
// scripts drive the model and the PostgreSQL implementation step by
// step (10.000 operations across 100 seeded sequences); after every
// step the outcome code and the full state must agree to the milliINK.
// Four injected mutants (mint, burn, double spend, phantom intention)
// prove the comparison bites, and a pure-model fuzz target guards the
// decoder. No case is excluded, no step is skipped.
//
// Comparison boundaries, stated once: transfer ids are excluded because
// PostgreSQL mints unguessable uuids (pairing integrity is proven by
// T04/T08); payload hashes are excluded because the conflict outcome
// already proves them; hold expiry instants are excluded because the
// model runs a step clock while PostgreSQL runs wall time, and both
// consult the same lapsed intent with wide margins.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
)

// oracleOp is one scripted step. Operands index small closed vocabularies
// so every script is reproducible from its seed; the fuzz decoder below
// reads the same layout from raw bytes.
type oracleOp struct {
	kind    int // 0 transfer, 1 reserve, 2 release, 3 capture, 4 expire, 5 intention, 6 genesis
	from    int
	to      int
	amount  int64
	key     int
	actor   int
	hold    int
	lapsed  bool
	invalid int // 0 valid, 1 unknown kind, 2 zero amount, 3 same custody
}

const (
	opTransfer = iota
	opReserve
	opRelease
	opCapture
	opExpire
	opIntention
	opGenesis
)

// oracleCustodies are the script address space: treasury, five holders
// and one named escrow that funds locked-source attempts.
var oracleCustodies = []struct{ kind, label string }{
	{"treasury", "main"},
	{"user", "u0"},
	{"user", "u1"},
	{"user", "u2"},
	{"user", "u3"},
	{"user", "u4"},
	{"escrow", "vault"},
}

// scriptKey renders the intention identity both sides store.
func scriptKey(key int) string { return fmt.Sprintf("script-%d", key) }

// scriptActor renders the intention actor both sides store.
func scriptActor(actor int) string { return fmt.Sprintf("actor-%d", actor) }

// scriptTriple renders the triple identity both sides store.
func scriptTriple(key, actor int) string {
	return scriptKey(key) + "|" + scriptActor(actor) + "|script-op"
}

// oracleHold is the model side of one reservation.
type oracleHold struct {
	owner  int
	amount int64
	status string
	lapsed bool
}

// oracleModel is the independent reference: plain maps, no SQL, no
// adapter code.
type oracleModel struct {
	balances   map[int]int64
	holds      []*oracleHold
	intentions map[string]int64
}

func newOracleModel() *oracleModel {
	balances := map[int]int64{0: domain.GenesisSupplyMillis}
	for i := 1; i < len(oracleCustodies); i++ {
		balances[i] = 0
	}
	return &oracleModel{balances: balances, intentions: map[string]int64{}}
}

func (m *oracleModel) sum() int64 {
	var total int64
	for _, balance := range m.balances {
		total += balance
	}
	for _, hold := range m.holds {
		if hold.status == "active" || hold.status == "expired" {
			total += hold.amount
		}
	}
	return total
}

// applyOp runs one scripted step on the model, answering the outcome
// code in the vocabulary the PostgreSQL mapping below also speaks.
func (m *oracleModel) applyOp(op oracleOp) string {
	switch op.kind {
	case opTransfer:
		return m.applyTransfer(op)
	case opReserve:
		return m.applyReserve(op)
	case opRelease:
		return m.applyRelease(op)
	case opCapture:
		return m.applyCapture(op)
	case opExpire:
		return m.applyExpire(op)
	case opIntention:
		return m.applyIntention(op)
	default:
		return "replayed"
	}
}

func (m *oracleModel) applyTransfer(op oracleOp) string {
	// Check order mirrors the use case: known kinds, distinct pair,
	// authorized source, positive amount, funded balance.
	if op.invalid == 1 {
		return "unknown"
	}
	from, to := op.from%len(oracleCustodies), op.to%len(oracleCustodies)
	if op.invalid == 3 {
		to = from
	}
	if from == to {
		return "same"
	}
	if !canSpendKind(oracleCustodies[from].kind) {
		return "unauthorized"
	}
	if op.invalid == 2 {
		return "invalid"
	}
	if m.balances[from] < op.amount {
		return "insufficient"
	}
	m.balances[from] -= op.amount
	m.balances[to] += op.amount
	return "ok"
}

func canSpendKind(kind string) bool {
	return kind == "treasury" || kind == "user"
}

func (m *oracleModel) applyReserve(op oracleOp) string {
	owner := op.from % len(oracleCustodies)
	if !canSpendKind(oracleCustodies[owner].kind) {
		return "unauthorized"
	}
	if op.amount <= 0 {
		return "invalid"
	}
	if m.balances[owner] < op.amount {
		return "insufficient"
	}
	m.balances[owner] -= op.amount
	m.holds = append(m.holds, &oracleHold{owner: owner, amount: op.amount, status: "active", lapsed: op.lapsed})
	return "ok"
}

func (m *oracleModel) holdAt(index int) *oracleHold {
	if index < 0 || index >= len(m.holds) {
		return nil
	}
	return m.holds[index]
}

func (m *oracleModel) applyRelease(op oracleOp) string {
	hold := m.holdAt(op.hold)
	if hold == nil {
		return "notfound"
	}
	if hold.status != "active" && hold.status != "expired" {
		return "state"
	}
	hold.status = "released"
	m.balances[hold.owner] += hold.amount
	return "ok"
}

func (m *oracleModel) applyCapture(op oracleOp) string {
	hold := m.holdAt(op.hold)
	if hold == nil {
		return "notfound"
	}
	if hold.status != "active" && hold.status != "expired" {
		return "state"
	}
	hold.status = "captured"
	m.balances[op.to%len(oracleCustodies)] += hold.amount
	return "ok"
}

func (m *oracleModel) applyExpire(op oracleOp) string {
	hold := m.holdAt(op.hold)
	if hold == nil {
		return "notfound"
	}
	if hold.status != "active" {
		return "state"
	}
	if !hold.lapsed {
		return "notexpired"
	}
	hold.status = "expired"
	return "ok"
}

func (m *oracleModel) applyIntention(op oracleOp) string {
	// Same-custody refusal precedes the triple lookup, mirroring the
	// use case that validates endpoints before the repository runs.
	to := op.to % len(oracleCustodies)
	if to == 0 {
		return "same"
	}
	triple := scriptTriple(op.key, op.actor)
	if stored, ok := m.intentions[triple]; ok {
		if stored != op.amount {
			return "conflict"
		}
		return "replayed"
	}
	if m.balances[0] < op.amount {
		return "insufficient"
	}
	m.balances[0] -= op.amount
	m.balances[to] += op.amount
	m.intentions[triple] = op.amount
	return "ok"
}

// pgOutcomeCode maps implementation errors to the oracle vocabulary.
func pgOutcomeCode(err error) string {
	if err == nil {
		return "ok"
	}
	switch {
	case errors.Is(err, domain.ErrInsufficientMilliInk):
		return "insufficient"
	case errors.Is(err, domain.ErrUnauthorizedCustody):
		return "unauthorized"
	case errors.Is(err, domain.ErrSameCustody):
		return "same"
	case errors.Is(err, domain.ErrUnknownCustody):
		return "unknown"
	case errors.Is(err, domain.ErrInvalidMilliInk),
		errors.Is(err, domain.ErrInvalidIntention),
		errors.Is(err, domain.ErrInvalidHold),
		errors.Is(err, domain.ErrNegativeMilliInk):
		return "invalid"
	case errors.Is(err, domain.ErrIntentionConflict):
		return "conflict"
	case errors.Is(err, domain.ErrHoldState):
		return "state"
	case errors.Is(err, domain.ErrHoldNotFound):
		return "notfound"
	case errors.Is(err, domain.ErrHoldNotExpired):
		return "notexpired"
	case errors.Is(err, domain.ErrGenesisAlreadyExists):
		return "exists"
	case errors.Is(err, domain.ErrEconomyFrozen):
		return "frozen"
	default:
		return "unexpected:" + err.Error()
	}
}

// oracleHarness drives both sides of one sequence in lockstep.
type oracleHarness struct {
	tb    testing.TB
	ctx   context.Context
	pool  *pgxpool.Pool
	repo  *postgres.Repository
	model *oracleModel
	holds []string
	clock time.Time
}

func newOracleHarness(tb testing.TB, ctx context.Context, pool *pgxpool.Pool) *oracleHarness {
	tb.Helper()
	return &oracleHarness{
		tb: tb, ctx: ctx, pool: pool,
		repo:  postgres.NewRepository(pool),
		model: newOracleModel(),
		clock: time.Now().UTC(),
	}
}

// fund runs Genesis and seeds every script custody, mirroring each move
// in the model so both sides start equal.
func (h *oracleHarness) fund() {
	h.tb.Helper()
	if _, err := h.repo.RunGenesis(h.ctx, application.GenesisRequest{Key: mustTransferKey(h.tb, "genesis-oracle"), Season: domain.SeasonKey(domain.CompatSeasonKey)}); err != nil {
		h.tb.Fatalf("seed Genesis: %v", err)
	}
	for i := 1; i < len(oracleCustodies); i++ {
		makeCustody(h.tb, h.ctx, h.pool, oracleCustodies[i].kind, oracleCustodies[i].label)
		amount, err := domain.NewMilliInk(20000)
		if err != nil {
			h.tb.Fatalf("NewMilliInk: %v", err)
		}
		kind, err := domain.ParseCustodyKind(oracleCustodies[i].kind)
		if err != nil {
			h.tb.Fatalf("ParseCustodyKind: %v", err)
		}
		if _, err := h.repo.Transfer(h.ctx, application.TransferRequest{
			FromSeason: domain.SeasonKey(domain.CompatSeasonKey),
			FromKind:   domain.CustodyTreasury, FromLabel: "main",
			ToSeason: domain.SeasonKey(domain.CompatSeasonKey),
			ToKind:   kind, ToLabel: oracleCustodies[i].label,
			Amount: amount,
		}); err != nil {
			h.tb.Fatalf("fund %s: %v", oracleCustodies[i].label, err)
		}
		h.model.balances[0] -= 20000
		h.model.balances[i] += 20000
	}
}

// scriptAmount renders a scripted millis value for the port: positive
// amounts convert exactly, and anything else becomes the zero value the
// adapter refuses as invalid.
func scriptAmount(millis int64) domain.MilliInk {
	if millis <= 0 {
		return domain.MilliInk{}
	}
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		return domain.MilliInk{}
	}
	return amount
}

// holdIDAt resolves the nth settled hold to its PostgreSQL id, or a
// well-formed unknown id when the model holds nothing there.
func (h *oracleHarness) holdIDAt(index int) string {
	if index < 0 || index >= len(h.holds) {
		return "00000000-0000-7000-8000-000000000000"
	}
	return h.holds[index]
}

// apply runs one scripted step on both sides and compares outcome and
// state. Any divergence fails the test with the step that caused it.
func (h *oracleHarness) apply(step int64, op oracleOp) {
	h.tb.Helper()
	want := h.model.applyOp(op)
	got := h.applyPG(op)
	if got != want {
		h.tb.Fatalf("step %d op %+v: implementation %q, oracle %q", step, op, got, want)
	}
	h.compareState(step, op)
}

// applyPG runs one scripted step against PostgreSQL, answering the
// oracle vocabulary.
func (h *oracleHarness) applyPG(op oracleOp) string {
	from, to := op.from%len(oracleCustodies), op.to%len(oracleCustodies)
	switch op.kind {
	case opTransfer:
		fromKind, toKind := oracleCustodies[from].kind, oracleCustodies[to].kind
		fromLabel, toLabel := oracleCustodies[from].label, oracleCustodies[to].label
		if op.invalid == 1 {
			fromKind = "vault"
		}
		if op.invalid == 3 {
			toKind, toLabel = fromKind, fromLabel
		}
		millis := op.amount
		if op.invalid == 2 {
			millis = 0
		}
		_, err := h.repo.Transfer(h.ctx, application.TransferRequest{
			FromSeason: domain.SeasonKey(domain.CompatSeasonKey),
			FromKind:   domain.CustodyKind(fromKind), FromLabel: fromLabel,
			ToSeason: domain.SeasonKey(domain.CompatSeasonKey),
			ToKind:   domain.CustodyKind(toKind), ToLabel: toLabel,
			Amount: scriptAmount(millis),
		})
		return pgOutcomeCode(err)
	case opReserve:
		expiresAt := h.clock.Add(2 * time.Hour)
		if op.lapsed {
			expiresAt = h.clock.Add(-2 * time.Hour)
		}
		purpose, _ := domain.ParseHoldPurpose(fmt.Sprintf("script-%d", op.key))
		view, err := h.repo.Reserve(h.ctx, application.HoldReservation{
			Season:    domain.SeasonKey(domain.CompatSeasonKey),
			OwnerKind: domain.CustodyKind(oracleCustodies[from].kind), OwnerLabel: oracleCustodies[from].label,
			Purpose: purpose, Amount: scriptAmount(op.amount), ExpiresAt: expiresAt,
		})
		code := pgOutcomeCode(err)
		if code == "ok" {
			h.holds = append(h.holds, view.HoldID)
		}
		return code
	case opRelease:
		_, err := h.repo.Release(h.ctx, h.holdIDAt(op.hold))
		return pgOutcomeCode(err)
	case opCapture:
		_, err := h.repo.Capture(h.ctx, h.holdIDAt(op.hold), oracleCustodies[to].kind, oracleCustodies[to].label)
		return pgOutcomeCode(err)
	case opExpire:
		_, err := h.repo.Expire(h.ctx, h.holdIDAt(op.hold))
		return pgOutcomeCode(err)
	case opIntention:
		useCase := application.NewIdempotentTransferUseCase(h.repo, h.repo)
		_, err := useCase.Execute(h.ctx, application.IdempotentTransferCommand{
			Key: scriptKey(op.key), Actor: scriptActor(op.actor), Operation: "script-op",
			FromSeason: domain.CompatSeasonKey,
			FromKind:   "treasury", FromLabel: "main",
			ToSeason: domain.CompatSeasonKey,
			ToKind:   oracleCustodies[to].kind, ToLabel: oracleCustodies[to].label,
			Millis: max(op.amount, 1),
		})
		code := pgOutcomeCode(err)
		if code == "replayed" {
			return "replayed"
		}
		return code
	default:
		_, err := h.repo.RunGenesis(h.ctx, application.GenesisRequest{Key: mustTransferKey(h.tb, "genesis-oracle"), Season: domain.SeasonKey(domain.CompatSeasonKey)})
		if err == nil {
			return "replayed"
		}
		return pgOutcomeCode(err)
	}
}

// compareState re-derives script custody balances, locked totals,
// intentions and holds from the journal and demands equality with the
// model, plus the global supply.
func (h *oracleHarness) compareState(step int64, op oracleOp) {
	h.tb.Helper()
	for i := range oracleCustodies {
		var balance int64
		err := h.pool.QueryRow(h.ctx,
			`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
			 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
			 WHERE c.kind = $1 AND c.label = $2`,
			oracleCustodies[i].kind, oracleCustodies[i].label).Scan(&balance)
		if err != nil {
			h.tb.Fatalf("step %d: journal sum: %v", step, err)
		}
		if balance != h.model.balances[i] {
			h.tb.Fatalf("step %d op %+v: %s/%s holds %d, oracle says %d",
				step, op, oracleCustodies[i].kind, oracleCustodies[i].label, balance, h.model.balances[i])
		}
	}
	h.compareIntentions(step, op)
	h.compareHolds(step, op)
	var credits, debits int64
	if err := h.pool.QueryRow(h.ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries WHERE direction = 'credit'`).Scan(&credits); err != nil {
		h.tb.Fatalf("step %d: sum credits: %v", step, err)
	}
	if err := h.pool.QueryRow(h.ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries WHERE direction = 'debit'`).Scan(&debits); err != nil {
		h.tb.Fatalf("step %d: sum debits: %v", step, err)
	}
	if credits-debits != domain.GenesisSupplyMillis {
		h.tb.Fatalf("step %d op %+v: supply %d - %d != S", step, op, credits, debits)
	}
	if h.model.sum() != domain.GenesisSupplyMillis {
		h.tb.Fatalf("step %d op %+v: oracle sum %d != S", step, op, h.model.sum())
	}
}

// compareIntentions matches settled triples to amounts on both sides.
// Transfer ids and payload hashes are excluded by design: uuids are
// unguessable, and the conflict outcome already proves the hashes.
func (h *oracleHarness) compareIntentions(step int64, op oracleOp) {
	h.tb.Helper()
	rows, err := h.pool.Query(h.ctx,
		`SELECT intention_key, actor, operation, amount_milli FROM app.economy_intentions`)
	if err != nil {
		h.tb.Fatalf("step %d: dump intentions: %v", step, err)
	}
	defer rows.Close()
	stored := map[string]int64{}
	for rows.Next() {
		var key, actor, operation string
		var amount int64
		if err := rows.Scan(&key, &actor, &operation, &amount); err != nil {
			h.tb.Fatalf("step %d: scan intention: %v", step, err)
		}
		stored[key+"|"+actor+"|"+operation] = amount
	}
	if err := rows.Err(); err != nil {
		h.tb.Fatalf("step %d: iterate intentions: %v", step, err)
	}
	if len(stored) != len(h.model.intentions) {
		h.tb.Fatalf("step %d op %+v: %d intentions stored, oracle holds %d", step, op, len(stored), len(h.model.intentions))
	}
	for triple, amount := range h.model.intentions {
		if stored[triple] != amount {
			h.tb.Fatalf("step %d op %+v: intention %s holds %d, oracle says %d", step, op, triple, stored[triple], amount)
		}
	}
}

// compareHolds matches the locked state per owner: every hold as
// (owner, amount, status), counted as a multiset because PostgreSQL
// mints the ids.
func (h *oracleHarness) compareHolds(step int64, op oracleOp) {
	h.tb.Helper()
	rows, err := h.pool.Query(h.ctx,
		`SELECT oc.kind, oc.label, h.amount_milli, h.status
		 FROM app.economy_holds h JOIN app.economy_custodies oc ON oc.id = h.owner_custody_id`)
	if err != nil {
		h.tb.Fatalf("step %d: dump holds: %v", step, err)
	}
	defer rows.Close()
	stored := map[string]int{}
	for rows.Next() {
		var kind, label, status string
		var amount int64
		if err := rows.Scan(&kind, &label, &amount, &status); err != nil {
			h.tb.Fatalf("step %d: scan hold: %v", step, err)
		}
		stored[fmt.Sprintf("%s/%s|%d|%s", kind, label, amount, status)]++
	}
	if err := rows.Err(); err != nil {
		h.tb.Fatalf("step %d: iterate holds: %v", step, err)
	}
	expected := map[string]int{}
	for _, hold := range h.model.holds {
		owner := oracleCustodies[hold.owner]
		expected[fmt.Sprintf("%s/%s|%d|%s", owner.kind, owner.label, hold.amount, hold.status)]++
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

// genScript renders one deterministic sequence from the seed stream:
// transfers dominate, with reserves, settlements, intentions, replays
// and invalid attempts mixed in. Amounts stay small against funding so
// insufficiency arises from contention, never from emptiness.
func genScript(rng interface{ Int64n(int64) int64 }, length int) []oracleOp {
	script := make([]oracleOp, 0, length)
	for i := 0; i < length; i++ {
		roll := rng.Int64n(100)
		op := oracleOp{
			from:   int(rng.Int64n(int64(len(oracleCustodies)))),
			to:     int(rng.Int64n(int64(len(oracleCustodies)))),
			amount: rng.Int64n(2000) + 1,
			key:    int(rng.Int64n(12)),
			actor:  int(rng.Int64n(3)),
			hold:   int(rng.Int64n(6)),
			lapsed: rng.Int64n(2) == 0,
		}
		switch {
		case roll < 55:
			op.kind = opTransfer
		case roll < 65:
			op.kind = opReserve
		case roll < 72:
			op.kind = opRelease
		case roll < 79:
			op.kind = opCapture
		case roll < 84:
			op.kind = opExpire
		case roll < 94:
			op.kind = opIntention
		case roll < 97:
			op.kind = opGenesis
		default:
			op.kind = opTransfer
			op.invalid = 1 + int(rng.Int64n(3))
		}
		script = append(script, op)
	}
	return script
}

// TestOracleAgreesAcrossSequences runs 10.000 scripted operations across
// 100 seeded sequences, comparing outcome and state after every step.
// Each sequence owns a disposable database closed at its end, so open
// pools never accumulate.
func TestOracleAgreesAcrossSequences(t *testing.T) {
	const sequences = 100
	const steps = 100
	var applied int64
	for sequence := 0; sequence < sequences; sequence++ {
		t.Run(fmt.Sprintf("sequence-%d", sequence), func(t *testing.T) {
			testDB := dbtest.New(t)
			pool := testDB.Pool.Pool()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			harness := newOracleHarness(t, ctx, pool)
			harness.fund()
			rng := testsource.NewRandom(testsource.SeedFor(t) + int64(sequence)*1000003)
			for _, op := range genScript(rng, steps) {
				harness.apply(applied, op)
				applied++
			}
		})
	}
	if applied != sequences*steps {
		t.Fatalf("applied %d operations, want %d", applied, sequences*steps)
	}
}

// TestOracleKillsMutants injects one fault per class with raw SQL and
// proves the comparison bites every time: mint, burn, double spend and
// phantom intention are all detected, with no case excluded.
func TestOracleKillsMutants(t *testing.T) {
	mutants := []struct {
		name   string
		inject func(t *testing.T, ctx context.Context, pool *pgxpool.Pool)
	}{
		{"mint", func(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
			t.Helper()
			if _, err := pool.Exec(ctx,
				`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
				 SELECT gen_random_uuid(), id, 'credit', 1000 FROM app.economy_custodies
				 WHERE kind = 'user' AND label = 'u0'`); err != nil {
				t.Fatalf("inject mint: %v", err)
			}
		}},
		{"burn", func(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
			t.Helper()
			if _, err := pool.Exec(ctx,
				`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
				 SELECT gen_random_uuid(), id, 'debit', 1000 FROM app.economy_custodies
				 WHERE kind = 'user' AND label = 'u0'`); err != nil {
				t.Fatalf("inject burn: %v", err)
			}
		}},
		{"double-spend", func(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
			t.Helper()
			if _, err := pool.Exec(ctx,
				`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
				 SELECT gen_random_uuid(), id, 'debit', 15000 FROM app.economy_custodies
				 WHERE kind = 'user' AND label = 'u1' UNION ALL
				 SELECT gen_random_uuid(), id, 'debit', 15000 FROM app.economy_custodies
				 WHERE kind = 'user' AND label = 'u1'`); err != nil {
				t.Fatalf("inject double spend: %v", err)
			}
		}},
		{"phantom-intention", func(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
			t.Helper()
			if _, err := pool.Exec(ctx,
				`INSERT INTO app.economy_intentions (intention_key, actor, operation, payload_hash, transfer_id, amount_milli)
				 VALUES ('phantom', 'ghost', 'sale', '0000000000000000000000000000000000000000000000000000000000000000', gen_random_uuid(), 100)`); err != nil {
				t.Fatalf("inject phantom intention: %v", err)
			}
		}},
	}
	for _, mutant := range mutants {
		t.Run(mutant.name, func(t *testing.T) {
			testDB := dbtest.New(t)
			pool := testDB.Pool.Pool()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			harness := newOracleHarness(t, ctx, pool)
			harness.fund()
			op := oracleOp{kind: opTransfer, from: 1, to: 2, amount: 100}
			harness.apply(0, op)
			mutant.inject(t, ctx, pool)
			if !mutantDiverged(t, ctx, pool, harness) {
				t.Fatalf("mutant %q survived: the comparison saw nothing", mutant.name)
			}
		})
	}
}

// mutantDiverged replays the oracle state against the tampered journal:
// any balance, supply, intention or hold drift counts as a kill.
func mutantDiverged(t *testing.T, ctx context.Context, pool *pgxpool.Pool, harness *oracleHarness) bool {
	t.Helper()
	for i := range oracleCustodies {
		var balance int64
		if err := pool.QueryRow(ctx,
			`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
			 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
			 WHERE c.kind = $1 AND c.label = $2`,
			oracleCustodies[i].kind, oracleCustodies[i].label).Scan(&balance); err != nil {
			t.Fatalf("journal sum: %v", err)
		}
		if balance != harness.model.balances[i] {
			return true
		}
	}
	var credits, debits int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries WHERE direction = 'credit'`).Scan(&credits); err != nil {
		t.Fatalf("sum credits: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_milli), 0) FROM app.economy_entries WHERE direction = 'debit'`).Scan(&debits); err != nil {
		t.Fatalf("sum debits: %v", err)
	}
	if credits-debits != domain.GenesisSupplyMillis {
		return true
	}
	var intentions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_intentions`).Scan(&intentions); err != nil {
		t.Fatalf("count intentions: %v", err)
	}
	return intentions != len(harness.model.intentions)
}

// decodeScript renders raw fuzzer bytes as operations in the generator
// layout, so fuzzing explores the same vocabulary as the sequences. The
// amount byte doubles as the invalid gate at its top: values from 250 up
// walk the unknown, zero and same-custody refusals.
func decodeScript(raw []byte) []oracleOp {
	script := []oracleOp{}
	for i := 0; i+4 <= len(raw); i += 4 {
		op := oracleOp{
			kind:   int(raw[i] % 7),
			from:   int(raw[i+1] % byte(len(oracleCustodies))),
			to:     int(raw[i+2] % byte(len(oracleCustodies))),
			amount: int64(raw[i+3]%20+1) * 100,
			key:    int(raw[i] % 4),
			actor:  int(raw[i+1] % 3),
			hold:   int(raw[i+2] % 6),
			lapsed: raw[i+3]%2 == 0,
		}
		if raw[i+3] >= 250 {
			op.invalid = 1 + int((raw[i+3]-250)%3)
		}
		script = append(script, op)
	}
	return script
}

// FuzzOracleModel proves the reference never panics and its invariants
// hold on arbitrary input: supply stays funded, balances stay
// non-negative, and every answer stays inside the outcome vocabulary.
// Model and implementation meet only in the scripted tests above, where
// PostgreSQL answers; here the model must hold alone.
func FuzzOracleModel(f *testing.F) {
	f.Add([]byte{0, 1, 2, 100, 3, 4, 5, 6})
	f.Add([]byte{5, 1, 2, 100})
	f.Add([]byte{1, 1, 1, 100, 2, 0, 0, 0, 3, 0, 0, 0})
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{6, 0, 0, 0})
	f.Add([]byte{0, 1, 1, 255})

	f.Fuzz(func(t *testing.T, raw []byte) {
		funded := map[int]int64{0: 1000000}
		for i := 1; i < len(oracleCustodies); i++ {
			funded[i] = 1000000
		}
		first := newOracleModel()
		first.balances = funded
		codes := map[string]bool{}
		for _, op := range decodeScript(raw) {
			codes[first.applyOp(op)] = true
		}
		if first.sum() != 7000000 {
			t.Fatalf("model sum %d moved", first.sum())
		}
		for i, balance := range first.balances {
			if balance < 0 {
				t.Fatalf("model custody %d negative", i)
			}
		}
		for code := range codes {
			switch code {
			case "ok", "replayed", "insufficient", "unauthorized", "same", "unknown",
				"invalid", "conflict", "state", "notfound", "notexpired", "exists":
			default:
				t.Fatalf("model answered outside the vocabulary: %q", code)
			}
		}

		second := newOracleModel()
		second.balances = map[int]int64{0: 1000000}
		for i := 1; i < len(oracleCustodies); i++ {
			second.balances[i] = 1000000
		}
		for _, op := range decodeScript(raw) {
			second.applyOp(op)
		}
		if second.sum() != first.sum() {
			t.Fatalf("model is not deterministic: %d vs %d", first.sum(), second.sum())
		}
	})
}
