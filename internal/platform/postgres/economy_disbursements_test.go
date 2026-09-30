package postgres_test

// P34-T05 — the disbursement audit registry holds its shape on real
// PostgreSQL.
//
// app.treasury_disbursements records one governed act per row with an
// allowlisted purpose, origin vault, beneficiary, two distinct
// governors and its settling transfer: duplicated acts die at UNIQUE,
// invalid purposes, single governors and self-approvals die at CHECKs,
// unknown accounts die at foreign keys, history rewrites die at
// triggers and grants, and the runtime role appends and reads but
// never rewrites. The suite runs on a disposable database and moves
// no money.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func disburseSchemaCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func disburseSchemaParties(t *testing.T, ctx context.Context, db *dbtest.TestDB) (beneficiary, approverOne, approverTwo string) {
	t.Helper()
	for _, slot := range []*string{&beneficiary, &approverOne, &approverTwo} {
		var id string
		if err := db.QueryRow(ctx,
			`INSERT INTO app.accounts (email, status) VALUES ('disburse-schema-' || gen_random_uuid()::text || '@invalid.example', 'active') RETURNING id::text`).Scan(&id); err != nil {
			t.Fatalf("seed party: %v", err)
		}
		*slot = id
	}
	return beneficiary, approverOne, approverTwo
}

// disburseSeed is one audit row as the disbursement port writes it.
type disburseSeed struct {
	key, vault, beneficiary, purpose string
	millis                           int64
	approverOne, approverTwo         string
}

func seedDisbursement(t *testing.T, ctx context.Context, db *dbtest.TestDB, seed disburseSeed) {
	t.Helper()
	var transfer string
	if err := db.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		t.Fatalf("transfer id: %v", err)
	}
	mustExecDisburseSchema(t, ctx, db,
		`INSERT INTO app.treasury_disbursements
		 (disbursement_key, origin_vault, beneficiary_account_id, purpose, amount_milli, approver_one, approver_two, transfer_id)
		 VALUES ($1, $2, $3::uuid, $4, $5, $6::uuid, $7::uuid, $8::uuid)`,
		seed.key, seed.vault, seed.beneficiary, seed.purpose, seed.millis, seed.approverOne, seed.approverTwo, transfer)
}

// TestDisbursementRegistryKeysAndPurposes proves one audit row per act
// with a closed purpose vocabulary: duplicated keys die at 23505 and
// unknown purposes, vaults and amounts die at 23514.
func TestDisbursementRegistryKeysAndPurposes(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := disburseSchemaCtx()
	defer cancel()

	beneficiary, approverOne, approverTwo := disburseSchemaParties(t, ctx, db)
	seedDisbursement(t, ctx, db, disburseSeed{key: "schema-act-1", vault: "operating_cash", beneficiary: beneficiary, purpose: "compensation", millis: 100, approverOne: approverOne, approverTwo: approverTwo})

	var transfer string
	if err := db.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transfer); err != nil {
		t.Fatalf("transfer id: %v", err)
	}
	badRows := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"duplicate key", `INSERT INTO app.treasury_disbursements
		 (disbursement_key, origin_vault, beneficiary_account_id, purpose, amount_milli, approver_one, approver_two, transfer_id)
		 VALUES ('schema-act-1', 'operating_cash', $1::uuid, 'compensation', 100, $2::uuid, $3::uuid, $4::uuid)`,
			[]any{beneficiary, approverOne, approverTwo, transfer}, "23505"},
		{"unknown purpose", `INSERT INTO app.treasury_disbursements
		 (disbursement_key, origin_vault, beneficiary_account_id, purpose, amount_milli, approver_one, approver_two, transfer_id)
		 VALUES ('schema-act-2', 'operating_cash', $1::uuid, 'decree', 100, $2::uuid, $3::uuid, $4::uuid)`,
			[]any{beneficiary, approverOne, approverTwo, transfer}, "23514"},
		{"unknown vault", `INSERT INTO app.treasury_disbursements
		 (disbursement_key, origin_vault, beneficiary_account_id, purpose, amount_milli, approver_one, approver_two, transfer_id)
		 VALUES ('schema-act-3', 'slush', $1::uuid, 'compensation', 100, $2::uuid, $3::uuid, $4::uuid)`,
			[]any{beneficiary, approverOne, approverTwo, transfer}, "23514"},
		{"zero amount", `INSERT INTO app.treasury_disbursements
		 (disbursement_key, origin_vault, beneficiary_account_id, purpose, amount_milli, approver_one, approver_two, transfer_id)
		 VALUES ('schema-act-4', 'operating_cash', $1::uuid, 'compensation', 0, $2::uuid, $3::uuid, $4::uuid)`,
			[]any{beneficiary, approverOne, approverTwo, transfer}, "23514"},
		{"single governor twice", `INSERT INTO app.treasury_disbursements
		 (disbursement_key, origin_vault, beneficiary_account_id, purpose, amount_milli, approver_one, approver_two, transfer_id)
		 VALUES ('schema-act-5', 'operating_cash', $1::uuid, 'compensation', 100, $2::uuid, $2::uuid, $3::uuid)`,
			[]any{beneficiary, approverOne, transfer}, "23514"},
		{"self approval", `INSERT INTO app.treasury_disbursements
		 (disbursement_key, origin_vault, beneficiary_account_id, purpose, amount_milli, approver_one, approver_two, transfer_id)
		 VALUES ('schema-act-6', 'operating_cash', $1::uuid, 'compensation', 100, $1::uuid, $2::uuid, $3::uuid)`,
			[]any{beneficiary, approverTwo, transfer}, "23514"},
		{"unknown beneficiary", `INSERT INTO app.treasury_disbursements
		 (disbursement_key, origin_vault, beneficiary_account_id, purpose, amount_milli, approver_one, approver_two, transfer_id)
		 VALUES ('schema-act-7', 'operating_cash', '00000000-0000-0000-0000-000000000000', 'compensation', 100, $1::uuid, $2::uuid, $3::uuid)`,
			[]any{approverOne, approverTwo, transfer}, "23503"},
	}
	for _, probe := range badRows {
		_, err := db.Exec(ctx, probe.sql, probe.args...)
		assertPgCode(t, err, probe.code)
	}
}

