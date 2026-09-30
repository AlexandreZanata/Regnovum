package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// stubTx satisfies pgx.Tx for the pure context tests without a database.
type stubTx struct {
	pgx.Tx
}

func TestTxContextRoundTrip(t *testing.T) {
	if _, ok := postgres.TxFromContext(context.Background()); ok {
		t.Fatal("a plain context must not carry a transaction")
	}
	//lint:ignore SA1012 o contexto nulo é o caso sob teste: a função tem de responder sem entrar em pânico
	if _, ok := postgres.TxFromContext(nil); ok {
		t.Fatal("a nil context must not carry a transaction")
	}

	stub := &stubTx{}
	ctx := postgres.WithTx(context.Background(), stub)
	carried, ok := postgres.TxFromContext(ctx)
	if !ok || carried != stub {
		t.Fatalf("TxFromContext() = %v/%v, want the stored transaction", carried, ok)
	}

	if _, ok := postgres.TxFromContext(postgres.WithTx(context.Background(), nil)); ok {
		t.Fatal("WithTx(nil) must not store a transaction")
	}
}

func TestTxManagerCommitsAndRollsBack(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	manager := postgres.NewTxManager(pool)
	q := postgres.New(pool)

	first := mustCreateAccount(t, ctx, q, "tx-manager-commit@arena.example.com")
	second := mustCreateAccount(t, ctx, q, "tx-manager-rollback@arena.example.com")

	// Commit: the write survives and the context carried the transaction.
	if err := manager.WithinTransaction(ctx, func(txCtx context.Context) error {
		tx, ok := postgres.TxFromContext(txCtx)
		if !ok {
			t.Error("participants must receive the active transaction in the context")
			return errors.New("no transaction in context")
		}
		_, err := tx.Exec(txCtx, "INSERT INTO app.wallet_accounts (account_id) VALUES ($1)", first.ID)
		return err
	}); err != nil {
		t.Fatalf("WithinTransaction(commit) error = %v", err)
	}
	var wallets int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM app.wallet_accounts").Scan(&wallets); err != nil {
		t.Fatalf("count wallets: %v", err)
	}
	if wallets != 1 {
		t.Fatalf("wallets after commit = %d, want 1", wallets)
	}

	// Rollback: a failure inside fn discards every write of the transaction.
	sentinel := errors.New("publication failed")
	err := manager.WithinTransaction(ctx, func(txCtx context.Context) error {
		tx, ok := postgres.TxFromContext(txCtx)
		if !ok {
			return errors.New("no transaction in context")
		}
		if _, err := tx.Exec(txCtx, "INSERT INTO app.wallet_accounts (account_id) VALUES ($1)", second.ID); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithinTransaction(rollback) error = %v, want the sentinel", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM app.wallet_accounts").Scan(&wallets); err != nil {
		t.Fatalf("count wallets: %v", err)
	}
	if wallets != 1 {
		t.Fatalf("wallets after rollback = %d, want 1 (write discarded)", wallets)
	}
}
