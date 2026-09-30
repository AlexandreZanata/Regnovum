package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func mustCreatePassLot(
	t *testing.T,
	ctx context.Context,
	q *postgres.Queries,
	accountID pgtype.UUID,
	origin string,
	quantity int32,
	expiresAt pgtype.Timestamptz,
	reference string,
) postgres.AppArenaPassLot {
	t.Helper()
	lot, err := q.CreateArenaPassLot(ctx, postgres.CreateArenaPassLotParams{
		AccountID:         accountID,
		Origin:            origin,
		Quantity:          quantity,
		RemainingQuantity: quantity,
		ExpiresAt:         expiresAt,
		Reference:         reference,
	})
	if err != nil {
		t.Fatalf("create pass lot %s/%s: %v", origin, reference, err)
	}
	return lot
}

func TestArenaPassLotConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "pass-lots@arena.example.com")
	other := mustCreateAccount(t, ctx, q, "pass-lots-other@arena.example.com")
	expiry := pgtype.Timestamptz{Time: time.Now().Add(30 * 24 * time.Hour).UTC(), Valid: true}

	// Valid origins: bought passes do not expire, Member lots expire and
	// admin grants are explicit.
	mustCreatePassLot(t, ctx, q, acc.ID, "PURCHASE", 5, pgtype.Timestamptz{}, "stripe:evt_purchase_1")
	mustCreatePassLot(t, ctx, q, acc.ID, "MEMBER", 1, expiry, "member:2026-09")
	mustCreatePassLot(t, ctx, q, acc.ID, "ADMIN", 2, pgtype.Timestamptz{}, "admin:ticket-7")

	probes := []struct {
		name              string
		accountID         pgtype.UUID
		origin            string
		quantity          int32
		remainingQuantity int32
		expiresAt         pgtype.Timestamptz
		reference         string
		wantCode          string
	}{
		{name: "zero quantity", accountID: acc.ID, origin: "PURCHASE", quantity: 0, remainingQuantity: 0, reference: "zero", wantCode: "23514"},
		{name: "negative quantity", accountID: acc.ID, origin: "PURCHASE", quantity: -1, remainingQuantity: -1, reference: "negative", wantCode: "23514"},
		{name: "remaining above quantity", accountID: acc.ID, origin: "PURCHASE", quantity: 1, remainingQuantity: 2, reference: "above", wantCode: "23514"},
		{name: "negative remaining", accountID: acc.ID, origin: "PURCHASE", quantity: 1, remainingQuantity: -1, reference: "negative-remaining", wantCode: "23514"},
		{name: "unknown origin", accountID: acc.ID, origin: "GIFT", quantity: 1, remainingQuantity: 1, reference: "gift", wantCode: "23514"},
		{name: "blank reference", accountID: acc.ID, origin: "PURCHASE", quantity: 1, remainingQuantity: 1, reference: "   ", wantCode: "23514"},
		{name: "duplicate grant", accountID: acc.ID, origin: "PURCHASE", quantity: 1, remainingQuantity: 1, reference: "stripe:evt_purchase_1", wantCode: "23505"},
		{name: "orphan account", accountID: pgtype.UUID{Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c}, Valid: true}, origin: "PURCHASE", quantity: 1, remainingQuantity: 1, reference: "orphan", wantCode: "23503"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := q.CreateArenaPassLot(ctx, postgres.CreateArenaPassLotParams{
				AccountID:         probe.accountID,
				Origin:            probe.origin,
				Quantity:          probe.quantity,
				RemainingQuantity: probe.remainingQuantity,
				ExpiresAt:         probe.expiresAt,
				Reference:         probe.reference,
			})
			assertPgCode(t, err, probe.wantCode)
		})
	}

	// Idempotency is scoped by account: another origin or another account may
	// reuse the reference text.
	mustCreatePassLot(t, ctx, q, acc.ID, "ADMIN", 1, pgtype.Timestamptz{}, "stripe:evt_purchase_1")
	mustCreatePassLot(t, ctx, q, other.ID, "PURCHASE", 1, pgtype.Timestamptz{}, "stripe:evt_purchase_1")
}

func TestArenaPassLotConsumptionAndConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "pass-consume@arena.example.com")
	lot := mustCreatePassLot(t, ctx, q, acc.ID, "PURCHASE", 2, pgtype.Timestamptz{}, "stripe:evt_consume")

	arenaA := pgtype.UUID{Bytes: [16]byte{0xa1, 0xa2, 0xa3, 0xa4, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c}, Valid: true}
	arenaB := pgtype.UUID{Bytes: [16]byte{0xb1, 0xb2, 0xb3, 0xb4, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c}, Valid: true}
	arenaC := pgtype.UUID{Bytes: [16]byte{0xc1, 0xc2, 0xc3, 0xc4, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c}, Valid: true}

	// The conditional decrement succeeds exactly while passes remain.
	for i := 0; i < 2; i++ {
		rows, err := q.ConsumeArenaPassLot(ctx, lot.ID)
		if err != nil {
			t.Fatalf("consume %d: %v", i, err)
		}
		if rows != 1 {
			t.Fatalf("consume %d affected %d rows, want 1", i, rows)
		}
	}
	if rows, err := q.ConsumeArenaPassLot(ctx, lot.ID); err != nil || rows != 0 {
		t.Fatalf("consume beyond the lot = %d rows, %v; want 0 rows", rows, err)
	}

	updated, err := q.GetArenaPassLot(ctx, lot.ID)
	if err != nil {
		t.Fatalf("reload lot: %v", err)
	}
	if updated.RemainingQuantity != 0 {
		t.Fatalf("remaining = %d, want 0 (never negative)", updated.RemainingQuantity)
	}

	// The unconditional decrement is refused by the CHECK constraint.
	_, err = db.Pool.Exec(ctx,
		"UPDATE app.arena_pass_lots SET remaining_quantity = remaining_quantity - 1 WHERE id = $1", lot.ID)
	assertPgCode(t, err, "23514")

	// One consumption per Arena, append-only rows.
	if _, err := q.CreateArenaPassConsumption(ctx, postgres.CreateArenaPassConsumptionParams{LotID: lot.ID, ArenaID: arenaA}); err != nil {
		t.Fatalf("first consumption: %v", err)
	}
	_, err = q.CreateArenaPassConsumption(ctx, postgres.CreateArenaPassConsumptionParams{LotID: lot.ID, ArenaID: arenaA})
	assertPgCode(t, err, "23505")

	// A distinct Arena may consume another lot (and the duplicate Arena is
	// refused there as well, globally).
	otherLot := mustCreatePassLot(t, ctx, q, acc.ID, "MEMBER", 1, pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true}, "member:2026-10")
	if _, err := q.CreateArenaPassConsumption(ctx, postgres.CreateArenaPassConsumptionParams{LotID: otherLot.ID, ArenaID: arenaB}); err != nil {
		t.Fatalf("consumption on another lot: %v", err)
	}
	_, err = q.CreateArenaPassConsumption(ctx, postgres.CreateArenaPassConsumptionParams{LotID: otherLot.ID, ArenaID: arenaB})
	assertPgCode(t, err, "23505")

	// Unknown lots are refused.
	orphan := pgtype.UUID{Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc}, Valid: true}
	_, err = q.CreateArenaPassConsumption(ctx, postgres.CreateArenaPassConsumptionParams{LotID: orphan, ArenaID: arenaC})
	assertPgCode(t, err, "23503")

	// The statement resolves through the lot and never mixes accounts.
	entries, err := q.ListArenaPassConsumptionsByAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("list consumptions: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("consumptions = %d, want 2", len(entries))
	}
	for _, entry := range entries {
		if entry.Origin == "" || entry.Reference == "" || !entry.ConsumedAt.Valid {
			t.Fatalf("incomplete consumption entry: %+v", entry)
		}
	}
}

