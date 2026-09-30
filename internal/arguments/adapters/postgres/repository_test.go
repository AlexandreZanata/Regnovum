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
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	walletpg "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// eligibleAccounts is a stub of the account eligibility port.
type eligibleAccounts struct{ err error }

func (e eligibleAccounts) EnsureEligible(context.Context, domain.AccountID) error { return e.err }

// openArenas is a stub of the Arena eligibility port.
type openArenas struct{ err error }

func (o openArenas) EnsureAcceptsArguments(context.Context, domain.ArenaID) error { return o.err }

func mustArgumentAuthor(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) pgtype.UUID {
	t.Helper()
	account, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return account.ID
}

// mustArgumentArena creates one published Arena through raw SQL; the Arena
// lifecycle belongs to the arenas module and is stubbed here.
func mustArgumentArena(t *testing.T, ctx context.Context, pool *pgxpool.Pool, creator pgtype.UUID, slug string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, 'Afirmação para o módulo de argumentos', 'technology', 'pt-BR', 'draft')
		RETURNING id`, creator).Scan(&id); err != nil {
		t.Fatalf("insert arena: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE app.arenas SET status = 'published', slug = $2, published_at = now(), version = version + 1
		WHERE id = $1`, id, slug); err != nil {
		t.Fatalf("publish arena: %v", err)
	}
	return id
}

// seedWalletCredit creates the wallet (when missing) and credits INK through
// the real wallet repository.
func seedWalletCredit(t *testing.T, ctx context.Context, repo *walletpg.Repository, accountID pgtype.UUID, bucket walletdomain.Bucket, operationType walletdomain.OperationType, amount int64, reference, key string) {
	t.Helper()
	ink, err := walletdomain.NewInk(amount)
	if err != nil {
		t.Fatalf("NewInk(%d): %v", amount, err)
	}
	parsedReference, err := walletdomain.ParseReference(reference)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", reference, err)
	}
	idempotencyKey, err := walletdomain.ParseIdempotencyKey(key)
	if err != nil {
		t.Fatalf("ParseIdempotencyKey(%q): %v", key, err)
	}
	direction, err := operationType.Direction()
	if err != nil {
		t.Fatalf("Direction(%q): %v", operationType, err)
	}
	delta, err := direction.Apply(ink)
	if err != nil {
		t.Fatalf("Apply(%d): %v", amount, err)
	}
	if _, err := repo.ApplyCredit(ctx, walletapp.CreditRequest{
		AccountID:      walletdomain.AccountID(uuidText(accountID)),
		Bucket:         bucket,
		OperationType:  operationType,
		IdempotencyKey: idempotencyKey,
		Reference:      parsedReference,
		Delta:          delta,
		ChangedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed credit %s: %v", key, err)
	}
}

func walletBalances(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID pgtype.UUID) (int64, int64) {
	t.Helper()
	var free, purchased int64
	if err := pool.QueryRow(ctx, `
		SELECT balance_free, balance_purchased FROM app.wallet_accounts WHERE account_id = $1`, accountID,
	).Scan(&free, &purchased); err != nil {
		t.Fatalf("read balances: %v", err)
	}
	return free, purchased
}

func argumentRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, authorID pgtype.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.arguments WHERE author_id = $1`, authorID).Scan(&count); err != nil {
		t.Fatalf("count arguments: %v", err)
	}
	return count
}

func walletOperationCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID pgtype.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.wallet_operations WHERE account_id = $1`, accountID).Scan(&count); err != nil {
		t.Fatalf("count wallet operations: %v", err)
	}
	return count
}

func uuidText(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func newPublishHarness(t *testing.T) (*pgxpool.Pool, *application.PublishArgumentUseCase, pgtype.UUID, pgtype.UUID, *walletpg.Repository) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	author := mustArgumentAuthor(t, ctx, q, "arguments-publish@arena.example.com")
	arena := mustArgumentArena(t, ctx, pool, author, "arguments-publish-arena")

	argumentsRepo := postgres.NewRepository(pool)
	walletRepo := walletpg.NewRepository(pool)
	clock := clockseed.NewClock()
	bridge := walletdebit.New(walletapp.NewDebitInkUseCase(walletRepo, clock))

	useCase := application.NewPublishArgumentUseCase(
		argumentsRepo,
		eligibleAccounts{},
		openArenas{},
		bridge,
		platformpg.NewTxManager(pool),
		text.GraphemeCount,
		domain.DefaultReplyPolicy(),
		clock,
	)
	return pool, useCase, author, arena, walletRepo
}

