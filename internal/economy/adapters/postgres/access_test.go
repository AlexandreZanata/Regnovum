package postgres_test

// P32-T10 — the arena_app least-privilege matrix over the economy book
// on real PostgreSQL.
//
// Every cell names actor (arena_app), table, operation and the exact
// PostgreSQL outcome: the granted minimum succeeds, everything else
// dies at the grant (42501) or the trigger (23514) level. Each denial
// is logged as the registered evidence of the refused attempt. A
// companion probe runs the same denials as the schema owner to prove
// the triggers bite below the grant level.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func accessCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// assertAccessCode demands one exact SQLSTATE: a denial with another
// code is another defect, and a nil error where a refusal belongs is a
// hole in the matrix.
func assertAccessCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected PostgreSQL error %s, got nil", wantCode)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected *pgconn.PgError, got %T: %v", err, err)
	}
	if pgErr.Code != wantCode {
		t.Fatalf("pgErr.Code = %q, want %q (%v)", pgErr.Code, wantCode, err)
	}
}

// asAppRole runs fn inside a rolled-back transaction acting as the
// application runtime, so the matrix exercises the privileges the
// arena_app login actually holds.
func asAppRole(t *testing.T, pool *pgxpool.Pool, fn func(ctx context.Context, tx pgx.Tx)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin role transaction: %v", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SET LOCAL ROLE arena_app"); err != nil {
		t.Fatalf("set local role arena_app: %v", err)
	}
	fn(ctx, tx)
}

// seedAccessBook creates one custody, one partition, one settled
// transfer pair and one active hold, so granted writes have valid rows
// to touch and denied writes have something to aim at.
func seedAccessBook(t *testing.T, ctx context.Context, db *dbtest.TestDB) (custody, hold, spare string) {
	t.Helper()
	var err error
	if err = db.QueryRow(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('user', 'holder') RETURNING id::text`,
	).Scan(&custody); err != nil {
		t.Fatalf("seed custody: %v", err)
	}
	if _, err = db.Exec(ctx,
		`INSERT INTO app.economy_partitions (custody_id, name) VALUES ($1::uuid, 'available')`, custody); err != nil {
		t.Fatalf("seed partition: %v", err)
	}
	transfer := "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"
	if _, err = db.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'credit', 1000)`, transfer, custody); err != nil {
		t.Fatalf("seed credit leg: %v", err)
	}
	if _, err = db.Exec(ctx,
		`INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli)
		 VALUES ($1::uuid, $2::uuid, 'debit', 1000)`, transfer, custody); err != nil {
		t.Fatalf("seed debit leg: %v", err)
	}
	if err = db.QueryRow(ctx,
		`INSERT INTO app.economy_holds (owner_custody_id, hold_custody_id, amount_milli, purpose, expires_at)
		 VALUES ($1::uuid, $1::uuid, 100, 'access probe', now() + interval '1 hour') RETURNING id::text`,
		custody).Scan(&hold); err != nil {
		t.Fatalf("seed hold: %v", err)
	}
	if _, err = db.Exec(ctx,
		`INSERT INTO app.economy_incidents (reason, detail) VALUES ('access probe', 'seed')`); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
	if err = db.QueryRow(ctx,
		`INSERT INTO app.economy_custodies (kind, label) VALUES ('user', 'spare') RETURNING id::text`).Scan(&spare); err != nil {
		t.Fatalf("seed spare custody: %v", err)
	}
	if _, err = db.Exec(ctx,
		`INSERT INTO app.economy_mode (singleton, frozen) VALUES (true, false) ON CONFLICT (singleton) DO NOTHING`); err != nil {
		t.Fatalf("seed mode: %v", err)
	}
	return custody, hold, spare
}

// roleCell is one actor × table × operation expectation: wantCode empty
// means the operation succeeds, otherwise the exact SQLSTATE refusal.
type roleCell struct {
	table string
	op    string
	sql   string
	args  []any
	want  string
}

