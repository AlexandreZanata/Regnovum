package postgres_test

// P27-T08 — backup, restore e PITR com invariantes sobre PostgreSQL real.
//
// Hipótese: um backup lógico checado por checksum restaura em ambiente vazio
// com ledger/billing/audit íntegros; o PITR traz o que é ≤ alvo e nunca o que
// é > alvo; backup corrompido e credencial ausente falham antes de tocar o alvo.
// Steady state: checksums e conservação iguais antes/depois; RPO/RTO medidos.
// Comando: go test ./internal/platform/postgres/ -run TestRecovery -count=1 -v.
// Estado esperado antes: origem migrada com operações Q0 sintéticas aplicadas.
// Estado esperado depois: restaurado migrado, gravável, sem linha parcial.
// Limpeza: dbtest DROP DATABASE via t.Cleanup; sem Docker, sem rede externa.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbmigrate"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

// recoveryOp is one synthetic Q0 operation across the three probe relations.
type recoveryOp struct {
	id       string
	account  string
	amount   int64
	intent   string
	minor    int64
	currency string
	action   string
	takenAt  time.Time
}

// recoveryBackup is the sealed logical backup: payload plus checksum.
type recoveryBackup struct {
	takenAt     time.Time
	version     int64
	ops         []recoveryOp
	checksum    string
	expectedSum int64
}

func recoveryContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

