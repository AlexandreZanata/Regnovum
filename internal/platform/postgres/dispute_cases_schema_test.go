package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbmigrate"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

// openDisputeSQL opens one migration handle per call: the goose provider
// closes the database it receives, so sharing a handle across UpTo, Up
// and CurrentVersion reads like success and then fails closed.
func openDisputeSQL(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open(dbmigrate.DriverName, dsn)
	if err != nil {
		t.Fatalf("open dispute connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestDisputeCasesSchemaUpgradesAt00062 is the upgrade proof migration
// 00062 owes the diff gate: an empty database advanced through the
// audited UpTo path to version 62 must gain app.dispute_cases with
// its key, envelope and instant columns, read back a sealed envelope,
// refuse what the constraints forbid, and still roll forward to the
// head of the history afterwards.
func TestDisputeCasesSchemaUpgradesAt00062(t *testing.T) {
	db := newTestDB(t, dbtest.WithoutMigrations())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	applied, err := dbmigrate.UpTo(ctx, openDisputeSQL(t, db.DSN), 62, nil)
	if err != nil {
		t.Fatalf("UpTo(62): %v", err)
	}
	if applied < 1 {
		t.Fatalf("UpTo(62) applied %d migrations, want at least the 00062 step itself", applied)
	}
	if version, err := dbmigrate.CurrentVersion(ctx, openDisputeSQL(t, db.DSN)); err != nil || version != 62 {
		t.Fatalf("CurrentVersion = %d, %v; want 62, nil", version, err)
	}

	pool := db.Pool.Pool()

	columns := map[string]string{}
	rows, err := pool.Query(ctx, `
		SELECT column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = 'app' AND table_name = 'dispute_cases'`)
	if err != nil {
		t.Fatalf("describe app.dispute_cases: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		columns[name] = typ
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read columns: %v", err)
	}
	if columns["key"] != "text" || columns["record"] != "jsonb" || !strings.HasPrefix(columns["updated_at"], "timestamp") {
		t.Fatalf("app.dispute_cases columns = %v, want key text, record jsonb and updated_at timestamptz", columns)
	}

	var pk string
	if err := pool.QueryRow(ctx, `
		SELECT kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name
		 AND tc.table_schema = kcu.table_schema
		WHERE tc.table_schema = 'app' AND tc.table_name = 'dispute_cases'
		  AND tc.constraint_type = 'PRIMARY KEY'`).Scan(&pk); err != nil {
		t.Fatalf("read primary key: %v", err)
	}
	if pk != "key" {
		t.Fatalf("primary key column = %q, want %q", pk, "key")
	}

	envelope := `{"version":1,"status":"proposed"}`
	var stored string
	var stamped time.Time
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.dispute_cases (key, record)
		VALUES ('caso-upgrade-62', $1::jsonb)
		RETURNING record::text, updated_at`, envelope).Scan(&stored, &stamped); err != nil {
		t.Fatalf("insert sealed envelope: %v", err)
	}
	// jsonb normalizes spacing and key order, so the round-trip is
	// compared field by field instead of byte by byte.
	var decoded map[string]any
	if err := json.Unmarshal([]byte(stored), &decoded); err != nil {
		t.Fatalf("decode sealed envelope: %v", err)
	}
	if decoded["version"] != float64(1) || decoded["status"] != "proposed" || len(decoded) != 2 {
		t.Fatalf("envelope round-trip = %s, want %s", stored, envelope)
	}
	if stamped.IsZero() {
		t.Fatal("updated_at default did not stamp the row")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.dispute_cases (key, record)
		VALUES ('caso-upgrade-62', '{}'::jsonb)`); err == nil {
		t.Fatal("duplicate negotiation key inserted, want primary key refusal")
	} else {
		assertPgCode(t, err, "23505")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.dispute_cases (key, record)
		VALUES ('', '{}'::jsonb)`); err == nil {
		t.Fatal("empty negotiation key inserted, want check refusal")
	} else {
		assertPgCode(t, err, "23514")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.dispute_cases (key, record)
		VALUES ($1, '{}'::jsonb)`, strings.Repeat("k", 129)); err == nil {
		t.Fatal("129-char negotiation key inserted, want check refusal")
	} else {
		assertPgCode(t, err, "23514")
	}

	versions, err := dbmigrate.Versions()
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	head := versions[len(versions)-1]
	if _, err := dbmigrate.Up(ctx, openDisputeSQL(t, db.DSN), nil); err != nil {
		t.Fatalf("Up past 62 to head: %v", err)
	}
	if version, err := dbmigrate.CurrentVersion(ctx, openDisputeSQL(t, db.DSN)); err != nil || version != head {
		t.Fatalf("CurrentVersion = %d, %v; want head %d, nil", version, err, head)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.dispute_cases WHERE key = 'caso-upgrade-62'`).Scan(&count); err != nil {
		t.Fatalf("reread sealed envelope past head: %v", err)
	}
	if count != 1 {
		t.Fatalf("sealed envelopes surviving past head = %d, want 1", count)
	}
}
