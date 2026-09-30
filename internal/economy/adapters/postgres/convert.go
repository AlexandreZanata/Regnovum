package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var _ application.ConversionRepository = (*Repository)(nil)

// settledTerms are the recorded opt-in terms re-read inside the
// settlement transaction: nothing converts on stale reads.
type settledTerms struct {
	optInID  string
	quantity domain.MilliInk
	rate     domain.ConversionRate
}

// conversionHash binds the settled outcome: opt-in, holder, converted
// amount and rate. A retry recomputes it identically; anything else is
// a different conversion and never a replay.
func conversionHash(optInID, account string, converted, rateNum, rateDen int64) string {
	canonical := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%d", optInID, account, converted, rateNum, rateDen)
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

// Convert settles one recorded opt-in across both books in a single
// transaction: the legacy debit extinguishes the right while the
// Treasury credit pays it in converted milliINK. Genesis supply never
// grows: every converted subunit leaves Treasury stock first. A unique
// collision retries once through re-lookup, so concurrent runs of one
// opt-in resolve the single settlement instead of duplicating it.
func (r *Repository) Convert(ctx context.Context, request application.ConversionRequest) (*application.ConversionResult, error) {
	if replayed, err := r.lookupConversion(ctx, request); err != nil || replayed != nil {
		return replayed, err
	}
	var last error
	for range 2 {
		result, err := r.createConversion(ctx, request)
		if err == nil {
			return result, nil
		}
		if !isUniqueConflict(err) {
			return nil, err
		}
		last = err
		if replayed, err := r.lookupConversion(ctx, request); err != nil || replayed != nil {
			return replayed, err
		}
	}
	return nil, fmt.Errorf("conversion unsettled after conflict: %w", last)
}

// lookupConversion resolves a settled conversion without writing: the
// post-commit retry path that makes crash recovery exactly-once. Terms
// that settle nothing resolve to absence, never to a guess.
func (r *Repository) lookupConversion(ctx context.Context, request application.ConversionRequest) (*application.ConversionResult, error) {
	terms, err := r.readOptInTerms(ctx, request.AccountID, request.Charter.String())
	if err != nil || terms == nil {
		return nil, err
	}
	var transferID string
	var millis int64
	var hash string
	err = r.pool.QueryRow(ctx,
		`SELECT transfer_id::text, amount_milli, payload_hash FROM app.economy_intentions
		 WHERE intention_key = $1 AND actor = $2 AND operation = 'convert'`,
		terms.optInID, request.AccountID).Scan(&transferID, &millis, &hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup conversion: %w", err)
	}
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		return nil, fmt.Errorf("stored conversion holds %d milliINK: %w", millis, err)
	}
	expected := conversionHash(terms.optInID, request.AccountID, amount.Millis(), terms.rate.Num, terms.rate.Den)
	if hash != expected {
		return nil, domain.ErrIntentionConflict
	}
	return &application.ConversionResult{TransferID: transferID, Converted: amount, Replayed: true}, nil
}

// readOptInTerms resolves the recorded intent without locking: absence
// is a normal answer here, failures are not.
func (r *Repository) readOptInTerms(ctx context.Context, accountID, charter string) (*settledTerms, error) {
	var terms settledTerms
	var quantity, rateNum, rateDen int64
	err := r.pool.QueryRow(ctx,
		`SELECT id::text, quantity_milli, rate_num, rate_den FROM app.economy_optins
		 WHERE account_id = $1::uuid AND charter_version = $2`,
		accountID, charter).Scan(&terms.optInID, &quantity, &rateNum, &rateDen)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read opt-in terms: %w", err)
	}
	if terms.quantity, err = domain.NewMilliInk(quantity); err != nil {
		return nil, fmt.Errorf("stored intent holds %d milliINK: %w", quantity, err)
	}
	if terms.rate, err = domain.ParseConversionRate(rateNum, rateDen); err != nil {
		return nil, fmt.Errorf("stored intent holds rate %d/%d: %w", rateNum, rateDen, err)
	}
	return &terms, nil
}

