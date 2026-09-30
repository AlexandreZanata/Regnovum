// Package postgres is the PostgreSQL outbound adapter of the wallet module.
// It implements the application credit port against the append-only ledger
// schema (migration 00008): the operation registry is written with ON
// CONFLICT DO NOTHING so retries resolve the original operation, and the
// operation, transaction and balance projection change in a single
// transaction.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	"github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// Repository implements the wallet application ports using PostgreSQL.
type Repository struct {
	pool    *pgxpool.Pool
	queries *platformpg.Queries
}

var (
	_ application.CreditRepository      = (*Repository)(nil)
	_ application.DebitRepository       = (*Repository)(nil)
	_ application.WalletQueryRepository = (*Repository)(nil)
	_ application.FreeCycleRepository   = (*Repository)(nil)
)

// NewRepository creates a PostgreSQL repository adapter for the wallet.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{
		pool:    pool,
		queries: platformpg.New(pool),
	}
}

// ApplyCredit atomically ensures the wallet, stores the operation under its
// idempotency key, records the bucket transaction and updates the balance.
// When the key was already processed, no write happens and the original
// operation is returned with Replayed set.
func (r *Repository) ApplyCredit(ctx context.Context, request application.CreditRequest) (*application.CreditResult, error) {
	pgUUID, err := pgUUIDFromAccountID(request.AccountID)
	if err != nil {
		return nil, fmt.Errorf("apply credit: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	if err := qtx.EnsureWalletAccount(ctx, pgUUID); err != nil {
		return nil, fmt.Errorf("ensure wallet account: %w", err)
	}

	operationRow, err := qtx.CreateWalletOperationIfAbsent(ctx, platformpg.CreateWalletOperationIfAbsentParams{
		AccountID:      pgUUID,
		OperationType:  request.OperationType.String(),
		IdempotencyKey: request.IdempotencyKey.String(),
		Reference:      request.Reference.String(),
		Reason:         operationReason(request.Reason),
		ActorAccountID: operationActor(request.ActorAccountID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return replayCredit(ctx, qtx, request)
	}
	if err != nil {
		return nil, fmt.Errorf("create wallet operation: %w", err)
	}

	if _, err := qtx.CreateWalletTransaction(ctx, platformpg.CreateWalletTransactionParams{
		OperationID: operationRow.ID,
		Bucket:      request.Bucket.String(),
		Amount:      request.Delta,
	}); err != nil {
		return nil, fmt.Errorf("create wallet transaction: %w", err)
	}

	if err := creditBalance(ctx, qtx, pgUUID, request); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	operation, err := mapOperationRow(operationRow)
	if err != nil {
		return nil, err
	}
	return &application.CreditResult{Operation: *operation}, nil
}

// replayCredit resolves a conflicting idempotency key to its original
// operation. The key is global: a replay from another account is refused
// instead of leaking the original reference. Nothing was written on this
// path, so the surrounding transaction is rolled back untouched.
func replayCredit(ctx context.Context, qtx *platformpg.Queries, request application.CreditRequest) (*application.CreditResult, error) {
	existingRow, err := qtx.GetWalletOperationByIdempotencyKey(ctx, request.IdempotencyKey.String())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("idempotency conflict without a stored operation")
		}
		return nil, fmt.Errorf("load replayed operation: %w", err)
	}

	if uuidToString(existingRow.AccountID) != request.AccountID.String() {
		return nil, application.ErrIdempotencyMismatch
	}

	operation, err := mapOperationRow(existingRow)
	if err != nil {
		return nil, err
	}
	return &application.CreditResult{Operation: *operation, Replayed: true}, nil
}

// ApplyDebit locks the wallet, plans the bucket consumption by priority,
// stores the operation under its idempotency key, records one line per
// consumed bucket and updates the balances atomically. Insufficient balance
// leaves no partial state, and concurrent debits cannot double spend
// (THR-WAL-01). When the context carries a shared transaction (P07-T05) the
// debit joins it so the caller commits or rolls back the whole operation;
// otherwise the adapter owns one transaction.
func (r *Repository) ApplyDebit(ctx context.Context, request application.DebitRequest) (*application.DebitResult, error) {
	if tx, ok := platformpg.TxFromContext(ctx); ok {
		return r.applyDebit(ctx, r.queries.WithTx(tx), request)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	result, err := r.applyDebit(ctx, r.queries.WithTx(tx), request)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return result, nil
}

// applyDebit runs the debit against the given queries, which may be bound
// to an owned or to a shared transaction.
func (r *Repository) applyDebit(ctx context.Context, qtx *platformpg.Queries, request application.DebitRequest) (*application.DebitResult, error) {
	pgUUID, err := pgUUIDFromAccountID(request.AccountID)
	if err != nil {
		return nil, fmt.Errorf("apply debit: %w", err)
	}

	// Fast path: an already processed key replays without locking the wallet.
	if result, found, err := replayDebit(ctx, qtx, request); err != nil {
		return nil, err
	} else if found {
		return result, nil
	}

	accountRow, err := qtx.GetWalletAccountForUpdate(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No wallet means no balance: nothing can be debited.
			return nil, domain.ErrInsufficientInk
		}
		return nil, fmt.Errorf("lock wallet account: %w", err)
	}

	// Re-check under the wallet lock: concurrent retries of the same key for
	// this account are serialized here and must replay, never debit twice.
	if result, found, err := replayDebit(ctx, qtx, request); err != nil {
		return nil, err
	} else if found {
		return result, nil
	}

	availableFree, err := domain.NewInk(accountRow.BalanceFree)
	if err != nil {
		return nil, fmt.Errorf("stored free balance is invalid: %w", err)
	}
	availablePurchased, err := domain.NewInk(accountRow.BalancePurchased)
	if err != nil {
		return nil, fmt.Errorf("stored purchased balance is invalid: %w", err)
	}

	allocation, err := domain.AllocateDebit(request.Amount, availableFree, availablePurchased)
	if err != nil {
		return nil, err
	}

	operationRow, err := qtx.CreateWalletOperationIfAbsent(ctx, platformpg.CreateWalletOperationIfAbsentParams{
		AccountID:      pgUUID,
		OperationType:  request.OperationType.String(),
		IdempotencyKey: request.IdempotencyKey.String(),
		Reference:      request.Reference.String(),
		Reason:         operationReason(request.Reason),
		ActorAccountID: operationActor(request.ActorAccountID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Cross-account key conflict: the wallet lock did not serialize it.
		if result, found, replayErr := replayDebit(ctx, qtx, request); replayErr != nil {
			return nil, replayErr
		} else if found {
			return result, nil
		}
		return nil, errors.New("idempotency conflict without a stored operation")
	}
	if err != nil {
		return nil, fmt.Errorf("create wallet operation: %w", err)
	}

	for _, line := range allocation.Lines() {
		delta, err := domain.DirectionDebit.Apply(line.Amount)
		if err != nil {
			return nil, err
		}
		if _, err := qtx.CreateWalletTransaction(ctx, platformpg.CreateWalletTransactionParams{
			OperationID: operationRow.ID,
			Bucket:      line.Bucket.String(),
			Amount:      delta,
		}); err != nil {
			return nil, fmt.Errorf("create wallet transaction: %w", err)
		}
	}

	if _, err := qtx.ApplyWalletDebit(ctx, platformpg.ApplyWalletDebitParams{
		AccountID:        pgUUID,
		BalanceFree:      allocation.FromFree().Int64(),
		BalancePurchased: allocation.FromPurchased().Int64(),
	}); err != nil {
		return nil, fmt.Errorf("apply wallet debit: %w", err)
	}

	operation, err := mapOperationRow(operationRow)
	if err != nil {
		return nil, err
	}
	return &application.DebitResult{Operation: *operation, Allocation: allocation}, nil
}

// replayDebit resolves a key that was already processed to its original
// operation and reconstructs the consumption plan from the stored ledger
// lines. The second result reports whether a replay was found.
func replayDebit(ctx context.Context, qtx *platformpg.Queries, request application.DebitRequest) (*application.DebitResult, bool, error) {
	existingRow, err := qtx.GetWalletOperationByIdempotencyKey(ctx, request.IdempotencyKey.String())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("load replayed operation: %w", err)
	}

	if uuidToString(existingRow.AccountID) != request.AccountID.String() {
		return nil, false, application.ErrIdempotencyMismatch
	}

	operation, err := mapOperationRow(existingRow)
	if err != nil {
		return nil, false, err
	}

	allocation, err := reconstructAllocation(ctx, qtx, existingRow.ID)
	if err != nil {
		return nil, false, err
	}

	return &application.DebitResult{Operation: *operation, Allocation: allocation, Replayed: true}, true, nil
}

// reconstructAllocation rebuilds the consumption plan of a stored debit from
// its ledger lines; stored debit lines carry negative amounts.
func reconstructAllocation(ctx context.Context, qtx *platformpg.Queries, operationID pgtype.UUID) (domain.Allocation, error) {
	rows, err := qtx.ListWalletTransactionsByOperationID(ctx, operationID)
	if err != nil {
		return domain.Allocation{}, fmt.Errorf("load operation lines: %w", err)
	}

	var fromFree, fromPurchased domain.Ink
	for _, row := range rows {
		magnitude := row.Amount
		if magnitude < 0 {
			magnitude = -magnitude
		}
		amount, err := domain.NewInk(magnitude)
		if err != nil {
			return domain.Allocation{}, fmt.Errorf("stored line amount is invalid: %w", err)
		}

		switch domain.Bucket(row.Bucket) {
		case domain.BucketFree:
			fromFree, err = fromFree.Add(amount)
		case domain.BucketPurchased:
			fromPurchased, err = fromPurchased.Add(amount)
		default:
			return domain.Allocation{}, fmt.Errorf("stored bucket is invalid: %q", row.Bucket)
		}
		if err != nil {
			return domain.Allocation{}, fmt.Errorf("sum stored lines: %w", err)
		}
	}

	return domain.NewAllocation(fromFree, fromPurchased), nil
}

// DerivedBalance recomputes both bucket balances from the append-only
// ledger: the projection is never trusted as the source of truth.
func (r *Repository) DerivedBalance(ctx context.Context, accountID domain.AccountID) (*application.WalletBalance, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, fmt.Errorf("derived balance: %w", err)
	}

	row, err := r.queries.GetDerivedWalletBalance(ctx, pgUUID)
	if err != nil {
		return nil, fmt.Errorf("get derived wallet balance: %w", err)
	}

	free, err := domain.NewInk(row.BalanceFree)
	if err != nil {
		return nil, fmt.Errorf("derived free balance is invalid: %w", err)
	}
	purchased, err := domain.NewInk(row.BalancePurchased)
	if err != nil {
		return nil, fmt.Errorf("derived purchased balance is invalid: %w", err)
	}

	return &application.WalletBalance{Free: free, Purchased: purchased}, nil
}

// ListStatementPage returns one keyset page of the account statement, newest
// first. The account filter is applied on every page; the cursor only
// positions the window inside that account's own history.
func (r *Repository) ListStatementPage(ctx context.Context, accountID domain.AccountID, after *application.StatementPosition, limit int) ([]application.StatementEntry, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, fmt.Errorf("list statement page: %w", err)
	}

	params := platformpg.ListWalletStatementPageParams{
		AccountID: pgUUID,
		PageLimit: int32(limit),
	}
	if after != nil {
		var afterID pgtype.UUID
		if err := afterID.Scan(after.TransactionID); err != nil {
			return nil, application.ErrInvalidCursor
		}
		params.AfterCreatedAt = pgtype.Timestamptz{Time: after.CreatedAt, Valid: true}
		params.AfterID = afterID
	}

	rows, err := r.queries.ListWalletStatementPage(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list wallet statement page: %w", err)
	}

	entries := make([]application.StatementEntry, 0, len(rows))
	for _, row := range rows {
		operationType, err := domain.ParseOperationType(row.OperationType)
		if err != nil {
			return nil, fmt.Errorf("stored operation type is invalid: %w", err)
		}
		reference, err := domain.ParseReference(row.Reference)
		if err != nil {
			return nil, fmt.Errorf("stored reference is invalid: %w", err)
		}
		bucket, err := domain.ParseBucket(row.Bucket)
		if err != nil {
			return nil, fmt.Errorf("stored bucket is invalid: %w", err)
		}

		entries = append(entries, application.StatementEntry{
			TransactionID: uuidToString(row.ID),
			OperationID:   uuidToString(row.OperationID),
			OperationType: operationType,
			Reference:     reference,
			Bucket:        bucket,
			Amount:        row.Amount,
			CreatedAt:     row.CreatedAt.Time.UTC(),
		})
	}
	return entries, nil
}

