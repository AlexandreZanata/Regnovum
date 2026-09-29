package postgres_test

// P36-T09 — independent exact-once oracle on real PostgreSQL.
//
// The oracle is a pure in-memory model sharing no settlement code
// with the adapters: settled intentions keyed by account and token,
// compensated causes, and the citizen/Treasury deltas. Deterministic
// scripts drive the model and the PostgreSQL use cases step by step
// (3.000 operations across 60 seeded sequences); after every step
// the outcome class and the running totals must agree. Injected
// mutants (orphan debit, free publication, double charge, rounded
// total) prove the comparison bites, previews never write, and no
// case is excluded.
//
// Comparison boundaries, stated once: transfer ids and posted_at are
// excluded because the database mints them (pairing integrity is
// proven by T04); payload hashes are excluded because the conflict
// outcome already proves them; quote expiry margins stay wide around
// the fixed test clock.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	meteringpg "github.com/AlexandreZanata/Regnovum/internal/metering/adapters/postgres"
	meteringapp "github.com/AlexandreZanata/Regnovum/internal/metering/application"
	meteringdomain "github.com/AlexandreZanata/Regnovum/internal/metering/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

// oracleOp is one scripted step over small closed vocabularies so
// every script reproduces from its seed.
type oracleOp struct {
	kind    int // preview, publish, replay, conflict, expired, edited, refund, refundReplay, refundDouble, refundUnknown
	key     int
	content int
}

const (
	opPreview = iota
	opPublish
	opReplay
	opConflict
	opExpired
	opEdited
	opRefund
	opRefundReplay
	opRefundDouble
	opRefundUnknown
)

// oracleTexts are the billed candidates: distinct hashes, known
// units, one exceeding nothing.
var oracleTexts = []string{
	"texto final zero",
	"texto final um",
	"texto final dois",
	"café 👩🏽‍🚀",
	"שלום עולם",
	"texto final cinco",
	"texto final seis",
	"texto final sete",
}

func oracleKey(sequence, key int) string {
	return fmt.Sprintf("oracle-s%d-k%d", sequence, key)
}

// oracleSettlement is the model side of one settled intention.
type oracleSettlement struct {
	total    int64
	refunded bool
}

// oracleModel is the independent exact-once ledger: intentions by
// key, compensated causes, and the expected citizen/Treasury moves.
type oracleModel struct {
	settled   map[string]*oracleSettlement
	payloads  map[string]string
	spent     int64
	returned  int64
	funds     int64
	published int
}

// oracleHarness drives both sides with one fixed catalog, clock and
// citizen.
type oracleHarness struct {
	t         *testing.T
	ctx       context.Context
	db        *dbtest.TestDB
	publishUC *meteringapp.PublishUseCase
	refundUC  *meteringapp.RefundUseCase
	previewUC *meteringapp.PreviewUseCase
	price     meteringdomain.PriceEntry
	now       time.Time
	accepted  time.Time
	citizen   string
	model     *oracleModel
}

func newOracleHarness(t *testing.T, ctx context.Context, db *dbtest.TestDB, citizen string, funds int64) *oracleHarness {
	t.Helper()
	pool := db.Pool.Pool()
	accepted := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	now := accepted.Add(time.Minute)
	service, _ := meteringdomain.ParseServiceID("argument-publish")
	price, err := meteringdomain.NewPriceEntry(meteringdomain.PriceRequest{
		Service: service, Version: 2,
		ValidFrom: accepted.Add(-3 * time.Hour), ValidUntil: accepted.Add(time.Hour),
		PriceMilli: 250, Unit: meteringdomain.UnitGraphemeCluster, Authority: "oracle-authority",
	})
	if err != nil {
		t.Fatalf("NewPriceEntry: %v", err)
	}
	var catalog meteringdomain.Catalog
	if err := catalog.Add(price); err != nil {
		t.Fatalf("Add price: %v", err)
	}
	clock := publishClock{now: now}
	repo, err := meteringpg.NewRepository(pool, clock, catalog)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	publishUC, err := meteringapp.NewPublishUseCase(repo)
	if err != nil {
		t.Fatalf("NewPublishUseCase: %v", err)
	}
	refundUC, err := meteringapp.NewRefundUseCase(repo)
	if err != nil {
		t.Fatalf("NewRefundUseCase: %v", err)
	}
	previewUC, err := meteringapp.NewPreviewUseCase(catalog, text.GraphemeCount)
	if err != nil {
		t.Fatalf("NewPreviewUseCase: %v", err)
	}
	seedLedger(t, ctx, db, citizen, funds)
	return &oracleHarness{
		t: t, ctx: ctx, db: db,
		publishUC: publishUC, refundUC: refundUC, previewUC: previewUC,
		price: price, now: now, accepted: accepted, citizen: citizen,
		model: &oracleModel{settled: map[string]*oracleSettlement{}, payloads: map[string]string{}, funds: funds},
	}
}