// FindTerms resolves the recorded intent for one account and charter
// for the conversion use case. Callers only ever see their own account.
func (r *Repository) FindTerms(ctx context.Context, accountID string, charter domain.CharterVersion) (*application.OptInTermsView, error) {
	terms, err := r.readOptInTerms(ctx, accountID, charter.String())
	if err != nil || terms == nil {
		return nil, err
	}
	var validTill time.Time
	if err := r.pool.QueryRow(ctx,
		`SELECT valid_until FROM app.economy_optins WHERE id = $1::uuid`,
		terms.optInID).Scan(&validTill); err != nil {
		return nil, fmt.Errorf("read opt-in validity: %w", err)
	}
	return &application.OptInTermsView{
		OptInID: terms.optInID, Quantity: terms.quantity, Rate: terms.rate, ValidTill: validTill,
	}, nil
}

// createConversion attempts the settlement once: revalidated terms,
// both journals, and the idempotent intention share one transaction.
func (r *Repository) createConversion(ctx context.Context, request application.ConversionRequest) (*application.ConversionResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin conversion transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := requireUnfrozen(ctx, tx); err != nil {
		return nil, err
	}
	terms, err := r.lockConversionTerms(ctx, tx, request)
	if err != nil {
		return nil, err
	}
	units, err := domain.LegacyUnitsOf(terms.quantity)
	if err != nil {
		return nil, err
	}
	converted, err := domain.ConvertedMillis(units, terms.rate)
	if err != nil {
		return nil, err
	}
	if err := r.extinguishLegacy(ctx, tx, request.AccountID, terms.optInID, units); err != nil {
		return nil, err
	}
	transferID, err := r.payFromTreasury(ctx, tx, request.AccountID, converted)
	if err != nil {
		return nil, err
	}
	hash := conversionHash(terms.optInID, request.AccountID, converted.Millis(), terms.rate.Num, terms.rate.Den)
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.economy_intentions (intention_key, actor, operation, payload_hash, transfer_id, amount_milli)
		 VALUES ($1, $2, 'convert', $3, $4::uuid, $5)`,
		terms.optInID, request.AccountID, hash, transferID, converted.Millis()); err != nil {
		return nil, fmt.Errorf("record conversion: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit conversion: %w", err)
	}
	return &application.ConversionResult{TransferID: transferID, Converted: converted}, nil
}

// lockConversionTerms re-reads the opt-in under lock and matches the
// caller rate, the standing acceptance and the live validity exactly:
// divergent, lapsed or unaccepted terms never reach the journals.
func (r *Repository) lockConversionTerms(ctx context.Context, tx pgx.Tx, request application.ConversionRequest) (settledTerms, error) {
	var terms settledTerms
	var quantity, rateNum, rateDen int64
	var accepted bool
	var valid bool
	err := tx.QueryRow(ctx,
		`SELECT o.id::text, o.quantity_milli, o.rate_num, o.rate_den,
		        o.valid_until > now(),
		        EXISTS (SELECT 1 FROM app.economy_charter_consents c
		                WHERE c.account_id = o.account_id AND c.charter_version = o.charter_version AND c.decision = 'accepted')
		 FROM app.economy_optins o
		 WHERE o.account_id = $1::uuid AND o.charter_version = $2 FOR UPDATE OF o`,
		request.AccountID, request.Charter.String(),
	).Scan(&terms.optInID, &quantity, &rateNum, &rateDen, &valid, &accepted)
	if err != nil {
		return settledTerms{}, domain.ErrOptInMissing
	}
	var parseErr error
	if terms.quantity, parseErr = domain.NewMilliInk(quantity); parseErr != nil {
		return settledTerms{}, fmt.Errorf("stored intent holds %d milliINK: %w", quantity, parseErr)
	}
	if terms.rate, parseErr = domain.ParseConversionRate(rateNum, rateDen); parseErr != nil {
		return settledTerms{}, fmt.Errorf("stored intent holds rate %d/%d: %w", rateNum, rateDen, parseErr)
	}
	if !accepted {
		return settledTerms{}, domain.ErrConsentRequired
	}
	if !terms.rate.Equals(request.Rate) {
		return settledTerms{}, domain.ErrRateMismatch
	}
	if !valid {
		return settledTerms{}, domain.ErrOptInExpired
	}
	return terms, nil
}

// extinguishLegacy debits the opted legacy units free-first and rewrites
// the cached projection in the rows it locked, so concurrent conversions
// serialize on the wallet row instead of double-spending it.
func (r *Repository) extinguishLegacy(ctx context.Context, tx pgx.Tx, accountID, optInID string, units int64) error {
	var free, purchased int64
	if err := tx.QueryRow(ctx,
		`SELECT balance_free, balance_purchased FROM app.wallet_accounts WHERE account_id = $1::uuid FOR UPDATE`,
		accountID).Scan(&free, &purchased); err != nil {
		return domain.ErrInsufficientMilliInk
	}
	freeTake := min(free, units)
	purchasedTake := units - freeTake
	if purchasedTake < 0 || free+purchased < units {
		return domain.ErrInsufficientMilliInk
	}
	var operation string
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference)
		 VALUES ($1::uuid, 'debit_conversion', $2, $3) RETURNING id::text`,
		accountID, "convert-"+optInID, optInID).Scan(&operation); err != nil {
		return fmt.Errorf("record conversion operation: %w", err)
	}
	for _, leg := range []struct {
		bucket string
		take   int64
	}{
		{"FREE_INK", freeTake},
		{"PURCHASED_INK", purchasedTake},
	} {
		if leg.take == 0 {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.wallet_transactions (operation_id, bucket, amount) VALUES ($1::uuid, $2, $3)`,
			operation, leg.bucket, -leg.take); err != nil {
			return fmt.Errorf("record legacy %s leg: %w", leg.bucket, err)
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE app.wallet_accounts SET balance_free = balance_free - $2, balance_purchased = balance_purchased - $3
		 WHERE account_id = $1::uuid`,
		accountID, freeTake, purchasedTake); err != nil {
		return fmt.Errorf("rewrite legacy projection: %w", err)
	}
	return nil
}