// TestArenaPassConsumptionIsAppendOnly proves the consumptions table can
// never be rewritten by the runtime: arena_app may only read and insert.
func TestArenaPassConsumptionIsAppendOnly(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "pass-append-only@arena.example.com")
	lot := mustCreatePassLot(t, ctx, q, acc.ID, "PURCHASE", 1, pgtype.Timestamptz{}, "stripe:evt_append")
	arenaID := pgtype.UUID{Bytes: [16]byte{0xf1, 0xf2, 0xf3, 0xf4, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c}, Valid: true}
	if _, err := q.CreateArenaPassConsumption(ctx, postgres.CreateArenaPassConsumptionParams{LotID: lot.ID, ArenaID: arenaID}); err != nil {
		t.Fatalf("seed consumption: %v", err)
	}

	checks := []struct {
		table string
		priv  string
		want  bool
	}{
		{"app.arena_pass_consumptions", "SELECT", true},
		{"app.arena_pass_consumptions", "INSERT", true},
		{"app.arena_pass_consumptions", "UPDATE", false},
		{"app.arena_pass_consumptions", "DELETE", false},
		{"app.arena_pass_lots", "SELECT", true},
		{"app.arena_pass_lots", "INSERT", true},
		{"app.arena_pass_lots", "UPDATE", true},
		{"app.arena_pass_lots", "DELETE", false},
	}
	for _, check := range checks {
		var allowed bool
		if err := db.Pool.QueryRow(ctx,
			"SELECT has_table_privilege('arena_app', $1, $2)", check.table, check.priv,
		).Scan(&allowed); err != nil {
			t.Fatalf("has_table_privilege(%s, %s): %v", check.table, check.priv, err)
		}
		if allowed != check.want {
			t.Errorf("arena_app %s on %s = %v, want %v", check.priv, check.table, allowed, check.want)
		}
	}

	probes := []struct {
		name string
		sql  string
	}{
		{"update consumption", "UPDATE app.arena_pass_consumptions SET consumed_at = now()"},
		{"delete consumption", "DELETE FROM app.arena_pass_consumptions"},
		{"delete lot", "DELETE FROM app.arena_pass_lots"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
				_, err := tx.Exec(ctx, probe.sql)
				assertPgCode(t, err, "42501")
			})
		})
	}
}

func TestArenaPassRetentionAndOrdering(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "pass-retention@arena.example.com")

	now := time.Now().UTC()
	mustCreatePassLot(t, ctx, q, acc.ID, "PURCHASE", 3, pgtype.Timestamptz{}, "stripe:evt_ordering_1")
	mustCreatePassLot(t, ctx, q, acc.ID, "MEMBER", 1, pgtype.Timestamptz{Time: now.Add(2 * time.Hour), Valid: true}, "member:2026-09")
	mustCreatePassLot(t, ctx, q, acc.ID, "MEMBER", 1, pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true}, "member:2026-08")

	lots, err := q.ListArenaPassLotsByAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("list lots: %v", err)
	}
	if len(lots) != 3 {
		t.Fatalf("lots = %d, want 3", len(lots))
	}
	if lots[0].Reference != "member:2026-08" || lots[1].Reference != "member:2026-09" {
		t.Fatalf("expiring lots are not ordered by nearest expiration: %q, %q", lots[0].Reference, lots[1].Reference)
	}
	if lots[2].ExpiresAt.Valid || lots[2].Reference != "stripe:evt_ordering_1" {
		t.Fatalf("non-expiring lot must come last: %+v", lots[2])
	}

	// Financial entitlements are retained: deleting the account is refused.
	_, err = db.Pool.Exec(ctx, "DELETE FROM app.accounts WHERE id = $1", acc.ID)
	assertPgCode(t, err, "23001")

	var lotsRemaining int
	if err := db.Pool.QueryRow(ctx,
		"SELECT count(*) FROM app.arena_pass_lots WHERE account_id = $1", acc.ID).Scan(&lotsRemaining); err != nil {
		t.Fatalf("count lots: %v", err)
	}
	if lotsRemaining != 3 {
		t.Fatalf("lots after refused deletion = %d, want 3", lotsRemaining)
	}
}