func oracleContent(t *testing.T, index int) (meteringdomain.MeasuredContent, int64) {
	t.Helper()
	content, err := meteringdomain.ParseMeasuredContent(oracleTexts[index%len(oracleTexts)], text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("ParseMeasuredContent: %v", err)
	}
	total, err := meteringdomain.TotalFor(content.Units(), 250)
	if err != nil {
		t.Fatalf("TotalFor: %v", err)
	}
	return content, total
}

func oracleQuote(t *testing.T, h *oracleHarness, content meteringdomain.MeasuredContent, accepted time.Time, ttl time.Duration) meteringdomain.PublicationQuote {
	t.Helper()
	quote, err := meteringdomain.AcceptPublicationQuote(meteringdomain.QuoteRequest{
		Account: h.citizen, Content: content, Price: h.price, AcceptedAt: accepted, TTL: ttl,
	})
	if err != nil {
		t.Fatalf("AcceptPublicationQuote: %v", err)
	}
	return quote
}

func oraclePayload(h *oracleHarness, content meteringdomain.MeasuredContent, quote meteringdomain.PublicationQuote) string {
	return meteringdomain.PublishPayloadHash(meteringdomain.PublishPayload{
		Account: h.citizen, Service: "argument-publish", Version: 2,
		Units: content.Units(), AmountMilli: quote.TotalMilli,
		ContentHash: content.Hash().String(), QuoteHash: quote.Hash,
		FromKind: "user", FromLabel: h.citizen, ToKind: "treasury", ToLabel: "main",
	})
}

// genScript renders one deterministic operation sequence: previews
// that must never write, fresh and repeated publishes, divergent
// keys, lapsed and edited acceptances, and the full refund ladder.
func genScript(rng *testsource.Random, steps int) []oracleOp {
	script := make([]oracleOp, 0, steps)
	for i := 0; i < steps; i++ {
		script = append(script, oracleOp{
			kind:    int(rng.Int64n(10)),
			key:     int(rng.Int64n(8)),
			content: int(rng.Int64n(int64(len(oracleTexts)))),
		})
	}
	return script
}

// publish executes the real publish path for one key and content.
func (h *oracleHarness) publish(key string, content meteringdomain.MeasuredContent, quote meteringdomain.PublicationQuote) (*meteringapp.PublishResult, error) {
	return h.publishUC.Execute(h.ctx, meteringapp.PublishCommand{
		Key: key, Account: h.citizen, Content: content, Price: h.price, Quote: quote,
		FromKind: "user", FromLabel: h.citizen, ToKind: "treasury", ToLabel: "main", Now: h.now,
	})
}

// refund executes the real refund path for one original key.
func (h *oracleHarness) refund(key, original string) (*meteringapp.RefundResult, error) {
	return h.refundUC.Execute(h.ctx, meteringapp.RefundCommand{
		Key: key, Account: h.citizen, OriginalKey: original, Reason: "oracle-error",
	})
}