// TestDisbursementRegistryImmutableHistory proves triggers refuse
// UPDATE and DELETE on the audit registry for the owner role itself.
func TestDisbursementRegistryImmutableHistory(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := disburseSchemaCtx()
	defer cancel()

	beneficiary, approverOne, approverTwo := disburseSchemaParties(t, ctx, db)
	seedDisbursement(t, ctx, db, disburseSeed{key: "schema-act-8", vault: "operating_cash", beneficiary: beneficiary, purpose: "compensation", millis: 100, approverOne: approverOne, approverTwo: approverTwo})

	mutations := []struct {
		name string
		sql  string
	}{
		{"rewrite purpose", `UPDATE app.treasury_disbursements SET purpose = 'due_payment' WHERE disbursement_key = 'schema-act-8'`},
		{"delete act", `DELETE FROM app.treasury_disbursements WHERE disbursement_key = 'schema-act-8'`},
	}
	for _, mutation := range mutations {
		_, err := db.Exec(ctx, mutation.sql)
		assertPgCode(t, err, "23514")
	}
}

// TestDisbursementRuntimeLeastPrivilege proves arena_app appends and
// reads but never rewrites: UPDATE and DELETE die at the grant level.
func TestDisbursementRuntimeLeastPrivilege(t *testing.T) {
	db := newTestDB(t)
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		probes := []struct {
			name string
			sql  string
			want string
		}{
			{"update act", `UPDATE app.treasury_disbursements SET purpose = 'due_payment'`, "42501"},
			{"delete act", `DELETE FROM app.treasury_disbursements`, "42501"},
		}
		for _, probe := range probes {
			if _, err := tx.Exec(ctx, `SAVEPOINT cell`); err != nil {
				t.Fatalf("savepoint: %v", err)
			}
			_, err := tx.Exec(ctx, probe.sql)
			assertPgCode(t, err, probe.want)
			if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT cell`); err != nil {
				t.Fatalf("rollback to savepoint: %v", err)
			}
		}
		var acts int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.treasury_disbursements`).Scan(&acts); err != nil {
			t.Fatalf("read acts: %v", err)
		}
	})
}

func mustExecDisburseSchema(t *testing.T, ctx context.Context, db *dbtest.TestDB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q args %+v: %v", sql, args, err)
	}
}
