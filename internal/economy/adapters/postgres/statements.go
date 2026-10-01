package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

var _ application.StatementRepository = (*Repository)(nil)

// authorizeStatement resolves the custody of one book and proves the
// caller owns it right now: unknown triples, system custodies without
// a holder, other holders and inactive owners are all refused before
// any leg is read.
func authorizeStatement(ctx context.Context, q rowQuerier, kind, label, caller, season string) (string, error) {
	var id string
	var ownerID *string
	var status *string
	err := q.QueryRow(ctx,
		`SELECT c.id::text, c.owner_account_id::text, a.status
		 FROM app.economy_custodies c
		 LEFT JOIN app.accounts a ON a.id = c.owner_account_id
		 WHERE c.kind = $1 AND c.label = $2 AND c.season_key = $3`,
		kind, label, season).Scan(&id, &ownerID, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrUnknownCustody
		}
		return "", fmt.Errorf("authorize statement: %w", err)
	}
	if ownerID == nil {
		return "", domain.ErrStatementForbidden
	}
	if *ownerID != caller {
		return "", domain.ErrStatementForbidden
	}
	if status == nil || *status != "active" {
		return "", domain.ErrStatementSuspended
	}
	return id, nil
}

// readStatementPage serves one journal page in entry order with keyset
// continuation: legs at or before the cursor never reappear, and legs
// after it are never skipped. The empty cursor opens the journal with a
// predicate-free query, so no empty text is ever cast to uuid.
func (r *Repository) readStatementPage(ctx context.Context, custodyID string, cursor application.StatementCursor, limit int) ([]application.StatementEntry, error) {
	const openPage = `SELECT e.id::text, e.transfer_id::text, e.direction, e.amount_milli, e.created_at
		 FROM app.economy_entries e
		 WHERE e.custody_id = $1::uuid
		 ORDER BY e.created_at, e.id
		 LIMIT $2`
	const nextPage = `SELECT e.id::text, e.transfer_id::text, e.direction, e.amount_milli, e.created_at
		 FROM app.economy_entries e
		 WHERE e.custody_id = $1::uuid
		   AND (e.created_at, e.id) > ($2, $3::uuid)
		 ORDER BY e.created_at, e.id
		 LIMIT $4`

	var rows pgx.Rows
	var err error
	if cursor.AfterID == "" {
		rows, err = r.pool.Query(ctx, openPage, custodyID, limit+1)
	} else {
		rows, err = r.pool.Query(ctx, nextPage, custodyID, cursor.AfterAt, cursor.AfterID, limit+1)
	}
	if err != nil {
		return nil, fmt.Errorf("read statement page: %w", err)
	}
	defer rows.Close()

	entries := []application.StatementEntry{}
	for rows.Next() {
		var entry application.StatementEntry
		var millis int64
		if err := rows.Scan(&entry.EntryID, &entry.TransferID, &entry.Direction, &millis, &entry.RecordedAt); err != nil {
			return nil, fmt.Errorf("scan statement entry: %w", err)
		}
		amount, err := domain.NewMilliInk(millis)
		if err != nil {
			return nil, fmt.Errorf("statement holds %d milliINK: %w", millis, err)
		}
		entry.Amount = amount
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate statement page: %w", err)
	}
	return entries, nil
}

// ReadStatement authorizes the caller and returns one page with the
// balance derived from the whole journal. Reading one extra leg tells
// whether the journal continues, without ever duplicating an entry.
func (r *Repository) ReadStatement(ctx context.Context, request application.StatementRequest) (*application.StatementPage, error) {
	custodyID, err := authorizeStatement(ctx, r.pool, request.Kind.String(), request.Label, request.CallerAccountID, request.Season.String())
	if err != nil {
		return nil, err
	}
	entries, err := r.readStatementPage(ctx, custodyID, request.Cursor, request.Limit)
	if err != nil {
		return nil, err
	}
	balance, err := r.custodyStatementBalance(ctx, custodyID)
	if err != nil {
		return nil, err
	}

	page := &application.StatementPage{CustodyID: custodyID, Balance: balance}
	if len(entries) > request.Limit {
		last := entries[request.Limit-1]
		page.NextCursor = application.StatementCursor{AfterAt: last.RecordedAt, AfterID: last.EntryID}.Encode()
		entries = entries[:request.Limit]
	}
	page.Entries = entries
	return page, nil
}

// custodyStatementBalance derives one custody balance from its legs:
// credits minus debits, read at one instant.
func (r *Repository) custodyStatementBalance(ctx context.Context, custodyID string) (domain.MilliInk, error) {
	var balance int64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_milli ELSE -amount_milli END), 0)
		 FROM app.economy_entries WHERE custody_id = $1::uuid`,
		custodyID).Scan(&balance)
	if err != nil {
		return domain.MilliInk{}, fmt.Errorf("derive balance: %w", err)
	}
	return domain.NewMilliInk(balance)
}

// RebuildAll re-derives every custody projection of one book from the
// journal in one pass, sealing each with its digest. The journal is
// only read.
func (r *Repository) RebuildAll(ctx context.Context, season domain.SeasonKey) ([]application.CustodyProjection, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT c.id::text, c.kind, c.label,
		        COALESCE(SUM(CASE e.direction WHEN 'credit' THEN e.amount_milli ELSE -e.amount_milli END), 0),
		        COUNT(e.id), COALESCE(MAX(e.id::text), '')
		 FROM app.economy_custodies c
		 LEFT JOIN app.economy_entries e ON e.custody_id = c.id
		 WHERE c.season_key = $1
		 GROUP BY c.id, c.kind, c.label
		 ORDER BY c.kind, c.label`, season.String())
	if err != nil {
		return nil, fmt.Errorf("rebuild projections: %w", err)
	}
	defer rows.Close()

	projections := []application.CustodyProjection{}
	for rows.Next() {
		var id, kindRaw, label, last string
		var balanceMillis, legs int64
		if err := rows.Scan(&id, &kindRaw, &label, &balanceMillis, &legs, &last); err != nil {
			return nil, fmt.Errorf("scan projection: %w", err)
		}
		kind, err := domain.ParseCustodyKind(kindRaw)
		if err != nil {
			return nil, fmt.Errorf("stored custody kind %q: %w", kindRaw, err)
		}
		balance, err := domain.NewMilliInk(balanceMillis)
		if err != nil {
			return nil, fmt.Errorf("projection holds %d milliINK: %w", balanceMillis, err)
		}
		projections = append(projections, application.SealProjection(id, kind, label, balance, legs, last))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projections: %w", err)
	}
	return projections, nil
}