func publishArgumentCommand(authorID, arenaID pgtype.UUID, key string) application.PublishArgumentCommand {
	return application.PublishArgumentCommand{
		AccountID:      uuidText(authorID),
		ArenaID:        uuidText(arenaID),
		Relation:       domain.RelationSupport,
		Content:        "A AGI existirá até 2040",
		IdempotencyKey: key,
	}
}

func TestPublishArgumentEndToEndAtomic(t *testing.T) {
	ctx := context.Background()
	pool, useCase, author, arena, walletRepo := newPublishHarness(t)
	seedWalletCredit(t, ctx, walletRepo, author, walletdomain.BucketFree, walletdomain.OperationCreditFree, 1000, "free:2026-09", "seed:free")

	command := publishArgumentCommand(author, arena, "attempt-1")
	command.Sources = []application.SourceCommand{
		{URL: "https://example.com/estudo", Description: "Estudo revisado"},
		{URL: "http://example.com/dados"},
	}

	result, err := useCase.Execute(ctx, command)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed || result.Argument.Content.GraphemeCost() != 23 || result.Argument.Status != "published" {
		t.Fatalf("result = %+v, want a fresh 23-cluster publication", result)
	}

	var relation, content, status, storedKey string
	var cost int32
	if err := pool.QueryRow(ctx, `
		SELECT relation, content, grapheme_cost, status, idempotency_key FROM app.arguments WHERE id = $1`,
		result.Argument.ID.String()).Scan(&relation, &content, &cost, &status, &storedKey); err != nil {
		t.Fatalf("read argument: %v", err)
	}
	if relation != domain.RelationSupport || content != command.Content || cost != 23 || status != "published" || storedKey != "attempt-1" {
		t.Fatalf("stored argument = %s/%q/%d/%s/%q", relation, content, cost, status, storedKey)
	}
	var sources int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.argument_sources WHERE argument_id = $1`, result.Argument.ID.String()).Scan(&sources); err != nil {
		t.Fatalf("count sources: %v", err)
	}
	if sources != 2 {
		t.Fatalf("sources = %d, want the two declared", sources)
	}

	free, purchased := walletBalances(t, ctx, pool, author)
	if free != 977 || purchased != 0 {
		t.Fatalf("balances = %d/%d, want 977/0 (FREE drained first)", free, purchased)
	}
	var operationType, reference string
	var amount int64
	if err := pool.QueryRow(ctx, `
		SELECT wo.operation_type, wo.reference, wt.amount
		FROM app.wallet_operations wo
		JOIN app.wallet_transactions wt ON wt.operation_id = wo.id
		WHERE wo.account_id = $1 AND wo.operation_type = 'debit_argument'`, author).Scan(&operationType, &reference, &amount); err != nil {
		t.Fatalf("read wallet operation: %v", err)
	}
	if operationType != "debit_argument" || reference != "argument:attempt-1" || amount != -23 {
		t.Fatalf("wallet operation = %s/%q/%d, want debit_argument/argument:attempt-1/-23", operationType, reference, amount)
	}

	// The retry resolves the recorded argument without charging again.
	retry, err := useCase.Execute(ctx, command)
	if err != nil {
		t.Fatalf("retry Execute() error = %v", err)
	}
	if !retry.Replayed || retry.Argument.ID.String() != result.Argument.ID.String() {
		t.Fatal("retry must resolve the recorded argument")
	}
	if argumentRowCount(t, ctx, pool, author) != 1 || walletOperationCount(t, ctx, pool, author) != 2 {
		// Two operations: the free grant and the single debit.
		t.Fatalf("arguments = %d, operations = %d, want 1 and 2", argumentRowCount(t, ctx, pool, author), walletOperationCount(t, ctx, pool, author))
	}
	if free, _ := walletBalances(t, ctx, pool, author); free != 977 {
		t.Fatalf("retry changed the balance: %d", free)
	}
}

func TestPublishArgumentSplitsBuckets(t *testing.T) {
	ctx := context.Background()
	pool, useCase, author, arena, walletRepo := newPublishHarness(t)
	seedWalletCredit(t, ctx, walletRepo, author, walletdomain.BucketFree, walletdomain.OperationCreditFree, 10, "free:2026-09", "seed:free")
	seedWalletCredit(t, ctx, walletRepo, author, walletdomain.BucketPurchased, walletdomain.OperationCreditPurchase, 100, "stripe:evt_split", "seed:purchased")

	if _, err := useCase.Execute(ctx, publishArgumentCommand(author, arena, "attempt-split")); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	free, purchased := walletBalances(t, ctx, pool, author)
	if free != 0 || purchased != 87 {
		t.Fatalf("balances = %d/%d, want 0/87 after draining FREE first", free, purchased)
	}
	var lines int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM app.wallet_transactions wt
		JOIN app.wallet_operations wo ON wo.id = wt.operation_id
		WHERE wo.account_id = $1 AND wo.operation_type = 'debit_argument'`, author).Scan(&lines); err != nil {
		t.Fatalf("count debit lines: %v", err)
	}
	if lines != 2 {
		t.Fatalf("debit lines = %d, want one per consumed bucket", lines)
	}
}