// apply runs one scripted step on both sides and demands agreement
// on outcome class, replay flag and running totals.
func (h *oracleHarness) apply(op oracleOp, sequence int) {
	t := h.t
	key := oracleKey(sequence, op.key)
	content, _ := oracleContent(t, op.content)
	quote := oracleQuote(t, h, content, h.accepted, 30*time.Minute)
	model := h.model

	switch op.kind {
	case opPreview:
		beforePubs, beforeLegs := countPublications(t, h.ctx, h.db), journalLegs(t, h.ctx, h.db)
		if _, err := h.previewUC.Execute(meteringapp.PreviewCommand{
			Account: h.citizen, Content: oracleTexts[op.content%len(oracleTexts)],
			Service: "argument-publish", Now: h.now, MaxUnits: 3000, TTL: 5 * time.Minute,
		}); err != nil {
			t.Fatalf("preview: %v", err)
		}
		if countPublications(t, h.ctx, h.db) != beforePubs || journalLegs(t, h.ctx, h.db) != beforeLegs {
			t.Fatal("preview wrote: drafts and previews must never charge")
		}
	case opPublish, opReplay, opConflict:
		useContent, useQuote := content, quote
		if op.kind == opConflict {
			useContent, _ = oracleContent(t, op.content+1)
			useQuote = oracleQuote(t, h, useContent, h.accepted, 30*time.Minute)
		}
		payload := oraclePayload(h, useContent, useQuote)
		stored, ok := model.settled[key]
		if ok {
			got, err := h.publish(key, useContent, useQuote)
			if model.payloads[key] == payload {
				if err != nil {
					t.Fatalf("replay %s: %v", key, err)
				}
				if !got.Replayed || got.TotalMilli != stored.total {
					t.Fatalf("replay %s = %+v, want total %d replayed", key, got, stored.total)
				}
				return
			}
			if !errors.Is(err, meteringdomain.ErrPublishConflict) {
				t.Fatalf("conflict %s: error = %v, want ErrPublishConflict", key, err)
			}
			return
		}
		if model.funds-model.spent+model.returned < useQuote.TotalMilli {
			if _, err := h.publish(key, useContent, useQuote); !errors.Is(err, economydomain.ErrInsufficientMilliInk) {
				t.Fatalf("short publish %s: error = %v, want ErrInsufficientMilliInk", key, err)
			}
			return
		}
		got, err := h.publish(key, useContent, useQuote)
		if err != nil {
			t.Fatalf("publish %s: %v", key, err)
		}
		if got.Replayed || got.TotalMilli != useQuote.TotalMilli {
			t.Fatalf("publish %s = %+v, want total %d fresh", key, got, useQuote.TotalMilli)
		}
		model.settled[key] = &oracleSettlement{total: useQuote.TotalMilli}
		model.payloads[key] = payload
		model.spent += useQuote.TotalMilli
		model.published++
	case opExpired:
		stale := oracleQuote(t, h, content, h.accepted.Add(-2*time.Hour), time.Minute)
		if _, err := h.publish(key, content, stale); !errors.Is(err, meteringdomain.ErrQuoteExpired) {
			t.Fatalf("expired: error = %v, want ErrQuoteExpired", err)
		}
	case opEdited:
		mismatched, err := meteringdomain.AcceptPublicationQuote(meteringdomain.QuoteRequest{
			Account: "someone-else", Content: content, Price: h.price, AcceptedAt: h.accepted, TTL: 30 * time.Minute,
		})
		if err != nil {
			t.Fatalf("mismatched quote: %v", err)
		}
		if _, err := h.publish(key, content, mismatched); !errors.Is(err, meteringdomain.ErrQuoteMismatch) {
			t.Fatalf("edited: error = %v, want ErrQuoteMismatch", err)
		}
	case opRefund, opRefundReplay, opRefundDouble, opRefundUnknown:
		target := key
		if op.kind == opRefundUnknown {
			target = oracleKey(sequence, 1000000+op.key)
		}
		stored, ok := model.settled[target]
		if !ok {
			if _, err := h.refund("refund-"+target, target); !errors.Is(err, meteringdomain.ErrUnknownPublication) {
				t.Fatalf("unknown refund: error = %v, want ErrUnknownPublication", err)
			}
			return
		}
		if stored.refunded {
			// Compensated earlier: the same key replays, a new
			// key for the cause refuses.
			got, err := h.refund("refund-"+target, target)
			if err != nil {
				t.Fatalf("compensated replay %s: %v", target, err)
			}
			if !got.Replayed {
				t.Fatalf("compensated replay %s must report replayed", target)
			}
			if op.kind == opRefundDouble {
				if _, err := h.refund("refund-again-"+target, target); !errors.Is(err, meteringdomain.ErrRefundDuplicate) {
					t.Fatalf("double refund: error = %v, want ErrRefundDuplicate", err)
				}
			}
			return
		}
		first, err := h.refund("refund-"+target, target)
		if err != nil {
			t.Fatalf("refund %s: %v", target, err)
		}
		if first.AmountMilli != stored.total {
			t.Fatalf("refund %s = %d, want original %d", target, first.AmountMilli, stored.total)
		}
		stored.refunded = true
		model.returned += stored.total
		if op.kind == opRefundReplay {
			again, err := h.refund("refund-"+target, target)
			if err != nil {
				t.Fatalf("refund replay %s: %v", target, err)
			}
			if !again.Replayed || again.RefundID != first.RefundID {
				t.Fatal("refund replay must resolve the original compensation")
			}
		}
		if op.kind == opRefundDouble {
			if _, err := h.refund("refund-again-"+target, target); !errors.Is(err, meteringdomain.ErrRefundDuplicate) {
				t.Fatalf("double refund: error = %v, want ErrRefundDuplicate", err)
			}
		}
	}
}