// payFromTreasury moves converted milliINK from Treasury stock to a
// holder custody created for the account. Stock is rechecked inside the
// row locks: an empty Treasury refuses instead of minting.
func (r *Repository) payFromTreasury(ctx context.Context, tx pgx.Tx, accountID string, converted domain.MilliInk) (string, error) {
	// Legacy conversion binds the compat book: per-family season
	// references land in P46-T05 with their own tests.
	var treasury string
	if err := tx.QueryRow(ctx,
		`SELECT id::text FROM app.economy_custodies WHERE kind = 'treasury' AND label = 'main' AND season_key = $1`,
		domain.CompatSeasonKey).Scan(&treasury); err != nil {
		return "", fmt.Errorf("resolve treasury: %w", err)
	}
	var holder string
	if err := tx.QueryRow(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('user', $1, $2)
		 ON CONFLICT (kind, label, season_key) DO NOTHING RETURNING id::text`,
		accountID, domain.CompatSeasonKey).Scan(&holder); err != nil {
		if err := tx.QueryRow(ctx,
			`SELECT id::text FROM app.economy_custodies WHERE kind = 'user' AND label = $1 AND season_key = $2`,
			accountID, domain.CompatSeasonKey).Scan(&holder); err != nil {
			return "", fmt.Errorf("resolve holder custody: %w", err)
		}
	}
	if err := lockCustodies(ctx, tx, treasury, holder); err != nil {
		return "", err
	}
	balance, err := custodyBalance(ctx, tx, treasury)
	if err != nil {
		return "", err
	}
	if _, err := balance.Sub(converted); err != nil {
		return "", domain.ErrInsufficientMilliInk
	}
	var transferID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&transferID); err != nil {
		return "", fmt.Errorf("generate transfer id: %w", err)
	}
	if err := moveLegs(ctx, tx, legMove{transferID: transferID, fromID: treasury, toID: holder, millis: converted.Millis(), season: domain.CompatSeasonKey}); err != nil {
		return "", err
	}
	return transferID, nil
}

// isUniqueConflict reports whether err is a unique collision that a
// re-lookup can resolve: a concurrent run of the same opt-in, or a
// transfer id collision getting a fresh id on retry.
func isUniqueConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == intentionTripleConstraint ||
		pgErr.ConstraintName == intentionTransferConstraint ||
		pgErr.ConstraintName == "wallet_operations_idempotency_key_unique"
}
