package postgres_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	walletpg "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func mustWalletAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) platformpg.AppAccount {
	t.Helper()
	acc, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return acc
}

func mustCreditRequest(
	t *testing.T,
	accountID domain.AccountID,
	bucket domain.Bucket,
	operationType domain.OperationType,
	amount int64,
	reference, idempotencyKey string,
) application.CreditRequest {
	t.Helper()
	ink, err := domain.NewInk(amount)
	if err != nil {
		t.Fatalf("NewInk(%d): %v", amount, err)
	}
	ref, err := domain.ParseReference(reference)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", reference, err)
	}
	key, err := domain.ParseIdempotencyKey(idempotencyKey)
	if err != nil {
		t.Fatalf("ParseIdempotencyKey(%q): %v", idempotencyKey, err)
	}
	direction, err := operationType.Direction()
	if err != nil {
		t.Fatalf("Direction(%q): %v", operationType, err)
	}
	delta, err := direction.Apply(ink)
	if err != nil {
		t.Fatalf("Apply(%d): %v", amount, err)
	}
	return application.CreditRequest{
		AccountID:      accountID,
		Bucket:         bucket,
		OperationType:  operationType,
		IdempotencyKey: key,
		Reference:      ref,
		Delta:          delta,
		ChangedAt:      time.Now().UTC(),
	}
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

func TestRepository_ApplyCreditPersistsAtomically(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "credit-atomic@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	result, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:2026-09", "free:2026-09:atomic",
	))
	if err != nil {
		t.Fatalf("ApplyCredit() error = %v", err)
	}
	if result.Replayed {
		t.Fatal("first credit must not be a replay")
	}
	if result.Operation.ID().IsZero() {
		t.Fatal("operation id is empty")
	}
	if result.Operation.Type() != domain.OperationCreditFree || !result.Operation.IsCredit() {
		t.Errorf("operation type = %q", result.Operation.Type())
	}
	if result.Operation.AccountID() != accountID {
		t.Errorf("operation account = %q", result.Operation.AccountID())
	}
	if result.Operation.IdempotencyKey().String() != "free:2026-09:atomic" {
		t.Errorf("operation key = %q", result.Operation.IdempotencyKey())
	}
	if result.Operation.Reference().String() != "free:2026-09" {
		t.Errorf("operation reference = %q", result.Operation.Reference())
	}
	if result.Operation.CreatedAt().IsZero() {
		t.Error("operation createdAt is zero")
	}

	wallet, err := q.GetWalletAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	if wallet.BalanceFree != 5000 || wallet.BalancePurchased != 0 {
		t.Fatalf("balances = %d/%d, want 5000/0", wallet.BalanceFree, wallet.BalancePurchased)
	}

	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", acc.ID); got != 1 {
		t.Fatalf("operations = %d, want 1", got)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_transactions"); got != 1 {
		t.Fatalf("transactions = %d, want 1", got)
	}

	entries, err := q.ListWalletTransactionsByAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("list transactions: %v", err)
	}
	if len(entries) != 1 || entries[0].Amount != 5000 || entries[0].Bucket != "FREE_INK" {
		t.Fatalf("entries = %+v, want one +5000 FREE_INK transaction", entries)
	}
}

func TestRepository_ApplyCreditIsIdempotentSequential(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "credit-sequential@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))
	request := mustCreditRequest(t, accountID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:2026-09", "free:2026-09:sequential")

	first, err := repo.ApplyCredit(ctx, request)
	if err != nil {
		t.Fatalf("first ApplyCredit() error = %v", err)
	}
	if first.Replayed {
		t.Fatal("first attempt must not be a replay")
	}

	second, err := repo.ApplyCredit(ctx, request)
	if err != nil {
		t.Fatalf("second ApplyCredit() error = %v", err)
	}
	if !second.Replayed {
		t.Fatal("second attempt with the same key must be a replay")
	}
	if second.Operation.ID() != first.Operation.ID() {
		t.Fatalf("replay returned operation %q, want original %q", second.Operation.ID(), first.Operation.ID())
	}
	if second.Operation.CreatedAt() != first.Operation.CreatedAt() {
		t.Errorf("replay createdAt = %v, want original %v", second.Operation.CreatedAt(), first.Operation.CreatedAt())
	}

	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", acc.ID); got != 1 {
		t.Fatalf("operations = %d, want 1", got)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_transactions"); got != 1 {
		t.Fatalf("transactions = %d, want 1", got)
	}
	wallet, err := q.GetWalletAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	if wallet.BalanceFree != 5000 {
		t.Fatalf("balance_free = %d, want 5000 (credited exactly once)", wallet.BalanceFree)
	}
}

// TestRepository_ApplyCreditConcurrent runs the P06-T03 acceptance probe:
// twenty identical attempts produce exactly one operation, one transaction
// and one balance change, and every caller resolves the same operation.
func TestRepository_ApplyCreditConcurrent(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "credit-concurrent@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))
	request := mustCreditRequest(t, accountID, domain.BucketPurchased, domain.OperationCreditPurchase, 10000, "stripe:evt_concurrent", "purchase:concurrent")

	const attempts = 20
	var (
		successes atomic.Int32
		replays   atomic.Int32
		wg        sync.WaitGroup
	)
	operationIDs := make([]string, attempts)
	errs := make([]error, attempts)

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := repo.ApplyCredit(ctx, request)
			if err != nil {
				errs[index] = err
				return
			}
			successes.Add(1)
			if result.Replayed {
				replays.Add(1)
			}
			operationIDs[index] = result.Operation.ID().String()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("attempt %d error = %v", i, err)
		}
	}
	if successes.Load() != attempts {
		t.Fatalf("successes = %d, want %d", successes.Load(), attempts)
	}
	if replays.Load() != attempts-1 {
		t.Fatalf("replays = %d, want %d", replays.Load(), attempts-1)
	}
	for i, operationID := range operationIDs {
		if operationID != operationIDs[0] {
			t.Fatalf("attempt %d resolved operation %q, want %q", i, operationID, operationIDs[0])
		}
	}

	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", acc.ID); got != 1 {
		t.Fatalf("operations = %d, want exactly 1", got)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_transactions"); got != 1 {
		t.Fatalf("transactions = %d, want exactly 1", got)
	}
	wallet, err := q.GetWalletAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	if wallet.BalancePurchased != 10000 || wallet.BalanceFree != 0 {
		t.Fatalf("balances = %d/%d, want 0/10000 (credited exactly once)", wallet.BalanceFree, wallet.BalancePurchased)
	}
}

