package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/walletdebit"
	"github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// assertArgumentLedgerInvariants proves the phase exit gate: no argument
// exists without its matching debit and no publication debit exists without
// its argument, and the balance projection always equals the append-only
// ledger sum.
func assertArgumentLedgerInvariants(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID pgtype.UUID) {
	t.Helper()

	var missingDebits int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM app.arguments a
		WHERE NOT EXISTS (
		    SELECT 1 FROM app.wallet_operations wo
		    WHERE wo.account_id = a.author_id
		      AND wo.operation_type = 'debit_argument'
		      AND wo.reference = 'argument:' || a.idempotency_key
		)`).Scan(&missingDebits); err != nil {
		t.Fatalf("check missing debits: %v", err)
	}
	if missingDebits != 0 {
		t.Fatalf("arguments without a matching debit = %d, want 0", missingDebits)
	}

	var orphanDebits int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM app.wallet_operations wo
		WHERE wo.operation_type = 'debit_argument'
		  AND NOT EXISTS (
		      SELECT 1 FROM app.arguments a
		      WHERE a.author_id = wo.account_id
		        AND 'argument:' || a.idempotency_key = wo.reference
		  )`).Scan(&orphanDebits); err != nil {
		t.Fatalf("check orphan debits: %v", err)
	}
	if orphanDebits != 0 {
		t.Fatalf("publication debits without an argument = %d, want 0", orphanDebits)
	}

	var projection, ledger int64
	if err := pool.QueryRow(ctx, `
		SELECT
		    (SELECT balance_free + balance_purchased FROM app.wallet_accounts WHERE account_id = $1),
		    (SELECT COALESCE(sum(wt.amount), 0)
		     FROM app.wallet_transactions wt
		     JOIN app.wallet_operations wo ON wo.id = wt.operation_id
		     WHERE wo.account_id = $1)`, accountID).Scan(&projection, &ledger); err != nil {
		t.Fatalf("read balance and ledger: %v", err)
	}
	if projection != ledger {
		t.Fatalf("balance projection = %d, ledger sum = %d, want equality", projection, ledger)
	}
}

// TestPublishArgumentDoubleSpendIsImpossible drives 30 simultaneous
// publications from one account whose balance only pays for four: exactly
// four succeed, the rest are refused for insufficient INK and the ledger
// invariants hold after the failures.
func TestPublishArgumentDoubleSpendIsImpossible(t *testing.T) {
	ctx := context.Background()
	pool, useCase, author, arena, walletRepo := newPublishHarness(t)
	seedWalletCredit(t, ctx, walletRepo, author, walletdomain.BucketFree, walletdomain.OperationCreditFree, 100, "free:2026-09", "seed:free")

	const workers = 30
	const cost = 23
	const affordable = 4 // 4 * 23 = 92 <= 100 < 115

	var waitGroup sync.WaitGroup
	errs := make(chan error, workers)
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			_, err := useCase.Execute(ctx, publishArgumentCommand(author, arena, fmt.Sprintf("double-spend-%d", index)))
			errs <- err
		}(index)
	}
	waitGroup.Wait()
	close(errs)

	successes := 0
	refusals := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, application.ErrInsufficientInk):
			refusals++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != affordable || refusals != workers-affordable {
		t.Fatalf("successes = %d, refusals = %d, want %d/%d", successes, refusals, affordable, workers-affordable)
	}

	if count := argumentRowCount(t, ctx, pool, author); count != affordable {
		t.Fatalf("arguments = %d, want %d", count, affordable)
	}
	// One seed grant plus the accepted publications.
	if operations := walletOperationCount(t, ctx, pool, author); operations != affordable+1 {
		t.Fatalf("wallet operations = %d, want %d", operations, affordable+1)
	}
	free, purchased := walletBalances(t, ctx, pool, author)
	if free != 100-affordable*cost || purchased != 0 {
		t.Fatalf("balances = %d/%d, want %d/0", free, purchased, 100-affordable*cost)
	}
	if free < 0 {
		t.Fatal("the balance must never go negative")
	}

	assertArgumentLedgerInvariants(t, ctx, pool, author)
}