func accessMatrix(custody, hold, spare string) []roleCell {
	exec := func(table, op, sql string, args []any, want string) roleCell {
		return roleCell{table: table, op: op, sql: sql, args: args, want: want}
	}
	return []roleCell{
		exec("custodies", "SELECT", `SELECT count(*) FROM app.economy_custodies`, nil, ""),
		exec("custodies", "INSERT", `INSERT INTO app.economy_custodies (kind, label) VALUES ('user', 'role-probe')`, nil, ""),
		exec("custodies", "UPDATE", `UPDATE app.economy_custodies SET label = 'renamed' WHERE id = $1::uuid`, []any{custody}, "42501"),
		exec("custodies", "DELETE", `DELETE FROM app.economy_custodies WHERE id = $1::uuid`, []any{custody}, "42501"),
		exec("partitions", "SELECT", `SELECT count(*) FROM app.economy_partitions`, nil, ""),
		exec("partitions", "INSERT", `INSERT INTO app.economy_partitions (custody_id, name) VALUES ($1::uuid, 'obligations')`, []any{custody}, ""),
		exec("partitions", "UPDATE", `UPDATE app.economy_partitions SET name = 'obligations' WHERE custody_id = $1::uuid`, []any{custody}, "42501"),
		exec("partitions", "DELETE", `DELETE FROM app.economy_partitions WHERE custody_id = $1::uuid`, []any{custody}, "42501"),
		exec("entries", "SELECT", `SELECT count(*) FROM app.economy_entries`, nil, ""),
		exec("entries", "INSERT", `INSERT INTO app.economy_entries (transfer_id, custody_id, direction, amount_milli) VALUES (gen_random_uuid(), $1::uuid, 'credit', 5)`, []any{custody}, ""),
		exec("entries", "UPDATE", `UPDATE app.economy_entries SET amount_milli = 6 WHERE custody_id = $1::uuid`, []any{custody}, "42501"),
		exec("entries", "DELETE", `DELETE FROM app.economy_entries WHERE custody_id = $1::uuid`, []any{custody}, "42501"),
		exec("genesis", "SELECT", `SELECT count(*) FROM app.economy_genesis`, nil, ""),
		exec("genesis", "INSERT", `INSERT INTO app.economy_genesis (genesis_key, treasury_custody_id, amount_milli) VALUES ('role-probe', $1::uuid, 2100000000000)`, []any{custody}, "42501"),
		exec("genesis", "UPDATE", `UPDATE app.economy_genesis SET amount_milli = 1`, nil, "42501"),
		exec("genesis", "DELETE", `DELETE FROM app.economy_genesis`, nil, "42501"),
		exec("intentions", "SELECT", `SELECT count(*) FROM app.economy_intentions`, nil, ""),
		exec("intentions", "INSERT", `INSERT INTO app.economy_intentions (intention_key, actor, operation, payload_hash, transfer_id, amount_milli) VALUES ('role-probe', 'role', 'probe', '0000000000000000000000000000000000000000000000000000000000000000', gen_random_uuid(), 5)`, nil, ""),
		exec("intentions", "UPDATE", `UPDATE app.economy_intentions SET amount_milli = 6`, nil, "42501"),
		exec("intentions", "DELETE", `DELETE FROM app.economy_intentions`, nil, "42501"),
		exec("holds", "SELECT", `SELECT count(*) FROM app.economy_holds`, nil, ""),
		exec("holds", "INSERT", `INSERT INTO app.economy_holds (owner_custody_id, hold_custody_id, amount_milli, purpose, expires_at) VALUES ($1::uuid, $2::uuid, 5, 'role probe', now() + interval '1 hour')`, []any{custody, spare}, ""),
		exec("holds", "UPDATE-legal", `UPDATE app.economy_holds SET status = 'released', closed_at = now() WHERE id = $1::uuid`, []any{hold}, ""),
		exec("holds", "DELETE", `DELETE FROM app.economy_holds WHERE id = $1::uuid`, []any{hold}, "42501"),
		exec("incidents", "SELECT", `SELECT count(*) FROM app.economy_incidents`, nil, ""),
		exec("incidents", "INSERT", `INSERT INTO app.economy_incidents (reason, detail) VALUES ('role probe', 'probe')`, nil, ""),
		exec("incidents", "UPDATE", `UPDATE app.economy_incidents SET detail = 'rewritten'`, nil, "42501"),
		exec("incidents", "DELETE", `DELETE FROM app.economy_incidents`, nil, "42501"),
		exec("mode", "SELECT", `SELECT count(*) FROM app.economy_mode`, nil, ""),
		exec("mode", "INSERT", `INSERT INTO app.economy_mode (singleton, frozen) VALUES (true, false) ON CONFLICT (singleton) DO NOTHING`, nil, ""),
		exec("mode", "UPDATE-unlawful", `UPDATE app.economy_mode SET frozen = true`, nil, "23514"),
		exec("mode", "DELETE", `DELETE FROM app.economy_mode`, nil, "42501"),
	}
}