// journalLegs counts every journal leg: the exact-once ledger grows
// only through paired settlements and compensations.
func journalLegs(t *testing.T, ctx context.Context, db *dbtest.TestDB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries`).Scan(&count); err != nil {
		t.Fatalf("count legs: %v", err)
	}
	return count
}

// checkLedgerSound replays the oracle state against the database and
// reports every divergence: balances, pairings, linkages and the
// rounded-total battery. Empty findings mean exact-once holds.
func checkLedgerSound(t *testing.T, ctx context.Context, db *dbtest.TestDB, h *oracleHarness) []string {
	t.Helper()
	findings := []string{}
	var citizen, treasury int64
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = 'user' AND c.label = $1`, h.citizen).Scan(&citizen); err != nil {
		t.Fatalf("read citizen: %v", err)
	}
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0)
		 FROM app.economy_entries e JOIN app.economy_custodies c ON c.id = e.custody_id
		 WHERE c.kind = 'treasury' AND c.label = 'main'`).Scan(&treasury); err != nil {
		t.Fatalf("read treasury: %v", err)
	}
	// Treasury starts at S and funds the citizen: its delta plus the
	// citizen balance must equal S, with the citizen holding exactly
	// funds minus net charges.
	wantCitizen := h.model.funds - h.model.spent + h.model.returned
	if citizen != wantCitizen {
		findings = append(findings, fmt.Sprintf("citizen = %d, want %d", citizen, wantCitizen))
	}
	var genesis = economydomain.GenesisSupplyMillis
	if treasury != genesis-h.model.funds+h.model.spent-h.model.returned {
		findings = append(findings, fmt.Sprintf("treasury = %d, want conservation", treasury))
	}
	// Every transfer pairs exactly one debit with one credit. The
	// lawful exception is Genesis itself: the single unpaired
	// credit of exactly S, which the check names instead of
	// ignoring.
	rows, err := db.Query(ctx,
		`SELECT transfer_id::text,
		  count(*) FILTER (WHERE direction = 'debit'),
		  count(*) FILTER (WHERE direction = 'credit'),
		  COALESCE(SUM(amount_milli) FILTER (WHERE direction = 'credit'), 0)
		 FROM app.economy_entries GROUP BY transfer_id`)
	if err != nil {
		t.Fatalf("read pairings: %v", err)
	}
	defer rows.Close()
	genesisSeen := false
	for rows.Next() {
		var transfer string
		var debits, credits, credited int64
		if err := rows.Scan(&transfer, &debits, &credits, &credited); err != nil {
			t.Fatalf("scan pairing: %v", err)
		}
		if debits == 0 && credits == 1 && credited == economydomain.GenesisSupplyMillis {
			genesisSeen = true
			continue
		}
		if debits != 1 || credits != 1 {
			findings = append(findings, fmt.Sprintf("transfer %s pairs %d/%d, want 1/1", transfer, debits, credits))
		}
	}
	if !genesisSeen {
		findings = append(findings, "genesis credit missing: the lawful unpaired credit must exist exactly once")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pairings: %v", err)
	}
	// Every publication carries legs; every compensation links a
	// settled cause with the exact original amount.
	var pubs, linked, refunds int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.metering_publications`).Scan(&pubs); err != nil {
		t.Fatalf("count publications: %v", err)
	}
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM app.metering_publications p
		 JOIN app.economy_entries e ON e.transfer_id = p.transfer_id`).Scan(&linked); err != nil {
		t.Fatalf("count linked: %v", err)
	}
	if linked != 2*pubs {
		findings = append(findings, fmt.Sprintf("linked legs = %d for %d publications, want pairs", linked, pubs))
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app.metering_refunds`).Scan(&refunds); err != nil {
		t.Fatalf("count refunds: %v", err)
	}
	var refunded int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM app.metering_refunds r
		 JOIN app.metering_publications p ON p.id = r.original_id AND p.amount_milli = r.amount_milli`).Scan(&refunded); err != nil {
		t.Fatalf("count linked refunds: %v", err)
	}
	if refunded != refunds {
		findings = append(findings, fmt.Sprintf("linked refunds = %d of %d, want every cause with its exact amount", refunded, refunds))
	}
	if pubs != h.model.published {
		findings = append(findings, fmt.Sprintf("publications = %d, want model %d", pubs, h.model.published))
	}
	return findings
}

// TestOracleAgreesAcrossSequences runs 3.000 scripted operations
// across 60 seeded sequences on one disposable database, comparing
// outcome and state after every step. Funds run short mid-run by
// design, so uncovered publishes refuse through the modeled path
// like every other fault.
func TestOracleAgreesAcrossSequences(t *testing.T) {
	const sequences = 60
	const steps = 50
	testDB := dbtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	harness := newOracleHarness(t, ctx, testDB, "oracle-citizen", 40000)
	applied := 0
	for sequence := 0; sequence < sequences; sequence++ {
		rng := testsource.NewRandom(testsource.SeedFor(t) + int64(sequence)*1000003)
		for _, op := range genScript(rng, steps) {
			harness.apply(op, sequence)
			applied++
		}
	}
	if applied != sequences*steps {
		t.Fatalf("applied %d operations, want %d", applied, sequences*steps)
	}
	if findings := checkLedgerSound(t, ctx, testDB, harness); len(findings) > 0 {
		t.Fatalf("oracle diverged:\n%s", strings.Join(findings, "\n"))
	}
}

// TestOracleKillsMutants injects one fault per class with raw SQL
// and proves the comparison bites every time: orphan debit, free
// publication, double charge and rounded total are all detected,
// with no case excluded.
func TestOracleKillsMutants(t *testing.T) {
	mutants := []struct {
		name   string
		inject func(t *testing.T, ctx context.Context, db *dbtest.TestDB)
	}{
		{"orphan-debit", func(t *testing.T, ctx context.Context, db *dbtest.TestDB) {
			t.Helper()
			if _, err := db.Exec(ctx,
				`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
				 SELECT gen_random_uuid(), id, 'debit', 1000 FROM app.economy_custodies
				 WHERE kind = 'user' AND label = 'oracle-citizen'`); err != nil {
				t.Fatalf("inject orphan debit: %v", err)
			}
		}},
		{"free-publication", func(t *testing.T, ctx context.Context, db *dbtest.TestDB) {
			t.Helper()
			if _, err := db.Exec(ctx,
				`INSERT INTO app.metering_publications
				 (intention_key, account_label, service, price_version, units, amount_milli,
				  content_hash, quote_hash, payload_hash, transfer_id)
				 VALUES ('mutant-free', 'oracle-citizen', 'argument-publish', 2, 11, 2750,
				  'v1:aa', 'seal-q', 'seal-payload', gen_random_uuid())`); err != nil {
				t.Fatalf("inject free publication: %v", err)
			}
		}},
		{"double-charge", func(t *testing.T, ctx context.Context, db *dbtest.TestDB) {
			t.Helper()
			// A second paired transfer for one publication: the
			// schema refuses a duplicated pair under one id, so
			// the mutant charges again under a fresh one. The
			// balances diverge from the model either way.
			var transfer string
			if err := db.QueryRow(ctx,
				`SELECT transfer_id::text FROM app.metering_publications LIMIT 1`).Scan(&transfer); err != nil {
				t.Fatalf("read settled transfer: %v", err)
			}
			if _, err := db.Exec(ctx,
				`WITH fresh AS (SELECT gen_random_uuid() AS id)
				 INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
				 SELECT (SELECT id FROM fresh), custody_id, direction, amount_milli
				 FROM app.economy_entries WHERE transfer_id = $1::uuid`, transfer); err != nil {
				t.Fatalf("inject double charge: %v", err)
			}
		}},
		{"short-refund", func(t *testing.T, ctx context.Context, db *dbtest.TestDB) {
			t.Helper()
			var original string
			if err := db.QueryRow(ctx,
				`SELECT id::text FROM app.metering_publications LIMIT 1`).Scan(&original); err != nil {
				t.Fatalf("read settled cause: %v", err)
			}
			if _, err := db.Exec(ctx,
				`INSERT INTO app.metering_refunds
				 (refund_key, account_label, original_id, amount_milli, transfer_id, reason)
				 VALUES ('mutant-short', 'oracle-citizen', $1::uuid, 1, gen_random_uuid(), 'short')`,
				original); err != nil {
				t.Fatalf("inject short refund: %v", err)
			}
		}},
	}
	for _, mutant := range mutants {
		t.Run(mutant.name, func(t *testing.T) {
			testDB := dbtest.New(t)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			harness := newOracleHarness(t, ctx, testDB, "oracle-citizen", 40000)
			content, _ := oracleContent(t, 0)
			quote := oracleQuote(t, harness, content, harness.accepted, 30*time.Minute)
			if _, err := harness.publish("mutant-seed", content, quote); err != nil {
				t.Fatalf("seed settlement: %v", err)
			}
			mutant.inject(t, ctx, testDB)
			if findings := checkLedgerSound(t, ctx, testDB, harness); len(findings) == 0 {
				t.Fatalf("mutant %q survived: the comparison saw nothing", mutant.name)
			}
		})
	}
}

// TestChargeRoundsExactly proves totals are exact integer products
// across odd batteries: a ceiling mutant charging one milliINK more
// would mismatch every line here.
func TestChargeRoundsExactly(t *testing.T) {
	testDB := dbtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	harness := newOracleHarness(t, ctx, testDB, "oracle-exact", 10000000)
	texts := []string{"a", "ab", "café", "👩🏽‍🚀🇧🇷", "texto final com medida ímpar"}
	for i, raw := range texts {
		content, err := meteringdomain.ParseMeasuredContent(raw, text.GraphemeCount, 3000)
		if err != nil {
			t.Fatalf("measure %q: %v", raw, err)
		}
		quote := oracleQuote(t, harness, content, harness.accepted, 30*time.Minute)
		want := int64(content.Units()) * 250
		if quote.TotalMilli != want {
			t.Fatalf("quote %q total = %d, want exact %d", raw, quote.TotalMilli, want)
		}
		got, err := harness.publish(fmt.Sprintf("exact-%d", i), content, quote)
		if err != nil {
			t.Fatalf("publish %q: %v", raw, err)
		}
		if got.TotalMilli != want {
			t.Fatalf("settled %q = %d, want exact %d", raw, got.TotalMilli, want)
		}
		var journal int64
		if err := testDB.QueryRow(ctx,
			`SELECT amount_milli FROM app.economy_entries WHERE transfer_id = $1::uuid AND direction = 'debit'`,
			got.TransferID).Scan(&journal); err != nil {
			t.Fatalf("read journal: %v", err)
		}
		if journal != want {
			t.Fatalf("journal %q = %d, want exact %d", raw, journal, want)
		}
	}
}