// TestPublishArgumentCancellationRollsBack covers cancellation during the
// transaction: a cancelled client (timeout or disconnect) must leave no
// debit and no argument behind.
func TestPublishArgumentCancellationRollsBack(t *testing.T) {
	ctx := context.Background()
	pool, useCase, author, arena, walletRepo := newPublishHarness(t)
	seedWalletCredit(t, ctx, walletRepo, author, walletdomain.BucketFree, walletdomain.OperationCreditFree, 1000, "free:2026-09", "seed:free")

	t.Run("pre-cancelled request", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		if _, err := useCase.Execute(cancelled, publishArgumentCommand(author, arena, "cancelled-before")); err == nil {
			t.Fatal("a cancelled request must fail")
		}
		if argumentRowCount(t, ctx, pool, author) != 0 {
			t.Fatal("a cancelled request must not write an argument")
		}
		if operations := walletOperationCount(t, ctx, pool, author); operations != 1 {
			t.Fatalf("wallet operations = %d, want only the seed grant", operations)
		}
		assertArgumentLedgerInvariants(t, ctx, pool, author)
	})

	t.Run("cancelled mid-transaction", func(t *testing.T) {
		arenaID, err := domain.ParseArenaID(uuidText(arena))
		if err != nil {
			t.Fatalf("ParseArenaID: %v", err)
		}
		accountID, err := domain.ParseAccountID(uuidText(author))
		if err != nil {
			t.Fatalf("ParseAccountID: %v", err)
		}
		relation, err := domain.ParseRelation(domain.RelationSupport)
		if err != nil {
			t.Fatalf("ParseRelation: %v", err)
		}
		content, err := domain.ParseContent("Publicação cancelada no meio da transação", text.GraphemeCount)
		if err != nil {
			t.Fatalf("ParseContent: %v", err)
		}
		key, err := domain.ParseIdempotencyKey("cancelled-mid")
		if err != nil {
			t.Fatalf("ParseIdempotencyKey: %v", err)
		}

		argumentsRepo := postgres.NewRepository(pool)
		bridge := walletdebit.New(walletapp.NewDebitInkUseCase(walletRepo, clockseed.NewClock()))
		uow := platformpg.NewTxManager(pool)

		cancelCtx, cancel := context.WithCancel(ctx)
		err = uow.WithinTransaction(cancelCtx, func(txCtx context.Context) error {
			if _, err := bridge.Debit(txCtx, application.InkDebitRequest{
				AccountID:      uuidText(author),
				Amount:         int64(content.GraphemeCost()),
				Reference:      key.WalletReference(),
				IdempotencyKey: key.String(),
			}); err != nil {
				return err
			}
			// The client disconnects (or the request times out) right after
			// the debit: the next statement must fail and roll everything
			// back.
			cancel()
			_, _, err := argumentsRepo.CreateArgument(txCtx, application.CreateArgumentRequest{
				ArenaID:        arenaID,
				AuthorID:       accountID,
				Relation:       relation,
				Content:        content,
				IdempotencyKey: key,
				CreatedAt:      time.Now().UTC(),
			})
			return err
		})
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("transaction error = %v, want a cancellation", err)
		}

		if argumentRowCount(t, ctx, pool, author) != 0 {
			t.Fatal("the cancelled transaction must not leave an argument")
		}
		if operations := walletOperationCount(t, ctx, pool, author); operations != 1 {
			t.Fatalf("wallet operations = %d, want the debit rolled back", operations)
		}
		if free, _ := walletBalances(t, ctx, pool, author); free != 1000 {
			t.Fatalf("balance = %d, want the debit rolled back", free)
		}
		assertArgumentLedgerInvariants(t, ctx, pool, author)
	})
}