func TestPublishArgumentRejectsInsufficientInk(t *testing.T) {
	ctx := context.Background()
	pool, useCase, author, arena, walletRepo := newPublishHarness(t)
	seedWalletCredit(t, ctx, walletRepo, author, walletdomain.BucketFree, walletdomain.OperationCreditFree, 10, "free:2026-09", "seed:free")

	if _, err := useCase.Execute(ctx, publishArgumentCommand(author, arena, "attempt-poor")); !errors.Is(err, application.ErrInsufficientInk) {
		t.Fatalf("error = %v, want ErrInsufficientInk", err)
	}
	if argumentRowCount(t, ctx, pool, author) != 0 || walletOperationCount(t, ctx, pool, author) != 1 {
		t.Fatal("an insufficient charge must leave no argument and no debit operation")
	}
	if free, _ := walletBalances(t, ctx, pool, author); free != 10 {
		t.Fatalf("balance = %d, want it untouched", free)
	}
}

// TestPublishArgumentFailureAfterDebitRollsBack forces a SQL failure after
// the INK debit (an Arena that does not exist passes the stubbed gate but
// violates the foreign key): the shared transaction rolls the debit and the
// argument back together.
func TestPublishArgumentFailureAfterDebitRollsBack(t *testing.T) {
	ctx := context.Background()
	pool, useCase, author, _, walletRepo := newPublishHarness(t)
	seedWalletCredit(t, ctx, walletRepo, author, walletdomain.BucketFree, walletdomain.OperationCreditFree, 1000, "free:2026-09", "seed:free")

	missingArena := pgtype.UUID{Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7a, 0x7b, 0x7c}, Valid: true}
	if _, err := useCase.Execute(ctx, publishArgumentCommand(author, missingArena, "attempt-rollback")); err == nil {
		t.Fatal("a missing Arena must fail the publication after the debit")
	}

	if argumentRowCount(t, ctx, pool, author) != 0 {
		t.Fatal("the argument must not survive the rollback")
	}
	if walletOperationCount(t, ctx, pool, author) != 1 {
		t.Fatal("the debit must not survive the rollback")
	}
	if free, _ := walletBalances(t, ctx, pool, author); free != 1000 {
		t.Fatalf("balance = %d, want the debit rolled back", free)
	}
}