func TestRepository_ApplyCreditAccumulatesByBucket(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "credit-buckets@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	operator := mustWalletAccount(t, ctx, q, "credit-buckets-operator@arena.example.com")
	adminReason, adminActor := mustAdminAudit(t, domain.AccountID(uuidString(operator.ID)), "manual grant approved in ticket 9")

	adminCredit := mustCreditRequest(t, accountID, domain.BucketFree, domain.OperationCreditAdmin, 100, "admin:ticket-9", "bucket-admin")
	adminCredit.Reason = adminReason
	adminCredit.ActorAccountID = adminActor

	credits := []application.CreditRequest{
		mustCreditRequest(t, accountID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:2026-09", "bucket-free"),
		mustCreditRequest(t, accountID, domain.BucketPurchased, domain.OperationCreditPurchase, 10000, "stripe:evt_purchase", "bucket-purchase"),
		mustCreditRequest(t, accountID, domain.BucketFree, domain.OperationCreditMember, 30000, "member:2026-09", "bucket-member"),
		mustCreditRequest(t, accountID, domain.BucketPurchased, domain.OperationCreditRefund, 2000, "moderation:case-7", "bucket-refund"),
		adminCredit,
	}
	for _, request := range credits {
		if _, err := repo.ApplyCredit(ctx, request); err != nil {
			t.Fatalf("ApplyCredit(%s) error = %v", request.OperationType, err)
		}
	}

	wallet, err := q.GetWalletAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	if wallet.BalanceFree != 35100 || wallet.BalancePurchased != 12000 {
		t.Fatalf("balances = %d/%d, want 35100/12000", wallet.BalanceFree, wallet.BalancePurchased)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", acc.ID); got != len(credits) {
		t.Fatalf("operations = %d, want %d", got, len(credits))
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_transactions"); got != len(credits) {
		t.Fatalf("transactions = %d, want %d", got, len(credits))
	}
}

func TestRepository_ApplyCreditRefusesCrossAccountReplay(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	owner := mustWalletAccount(t, ctx, q, "credit-owner@arena.example.com")
	intruder := mustWalletAccount(t, ctx, q, "credit-intruder@arena.example.com")
	ownerID := domain.AccountID(uuidString(owner.ID))
	intruderID := domain.AccountID(uuidString(intruder.ID))

	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, ownerID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:2026-09", "shared-key",
	)); err != nil {
		t.Fatalf("owner credit error = %v", err)
	}

	_, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, intruderID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:2026-09", "shared-key",
	))
	if !errors.Is(err, application.ErrIdempotencyMismatch) {
		t.Fatalf("cross-account replay error = %v, want ErrIdempotencyMismatch", err)
	}

	// The intruder's wallet row was rolled back with the refused attempt.
	if _, err := q.GetWalletAccount(ctx, intruder.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("intruder wallet error = %v, want ErrNoRows (no partial state)", err)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", owner.ID); got != 1 {
		t.Fatalf("owner operations = %d, want 1", got)
	}
	ownerWallet, err := q.GetWalletAccount(ctx, owner.ID)
	if err != nil {
		t.Fatalf("get owner wallet: %v", err)
	}
	if ownerWallet.BalanceFree != 5000 {
		t.Fatalf("owner balance_free = %d, want 5000", ownerWallet.BalanceFree)
	}
}

// TestRepository_ApplyCreditRollsBackOnBalanceFailure proves the operation,
// transaction and balance change are one atomic unit: a balance overflow
// leaves no partial ledger rows.
func TestRepository_ApplyCreditRollsBackOnBalanceFailure(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "credit-overflow@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	// Seed the wallet and push the free balance to the 64-bit ceiling.
	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketFree, domain.OperationCreditFree, 1, "free:seed", "overflow-seed",
	)); err != nil {
		t.Fatalf("seed credit error = %v", err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE app.wallet_accounts SET balance_free = 9223372036854775807 WHERE account_id = $1", acc.ID,
	); err != nil {
		t.Fatalf("seed max balance: %v", err)
	}
	baseline := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", acc.ID)

	_, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketFree, domain.OperationCreditFree, 1, "free:overflow", "overflow-credit",
	))
	if err == nil {
		t.Fatal("credit beyond the bigint ceiling must fail")
	}

	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", acc.ID); got != baseline {
		t.Fatalf("operations after failure = %d, want %d (no partial state)", got, baseline)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_transactions"); got != baseline {
		t.Fatalf("transactions after failure = %d, want %d (no partial state)", got, baseline)
	}
	wallet, err := q.GetWalletAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	if wallet.BalanceFree != 9223372036854775807 {
		t.Fatalf("balance_free = %d, want the untouched ceiling", wallet.BalanceFree)
	}
}

func mustAdminAudit(t *testing.T, actor domain.AccountID, rawReason string) (domain.Reason, domain.AccountID) {
	t.Helper()
	reason, err := domain.ParseReason(rawReason)
	if err != nil {
		t.Fatalf("ParseReason(%q): %v", rawReason, err)
	}
	return reason, actor
}

func mustDebitRequest(
	t *testing.T,
	accountID domain.AccountID,
	operationType domain.OperationType,
	amount int64,
	reference, idempotencyKey string,
) application.DebitRequest {
	t.Helper()
	ink, err := domain.NewInk(amount)
	if err != nil {
		t.Fatalf("NewInk(%d): %v", amount, err)
	}
	ref, err := domain.ParseReference(reference)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", reference, err)
	}
	key, err := domain.ParseIdempotencyKey(idempotencyKey)
	if err != nil {
		t.Fatalf("ParseIdempotencyKey(%q): %v", idempotencyKey, err)
	}
	return application.DebitRequest{
		AccountID:      accountID,
		OperationType:  operationType,
		IdempotencyKey: key,
		Reference:      ref,
		Amount:         ink,
		ChangedAt:      time.Now().UTC(),
	}
}

func mustUUID(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		t.Fatalf("scan uuid %q: %v", raw, err)
	}
	return id
}

func assertAllocation(t *testing.T, allocation domain.Allocation, wantFree, wantPurchased int64) {
	t.Helper()
	if allocation.FromFree().Int64() != wantFree || allocation.FromPurchased().Int64() != wantPurchased {
		t.Fatalf("allocation = %d/%d, want %d/%d",
			allocation.FromFree().Int64(), allocation.FromPurchased().Int64(), wantFree, wantPurchased)
	}
}

func TestRepository_ApplyDebitFreeOnly(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "debit-free@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:2026-09", "debit-free:credit",
	)); err != nil {
		t.Fatalf("seed credit error = %v", err)
	}

	result, err := repo.ApplyDebit(ctx, mustDebitRequest(
		t, accountID, domain.OperationDebitArgument, 3000, "argument:1", "debit-free:debit",
	))
	if err != nil {
		t.Fatalf("ApplyDebit() error = %v", err)
	}
	if result.Replayed {
		t.Fatal("fresh debit must not be a replay")
	}
	assertAllocation(t, result.Allocation, 3000, 0)

	wallet, err := q.GetWalletAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	if wallet.BalanceFree != 2000 || wallet.BalancePurchased != 0 {
		t.Fatalf("balances = %d/%d, want 2000/0", wallet.BalanceFree, wallet.BalancePurchased)
	}

	entries, err := q.ListWalletTransactionsByAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("list transactions: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("transactions = %d, want 2 (credit + debit)", len(entries))
	}
	if entries[0].Amount != -3000 || entries[0].Bucket != "FREE_INK" || entries[0].OperationType != "debit_argument" {
		t.Fatalf("newest entry = %+v, want a -3000 FREE_INK debit", entries[0])
	}
	if entries[1].Amount != 5000 {
		t.Fatalf("oldest entry = %+v, want the +5000 credit", entries[1])
	}
}

func TestRepository_ApplyDebitSplitsAcrossBuckets(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "debit-split@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:2026-09", "debit-split:free",
	)); err != nil {
		t.Fatalf("free credit error = %v", err)
	}
	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketPurchased, domain.OperationCreditPurchase, 10000, "stripe:evt_split", "debit-split:purchased",
	)); err != nil {
		t.Fatalf("purchased credit error = %v", err)
	}

	result, err := repo.ApplyDebit(ctx, mustDebitRequest(
		t, accountID, domain.OperationDebitArgument, 12000, "argument:2", "debit-split:debit",
	))
	if err != nil {
		t.Fatalf("ApplyDebit() error = %v", err)
	}
	assertAllocation(t, result.Allocation, 5000, 7000)

	wallet, err := q.GetWalletAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	if wallet.BalanceFree != 0 || wallet.BalancePurchased != 3000 {
		t.Fatalf("balances = %d/%d, want 0/3000", wallet.BalanceFree, wallet.BalancePurchased)
	}

	lines, err := q.ListWalletTransactionsByOperationID(ctx, mustUUID(t, result.Operation.ID().String()))
	if err != nil {
		t.Fatalf("list debit lines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("debit lines = %d, want 2 (split)", len(lines))
	}
	if lines[0].Bucket != "FREE_INK" || lines[0].Amount != -5000 {
		t.Errorf("line 0 = %s/%d, want FREE_INK/-5000", lines[0].Bucket, lines[0].Amount)
	}
	if lines[1].Bucket != "PURCHASED_INK" || lines[1].Amount != -7000 {
		t.Errorf("line 1 = %s/%d, want PURCHASED_INK/-7000", lines[1].Bucket, lines[1].Amount)
	}
}