// FreeCycleAnchor returns the persisted activation anchor of the free
// cycle.
func (r *Repository) FreeCycleAnchor(ctx context.Context, accountID domain.AccountID) (time.Time, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return time.Time{}, application.ErrWalletNotFound
	}

	anchor, err := r.queries.GetWalletFreeCycleAnchor(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, application.ErrWalletNotFound
		}
		return time.Time{}, fmt.Errorf("get free cycle anchor: %w", err)
	}
	return anchor.Time.UTC(), nil
}

// RenewFreePeriod atomically expires the remaining FREE_INK balance and
// grants the period franchise under the wallet lock. The period grant key is
// the idempotency marker: a retry returns Replayed without writing anything.
func (r *Repository) RenewFreePeriod(ctx context.Context, request application.FreeCycleRenewalRequest) (*application.PeriodRenewal, error) {
	pgUUID, err := pgUUIDFromAccountID(request.AccountID)
	if err != nil {
		return nil, application.ErrWalletNotFound
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	accountRow, err := qtx.GetWalletAccountForUpdate(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrWalletNotFound
		}
		return nil, fmt.Errorf("lock wallet account: %w", err)
	}

	// The grant operation is the period marker: its key is written exactly
	// once, together with the expiry, inside this transaction.
	grantOperation, err := qtx.GetWalletOperationByIdempotencyKey(ctx, request.GrantKey.String())
	switch {
	case err == nil:
		if uuidToString(grantOperation.AccountID) != request.AccountID.String() {
			return nil, application.ErrIdempotencyMismatch
		}
		return &application.PeriodRenewal{PeriodStart: request.PeriodStart, Replayed: true}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("load period grant operation: %w", err)
	}

	expired, err := domain.NewInk(accountRow.BalanceFree)
	if err != nil {
		return nil, fmt.Errorf("stored free balance is invalid: %w", err)
	}

	renewal := &application.PeriodRenewal{PeriodStart: request.PeriodStart, Granted: request.Franchise}
	if !expired.IsZero() {
		expireOperation, err := qtx.CreateWalletOperationIfAbsent(ctx, platformpg.CreateWalletOperationIfAbsentParams{
			AccountID:      pgUUID,
			OperationType:  domain.OperationExpireFree.String(),
			IdempotencyKey: request.ExpireKey.String(),
			Reference:      request.Reference.String(),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("free cycle expire key conflict without an operation")
		}
		if err != nil {
			return nil, fmt.Errorf("create free cycle expire operation: %w", err)
		}

		delta, err := domain.DirectionDebit.Apply(expired)
		if err != nil {
			return nil, err
		}
		if _, err := qtx.CreateWalletTransaction(ctx, platformpg.CreateWalletTransactionParams{
			OperationID: expireOperation.ID,
			Bucket:      domain.BucketFree.String(),
			Amount:      delta,
		}); err != nil {
			return nil, fmt.Errorf("create free cycle expire transaction: %w", err)
		}
		renewal.Expired = expired
	}

	grantOperation, err = qtx.CreateWalletOperationIfAbsent(ctx, platformpg.CreateWalletOperationIfAbsentParams{
		AccountID:      pgUUID,
		OperationType:  domain.OperationCreditFree.String(),
		IdempotencyKey: request.GrantKey.String(),
		Reference:      request.Reference.String(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("free cycle grant key conflict without an operation")
	}
	if err != nil {
		return nil, fmt.Errorf("create free cycle grant operation: %w", err)
	}
	if _, err := qtx.CreateWalletTransaction(ctx, platformpg.CreateWalletTransactionParams{
		OperationID: grantOperation.ID,
		Bucket:      domain.BucketFree.String(),
		Amount:      request.Franchise.Int64(),
	}); err != nil {
		return nil, fmt.Errorf("create free cycle grant transaction: %w", err)
	}

	netDelta := request.Franchise.Int64() - expired.Int64()
	if _, err := qtx.ApplyFreeBalanceDelta(ctx, platformpg.ApplyFreeBalanceDeltaParams{
		AccountID:   pgUUID,
		BalanceFree: netDelta,
	}); err != nil {
		return nil, fmt.Errorf("apply free balance delta: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return renewal, nil
}

// creditBalance adds the signed delta to the balance projection of the
// credited bucket.
func creditBalance(ctx context.Context, qtx *platformpg.Queries, accountID pgtype.UUID, request application.CreditRequest) error {
	switch request.Bucket {
	case domain.BucketFree:
		if _, err := qtx.CreditFreeBalance(ctx, platformpg.CreditFreeBalanceParams{
			AccountID:   accountID,
			BalanceFree: request.Delta,
		}); err != nil {
			return fmt.Errorf("credit free balance: %w", err)
		}
	case domain.BucketPurchased:
		if _, err := qtx.CreditPurchasedBalance(ctx, platformpg.CreditPurchasedBalanceParams{
			AccountID:        accountID,
			BalancePurchased: request.Delta,
		}); err != nil {
			return fmt.Errorf("credit purchased balance: %w", err)
		}
	default:
		return domain.ErrInvalidBucket
	}
	return nil
}

func mapOperationRow(row platformpg.AppWalletOperation) (*domain.Operation, error) {
	operationType, err := domain.ParseOperationType(row.OperationType)
	if err != nil {
		return nil, fmt.Errorf("stored operation type is invalid: %w", err)
	}
	idempotencyKey, err := domain.ParseIdempotencyKey(row.IdempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("stored idempotency key is invalid: %w", err)
	}
	reference, err := domain.ParseReference(row.Reference)
	if err != nil {
		return nil, fmt.Errorf("stored reference is invalid: %w", err)
	}

	reason := domain.Reason{}
	if row.Reason.Valid {
		reason, err = domain.ParseReason(row.Reason.String)
		if err != nil {
			return nil, fmt.Errorf("stored reason is invalid: %w", err)
		}
	}

	actor := domain.AccountID("")
	if row.ActorAccountID.Valid {
		actor = domain.AccountID(uuidToString(row.ActorAccountID))
	}

	return domain.ReconstituteOperation(
		domain.OperationID(uuidToString(row.ID)),
		domain.AccountID(uuidToString(row.AccountID)),
		operationType,
		idempotencyKey,
		reference,
		reason,
		actor,
		row.CreatedAt.Time,
	)
}

func operationReason(reason domain.Reason) pgtype.Text {
	if reason.IsZero() {
		return pgtype.Text{}
	}
	return pgtype.Text{String: reason.String(), Valid: true}
}

func operationActor(actor domain.AccountID) pgtype.UUID {
	if actor.IsZero() {
		return pgtype.UUID{}
	}
	pgUUID, err := pgUUIDFromAccountID(actor)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgUUID
}

func pgUUIDFromAccountID(id domain.AccountID) (pgtype.UUID, error) {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(id.String()); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid account id format: %w", err)
	}
	return pgUUID, nil
}

func uuidToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}