func TestPublishArgumentRejectsInvalidParents(t *testing.T) {
	ctx := context.Background()
	pool, useCase, author, arena, walletRepo := newPublishHarness(t)
	seedWalletCredit(t, ctx, walletRepo, author, walletdomain.BucketFree, walletdomain.OperationCreditFree, 1000, "free:2026-09", "seed:free")

	otherArena := mustArgumentArena(t, ctx, pool, author, "arguments-other-arena")
	missing := pgtype.UUID{Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x81, 0x82, 0x83, 0x84, 0x85, 0x86, 0x87, 0x88, 0x89, 0x8a, 0x8b, 0x8c}, Valid: true}

	// A published parent in another Arena cannot receive this reply.
	foreign := publishArgumentCommand(author, otherArena, "attempt-foreign-parent")
	if _, err := useCase.Execute(ctx, foreign); err != nil {
		t.Fatalf("publish foreign parent: %v", err)
	}

	tests := []struct {
		name   string
		key    string
		parent pgtype.UUID
		want   error
	}{
		{name: "missing parent", key: "attempt-missing-parent", parent: missing, want: application.ErrParentNotFound},
		{name: "cross arena parent", key: "attempt-cross-arena-parent", parent: pgtype.UUID{}, want: application.ErrParentNotAvailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := test.parent
			if test.name == "cross arena parent" {
				// The parent of the foreign publication lives in otherArena.
				var foreignID pgtype.UUID
				if err := pool.QueryRow(ctx, `
					SELECT id FROM app.arguments WHERE author_id = $1 AND idempotency_key = 'attempt-foreign-parent'`, author).Scan(&foreignID); err != nil {
					t.Fatalf("read foreign argument: %v", err)
				}
				parent = foreignID
			}
			command := publishArgumentCommand(author, arena, test.key)
			command.ParentID = uuidText(parent)
			if _, err := useCase.Execute(ctx, command); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	// A withdrawn parent is not available either.
	var parentID pgtype.UUID
	withdrawnContent := "Argumento retirado do teste"
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost, idempotency_key)
		VALUES ($1, $2, 'support', $3, $4, 26, 'parent-withdrawn')
		RETURNING id`, arena, author, withdrawnContent, domain.HashContent(withdrawnContent).String()).Scan(&parentID); err != nil {
		t.Fatalf("insert withdrawn parent: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.arguments SET status = 'withdrawn' WHERE id = $1`, parentID); err != nil {
		t.Fatalf("withdraw parent: %v", err)
	}
	command := publishArgumentCommand(author, arena, "attempt-withdrawn-parent")
	command.ParentID = uuidText(parentID)
	if _, err := useCase.Execute(ctx, command); !errors.Is(err, application.ErrParentNotAvailable) {
		t.Fatalf("error = %v, want ErrParentNotAvailable", err)
	}

	if argumentRowCount(t, ctx, pool, author) != 2 {
		t.Fatalf("arguments = %d, want only the foreign publication and the withdrawn parent", argumentRowCount(t, ctx, pool, author))
	}
}

func TestPublishArgumentConcurrentDistinctKeys(t *testing.T) {
	ctx := context.Background()
	pool, useCase, author, arena, walletRepo := newPublishHarness(t)
	seedWalletCredit(t, ctx, walletRepo, author, walletdomain.BucketFree, walletdomain.OperationCreditFree, 3000, "free:2026-09", "seed:free")

	const workers = 30
	var waitGroup sync.WaitGroup
	errs := make(chan error, workers)
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			_, err := useCase.Execute(ctx, publishArgumentCommand(author, arena, fmt.Sprintf("concurrent-%d", index)))
			errs <- err
		}(index)
	}
	waitGroup.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent publish error = %v", err)
		}
	}
	if argumentRowCount(t, ctx, pool, author) != workers {
		t.Fatalf("arguments = %d, want %d", argumentRowCount(t, ctx, pool, author), workers)
	}
	if operations := walletOperationCount(t, ctx, pool, author); operations != workers+1 {
		t.Fatalf("wallet operations = %d, want %d debits plus the seed grant", operations, workers)
	}
	free, purchased := walletBalances(t, ctx, pool, author)
	if free != 3000-int64(workers*23) || purchased != 0 {
		t.Fatalf("balances = %d/%d, want %d/0", free, purchased, 3000-workers*23)
	}
}