func TestRepository_ApplyDebitPurchasedOnly(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "debit-purchased@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketPurchased, domain.OperationCreditPurchase, 10000, "stripe:evt_only", "debit-purchased:credit",
	)); err != nil {
		t.Fatalf("credit error = %v", err)
	}

	operator := mustWalletAccount(t, ctx, q, "debit-purchased-operator@arena.example.com")
	adminReason, adminActor := mustAdminAudit(t, domain.AccountID(uuidString(operator.ID)), "manual adjustment of ticket 1")

	adminDebit := mustDebitRequest(t, accountID, domain.OperationDebitAdmin, 1000, "admin:ticket-1", "debit-purchased:debit")
	adminDebit.Reason = adminReason
	adminDebit.ActorAccountID = adminActor

	result, err := repo.ApplyDebit(ctx, adminDebit)
	if err != nil {
		t.Fatalf("ApplyDebit() error = %v", err)
	}
	assertAllocation(t, result.Allocation, 0, 1000)

	wallet, err := q.GetWalletAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	if wallet.BalanceFree != 0 || wallet.BalancePurchased != 9000 {
		t.Fatalf("balances = %d/%d, want 0/9000", wallet.BalanceFree, wallet.BalancePurchased)
	}
}

func TestRepository_ApplyDebitInsufficientLeavesNoPartialState(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "debit-insufficient@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketFree, domain.OperationCreditFree, 100, "free:seed", "insufficient:seed",
	)); err != nil {
		t.Fatalf("seed credit error = %v", err)
	}
	baselineOperations := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", acc.ID)
	baselineTransactions := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_transactions")

	request := mustDebitRequest(t, accountID, domain.OperationDebitArgument, 101, "argument:3", "insufficient:debit")
	if _, err := repo.ApplyDebit(ctx, request); !errors.Is(err, domain.ErrInsufficientInk) {
		t.Fatalf("insufficient debit error = %v, want ErrInsufficientInk", err)
	}

	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", acc.ID); got != baselineOperations {
		t.Fatalf("operations after failure = %d, want %d", got, baselineOperations)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_transactions"); got != baselineTransactions {
		t.Fatalf("transactions after failure = %d, want %d", got, baselineTransactions)
	}
	wallet, err := q.GetWalletAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	if wallet.BalanceFree != 100 {
		t.Fatalf("balance_free = %d, want 100 untouched", wallet.BalanceFree)
	}

	// The failed attempt did not burn the idempotency key: after funding,
	// the same request succeeds with the same key.
	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketFree, domain.OperationCreditFree, 1000, "free:refill", "insufficient:refill",
	)); err != nil {
		t.Fatalf("refill credit error = %v", err)
	}
	result, err := repo.ApplyDebit(ctx, request)
	if err != nil {
		t.Fatalf("retry after funding error = %v", err)
	}
	if result.Replayed || result.Operation.Reference().String() != "argument:3" {
		t.Fatalf("retry result = %+v, want a fresh debit of argument:3", result)
	}
}

func TestRepository_ApplyDebitWithoutWalletIsInsufficient(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "debit-no-wallet@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	if _, err := repo.ApplyDebit(ctx, mustDebitRequest(
		t, accountID, domain.OperationDebitArgument, 1, "argument:4", "no-wallet:debit",
	)); !errors.Is(err, domain.ErrInsufficientInk) {
		t.Fatalf("debit without wallet error = %v, want ErrInsufficientInk", err)
	}

	if _, err := q.GetWalletAccount(ctx, acc.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("wallet error = %v, want ErrNoRows (no wallet created)", err)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", acc.ID); got != 0 {
		t.Fatalf("operations = %d, want 0", got)
	}
}

func TestRepository_ApplyDebitIsIdempotent(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "debit-idempotent@arena.example.com")
	other := mustWalletAccount(t, ctx, q, "debit-idempotent-other@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))
	otherID := domain.AccountID(uuidString(other.ID))

	for _, credit := range []application.CreditRequest{
		mustCreditRequest(t, accountID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:2026-09", "debit-idempotent:free"),
		mustCreditRequest(t, accountID, domain.BucketPurchased, domain.OperationCreditPurchase, 10000, "stripe:evt_idem", "debit-idempotent:purchased"),
	} {
		if _, err := repo.ApplyCredit(ctx, credit); err != nil {
			t.Fatalf("seed credit error = %v", err)
		}
	}

	request := mustDebitRequest(t, accountID, domain.OperationDebitArgument, 12000, "argument:5", "debit-idempotent:debit")
	first, err := repo.ApplyDebit(ctx, request)
	if err != nil {
		t.Fatalf("first ApplyDebit() error = %v", err)
	}
	assertAllocation(t, first.Allocation, 5000, 7000)

	second, err := repo.ApplyDebit(ctx, request)
	if err != nil {
		t.Fatalf("second ApplyDebit() error = %v", err)
	}
	if !second.Replayed {
		t.Fatal("second attempt with the same key must be a replay")
	}
	if second.Operation.ID() != first.Operation.ID() {
		t.Fatalf("replay operation = %q, want %q", second.Operation.ID(), first.Operation.ID())
	}
	if !second.Allocation.Equals(first.Allocation) {
		t.Fatalf("replay allocation = %d/%d, want %d/%d",
			second.Allocation.FromFree().Int64(), second.Allocation.FromPurchased().Int64(),
			first.Allocation.FromFree().Int64(), first.Allocation.FromPurchased().Int64())
	}

	// The key is global: another account cannot replay it.
	if _, err := repo.ApplyDebit(ctx, mustDebitRequest(
		t, otherID, domain.OperationDebitArgument, 12000, "argument:5", "debit-idempotent:debit",
	)); !errors.Is(err, application.ErrIdempotencyMismatch) {
		t.Fatalf("cross-account replay error = %v, want ErrIdempotencyMismatch", err)
	}

	wallet, err := q.GetWalletAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	if wallet.BalanceFree != 0 || wallet.BalancePurchased != 3000 {
		t.Fatalf("balances = %d/%d, want 0/3000 (debited exactly once)", wallet.BalanceFree, wallet.BalancePurchased)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", acc.ID); got != 3 {
		t.Fatalf("operations = %d, want 3 (2 credits + 1 debit)", got)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_transactions"); got != 4 {
		t.Fatalf("transactions = %d, want 4 (2 credits + 1 split debit)", got)
	}
}

// TestRepository_ApplyDebitConcurrentNoDoubleSpend is the THR-WAL-01 probe:
// fifty concurrent debits compete for a balance that funds exactly one
// operation. Exactly one succeeds, forty-nine fail with insufficient ink,
// and the balance never goes negative.
func TestRepository_ApplyDebitConcurrentNoDoubleSpend(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t, dbtest.WithPoolLimits(50, 1))
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "debit-race@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketFree, domain.OperationCreditFree, 10000, "free:2026-09", "debit-race:seed",
	)); err != nil {
		t.Fatalf("seed credit error = %v", err)
	}

	// Build every request before spawning goroutines: testing.T must not
	// fail from a non-test goroutine.
	const attempts = 50
	requests := make([]application.DebitRequest, attempts)
	for i := range requests {
		requests[i] = mustDebitRequest(
			t, accountID, domain.OperationDebitArgument, 10000,
			fmt.Sprintf("argument:%d", i), fmt.Sprintf("debit-race:%d", i),
		)
	}

	var (
		successes    atomic.Int32
		insufficient atomic.Int32
		wg           sync.WaitGroup
	)
	unexpected := make([]error, attempts)
	results := make([]*application.DebitResult, attempts)

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := repo.ApplyDebit(ctx, requests[index])
			if err != nil {
				if errors.Is(err, domain.ErrInsufficientInk) {
					insufficient.Add(1)
				} else {
					unexpected[index] = err
				}
				return
			}
			successes.Add(1)
			results[index] = result
		}(i)
	}
	wg.Wait()

	for i, err := range unexpected {
		if err != nil {
			t.Fatalf("attempt %d unexpected error = %v", i, err)
		}
	}
	if successes.Load() != 1 {
		t.Fatalf("successes = %d, want exactly 1", successes.Load())
	}
	if insufficient.Load() != attempts-1 {
		t.Fatalf("insufficient = %d, want %d", insufficient.Load(), attempts-1)
	}

	wallet, err := q.GetWalletAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	if wallet.BalanceFree != 0 || wallet.BalancePurchased != 0 {
		t.Fatalf("balances = %d/%d, want 0/0 (no double spend, never negative)", wallet.BalanceFree, wallet.BalancePurchased)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", acc.ID); got != 2 {
		t.Fatalf("operations = %d, want 2 (1 credit + 1 debit)", got)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_transactions"); got != 2 {
		t.Fatalf("transactions = %d, want 2 (1 credit + 1 debit)", got)
	}

	var winner *application.DebitResult
	for _, result := range results {
		if result != nil {
			winner = result
		}
	}
	if winner == nil {
		t.Fatal("no successful debit produced an operation")
	}
	assertAllocation(t, winner.Allocation, 10000, 0)
}