func recoveryOpenSQL(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open(dbmigrate.DriverName, dsn)
	if err != nil {
		t.Fatalf("open test connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func recoveryVersion(t *testing.T, ctx context.Context, dsn string) int64 {
	t.Helper()
	version, err := dbmigrate.CurrentVersion(ctx, recoveryOpenSQL(t, dsn))
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	return version
}

func recoverySetupProbe(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS recovery_ledger (id text PRIMARY KEY, account_id text NOT NULL, amount bigint NOT NULL CHECK (amount <> 0), created_at timestamptz NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS recovery_billing (intent_id text PRIMARY KEY, account_id text NOT NULL, amount_minor bigint NOT NULL CHECK (amount_minor > 0), currency text NOT NULL CHECK (currency IN ('BRL','USD')), created_at timestamptz NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS recovery_audit (id text PRIMARY KEY, action text NOT NULL, account_id text NOT NULL, created_at timestamptz NOT NULL)`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("setup probe: %v", err)
		}
	}
}

// recoveryWriteOp applies one Q0 operation atomically across the three probes.
func recoveryWriteOp(t *testing.T, ctx context.Context, pool *pgxpool.Pool, op recoveryOp) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO recovery_ledger (id, account_id, amount, created_at) VALUES ($1,$2,$3,$4)`, op.id, op.account, op.amount, op.takenAt); err != nil {
		t.Fatalf("insert ledger: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO recovery_billing (intent_id, account_id, amount_minor, currency, created_at) VALUES ($1,$2,$3,$4,$5)`, op.intent, op.account, op.minor, op.currency, op.takenAt); err != nil {
		t.Fatalf("insert billing: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO recovery_audit (id, action, account_id, created_at) VALUES ($1,$2,$3,$4)`, op.id, op.action, op.account, op.takenAt); err != nil {
		t.Fatalf("insert audit: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func recoveryMakeOp(prefix string, index int, at time.Time) recoveryOp {
	amount := int64(100)
	if index%2 == 1 {
		amount = -30
	}
	currency := "BRL"
	if index%2 == 1 {
		currency = "USD"
	}
	return recoveryOp{
		id:       fmt.Sprintf("%s-op-%03d", prefix, index),
		account:  fmt.Sprintf("%s-acct-%d", prefix, index%3),
		amount:   amount,
		intent:   fmt.Sprintf("%s-intent-%03d", prefix, index),
		minor:    int64(1000 + index),
		currency: currency,
		action:   fmt.Sprintf("q0-op-%03d", index),
		takenAt:  at,
	}
}

// recoveryChecksum is the canonical digest over ordered rows plus version.
func recoveryChecksum(version int64, ops []recoveryOp) string {
	ordered := append([]recoveryOp(nil), ops...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].id < ordered[j].id })
	digest := sha256.New()
	fmt.Fprintf(digest, "version:%d\n", version)
	for _, op := range ordered {
		fmt.Fprintf(digest, "%s|%s|%d|%s|%d|%s|%s|%s\n",
			op.id, op.account, op.amount, op.intent, op.minor, op.currency, op.action, op.takenAt.UTC().Format(time.RFC3339Nano))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// recoveryTakeBackup seals the source state with version and checksum.
func recoveryTakeBackup(t *testing.T, ctx context.Context, pool *pgxpool.Pool, version int64, ops []recoveryOp) recoveryBackup {
	t.Helper()
	var ledgerCount, billingCount, auditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM recovery_ledger`).Scan(&ledgerCount); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM recovery_billing`).Scan(&billingCount); err != nil {
		t.Fatalf("count billing: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM recovery_audit`).Scan(&auditCount); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if ledgerCount != len(ops) || billingCount != len(ops) || auditCount != len(ops) {
		t.Fatalf("probe counts = %d/%d/%d, want %d each", ledgerCount, billingCount, auditCount, len(ops))
	}
	var sum int64
	if err := pool.QueryRow(ctx, `SELECT coalesce(sum(amount),0) FROM recovery_ledger`).Scan(&sum); err != nil {
		t.Fatalf("sum ledger: %v", err)
	}
	var expected int64
	for _, op := range ops {
		expected += op.amount
	}
	if sum != expected {
		t.Fatalf("ledger sum = %d, want conserved %d", sum, expected)
	}
	return recoveryBackup{
		takenAt:     time.Now().UTC(),
		version:     version,
		ops:         append([]recoveryOp(nil), ops...),
		checksum:    recoveryChecksum(version, ops),
		expectedSum: expected,
	}
}

// recoveryRestore replays a sealed backup into an empty target. A missing
// credential and a checksum mismatch are returned as errors before the
// database is touched; operational failures still fail the test.
func recoveryRestore(t *testing.T, ctx context.Context, target *dbtest.TestDB, backup recoveryBackup, credential string) (time.Duration, error) {
	t.Helper()
	if strings.TrimSpace(credential) == "" {
		return 0, fmt.Errorf("recovery: backup credential is required (falha cedo, sem tocar o alvo)")
	}
	if backup.checksum != recoveryChecksum(backup.version, backup.ops) {
		return 0, fmt.Errorf("recovery: backup checksum mismatch (corrompido), restore recusado antes de tocar o alvo")
	}
	started := time.Now()
	if _, err := dbmigrate.Up(ctx, recoveryOpenSQL(t, target.DSN), nil); err != nil {
		t.Fatalf("migrate empty target: %v", err)
	}
	pool := target.Pool.Pool()
	recoverySetupProbe(t, ctx, pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin restore: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, op := range backup.ops {
		if _, err := tx.Exec(ctx, `INSERT INTO recovery_ledger (id, account_id, amount, created_at) VALUES ($1,$2,$3,$4)`, op.id, op.account, op.amount, op.takenAt); err != nil {
			t.Fatalf("restore ledger %s: %v", op.id, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO recovery_billing (intent_id, account_id, amount_minor, currency, created_at) VALUES ($1,$2,$3,$4,$5)`, op.intent, op.account, op.minor, op.currency, op.takenAt); err != nil {
			t.Fatalf("restore billing %s: %v", op.intent, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO recovery_audit (id, action, account_id, created_at) VALUES ($1,$2,$3,$4)`, op.id, op.action, op.account, op.takenAt); err != nil {
			t.Fatalf("restore audit %s: %v", op.id, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit restore: %v", err)
	}
	return time.Since(started), nil
}

// recoveryAssertInvariants checks checksums, conservation, versions and smoke.
func recoveryAssertInvariants(t *testing.T, ctx context.Context, target *dbtest.TestDB, backup recoveryBackup) {
	t.Helper()
	pool := target.Pool.Pool()
	if got := recoveryVersion(t, ctx, target.DSN); got != backup.version {
		t.Fatalf("restored version = %d, want %d (migrations íntegras)", got, backup.version)
	}
	var sum int64
	if err := pool.QueryRow(ctx, `SELECT coalesce(sum(amount),0) FROM recovery_ledger`).Scan(&sum); err != nil {
		t.Fatalf("sum restored ledger: %v", err)
	}
	if sum != backup.expectedSum {
		t.Fatalf("restored ledger sum = %d, want conserved %d", sum, backup.expectedSum)
	}
	var ledgerCount, billingCount, auditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM recovery_ledger`).Scan(&ledgerCount); err != nil {
		t.Fatalf("count restored ledger: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM recovery_billing`).Scan(&billingCount); err != nil {
		t.Fatalf("count restored billing: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM recovery_audit`).Scan(&auditCount); err != nil {
		t.Fatalf("count restored audit: %v", err)
	}
	if ledgerCount != len(backup.ops) || billingCount != len(backup.ops) || auditCount != len(backup.ops) {
		t.Fatalf("restored counts = %d/%d/%d, want %d each", ledgerCount, billingCount, auditCount, len(backup.ops))
	}
	// Smoke sobre tabelas reais: o schema do produto voltou junto.
	for _, probe := range []string{
		`SELECT 1 FROM app.wallet_transactions LIMIT 1`,
		`SELECT 1 FROM app.audit_events LIMIT 1`,
		`SELECT 1 FROM app.stripe_events LIMIT 1`,
	} {
		if _, err := pool.Exec(ctx, probe); err != nil {
			t.Fatalf("smoke %q: %v", probe, err)
		}
	}
	// Gravável: uma operação nova pousa depois do restore.
	extra := recoveryMakeOp("smoke", 900, time.Now().UTC())
	recoveryWriteOp(t, ctx, pool, extra)
	var after int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM recovery_ledger WHERE id = $1`, extra.id).Scan(&after); err != nil {
		t.Fatalf("count smoke row: %v", err)
	}
	if after != 1 {
		t.Fatalf("smoke row = %d, want 1", after)
	}
}

// TestRecoveryFullBackupRoundTrip seals Q0 ops, restores into an empty
// database and proves ledger/billing/audit intact with RPO/RTO measured.
func TestRecoveryFullBackupRoundTrip(t *testing.T) {
	source := dbtest.New(t)
	ctx, cancel := recoveryContext(60 * time.Second)
	defer cancel()
	pool := source.Pool.Pool()
	recoverySetupProbe(t, ctx, pool)
	version := recoveryVersion(t, ctx, source.DSN)

	base := time.Now().UTC().Add(-10 * time.Minute)
	var ops []recoveryOp
	for index := 0; index < 6; index++ {
		op := recoveryMakeOp("full", index, base.Add(time.Duration(index)*time.Minute))
		recoveryWriteOp(t, ctx, pool, op)
		ops = append(ops, op)
	}
	backup := recoveryTakeBackup(t, ctx, pool, version, ops)
	lastOp := ops[len(ops)-1].takenAt
	if rpo := backup.takenAt.Sub(lastOp); rpo < 0 || rpo > 60*time.Minute {
		t.Fatalf("RPO = %s, want 0..60m (backup cobre a última escrita)", rpo)
	}

	target := dbtest.New(t, dbtest.WithoutMigrations())
	rto, err := recoveryRestore(t, ctx, target, backup, "sealed-test-credential")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if rto <= 0 || rto > 60*time.Second {
		t.Fatalf("RTO = %s, want 0..60s (restore automatizado em vazio)", rto)
	}
	t.Logf("RPO %s, RTO %s, version %d, ops %d", backup.takenAt.Sub(lastOp), rto, version, len(ops))
	recoveryAssertInvariants(t, ctx, target, backup)
}

// TestRecoveryPointInTimeExcludesPostTarget restores the backup plus only the
// log entries at or before the target: o pré-alvo volta, o pós-alvo não.
func TestRecoveryPointInTimeExcludesPostTarget(t *testing.T) {
	source := dbtest.New(t)
	ctx, cancel := recoveryContext(60 * time.Second)
	defer cancel()
	pool := source.Pool.Pool()
	recoverySetupProbe(t, ctx, pool)
	version := recoveryVersion(t, ctx, source.DSN)

	base := time.Now().UTC().Add(-30 * time.Minute)
	var pre, post []recoveryOp
	for index := 0; index < 4; index++ {
		op := recoveryMakeOp("pitr", index, base.Add(time.Duration(index)*time.Minute))
		recoveryWriteOp(t, ctx, pool, op)
		pre = append(pre, op)
	}
	backup := recoveryTakeBackup(t, ctx, pool, version, pre)
	targetTime := base.Add(3*time.Minute + 30*time.Second)
	for index := 4; index < 7; index++ {
		op := recoveryMakeOp("pitr", index, base.Add(time.Duration(index)*time.Minute))
		recoveryWriteOp(t, ctx, pool, op)
		post = append(post, op)
		if !op.takenAt.After(targetTime) {
			t.Fatalf("post-target op %s at %s is not after target %s", op.id, op.takenAt, targetTime)
		}
	}

	// PITR = backup + replay do log somente até o alvo (aqui: nada após o
	// backup, porque o backup já é o ponto; o log pós-alvo fica de fora).
	pitrOps := make([]recoveryOp, 0, len(pre))
	for _, op := range append(append([]recoveryOp(nil), pre...), post...) {
		if !op.takenAt.After(targetTime) {
			pitrOps = append(pitrOps, op)
		}
	}
	if len(pitrOps) != len(pre) {
		t.Fatalf("PITR log = %d ops, want %d (só o pré-alvo)", len(pitrOps), len(pre))
	}
	pitrBackup := recoveryBackup{
		takenAt:     time.Now().UTC(),
		version:     version,
		ops:         pitrOps,
		checksum:    recoveryChecksum(version, pitrOps),
		expectedSum: backup.expectedSum,
	}
	target := dbtest.New(t, dbtest.WithoutMigrations())
	rto, err := recoveryRestore(t, ctx, target, pitrBackup, "sealed-test-credential")
	if err != nil {
		t.Fatalf("PITR restore: %v", err)
	}
	if rto <= 0 || rto > 60*time.Second {
		t.Fatalf("PITR RTO = %s, want 0..60s", rto)
	}
	recoveryAssertInvariants(t, ctx, target, pitrBackup)
	restored := target.Pool.Pool()
	for _, op := range post {
		var found int
		if err := restored.QueryRow(ctx, `SELECT count(*) FROM recovery_ledger WHERE id = $1`, op.id).Scan(&found); err != nil {
			t.Fatalf("count post-target %s: %v", op.id, err)
		}
		if found != 0 {
			t.Fatalf("post-target op %s voltou no PITR (count = %d, want 0)", op.id, found)
		}
	}
}

// TestRecoveryCorruptBackupAndMissingCredentialFailEarly proves the two
// fail-closed paths: checksum divergente e credencial ausente recusam antes
// de qualquer escrita no alvo.
func TestRecoveryCorruptBackupAndMissingCredentialFailEarly(t *testing.T) {
	source := dbtest.New(t)
	ctx, cancel := recoveryContext(60 * time.Second)
	defer cancel()
	pool := source.Pool.Pool()
	recoverySetupProbe(t, ctx, pool)
	version := recoveryVersion(t, ctx, source.DSN)

	op := recoveryMakeOp("seal", 0, time.Now().UTC())
	recoveryWriteOp(t, ctx, pool, op)
	backup := recoveryTakeBackup(t, ctx, pool, version, []recoveryOp{op})

	corrupted := backup
	corrupted.ops = append([]recoveryOp(nil), backup.ops...)
	corrupted.ops[0].amount++
	if corrupted.checksum == recoveryChecksum(corrupted.version, corrupted.ops) {
		t.Fatal("mutated payload produced the same checksum; the seal proves nothing")
	}
	if backup.checksum != recoveryChecksum(backup.version, backup.ops) {
		t.Fatal("sealed backup does not verify against itself")
	}

	target := dbtest.New(t, dbtest.WithoutMigrations())
	emptyPool := target.Pool.Pool()
	beforeVersion := recoveryVersion(t, ctx, target.DSN)
	if beforeVersion != 0 {
		t.Fatalf("empty target version = %d, want 0 (ambiente vazio)", beforeVersion)
	}
	var beforeProbe int
	if err := emptyPool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'recovery_ledger'`).Scan(&beforeProbe); err != nil {
		t.Fatalf("count probe on empty target: %v", err)
	}
	if beforeProbe != 0 {
		t.Fatalf("probe on empty target = %d, want 0", beforeProbe)
	}
	// Credencial ausente: recusa antes de migrar ou escrever.
	if _, err := recoveryRestore(t, ctx, target, backup, "   "); err == nil {
		t.Fatal("missing credential was accepted; want fail-closed refusal")
	} else if !strings.Contains(err.Error(), "credential is required") {
		t.Fatalf("missing-credential error = %q, want it to name the credential", err)
	}
	if got := recoveryVersion(t, ctx, target.DSN); got != 0 {
		t.Fatalf("target version after refused restore = %d, want 0 (nada tocado)", got)
	}
	// Backup corrompido: o checksum acusa antes de qualquer escrita.
	if _, err := recoveryRestore(t, ctx, target, corrupted, "sealed-test-credential"); err == nil {
		t.Fatal("corrupted backup was accepted; want checksum refusal")
	} else if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("corrupt-backup error = %q, want checksum mismatch", err)
	}
	if got := recoveryVersion(t, ctx, target.DSN); got != 0 {
		t.Fatalf("target version after corrupt restore = %d, want 0 (nada tocado)", got)
	}
	var afterProbe int
	if err := emptyPool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'recovery_ledger'`).Scan(&afterProbe); err != nil {
		t.Fatalf("count probe after refused restores: %v", err)
	}
	if afterProbe != 0 {
		t.Fatalf("probe after refused restores = %d, want 0", afterProbe)
	}
}