func TestPublishArgumentConcurrentSameKey(t *testing.T) {
	ctx := context.Background()
	pool, useCase, author, arena, walletRepo := newPublishHarness(t)
	seedWalletCredit(t, ctx, walletRepo, author, walletdomain.BucketFree, walletdomain.OperationCreditFree, 1000, "free:2026-09", "seed:free")

	const workers = 10
	var waitGroup sync.WaitGroup
	results := make(chan application.PublishArgumentResult, workers)
	errs := make(chan error, workers)
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			result, err := useCase.Execute(ctx, publishArgumentCommand(author, arena, "same-key"))
			if err != nil {
				errs <- err
				return
			}
			results <- *result
		}()
	}
	waitGroup.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("same-key publish error = %v", err)
	}

	fresh := 0
	ids := map[string]bool{}
	for result := range results {
		if !result.Replayed {
			fresh++
		}
		ids[result.Argument.ID.String()] = true
	}
	if fresh != 1 || len(ids) != 1 {
		t.Fatalf("fresh = %d, distinct arguments = %d, want exactly one publication", fresh, len(ids))
	}
	if argumentRowCount(t, ctx, pool, author) != 1 {
		t.Fatalf("arguments = %d, want exactly one", argumentRowCount(t, ctx, pool, author))
	}
	if operations := walletOperationCount(t, ctx, pool, author); operations != 2 {
		t.Fatalf("wallet operations = %d, want the seed grant plus one debit", operations)
	}
	if free, _ := walletBalances(t, ctx, pool, author); free != 977 {
		t.Fatalf("balance = %d, want a single charge", free)
	}
}

