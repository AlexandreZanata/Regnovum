package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func assertPgCode(t *testing.T, err error, wantCode string) {
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

func createWallet(t *testing.T, ctx context.Context, q *postgres.Queries, accountID pgtype.UUID) postgres.AppWalletAccount {
	t.Helper()
	wallet, err := q.CreateWalletAccount(ctx, accountID)
	if err != nil {
		t.Fatalf("create wallet: %v", err)
	}
	return wallet
}

// withAppRole runs fn inside a transaction whose current role is the
// application runtime, so the probes below exercise the same privileges the
// arena_app login actually holds. Everything is rolled back.
func withAppRole(t *testing.T, pool *pgxpool.Pool, fn func(ctx context.Context, tx pgx.Tx)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

// TestWalletAppendOnlyPrivileges proves the ledger is append-only at the
// privilege level (THR-WAL-02): the runtime may read and insert but never
// update or delete operations or transactions, and never delete a wallet.
func TestWalletAppendOnlyPrivileges(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "wallet-privileges@arena.example.com")
	createWallet(t, ctx, q, acc.ID)

	checks := []struct {
		table string
		priv  string
		want  bool
	}{
		{"app.wallet_transactions", "SELECT", true},
		{"app.wallet_transactions", "INSERT", true},
		{"app.wallet_transactions", "UPDATE", false},
		{"app.wallet_transactions", "DELETE", false},
		{"app.wallet_operations", "SELECT", true},
		{"app.wallet_operations", "INSERT", true},
		{"app.wallet_operations", "UPDATE", false},
		{"app.wallet_operations", "DELETE", false},
		{"app.wallet_accounts", "SELECT", true},
		{"app.wallet_accounts", "INSERT", true},
		{"app.wallet_accounts", "UPDATE", true},
		{"app.wallet_accounts", "DELETE", false},
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

	// Runtime inserts work through the real role.
	withAppRole(t, db.Pool.Pool(), func(ctx context.Context, tx pgx.Tx) {
		var operationID pgtype.UUID
		if err := tx.QueryRow(ctx,
			`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference)
			 VALUES ($1, 'credit_free', 'role-insert-1', 'role-probe')
			 RETURNING id`, acc.ID).Scan(&operationID); err != nil {
			t.Fatalf("runtime insert operation: %v", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.wallet_transactions (operation_id, bucket, amount)
			 VALUES ($1, 'FREE_INK', 10)`, operationID); err != nil {
			t.Fatalf("runtime insert transaction: %v", err)
		}
	})

	// Update and delete probes must fail with insufficient_privilege.
	probes := []struct {
		name string
		sql  string
	}{
		{"update transaction", "UPDATE app.wallet_transactions SET amount = 1"},
		{"delete transaction", "DELETE FROM app.wallet_transactions"},
		{"update operation", "UPDATE app.wallet_operations SET reference = 'mutated'"},
		{"delete operation", "DELETE FROM app.wallet_operations"},
		{"delete wallet", "DELETE FROM app.wallet_accounts"},
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

// TestWalletIdempotencyKeyUnique proves the idempotency registry accepts a
// key exactly once, globally.
func TestWalletIdempotencyKeyUnique(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	first := mustCreateAccount(t, ctx, q, "wallet-idem-a@arena.example.com")
	second := mustCreateAccount(t, ctx, q, "wallet-idem-b@arena.example.com")
	createWallet(t, ctx, q, first.ID)
	createWallet(t, ctx, q, second.ID)

	created, err := q.CreateWalletOperation(ctx, postgres.CreateWalletOperationParams{
		AccountID:      first.ID,
		OperationType:  "credit_free",
		IdempotencyKey: "idem-unique-1",
		Reference:      "free:2026-09",
		Reason:         pgtype.Text{},
		ActorAccountID: pgtype.UUID{},
	})
	if err != nil {
		t.Fatalf("first operation: %v", err)
	}

	_, err = q.CreateWalletOperation(ctx, postgres.CreateWalletOperationParams{
		AccountID:      first.ID,
		OperationType:  "credit_free",
		IdempotencyKey: "idem-unique-1",
		Reference:      "free:2026-09-retry",
		Reason:         pgtype.Text{},
		ActorAccountID: pgtype.UUID{},
	})
	assertPgCode(t, err, "23505")

	// The key is globally unique, not scoped per account.
	_, err = q.CreateWalletOperation(ctx, postgres.CreateWalletOperationParams{
		AccountID:      second.ID,
		OperationType:  "credit_free",
		IdempotencyKey: "idem-unique-1",
		Reference:      "free:2026-09",
		Reason:         pgtype.Text{},
		ActorAccountID: pgtype.UUID{},
	})
	assertPgCode(t, err, "23505")

	// Retries resolve the original operation.
	found, err := q.GetWalletOperationByIdempotencyKey(ctx, "idem-unique-1")
	if err != nil {
		t.Fatalf("get operation by key: %v", err)
	}
	if found.ID != created.ID || found.Reference != "free:2026-09" {
		t.Fatalf("found = %+v, want original operation %+v", found, created)
	}
}

// TestWalletOperationTypeAndReferenceConstraints locks the operation
// vocabulary and the non-empty reference/key invariants.
func TestWalletOperationTypeAndReferenceConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "wallet-types@arena.example.com")
	createWallet(t, ctx, q, acc.ID)

	validTypes := []string{
		"credit_free", "credit_member", "credit_purchase", "credit_refund",
		"credit_admin", "debit_argument", "debit_admin", "debit_refund", "expire_free",
	}
	for i, operationType := range validTypes {
		if _, err := q.CreateWalletOperation(ctx, postgres.CreateWalletOperationParams{
			AccountID:      acc.ID,
			OperationType:  operationType,
			IdempotencyKey: "type-" + operationType + "-" + string(rune('a'+i)),
			Reference:      "ref-" + operationType,
			Reason:         pgtype.Text{String: "schema probe justification", Valid: true},
			ActorAccountID: acc.ID,
		}); err != nil {
			t.Fatalf("valid operation type %q rejected: %v", operationType, err)
		}
	}

	_, err := q.CreateWalletOperation(ctx, postgres.CreateWalletOperationParams{
		AccountID:      acc.ID,
		OperationType:  "mint_ink",
		IdempotencyKey: "type-invalid",
		Reference:      "ref-invalid",
		Reason:         pgtype.Text{},
		ActorAccountID: pgtype.UUID{},
	})
	assertPgCode(t, err, "23514")

	_, err = q.CreateWalletOperation(ctx, postgres.CreateWalletOperationParams{
		AccountID:      acc.ID,
		OperationType:  "credit_free",
		IdempotencyKey: "blank-ref",
		Reference:      "   ",
		Reason:         pgtype.Text{},
		ActorAccountID: pgtype.UUID{},
	})
	assertPgCode(t, err, "23514")

	_, err = q.CreateWalletOperation(ctx, postgres.CreateWalletOperationParams{
		AccountID:      acc.ID,
		OperationType:  "credit_free",
		IdempotencyKey: "",
		Reference:      "ref",
		Reason:         pgtype.Text{},
		ActorAccountID: pgtype.UUID{},
	})
	assertPgCode(t, err, "23514")
}

// TestWalletTransactionConstraints covers bucket, amount, uniqueness and the
// foreign keys of the ledger.
func TestWalletTransactionConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "wallet-transactions@arena.example.com")
	createWallet(t, ctx, q, acc.ID)

	operation, err := q.CreateWalletOperation(ctx, postgres.CreateWalletOperationParams{
		AccountID:      acc.ID,
		OperationType:  "debit_argument",
		IdempotencyKey: "tx-constraints-1",
		Reference:      "argument-1",
		Reason:         pgtype.Text{},
		ActorAccountID: pgtype.UUID{},
	})
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}

	// Zero and invalid buckets are rejected.
	_, err = q.CreateWalletTransaction(ctx, postgres.CreateWalletTransactionParams{
		OperationID: operation.ID,
		Bucket:      "FREE_INK",
		Amount:      0,
	})
	assertPgCode(t, err, "23514")

	_, err = q.CreateWalletTransaction(ctx, postgres.CreateWalletTransactionParams{
		OperationID: operation.ID,
		Bucket:      "GOLD_INK",
		Amount:      10,
	})
	assertPgCode(t, err, "23514")

	// Signed amounts are valid in both directions.
	debit, err := q.CreateWalletTransaction(ctx, postgres.CreateWalletTransactionParams{
		OperationID: operation.ID,
		Bucket:      "FREE_INK",
		Amount:      -100,
	})
	if err != nil {
		t.Fatalf("free debit: %v", err)
	}
	if debit.Amount != -100 {
		t.Fatalf("Amount = %d, want -100", debit.Amount)
	}
	if _, err := q.CreateWalletTransaction(ctx, postgres.CreateWalletTransactionParams{
		OperationID: operation.ID,
		Bucket:      "PURCHASED_INK",
		Amount:      50,
	}); err != nil {
		t.Fatalf("purchased credit: %v", err)
	}

	// One operation touches each bucket at most once.
	_, err = q.CreateWalletTransaction(ctx, postgres.CreateWalletTransactionParams{
		OperationID: operation.ID,
		Bucket:      "FREE_INK",
		Amount:      1,
	})
	assertPgCode(t, err, "23505")

	// Foreign keys: orphan transactions, operations and wallets fail.
	orphan := pgtype.UUID{
		Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x70, 0x80, 0x90, 0xa0, 0xb0, 0xc0, 0xd0, 0xe0, 0xf0, 0x01, 0x02, 0x03},
		Valid: true,
	}
	_, err = q.CreateWalletTransaction(ctx, postgres.CreateWalletTransactionParams{
		OperationID: orphan,
		Bucket:      "FREE_INK",
		Amount:      10,
	})
	assertPgCode(t, err, "23503")

	_, err = q.CreateWalletOperation(ctx, postgres.CreateWalletOperationParams{
		AccountID:      orphan,
		OperationType:  "credit_free",
		IdempotencyKey: "tx-orphan-operation",
		Reference:      "ref",
		Reason:         pgtype.Text{},
		ActorAccountID: pgtype.UUID{},
	})
	assertPgCode(t, err, "23503")

	_, err = q.CreateWalletAccount(ctx, orphan)
	assertPgCode(t, err, "23503")
}

// TestWalletBalancesNeverNegative locks the strict balance CHECK used by the
// double-spend guard (THR-WAL-01).
func TestWalletBalancesNeverNegative(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "wallet-negative@arena.example.com")
	wallet := createWallet(t, ctx, q, acc.ID)
	if wallet.BalanceFree != 0 || wallet.BalancePurchased != 0 {
		t.Fatalf("new wallet balances = %d/%d, want 0/0", wallet.BalanceFree, wallet.BalancePurchased)
	}

	negative := mustCreateAccount(t, ctx, q, "wallet-negative-2@arena.example.com")
	_, err := db.Pool.Exec(ctx,
		"INSERT INTO app.wallet_accounts (account_id, balance_free) VALUES ($1, -1)", negative.ID)
	assertPgCode(t, err, "23514")

	_, err = db.Pool.Exec(ctx,
		"UPDATE app.wallet_accounts SET balance_purchased = -1 WHERE account_id = $1", acc.ID)
	assertPgCode(t, err, "23514")
}

// TestWalletQuantitiesAreBigint proves every INK quantity column is a bigint
// (never float, numeric or money) and round-trips the full 64-bit range.
func TestWalletQuantitiesAreBigint(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	rows, err := db.Pool.Query(ctx, `
		SELECT table_name, column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = 'app'
		  AND table_name IN ('wallet_accounts', 'wallet_operations', 'wallet_transactions')
	`)
	if err != nil {
		t.Fatalf("query columns: %v", err)
	}
	defer rows.Close()

	quantityColumns := map[string]bool{
		"wallet_accounts.balance_free":      true,
		"wallet_accounts.balance_purchased": true,
		"wallet_transactions.amount":        true,
	}
	seen := map[string]bool{}
	for rows.Next() {
		var table, column, dataType string
		if err := rows.Scan(&table, &column, &dataType); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		key := table + "." + column
		switch dataType {
		case "double precision", "real", "numeric", "money":
			t.Fatalf("SECURITY VIOLATION: %s is %s; INK quantities must be integer bigint", key, dataType)
		}
		if quantityColumns[key] {
			seen[key] = true
			if dataType != "bigint" {
				t.Errorf("%s data_type = %q, want bigint", key, dataType)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	for column := range quantityColumns {
		if !seen[column] {
			t.Errorf("quantity column %s not found in the catalog", column)
		}
	}

	// Full 64-bit round-trip.
	const maxBigint = int64(9223372036854775807)
	acc := mustCreateAccount(t, ctx, q, "wallet-bigint@arena.example.com")
	createWallet(t, ctx, q, acc.ID)

	if _, err := db.Pool.Exec(ctx,
		"UPDATE app.wallet_accounts SET balance_free = $2 WHERE account_id = $1", acc.ID, maxBigint,
	); err != nil {
		t.Fatalf("store max bigint balance: %v", err)
	}
	wallet, err := q.GetWalletAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	if wallet.BalanceFree != maxBigint {
		t.Fatalf("BalanceFree = %d, want %d", wallet.BalanceFree, maxBigint)
	}

	operation, err := q.CreateWalletOperation(ctx, postgres.CreateWalletOperationParams{
		AccountID:      acc.ID,
		OperationType:  "credit_admin",
		IdempotencyKey: "bigint-max-1",
		Reference:      "bigint-probe",
		Reason:         pgtype.Text{String: "bigint boundary probe", Valid: true},
		ActorAccountID: acc.ID,
	})
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	transaction, err := q.CreateWalletTransaction(ctx, postgres.CreateWalletTransactionParams{
		OperationID: operation.ID,
		Bucket:      "PURCHASED_INK",
		Amount:      maxBigint,
	})
	if err != nil {
		t.Fatalf("store max bigint amount: %v", err)
	}
	if transaction.Amount != maxBigint {
		t.Fatalf("Amount = %d, want %d", transaction.Amount, maxBigint)
	}

	// Beyond bigint is rejected by the type itself.
	_, err = db.Pool.Exec(ctx,
		"UPDATE app.wallet_accounts SET balance_free = 9223372036854775808 WHERE account_id = $1", acc.ID)
	assertPgCode(t, err, "22003")
}

// TestWalletFinancialHistoryIsRetained proves wallet rows do not cascade
// from accounts: an account with ledger history cannot be deleted silently.
func TestWalletFinancialHistoryIsRetained(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "wallet-retention@arena.example.com")
	createWallet(t, ctx, q, acc.ID)

	_, err := db.Pool.Exec(ctx, "DELETE FROM app.accounts WHERE id = $1", acc.ID)
	// ON DELETE RESTRICT raises restrict_violation (23001), distinct from the
	// foreign_key_violation (23503) of the referencing side.
	assertPgCode(t, err, "23001")

	var wallets int
	if err := db.Pool.QueryRow(ctx,
		"SELECT count(*) FROM app.wallet_accounts WHERE account_id = $1", acc.ID).Scan(&wallets); err != nil {
		t.Fatalf("count wallets: %v", err)
	}
	if wallets != 1 {
		t.Fatalf("wallets = %d, want 1 (history retained)", wallets)
	}
}

// TestWalletTransactionsAreListedNewestFirst covers the statement read
// query: transactions resolve through their operation to the account and are
// ordered newest-first.
func TestWalletTransactionsAreListedNewestFirst(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc := mustCreateAccount(t, ctx, q, "wallet-statement@arena.example.com")
	other := mustCreateAccount(t, ctx, q, "wallet-statement-other@arena.example.com")
	createWallet(t, ctx, q, acc.ID)
	createWallet(t, ctx, q, other.ID)

	first, err := q.CreateWalletOperation(ctx, postgres.CreateWalletOperationParams{
		AccountID:      acc.ID,
		OperationType:  "credit_free",
		IdempotencyKey: "statement-1",
		Reference:      "free:2026-09",
		Reason:         pgtype.Text{},
		ActorAccountID: pgtype.UUID{},
	})
	if err != nil {
		t.Fatalf("create first operation: %v", err)
	}
	if _, err := q.CreateWalletTransaction(ctx, postgres.CreateWalletTransactionParams{
		OperationID: first.ID,
		Bucket:      "FREE_INK",
		Amount:      5000,
	}); err != nil {
		t.Fatalf("create first transaction: %v", err)
	}

	second, err := q.CreateWalletOperation(ctx, postgres.CreateWalletOperationParams{
		AccountID:      acc.ID,
		OperationType:  "debit_argument",
		IdempotencyKey: "statement-2",
		Reference:      "argument-42",
		Reason:         pgtype.Text{},
		ActorAccountID: pgtype.UUID{},
	})
	if err != nil {
		t.Fatalf("create second operation: %v", err)
	}
	if _, err := q.CreateWalletTransaction(ctx, postgres.CreateWalletTransactionParams{
		OperationID: second.ID,
		Bucket:      "FREE_INK",
		Amount:      -10,
	}); err != nil {
		t.Fatalf("create second transaction: %v", err)
	}

	otherOperation, err := q.CreateWalletOperation(ctx, postgres.CreateWalletOperationParams{
		AccountID:      other.ID,
		OperationType:  "credit_free",
		IdempotencyKey: "statement-other",
		Reference:      "free:2026-09",
		Reason:         pgtype.Text{},
		ActorAccountID: pgtype.UUID{},
	})
	if err != nil {
		t.Fatalf("create other operation: %v", err)
	}
	if _, err := q.CreateWalletTransaction(ctx, postgres.CreateWalletTransactionParams{
		OperationID: otherOperation.ID,
		Bucket:      "FREE_INK",
		Amount:      5000,
	}); err != nil {
		t.Fatalf("create other transaction: %v", err)
	}

	entries, err := q.ListWalletTransactionsByAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("list transactions: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (only this account)", len(entries))
	}
	if entries[0].Reference != "argument-42" || entries[1].Reference != "free:2026-09" {
		t.Fatalf("order = [%s, %s], want [argument-42, free:2026-09]", entries[0].Reference, entries[1].Reference)
	}
	if entries[0].Amount != -10 || entries[1].Amount != 5000 {
		t.Fatalf("amounts = [%d, %d], want [-10, 5000]", entries[0].Amount, entries[1].Amount)
	}
	if entries[0].OperationType != "debit_argument" {
		t.Errorf("operation type = %q, want debit_argument", entries[0].OperationType)
	}
}