// TestRepository_DerivedBalanceMatchesProjectionAfterRandomSequence drives a
// deterministic pseudo-random mix of credits and debits and proves the
// ledger-derived balance equals both the SQL sum and the cached projection.
func TestRepository_DerivedBalanceMatchesProjectionAfterRandomSequence(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "balance-derived@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	// The stream comes from the registered source and not from the global generator: the
	// seed is printed on every run and replayed with `ARENA_TEST_SEED`, which is what
	// makes a failure of this sequence reproducible at all.
	rng := testsource.NewRandom(testsource.SeedFor(t))
	var expectedFree, expectedPurchased int64
	operations := 0

	for i := 0; i < 60; i++ {
		key := fmt.Sprintf("random-sequence:%d", i)
		switch rng.Int64n(3) {
		case 0:
			amount := rng.Int64n(5000) + 1
			if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
				t, accountID, domain.BucketFree, domain.OperationCreditFree, amount, fmt.Sprintf("free:%d", i), key,
			)); err != nil {
				t.Fatalf("free credit %d error = %v", i, err)
			}
			expectedFree += amount
		case 1:
			amount := rng.Int64n(5000) + 1
			if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
				t, accountID, domain.BucketPurchased, domain.OperationCreditPurchase, amount, fmt.Sprintf("stripe:evt_%d", i), key,
			)); err != nil {
				t.Fatalf("purchased credit %d error = %v", i, err)
			}
			expectedPurchased += amount
		case 2:
			total := expectedFree + expectedPurchased
			if total == 0 {
				continue
			}
			amount := rng.Int64n(total) + 1
			if _, err := repo.ApplyDebit(ctx, mustDebitRequest(
				t, accountID, domain.OperationDebitArgument, amount, fmt.Sprintf("argument:%d", i), key,
			)); err != nil {
				t.Fatalf("debit %d error = %v", i, err)
			}
			if amount <= expectedFree {
				expectedFree -= amount
			} else {
				expectedPurchased -= amount - expectedFree
				expectedFree = 0
			}
		}
		operations++
	}

	if expectedFree < 0 || expectedPurchased < 0 {
		t.Fatalf("test model produced negative expectation: %d/%d", expectedFree, expectedPurchased)
	}

	derived, err := application.NewGetWalletBalanceUseCase(repo).Execute(ctx, accountID)
	if err != nil {
		t.Fatalf("derived balance error = %v", err)
	}
	if derived.Free.Int64() != expectedFree || derived.Purchased.Int64() != expectedPurchased {
		t.Fatalf("derived balance = %d/%d, want %d/%d",
			derived.Free.Int64(), derived.Purchased.Int64(), expectedFree, expectedPurchased)
	}

	cached, err := q.GetWalletAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("cached projection error = %v", err)
	}
	if cached.BalanceFree != derived.Free.Int64() || cached.BalancePurchased != derived.Purchased.Int64() {
		t.Fatalf("cached projection = %d/%d, derived = %d/%d (projection drifted from the ledger)",
			cached.BalanceFree, cached.BalancePurchased, derived.Free.Int64(), derived.Purchased.Int64())
	}

	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", acc.ID); got != operations {
		t.Fatalf("operations = %d, want %d", got, operations)
	}
}