// TestPublishArgumentBoundedRepliesEndToEnd is the P10-T05 proof on real
// PostgreSQL: another eligible account replies to a top-level argument, the
// derived depth query reports the chain, a reply to a reply exceeds the
// single recursion level and a removed parent accepts nothing.
func TestPublishArgumentBoundedRepliesEndToEnd(t *testing.T) {
	ctx := context.Background()
	pool, useCase, author, arena, walletRepo := newPublishHarness(t)
	seedWalletCredit(t, ctx, walletRepo, author, walletdomain.BucketFree, walletdomain.OperationCreditFree, 1000, "free:2026-09", "seed:free")

	topCommand := publishArgumentCommand(author, arena, "reply-top-1")
	top, err := useCase.Execute(ctx, topCommand)
	if err != nil {
		t.Fatalf("top-level Execute() error = %v", err)
	}
	if !top.Argument.ParentID.IsZero() {
		t.Fatal("a top-level argument must have no parent")
	}

	// Authorization: replying never requires owning the parent.
	replier := mustArgumentAuthor(t, ctx, platformpg.New(pool), "arguments-replier@arena.example.com")
	seedWalletCredit(t, ctx, walletRepo, replier, walletdomain.BucketFree, walletdomain.OperationCreditFree, 1000, "free:2026-09", "seed:replier")

	replyCommand := publishArgumentCommand(replier, arena, "reply-1")
	replyCommand.ParentID = top.Argument.ID.String()
	replyCommand.Content = "Resposta direta ao argumento principal"
	reply, err := useCase.Execute(ctx, replyCommand)
	if err != nil {
		t.Fatalf("reply Execute() error = %v", err)
	}
	if !reply.Argument.ParentID.Equals(top.Argument.ID) || reply.Argument.AuthorID.String() != uuidText(replier) {
		t.Fatalf("reply = %+v, want the other account replying to the top-level argument", reply.Argument)
	}

	// The derived depth query reports the chain without denormalization.
	argumentsRepo := postgres.NewRepository(pool)
	_, topDepth, err := argumentsRepo.GetParent(ctx, top.Argument.ID)
	if err != nil {
		t.Fatalf("GetParent(top) error = %v", err)
	}
	_, replyDepth, err := argumentsRepo.GetParent(ctx, reply.Argument.ID)
	if err != nil {
		t.Fatalf("GetParent(reply) error = %v", err)
	}
	if topDepth != 0 || replyDepth != 1 {
		t.Fatalf("depths = %d/%d, want 0/1", topDepth, replyDepth)
	}

	// A reply to the reply exceeds the single recursion level and charges
	// nothing.
	freeBefore, purchasedBefore := walletBalances(t, ctx, pool, author)
	deepCommand := publishArgumentCommand(author, arena, "reply-deep-1")
	deepCommand.ParentID = reply.Argument.ID.String()
	deepCommand.Content = "Resposta a uma resposta"
	if _, err := useCase.Execute(ctx, deepCommand); !errors.Is(err, application.ErrReplyDepthExceeded) {
		t.Fatalf("deep reply error = %v, want ErrReplyDepthExceeded", err)
	}
	if free, purchased := walletBalances(t, ctx, pool, author); free != freeBefore || purchased != purchasedBefore {
		t.Fatal("a refused deep reply must not charge")
	}

	// A removed parent accepts no replies either.
	removedCommand := publishArgumentCommand(author, arena, "reply-removed-parent")
	removedCommand.Content = "Argumento que será removido pela moderação"
	removed, err := useCase.Execute(ctx, removedCommand)
	if err != nil {
		t.Fatalf("removed parent Execute() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.arguments SET status = 'removed' WHERE id = $1`, removed.Argument.ID.String()); err != nil {
		t.Fatalf("remove argument: %v", err)
	}
	replyToRemoved := publishArgumentCommand(replier, arena, "reply-to-removed")
	replyToRemoved.ParentID = removed.Argument.ID.String()
	if _, err := useCase.Execute(ctx, replyToRemoved); !errors.Is(err, application.ErrParentNotAvailable) {
		t.Fatalf("removed parent error = %v, want ErrParentNotAvailable", err)
	}

	// Only the accepted publications exist: top, reply and the removed
	// parent.
	if count := argumentRowCount(t, ctx, pool, author); count != 2 {
		t.Fatalf("author arguments = %d, want the top-level and the removed parent", count)
	}
	if count := argumentRowCount(t, ctx, pool, replier); count != 1 {
		t.Fatalf("replier arguments = %d, want the single accepted reply", count)
	}
}

func TestWithdrawArgumentEndToEnd(t *testing.T) {
	ctx := context.Background()
	pool, publishUseCase, author, arena, walletRepo := newPublishHarness(t)
	seedWalletCredit(t, ctx, walletRepo, author, walletdomain.BucketFree, walletdomain.OperationCreditFree, 1000, "free:2026-09", "seed:free")

	published, err := publishUseCase.Execute(ctx, publishArgumentCommand(author, arena, "withdraw-1"))
	if err != nil {
		t.Fatalf("publish Execute() error = %v", err)
	}

	withdraw := application.NewWithdrawArgumentUseCase(postgres.NewRepository(pool), clockseed.NewClock())
	command := application.WithdrawArgumentCommand{
		AccountID:  uuidText(author),
		ArgumentID: published.Argument.ID.String(),
	}
	result, err := withdraw.Execute(ctx, command)
	if err != nil {
		t.Fatalf("withdraw Execute() error = %v", err)
	}
	if result.Replayed || result.Argument.Status != "withdrawn" || result.Argument.WithdrawnAt == nil {
		t.Fatalf("result = %+v, want a fresh audited withdrawal", result)
	}

	// The historical content survives and the withdrawal instant is stored.
	var content, status string
	var cost int32
	var withdrawnAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT content, grapheme_cost, status, withdrawn_at FROM app.arguments WHERE id = $1`,
		published.Argument.ID.String()).Scan(&content, &cost, &status, &withdrawnAt); err != nil {
		t.Fatalf("read withdrawn argument: %v", err)
	}
	if content != published.Argument.Content.String() || cost != 23 || status != "withdrawn" || withdrawnAt.IsZero() {
		t.Fatalf("stored withdrawal = %q/%d/%s/%v, want the preserved content and recorded instant", content, cost, status, withdrawnAt)
	}
	recordedInstant := result.Argument.WithdrawnAt.UTC()

	// Repeat resolves the recorded withdrawal with the original instant.
	retry, err := withdraw.Execute(ctx, command)
	if err != nil {
		t.Fatalf("retry Execute() error = %v", err)
	}
	if !retry.Replayed || retry.Argument.WithdrawnAt == nil || !retry.Argument.WithdrawnAt.UTC().Equal(recordedInstant) {
		t.Fatalf("retry = %+v, want the recorded withdrawal replayed", retry)
	}

	// A foreign author never reaches the argument.
	other := mustArgumentAuthor(t, ctx, platformpg.New(pool), "arguments-withdraw-other@arena.example.com")
	foreign := command
	foreign.AccountID = uuidText(other)
	if _, err := withdraw.Execute(ctx, foreign); !errors.Is(err, application.ErrArgumentNotFound) {
		t.Fatalf("foreign withdrawal error = %v, want ErrArgumentNotFound", err)
	}

	// Withdrawal never refunds: the single debit stands.
	if free, _ := walletBalances(t, ctx, pool, author); free != 977 {
		t.Fatalf("balance = %d, want the debit untouched (no automatic refund)", free)
	}

	// Moderation removal is never overridden by the author.
	removed, err := publishUseCase.Execute(ctx, publishArgumentCommand(author, arena, "withdraw-removed"))
	if err != nil {
		t.Fatalf("publish removed candidate: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.arguments SET status = 'removed' WHERE id = $1`, removed.Argument.ID.String()); err != nil {
		t.Fatalf("remove argument: %v", err)
	}
	removeCommand := application.WithdrawArgumentCommand{
		AccountID:  uuidText(author),
		ArgumentID: removed.Argument.ID.String(),
	}
	if _, err := withdraw.Execute(ctx, removeCommand); !errors.Is(err, application.ErrArgumentNotWithdrawable) {
		t.Fatalf("removed withdrawal error = %v, want ErrArgumentNotWithdrawable", err)
	}
}