// TestArenaAppLeastPrivilegeMatrix proves the exact grant surface cell
// by cell as arena_app: the granted minimum lands, every rewrite or
// removal of history dies denied, and each denial is logged as the
// registered attempt.
func TestArenaAppLeastPrivilegeMatrix(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := accessCtx()
	defer cancel()

	custody, hold, spare := seedAccessBook(t, ctx, db)
	asAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		for _, cell := range accessMatrix(custody, hold, spare) {
			if _, err := tx.Exec(ctx, `SAVEPOINT cell`); err != nil {
				t.Fatalf("savepoint: %v", err)
			}
			_, err := tx.Exec(ctx, cell.sql, cell.args...)
			if cell.want == "" {
				if err != nil {
					t.Errorf("arena_app %s %s: %v, want success", cell.table, cell.op, err)
				}
			} else {
				assertAccessCode(t, err, cell.want)
				t.Logf("denied and registered: arena_app %s %s -> %s", cell.table, cell.op, cell.want)
			}
			if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT cell`); err != nil {
				t.Fatalf("rollback to savepoint: %v", err)
			}
		}
	})
}

// TestTriggersBiteBelowGrants proves the triggers refuse history rewrites
// even for the schema owner: UPDATE and DELETE on the append-only tables
// and an unlawful mode flip all die at 23514 where no grant stops them.
func TestTriggersBiteBelowGrants(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := accessCtx()
	defer cancel()

	_, hold, _ := seedAccessBook(t, ctx, db)
	probes := []struct {
		name string
		sql  string
		args []any
	}{
		{"update entry", `UPDATE app.economy_entries SET amount_milli = 7`, nil},
		{"delete entry", `DELETE FROM app.economy_entries`, nil},
		{"update hold amount", `UPDATE app.economy_holds SET amount_milli = 7 WHERE id = $1::uuid`, []any{hold}},
		{"delete hold", `DELETE FROM app.economy_holds WHERE id = $1::uuid`, []any{hold}},
		{"reopen hold", `UPDATE app.economy_holds SET status = 'active', closed_at = NULL WHERE id = $1::uuid`, []any{hold}},
		{"update incident", `UPDATE app.economy_incidents SET detail = 'rewritten'`, nil},
		{"delete incident", `DELETE FROM app.economy_incidents`, nil},
		{"mode flip without incident", `INSERT INTO app.economy_mode (singleton, frozen) VALUES (true, true) ON CONFLICT (singleton) DO UPDATE SET frozen = true`, nil},
	}
	for _, probe := range probes {
		_, err := db.Exec(ctx, probe.sql, probe.args...)
		assertAccessCode(t, err, "23514")
		t.Logf("denied and registered: owner %s -> 23514", probe.name)
	}
}

func fundOwned(t *testing.T, ctx context.Context, repo *postgres.Repository, label string, millis int64) {
	t.Helper()
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		t.Fatalf("NewMilliInk(%d): %v", millis, err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: domain.SeasonKey(domain.CompatSeasonKey),
		FromKind:   domain.CustodyTreasury, FromLabel: "main",
		ToSeason: domain.SeasonKey(domain.CompatSeasonKey),
		ToKind:   domain.CustodyUser, ToLabel: label,
		Amount: amount,
	}); err != nil {
		t.Fatalf("fund %s: %v", label, err)
	}
}

// TestFrozenBookDeniesEveryMutation completes the actor × operation ×
// state matrix for the frozen state: genesis, transfer, fresh
// intentions, reserves, releases, captures and expiries are all refused
// with ErrEconomyFrozen while settled replays, statements, rebuilds and
// the compensated resolution keep working. Each denial is logged as the
// registered attempt.
func TestFrozenBookDeniesEveryMutation(t *testing.T) {
	db := dbtest.New(t)
	ctx, cancel := accessCtx()
	defer cancel()

	repo := postgres.NewRepository(db.Pool.Pool())
	fundTreasury(t, ctx, repo)
	owner := makeAccount(t, ctx, db.Pool.Pool(), "ana@arena.example.com", "active")
	makeOwnedCustody(t, ctx, db.Pool.Pool(), owner, "user", "ana")
	fundOwned(t, ctx, repo, "ana", 1000)

	useCases := application.NewIdempotentTransferUseCase(repo, repo)
	settled, err := useCases.Execute(ctx, application.IdempotentTransferCommand{
		FromSeason: domain.CompatSeasonKey,
		Key:        "frozen-replay", Actor: "ophelia", Operation: "sale",
		FromKind: "treasury", FromLabel: "main",
		ToSeason: domain.CompatSeasonKey,
		ToKind:   "user", ToLabel: "ana", Millis: 100,
	})
	if err != nil {
		t.Fatalf("settle intention: %v", err)
	}
	reserve := application.NewReserveUseCase(repo, fixedHoldClock{now: time.Now().UTC()}, repo)
	held, err := reserve.Execute(ctx, application.ReserveCommand{
		Season:    domain.CompatSeasonKey,
		OwnerKind: "user", OwnerLabel: "ana", Purpose: "frozen probe",
		Millis: 100, ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}

	freezeWithOrphan(t, ctx, db.Pool.Pool(), "ana")
	report, err := repo.Reconcile(ctx, domain.SeasonKey(domain.CompatSeasonKey))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !report.Frozen {
		t.Fatalf("book stayed open without a break to deny against")
	}

	amount, _ := domain.NewMilliInk(10)
	denied := []struct {
		name string
		call func() error
	}{
		{"genesis", func() error {
			_, err := repo.RunGenesis(ctx, application.GenesisRequest{Key: mustTransferKey(t, "genesis-frozen"), Season: domain.SeasonKey(domain.CompatSeasonKey)})
			return err
		}},
		{"transfer", func() error {
			_, err := repo.Transfer(ctx, application.TransferRequest{
				FromSeason: domain.SeasonKey(domain.CompatSeasonKey),
				FromKind:   domain.CustodyTreasury, FromLabel: "main",
				ToSeason: domain.SeasonKey(domain.CompatSeasonKey),
				ToKind:   domain.CustodyUser, ToLabel: "ana", Amount: amount,
			})
			return err
		}},
		{"intention", func() error {
			_, err := useCases.Execute(ctx, application.IdempotentTransferCommand{
				Key: "frozen-fresh", Actor: "ophelia", Operation: "sale",
				FromSeason: domain.CompatSeasonKey,
				FromKind:   "treasury", FromLabel: "main",
				ToSeason: domain.CompatSeasonKey,
				ToKind:   "user", ToLabel: "ana", Millis: 10,
			})
			return err
		}},
		{"reserve", func() error {
			_, err := reserve.Execute(ctx, application.ReserveCommand{
				Season:    domain.CompatSeasonKey,
				OwnerKind: "user", OwnerLabel: "ana", Purpose: "frozen probe",
				Millis: 10, ExpiresAt: time.Now().UTC().Add(time.Hour),
			})
			return err
		}},
		{"release", func() error {
			_, err := repo.Release(ctx, held.HoldID)
			return err
		}},
		{"capture", func() error {
			_, err := repo.Capture(ctx, held.HoldID, "user", "ana")
			return err
		}},
		{"expire", func() error {
			_, err := repo.Expire(ctx, held.HoldID)
			return err
		}},
	}
	for _, probe := range denied {
		if err := probe.call(); !errors.Is(err, domain.ErrEconomyFrozen) {
			t.Errorf("frozen %s = %v, want ErrEconomyFrozen", probe.name, err)
		} else {
			t.Logf("denied and registered: frozen %s -> ErrEconomyFrozen", probe.name)
		}
	}

	replayed, err := useCases.Execute(ctx, application.IdempotentTransferCommand{
		Key: "frozen-replay", Actor: "ophelia", Operation: "sale",
		FromSeason: domain.CompatSeasonKey,
		FromKind:   "treasury", FromLabel: "main",
		ToSeason: domain.CompatSeasonKey,
		ToKind:   "user", ToLabel: "ana", Millis: 100,
	})
	if err != nil || !replayed.Replayed || replayed.TransferID != settled.TransferID {
		t.Fatalf("settled intention did not replay while frozen: %+v, %v", replayed, err)
	}
	if _, err := repo.ReadStatement(ctx, application.StatementRequest{
		Season: domain.SeasonKey(domain.CompatSeasonKey),
		Kind:   domain.CustodyUser, Label: "ana", CallerAccountID: owner, Limit: 10,
	}); err != nil {
		t.Fatalf("statement while frozen: %v (reads must continue serving)", err)
	}
	if _, err := repo.RebuildAll(ctx, domain.SeasonKey(domain.CompatSeasonKey)); err != nil {
		t.Fatalf("rebuild while frozen: %v (reads must continue serving)", err)
	}
	if err := repo.Resolve(ctx, application.ResolveCommand{IncidentID: report.IncidentID, Note: "access probe compensation"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := repo.Transfer(ctx, application.TransferRequest{
		FromSeason: domain.SeasonKey(domain.CompatSeasonKey),
		FromKind:   domain.CustodyTreasury, FromLabel: "main",
		ToSeason: domain.SeasonKey(domain.CompatSeasonKey),
		ToKind:   domain.CustodyUser, ToLabel: "ana", Amount: amount,
	}); err != nil {
		t.Fatalf("transfer after resolution: %v", err)
	}
}