// TestRepository_StatementIsPaginatedAndOwnerScoped proves the cursor pages
// never duplicate or skip entries and that the account filter is independent
// of the cursor: another account's history is unreachable even with a
// syntactically valid cursor.
func TestRepository_StatementIsPaginatedAndOwnerScoped(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	owner := mustWalletAccount(t, ctx, q, "statement-owner@arena.example.com")
	other := mustWalletAccount(t, ctx, q, "statement-other@arena.example.com")
	ownerID := domain.AccountID(uuidString(owner.ID))
	otherID := domain.AccountID(uuidString(other.ID))

	const ownerEntries = 12
	const otherEntries = 5

	// Interleave both histories so the owner cursor positions itself inside
	// the other account's timeline: a foreign cursor must filter, never
	// widen, the account scope.
	for i := 0; i < ownerEntries; i++ {
		bucket := domain.BucketFree
		operationType := domain.OperationCreditFree
		reference := fmt.Sprintf("owner-reference:%d", i)
		if i%2 == 1 {
			bucket = domain.BucketPurchased
			operationType = domain.OperationCreditPurchase
		}
		if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
			t, ownerID, bucket, operationType, int64(100+i), reference, fmt.Sprintf("owner-key:%d", i),
		)); err != nil {
			t.Fatalf("owner credit %d error = %v", i, err)
		}

		if i < otherEntries {
			if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
				t, otherID, domain.BucketFree, domain.OperationCreditFree, 50,
				fmt.Sprintf("other-reference:%d", i), fmt.Sprintf("other-key:%d", i),
			)); err != nil {
				t.Fatalf("other credit %d error = %v", i, err)
			}
		}
	}

	codec, err := application.NewStatementCursorCodec([]byte("test-cursor-secret-0123456789abcd"))
	if err != nil {
		t.Fatalf("build cursor codec: %v", err)
	}
	useCase := application.NewGetWalletStatementUseCase(repo, codec)
	seen := make(map[string]bool, ownerEntries)
	cursor := ""
	pages := 0
	for {
		statement, err := useCase.Execute(ctx, ownerID, cursor, 5)
		if err != nil {
			t.Fatalf("statement page %d error = %v", pages, err)
		}
		pages++
		for _, entry := range statement.Entries {
			if entry.TransactionID == "" || entry.OperationID == "" {
				t.Fatalf("entry without identifiers: %+v", entry)
			}
			if seen[entry.TransactionID] {
				t.Fatalf("duplicate entry %q across pages", entry.TransactionID)
			}
			seen[entry.TransactionID] = true
			if !entry.OperationType.IsValid() || !entry.Bucket.IsValid() || entry.Reference.IsZero() {
				t.Fatalf("entry with invalid decoded values: %+v", entry)
			}
			if strings.HasPrefix(entry.Reference.String(), "other-reference:") {
				t.Fatalf("SECURITY VIOLATION: owner statement leaked %q", entry.Reference)
			}
		}
		if statement.NextCursor == "" {
			break
		}
		cursor = statement.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != ownerEntries {
		t.Fatalf("owner entries delivered = %d, want %d", len(seen), ownerEntries)
	}
	if pages < 2 {
		t.Fatalf("pages = %d, want multiple pages for limit 5 and %d entries", pages, ownerEntries)
	}

	otherStatement, err := useCase.Execute(ctx, otherID, "", 100)
	if err != nil {
		t.Fatalf("other statement error = %v", err)
	}
	if len(otherStatement.Entries) != otherEntries {
		t.Fatalf("other entries = %d, want %d", len(otherStatement.Entries), otherEntries)
	}
	for _, entry := range otherStatement.Entries {
		if strings.HasPrefix(entry.Reference.String(), "owner-reference:") {
			t.Fatalf("SECURITY VIOLATION: other statement leaked %q", entry.Reference)
		}
	}

	// A cursor positions the window; it never changes the account scope.
	foreignCursor := firstCursor(t, useCase, ctx, ownerID)
	foreign, err := useCase.Execute(ctx, otherID, foreignCursor, 100)
	if err != nil {
		t.Fatalf("foreign cursor error = %v", err)
	}
	if len(foreign.Entries) == 0 {
		t.Fatal("foreign cursor filtered every entry: the scope assertion would pass vacuously")
	}
	for _, entry := range foreign.Entries {
		if strings.HasPrefix(entry.Reference.String(), "owner-reference:") {
			t.Fatalf("SECURITY VIOLATION: foreign cursor leaked %q", entry.Reference)
		}
	}

	// Malformed cursors are rejected, never reflected.
	if _, err := useCase.Execute(ctx, ownerID, "%%not-base64%%", 5); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("malformed cursor error = %v, want ErrInvalidCursor", err)
	}
	badUUID := base64.RawURLEncoding.EncodeToString([]byte("v1|2026-09-17T12:00:00Z|not-a-uuid"))
	if _, err := useCase.Execute(ctx, ownerID, badUUID, 5); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("bad uuid cursor error = %v, want ErrInvalidCursor", err)
	}
}

func firstCursor(t *testing.T, useCase *application.GetWalletStatementUseCase, ctx context.Context, accountID domain.AccountID) string {
	t.Helper()
	statement, err := useCase.Execute(ctx, accountID, "", 1)
	if err != nil {
		t.Fatalf("first cursor error = %v", err)
	}
	return statement.NextCursor
}

type testClock struct {
	now time.Time
}

func (c testClock) Now() time.Time { return c.now }

func setFreeCycleAnchor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID pgtype.UUID, anchor time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		"UPDATE app.wallet_accounts SET free_cycle_anchor_at = $2 WHERE account_id = $1", accountID, anchor.UTC(),
	); err != nil {
		t.Fatalf("set free cycle anchor: %v", err)
	}
}

func countOperationType(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID pgtype.UUID, operationType string) int {
	t.Helper()
	return countRows(t, ctx, pool,
		"SELECT count(*) FROM app.wallet_operations WHERE account_id = $1 AND operation_type = $2",
		accountID, operationType)
}

func TestRepository_RenewFreeCycleNormalTurn(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	acc := mustWalletAccount(t, ctx, q, "free-cycle-normal@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:activation", "free-cycle:activation",
	)); err != nil {
		t.Fatalf("activation credit error = %v", err)
	}
	setFreeCycleAnchor(t, ctx, pool, acc.ID, now.Add(-35*24*time.Hour))

	useCase := application.NewRenewFreeCycleUseCase(repo, domain.DefaultFreeCyclePolicy(), testClock{now: now})
	result, err := useCase.Execute(ctx, application.RenewFreeCycleCommand{AccountID: accountID.String()})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result.Renewals) != 1 {
		t.Fatalf("renewals = %+v, want exactly 1", result.Renewals)
	}
	renewal := result.Renewals[0]
	if renewal.Replayed {
		t.Error("the first renewal of a period must not be a replay")
	}
	if renewal.Expired.Int64() != 5000 || renewal.Granted.Int64() != 5000 {
		t.Fatalf("renewal = expired %d / granted %d, want 5000/5000", renewal.Expired.Int64(), renewal.Granted.Int64())
	}
	if renewal.PeriodStart.Location() != time.UTC {
		t.Errorf("PeriodStart location = %v, want UTC", renewal.PeriodStart.Location())
	}

	balance, err := application.NewGetWalletBalanceUseCase(repo).Execute(ctx, accountID)
	if err != nil {
		t.Fatalf("derived balance error = %v", err)
	}
	if balance.Free.Int64() != 5000 || balance.Purchased.Int64() != 0 {
		t.Fatalf("balance = %d/%d, want 5000/0 (franchise renewed, not accumulated)", balance.Free.Int64(), balance.Purchased.Int64())
	}
	if got := countOperationType(t, ctx, pool, acc.ID, "expire_free"); got != 1 {
		t.Fatalf("expire_free operations = %d, want 1", got)
	}
	if got := countOperationType(t, ctx, pool, acc.ID, "credit_free"); got != 2 {
		t.Fatalf("credit_free operations = %d, want 2 (activation + renewal)", got)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_transactions"); got != 3 {
		t.Fatalf("transactions = %d, want 3", got)
	}
}

func TestRepository_RenewFreeCycleDelayWithSpending(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	acc := mustWalletAccount(t, ctx, q, "free-cycle-delay@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:activation", "free-cycle:delay:activation",
	)); err != nil {
		t.Fatalf("activation credit error = %v", err)
	}
	if _, err := repo.ApplyDebit(ctx, mustDebitRequest(
		t, accountID, domain.OperationDebitArgument, 2000, "argument:delay", "free-cycle:delay:spend",
	)); err != nil {
		t.Fatalf("spending debit error = %v", err)
	}
	setFreeCycleAnchor(t, ctx, pool, acc.ID, now.Add(-45*24*time.Hour))

	useCase := application.NewRenewFreeCycleUseCase(repo, domain.DefaultFreeCyclePolicy(), testClock{now: now})
	result, err := useCase.Execute(ctx, application.RenewFreeCycleCommand{AccountID: accountID.String()})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result.Renewals) != 1 || result.Renewals[0].Expired.Int64() != 3000 {
		t.Fatalf("renewals = %+v, want one renewal expiring the 3000 left", result.Renewals)
	}

	balance, err := application.NewGetWalletBalanceUseCase(repo).Execute(ctx, accountID)
	if err != nil {
		t.Fatalf("derived balance error = %v", err)
	}
	if balance.Free.Int64() != 5000 {
		t.Fatalf("balance_free = %d, want 5000 after the delayed renewal", balance.Free.Int64())
	}
}