// TestWithdrawArgumentKeepsRepliesCoherent proves the withdrawal only moves
// the author status: replies and the historical content stay stored, so
// derived metrics and reply chains never diverge from the placeholder.
func TestWithdrawArgumentKeepsRepliesCoherent(t *testing.T) {
	ctx := context.Background()
	pool, publishUseCase, author, arena, walletRepo := newPublishHarness(t)
	seedWalletCredit(t, ctx, walletRepo, author, walletdomain.BucketFree, walletdomain.OperationCreditFree, 1000, "free:2026-09", "seed:free")

	parent, err := publishUseCase.Execute(ctx, publishArgumentCommand(author, arena, "coherent-parent"))
	if err != nil {
		t.Fatalf("publish parent: %v", err)
	}
	replier := mustArgumentAuthor(t, ctx, platformpg.New(pool), "arguments-coherent-replier@arena.example.com")
	seedWalletCredit(t, ctx, walletRepo, replier, walletdomain.BucketFree, walletdomain.OperationCreditFree, 1000, "free:2026-09", "seed:replier")
	replyCommand := publishArgumentCommand(replier, arena, "coherent-reply")
	replyCommand.ParentID = parent.Argument.ID.String()
	replyCommand.Content = "Resposta que permanece coerente"
	reply, err := publishUseCase.Execute(ctx, replyCommand)
	if err != nil {
		t.Fatalf("publish reply: %v", err)
	}

	withdraw := application.NewWithdrawArgumentUseCase(postgres.NewRepository(pool), clockseed.NewClock())
	if _, err := withdraw.Execute(ctx, application.WithdrawArgumentCommand{
		AccountID:  uuidText(author),
		ArgumentID: parent.Argument.ID.String(),
	}); err != nil {
		t.Fatalf("withdraw parent: %v", err)
	}

	var replyStatus, replyContent, replyParent string
	if err := pool.QueryRow(ctx, `
		SELECT status, content, parent_id FROM app.arguments WHERE id = $1`,
		reply.Argument.ID.String()).Scan(&replyStatus, &replyContent, &replyParent); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if replyStatus != "published" || replyContent != replyCommand.Content || replyParent != parent.Argument.ID.String() {
		t.Fatalf("reply = %s/%q/%s, want it untouched by the parent withdrawal", replyStatus, replyContent, replyParent)
	}

	// The withdrawn parent keeps its historical content internally.
	var parentContent, parentStatus string
	if err := pool.QueryRow(ctx, `
		SELECT content, status FROM app.arguments WHERE id = $1`,
		parent.Argument.ID.String()).Scan(&parentContent, &parentStatus); err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if parentStatus != "withdrawn" || parentContent != parent.Argument.Content.String() {
		t.Fatalf("parent = %s/%q, want the preserved content under withdrawn status", parentStatus, parentContent)
	}
}