func TestRepository_RenewFreeCycleIsIdempotentOnRetry(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	acc := mustWalletAccount(t, ctx, q, "free-cycle-retry@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:activation", "free-cycle:retry:activation",
	)); err != nil {
		t.Fatalf("activation credit error = %v", err)
	}
	setFreeCycleAnchor(t, ctx, pool, acc.ID, now.Add(-35*24*time.Hour))

	useCase := application.NewRenewFreeCycleUseCase(repo, domain.DefaultFreeCyclePolicy(), testClock{now: now})
	if _, err := useCase.Execute(ctx, application.RenewFreeCycleCommand{AccountID: accountID.String()}); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}

	retry, err := useCase.Execute(ctx, application.RenewFreeCycleCommand{AccountID: accountID.String()})
	if err != nil {
		t.Fatalf("retry Execute() error = %v", err)
	}
	if len(retry.Renewals) != 1 || !retry.Renewals[0].Replayed {
		t.Fatalf("retry renewals = %+v, want one replayed period", retry.Renewals)
	}
	if got := countOperationType(t, ctx, pool, acc.ID, "expire_free"); got != 1 {
		t.Fatalf("expire_free operations after retry = %d, want 1", got)
	}
	if got := countOperationType(t, ctx, pool, acc.ID, "credit_free"); got != 2 {
		t.Fatalf("credit_free operations after retry = %d, want 2", got)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_transactions"); got != 3 {
		t.Fatalf("transactions after retry = %d, want 3", got)
	}
	balance, err := application.NewGetWalletBalanceUseCase(repo).Execute(ctx, accountID)
	if err != nil {
		t.Fatalf("derived balance error = %v", err)
	}
	if balance.Free.Int64() != 5000 {
		t.Fatalf("balance_free after retry = %d, want 5000", balance.Free.Int64())
	}
}

func TestRepository_RenewFreeCycleMultiplePeriods(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	acc := mustWalletAccount(t, ctx, q, "free-cycle-multi@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:activation", "free-cycle:multi:activation",
	)); err != nil {
		t.Fatalf("activation credit error = %v", err)
	}
	setFreeCycleAnchor(t, ctx, pool, acc.ID, now.Add(-100*24*time.Hour))

	useCase := application.NewRenewFreeCycleUseCase(repo, domain.DefaultFreeCyclePolicy(), testClock{now: now})
	result, err := useCase.Execute(ctx, application.RenewFreeCycleCommand{AccountID: accountID.String()})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result.Renewals) != 3 {
		t.Fatalf("renewals = %d, want 3 elapsed periods", len(result.Renewals))
	}
	var expiredTotal, grantedTotal int64
	for i, renewal := range result.Renewals {
		if renewal.Replayed {
			t.Errorf("renewal %d must be fresh", i)
		}
		expiredTotal += renewal.Expired.Int64()
		grantedTotal += renewal.Granted.Int64()
	}
	if expiredTotal != 15000 || grantedTotal != 15000 {
		t.Fatalf("totals = expired %d / granted %d, want 15000/15000", expiredTotal, grantedTotal)
	}

	balance, err := application.NewGetWalletBalanceUseCase(repo).Execute(ctx, accountID)
	if err != nil {
		t.Fatalf("derived balance error = %v", err)
	}
	if balance.Free.Int64() != 5000 {
		t.Fatalf("balance_free = %d, want exactly one franchise after catch-up", balance.Free.Int64())
	}
	if got := countOperationType(t, ctx, pool, acc.ID, "expire_free"); got != 3 {
		t.Fatalf("expire_free operations = %d, want 3", got)
	}
	if got := countOperationType(t, ctx, pool, acc.ID, "credit_free"); got != 4 {
		t.Fatalf("credit_free operations = %d, want 4 (activation + 3 renewals)", got)
	}
}

func TestRepository_RenewFreeCycleTimeZoneIrrelevant(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	anchor := now.Add(-35 * 24 * time.Hour)
	brazil := time.FixedZone("BRT", -3*3600)

	accounts := []struct {
		email string
		clock application.Clock
	}{
		{email: "free-cycle-tz-utc@arena.example.com", clock: testClock{now: now}},
		{email: "free-cycle-tz-brt@arena.example.com", clock: testClock{now: now.In(brazil)}},
	}

	results := make([]*application.RenewFreeCycleResult, 0, len(accounts))
	for _, account := range accounts {
		acc := mustWalletAccount(t, ctx, q, account.email)
		accountID := domain.AccountID(uuidString(acc.ID))
		if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
			t, accountID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:activation", "free-cycle:tz:"+account.email,
		)); err != nil {
			t.Fatalf("activation credit error = %v", err)
		}
		setFreeCycleAnchor(t, ctx, pool, acc.ID, anchor)

		useCase := application.NewRenewFreeCycleUseCase(repo, domain.DefaultFreeCyclePolicy(), account.clock)
		result, err := useCase.Execute(ctx, application.RenewFreeCycleCommand{AccountID: accountID.String()})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		results = append(results, result)
	}

	first, second := results[0], results[1]
	if !first.CurrentPeriodStart.Equal(second.CurrentPeriodStart) {
		t.Fatalf("period starts diverged by clock zone: %v vs %v", first.CurrentPeriodStart, second.CurrentPeriodStart)
	}
	if first.CurrentPeriodStart.Location() != time.UTC {
		t.Errorf("CurrentPeriodStart location = %v, want UTC", first.CurrentPeriodStart.Location())
	}
	if len(first.Renewals) != 1 || len(second.Renewals) != 1 {
		t.Fatalf("renewals diverged by clock zone: %d vs %d", len(first.Renewals), len(second.Renewals))
	}
	if first.Renewals[0].Expired.Int64() != second.Renewals[0].Expired.Int64() ||
		first.Renewals[0].Granted.Int64() != second.Renewals[0].Granted.Int64() {
		t.Fatalf("renewal outcomes diverged by clock zone: %+v vs %+v", first.Renewals[0], second.Renewals[0])
	}
}

// TestRepository_RenewFreeCycleConcurrentWorkers runs ten workers through
// the same renewal: the wallet lock and the period keys allow exactly one
// fresh renewal, and every other worker replays it.
func TestRepository_RenewFreeCycleConcurrentWorkers(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t, dbtest.WithPoolLimits(10, 1))
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	acc := mustWalletAccount(t, ctx, q, "free-cycle-race@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, accountID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:activation", "free-cycle:race:activation",
	)); err != nil {
		t.Fatalf("activation credit error = %v", err)
	}
	setFreeCycleAnchor(t, ctx, pool, acc.ID, now.Add(-40*24*time.Hour))

	useCase := application.NewRenewFreeCycleUseCase(repo, domain.DefaultFreeCyclePolicy(), testClock{now: now})

	const workers = 10
	var wg sync.WaitGroup
	results := make([]*application.RenewFreeCycleResult, workers)
	errs := make([]error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := useCase.Execute(ctx, application.RenewFreeCycleCommand{AccountID: accountID.String()})
			if err != nil {
				errs[index] = err
				return
			}
			results[index] = result
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d error = %v", i, err)
		}
	}

	fresh, replayed := 0, 0
	for _, result := range results {
		if result == nil || len(result.Renewals) != 1 {
			t.Fatalf("worker result = %+v, want one renewal", result)
		}
		if result.Renewals[0].Replayed {
			replayed++
		} else {
			fresh++
		}
	}
	if fresh != 1 || replayed != workers-1 {
		t.Fatalf("fresh=%d replayed=%d, want 1/%d", fresh, replayed, workers-1)
	}

	if got := countOperationType(t, ctx, pool, acc.ID, "expire_free"); got != 1 {
		t.Fatalf("expire_free operations = %d, want 1", got)
	}
	if got := countOperationType(t, ctx, pool, acc.ID, "credit_free"); got != 2 {
		t.Fatalf("credit_free operations = %d, want 2", got)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_transactions"); got != 3 {
		t.Fatalf("transactions = %d, want 3", got)
	}
	balance, err := application.NewGetWalletBalanceUseCase(repo).Execute(ctx, accountID)
	if err != nil {
		t.Fatalf("derived balance error = %v", err)
	}
	if balance.Free.Int64() != 5000 {
		t.Fatalf("balance_free = %d, want 5000", balance.Free.Int64())
	}
}

func TestRepository_RenewFreeCycleMissingWallet(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "free-cycle-missing@arena.example.com")
	useCase := application.NewRenewFreeCycleUseCase(repo, domain.DefaultFreeCyclePolicy(), testClock{now: time.Now().UTC()})

	_, err := useCase.Execute(ctx, application.RenewFreeCycleCommand{AccountID: uuidString(acc.ID)})
	if !errors.Is(err, application.ErrWalletNotFound) {
		t.Fatalf("missing wallet error = %v, want ErrWalletNotFound", err)
	}
}

func TestRepository_AdminAdjustmentRowsCarryReasonAndActor(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	operator := mustWalletAccount(t, ctx, q, "admin-operator@arena.example.com")
	target := mustWalletAccount(t, ctx, q, "admin-target@arena.example.com")
	operatorID := domain.AccountID(uuidString(operator.ID))
	targetID := domain.AccountID(uuidString(target.ID))

	creditRequest := mustCreditRequest(t, targetID, domain.BucketFree, domain.OperationCreditAdmin, 1500, "admin:ticket-77", "admin-credit:77")
	creditRequest.Reason, creditRequest.ActorAccountID = mustAdminAudit(t, operatorID, "concessão manual aprovada no ticket 77")
	credit, err := repo.ApplyCredit(ctx, creditRequest)
	if err != nil {
		t.Fatalf("admin credit error = %v", err)
	}

	debitRequest := mustDebitRequest(t, targetID, domain.OperationDebitAdmin, 500, "admin:ticket-78", "admin-debit:78")
	debitRequest.Reason, debitRequest.ActorAccountID = mustAdminAudit(t, operatorID, "reversão parcial aprovada no ticket 78")
	debit, err := repo.ApplyDebit(ctx, debitRequest)
	if err != nil {
		t.Fatalf("admin debit error = %v", err)
	}

	rows := []struct {
		operationID pgtype.UUID
		operation   string
		wantReason  string
	}{
		{operationID: mustUUID(t, credit.Operation.ID().String()), operation: "credit_admin", wantReason: "concessão manual aprovada no ticket 77"},
		{operationID: mustUUID(t, debit.Operation.ID().String()), operation: "debit_admin", wantReason: "reversão parcial aprovada no ticket 78"},
	}
	for _, row := range rows {
		var reason string
		var actor pgtype.UUID
		var operationType string
		if err := pool.QueryRow(ctx,
			"SELECT operation_type, reason, actor_account_id FROM app.wallet_operations WHERE id = $1", row.operationID,
		).Scan(&operationType, &reason, &actor); err != nil {
			t.Fatalf("load admin operation: %v", err)
		}
		if operationType != row.operation || reason != row.wantReason {
			t.Errorf("row = %q/%q, want %q/%q", operationType, reason, row.operation, row.wantReason)
		}
		if !actor.Valid || uuidString(actor) != operatorID.String() {
			t.Errorf("actor = %+v, want %q", actor, operatorID)
		}
	}

	// The operation entity round-trips the audit fields as well.
	if credit.Operation.Reason().String() != "concessão manual aprovada no ticket 77" {
		t.Errorf("operation reason = %q", credit.Operation.Reason())
	}
	if credit.Operation.ActorAccountID() != operatorID {
		t.Errorf("operation actor = %q, want %q", credit.Operation.ActorAccountID(), operatorID)
	}
	balance, err := application.NewGetWalletBalanceUseCase(repo).Execute(ctx, targetID)
	if err != nil {
		t.Fatalf("derived balance error = %v", err)
	}
	if balance.Free.Int64() != 1000 {
		t.Fatalf("balance_free = %d, want 1000", balance.Free.Int64())
	}
}

// TestRepository_AdminAdjustmentSchemaRequiresReasonAndActor proves the
// database refuses administrative operations without justification and
// actor, even if an application path is bypassed (THR-ADM-02).
func TestRepository_AdminAdjustmentSchemaRequiresReasonAndActor(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	q := platformpg.New(pool)

	acc := mustWalletAccount(t, ctx, q, "admin-schema@arena.example.com")
	if err := q.EnsureWalletAccount(ctx, acc.ID); err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}

	validReason := pgtype.Text{String: "schema probe", Valid: true}
	blankReason := pgtype.Text{String: "   ", Valid: true}
	missingReason := pgtype.Text{}
	missingActor := pgtype.UUID{}

	probes := []struct {
		name          string
		operationType string
		reason        pgtype.Text
		actor         pgtype.UUID
		wantCode      string
	}{
		{name: "admin without reason", operationType: "credit_admin", reason: missingReason, actor: acc.ID, wantCode: "23514"},
		{name: "admin without actor", operationType: "credit_admin", reason: validReason, actor: missingActor, wantCode: "23514"},
		{name: "admin debt without reason", operationType: "debit_admin", reason: missingReason, actor: acc.ID, wantCode: "23514"},
		{name: "admin with blank reason", operationType: "debit_admin", reason: blankReason, actor: acc.ID, wantCode: "23514"},
		{name: "reason too long", operationType: "credit_free", reason: pgtype.Text{String: strings.Repeat("a", 501), Valid: true}, actor: missingActor, wantCode: "23514"},
		{name: "regular without audit fields", operationType: "credit_free", reason: missingReason, actor: missingActor, wantCode: ""},
		{name: "regular with reason", operationType: "debit_argument", reason: validReason, actor: acc.ID, wantCode: ""},
	}

	for i, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := q.CreateWalletOperation(ctx, platformpg.CreateWalletOperationParams{
				AccountID:      acc.ID,
				OperationType:  probe.operationType,
				IdempotencyKey: fmt.Sprintf("schema-probe-%d", i),
				Reference:      "schema:probe",
				Reason:         probe.reason,
				ActorAccountID: probe.actor,
			})
			if probe.wantCode == "" {
				if err != nil {
					t.Fatalf("operation rejected: %v", err)
				}
				return
			}
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != probe.wantCode {
				t.Fatalf("error = %v, want SQLSTATE %s", err, probe.wantCode)
			}
		})
	}
}

func TestRepository_AdminAdjustmentInsufficientDebitIsControlled(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	operator := mustWalletAccount(t, ctx, q, "admin-insufficient-operator@arena.example.com")
	target := mustWalletAccount(t, ctx, q, "admin-insufficient-target@arena.example.com")
	operatorID := domain.AccountID(uuidString(operator.ID))
	targetID := domain.AccountID(uuidString(target.ID))

	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, targetID, domain.BucketFree, domain.OperationCreditFree, 100, "free:seed", "admin-insufficient:seed",
	)); err != nil {
		t.Fatalf("seed credit error = %v", err)
	}
	baseline := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", target.ID)

	request := mustDebitRequest(t, targetID, domain.OperationDebitAdmin, 101, "admin:ticket-79", "admin-insufficient:debit")
	request.Reason, request.ActorAccountID = mustAdminAudit(t, operatorID, "reversão acima do saldo")
	if _, err := repo.ApplyDebit(ctx, request); !errors.Is(err, domain.ErrInsufficientInk) {
		t.Fatalf("insufficient admin debit error = %v, want ErrInsufficientInk", err)
	}

	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", target.ID); got != baseline {
		t.Fatalf("operations after failure = %d, want %d (no partial state)", got, baseline)
	}
	balance, err := application.NewGetWalletBalanceUseCase(repo).Execute(ctx, targetID)
	if err != nil {
		t.Fatalf("derived balance error = %v", err)
	}
	if balance.Free.Int64() != 100 {
		t.Fatalf("balance_free = %d, want 100 untouched", balance.Free.Int64())
	}
}

func TestRepository_AdminAdjustmentReplayKeepsSingleOperation(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	operator := mustWalletAccount(t, ctx, q, "admin-replay-operator@arena.example.com")
	target := mustWalletAccount(t, ctx, q, "admin-replay-target@arena.example.com")
	operatorID := domain.AccountID(uuidString(operator.ID))
	targetID := domain.AccountID(uuidString(target.ID))

	request := mustCreditRequest(t, targetID, domain.BucketPurchased, domain.OperationCreditAdmin, 700, "admin:ticket-80", "admin-replay:80")
	request.Reason, request.ActorAccountID = mustAdminAudit(t, operatorID, "ajuste único")

	first, err := repo.ApplyCredit(ctx, request)
	if err != nil {
		t.Fatalf("first ApplyCredit() error = %v", err)
	}
	second, err := repo.ApplyCredit(ctx, request)
	if err != nil {
		t.Fatalf("second ApplyCredit() error = %v", err)
	}
	if !second.Replayed || second.Operation.ID() != first.Operation.ID() {
		t.Fatalf("replay = %+v, want the original operation", second)
	}
	if second.Operation.Reason().String() != "ajuste único" || second.Operation.ActorAccountID() != operatorID {
		t.Fatalf("replay lost audit fields: %+v", second.Operation)
	}

	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", target.ID); got != 1 {
		t.Fatalf("operations = %d, want exactly 1", got)
	}
	balance, err := application.NewGetWalletBalanceUseCase(repo).Execute(ctx, targetID)
	if err != nil {
		t.Fatalf("derived balance error = %v", err)
	}
	if balance.Purchased.Int64() != 700 {
		t.Fatalf("balance_purchased = %d, want 700 applied once", balance.Purchased.Int64())
	}
}

type allowAllAuthorizer struct {
	calls int
}

func (a *allowAllAuthorizer) EnsureAdministrator(_ context.Context, _ domain.AccountID) error {
	a.calls++
	return nil
}

type denyAllAuthorizer struct {
	calls int
}

func (a *denyAllAuthorizer) EnsureAdministrator(_ context.Context, _ domain.AccountID) error {
	a.calls++
	return application.ErrNotAuthorized
}

type captureAuditRecorder struct {
	events []application.AdminAdjustmentEvent
}

func (r *captureAuditRecorder) RecordAdminAdjustment(_ context.Context, event application.AdminAdjustmentEvent) error {
	r.events = append(r.events, event)
	return nil
}

func TestRepository_AdminAdjustmentEndToEnd(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := walletpg.NewRepository(pool)
	q := platformpg.New(pool)

	operator := mustWalletAccount(t, ctx, q, "admin-e2e-operator@arena.example.com")
	target := mustWalletAccount(t, ctx, q, "admin-e2e-target@arena.example.com")
	operatorID := domain.AccountID(uuidString(operator.ID))
	targetID := domain.AccountID(uuidString(target.ID))

	if _, err := repo.ApplyCredit(ctx, mustCreditRequest(
		t, targetID, domain.BucketFree, domain.OperationCreditFree, 5000, "free:seed", "admin-e2e:seed",
	)); err != nil {
		t.Fatalf("seed credit error = %v", err)
	}

	authorizer := &allowAllAuthorizer{}
	audit := &captureAuditRecorder{}
	useCase := application.NewAdjustInkUseCase(repo, repo, authorizer, audit, testClock{now: time.Now().UTC()})

	// Positive adjustment.
	credited, err := useCase.Execute(ctx, application.AdjustInkCommand{
		ActorAccountID: operatorID.String(),
		AccountID:      targetID.String(),
		Bucket:         "FREE_INK",
		OperationType:  "credit_admin",
		Amount:         1000,
		Reference:      "admin:ticket-81",
		Reason:         "concessão aprovada no ticket 81",
		IdempotencyKey: "admin-e2e:81",
	})
	if err != nil {
		t.Fatalf("positive adjustment error = %v", err)
	}
	if credited.Replayed || credited.Operation.Type() != domain.OperationCreditAdmin {
		t.Fatalf("positive adjustment = %+v", credited)
	}

	// Negative adjustment follows the FREE-first priority.
	debited, err := useCase.Execute(ctx, application.AdjustInkCommand{
		ActorAccountID: operatorID.String(),
		AccountID:      targetID.String(),
		OperationType:  "debit_admin",
		Amount:         4000,
		Reference:      "admin:ticket-82",
		Reason:         "reversão aprovada no ticket 82",
		IdempotencyKey: "admin-e2e:82",
	})
	if err != nil {
		t.Fatalf("negative adjustment error = %v", err)
	}
	if debited.Replayed || debited.Allocation.FromFree().Int64() != 4000 {
		t.Fatalf("negative adjustment = %+v, want 4000 from FREE_INK", debited)
	}

	balance, err := application.NewGetWalletBalanceUseCase(repo).Execute(ctx, targetID)
	if err != nil {
		t.Fatalf("derived balance error = %v", err)
	}
	if balance.Free.Int64() != 2000 || balance.Purchased.Int64() != 0 {
		t.Fatalf("balance = %d/%d, want 2000/0", balance.Free.Int64(), balance.Purchased.Int64())
	}
	if len(audit.events) != 2 {
		t.Fatalf("audit events = %d, want 2", len(audit.events))
	}
	for i, event := range audit.events {
		if event.Replayed || event.ActorAccountID != operatorID || event.Reason.IsZero() {
			t.Fatalf("audit event %d = %+v, want a fresh justified record", i, event)
		}
	}

	// Controlled insufficient balance.
	if _, err := useCase.Execute(ctx, application.AdjustInkCommand{
		ActorAccountID: operatorID.String(),
		AccountID:      targetID.String(),
		OperationType:  "debit_admin",
		Amount:         5000,
		Reference:      "admin:ticket-83",
		Reason:         "reversão acima do saldo",
		IdempotencyKey: "admin-e2e:83",
	}); !errors.Is(err, domain.ErrInsufficientInk) {
		t.Fatalf("insufficient error = %v, want ErrInsufficientInk", err)
	}
	if len(audit.events) != 2 {
		t.Fatalf("audit events after failure = %d, want 2", len(audit.events))
	}

	// Negative authorization: nothing is written and nothing is audited.
	denied := &denyAllAuthorizer{}
	deniedUseCase := application.NewAdjustInkUseCase(repo, repo, denied, audit, testClock{now: time.Now().UTC()})
	if _, err := deniedUseCase.Execute(ctx, application.AdjustInkCommand{
		ActorAccountID: operatorID.String(),
		AccountID:      targetID.String(),
		Bucket:         "FREE_INK",
		OperationType:  "credit_admin",
		Amount:         1000,
		Reference:      "admin:ticket-84",
		Reason:         "tentativa sem autorização",
		IdempotencyKey: "admin-e2e:84",
	}); !errors.Is(err, application.ErrNotAuthorized) {
		t.Fatalf("denied actor error = %v, want ErrNotAuthorized", err)
	}
	if len(audit.events) != 2 {
		t.Fatalf("audit events after denial = %d, want 2", len(audit.events))
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM app.wallet_operations WHERE account_id = $1", target.ID); got != 3 {
		t.Fatalf("operations = %d, want 3 (seed + 2 adjustments)", got)
	}
}
