// Package postgres is the PostgreSQL outbound adapter of the moderation
// module (P13-T02, P19-T09). It implements the application RoleRepository and
// the write side of the administrative bootstrap against the assignment schema
// (migrations 00022 and 00023): one row per account, revoked assignments
// returned (not hidden) so the caller distinguishes "never authorized" from
// "revoked".
//
// The write side is what the local bootstrap command uses, and its lock is what
// makes "this installation has no administrator" a decision instead of a
// guess: every administration of the schema takes the same table lock inside
// its transaction, so two commands starting together cannot both insert first
// administrators.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// Repository implements the moderation application ports using PostgreSQL.
type Repository struct {
	pool    *pgxpool.Pool
	queries *platformpg.Queries
}

var _ application.RoleRepository = (*Repository)(nil)

// NewRepository creates a PostgreSQL repository adapter for moderation.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{
		pool:    pool,
		queries: platformpg.New(pool),
	}
}

var _ application.RoleAdministration = (*Repository)(nil)

// AssignmentFor resolves the administrative assignment of the account, or
// nil when the account holds none. Revocation is carried, not filtered: a
// revoked role in an active session still denies.
func (r *Repository) AssignmentFor(ctx context.Context, accountID domain.AccountID) (*application.RoleAssignment, error) {
	if accountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, fmt.Errorf("load assignment: %w", err)
	}

	row, err := r.queriesFor(ctx).GetAdminRoleByAccount(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load assignment: %w", err)
	}

	role, err := domain.ParseRole(row.Role)
	if err != nil {
		return nil, fmt.Errorf("stored role is invalid: %w", err)
	}

	return &application.RoleAssignment{
		AccountID: accountID,
		Role:      role,
		Revoked:   row.RevokedAt.Valid,
	}, nil
}

// LockAssignments takes the administrative write lock of the assignment
// schema, inside the caller transaction.
//
// The lock is the reason the bootstrap can decide "the installation has no
// administrator" and act on it: two concurrent commands would otherwise both
// read an empty table and both insert, and the schema would hold two first
// administrators. A table lock (rather than a row lock) is what covers the
// case, because the rows that make the decision are the ones that do not exist
// yet — and it is taken here, in the adapter, where the database is spoken to.
//
// It refuses to run without a transaction: a lock released at the end of a
// statement would protect nothing.
func (r *Repository) LockAssignments(ctx context.Context) error {
	tx, ok := platformpg.TxFromContext(ctx)
	if !ok {
		return application.ErrAdministrationOutsideTransaction
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE app.admin_roles IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock administrative assignments: %w", err)
	}
	return nil
}

// AnyActiveAssignment reports whether any account holds an active
// assignment, whatever the role.
func (r *Repository) AnyActiveAssignment(ctx context.Context) (bool, error) {
	has, err := r.queriesFor(ctx).HasActiveAdminRole(ctx)
	if err != nil {
		return false, fmt.Errorf("count active assignments: %w", err)
	}
	return has, nil
}

// Grant writes the assignment. A first grant inserts the row; a grant for an
// account whose assignment was revoked revives it, and the statement leaves
// granted_by and granted_at untouched because the schema declares the origin
// of an assignment immutable.
func (r *Repository) Grant(ctx context.Context, grant application.RoleGrantRequest) (*application.RoleGrantRecord, error) {
	if grant.AccountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	if !grant.Role.IsValid() {
		return nil, domain.ErrInvalidRole
	}
	account, err := pgUUIDFromAccountID(grant.AccountID)
	if err != nil {
		return nil, fmt.Errorf("grant assignment: %w", err)
	}
	grantedBy, err := pgUUIDFromAccountID(grant.GrantedBy)
	if err != nil {
		return nil, fmt.Errorf("grant assignment: %w", err)
	}

	row, err := r.queriesFor(ctx).GrantAdminRole(ctx, platformpg.GrantAdminRoleParams{
		AccountID: account,
		Role:      grant.Role.String(),
		GrantedBy: grantedBy,
		GrantedAt: pgtype.Timestamptz{Time: grant.GrantedAt.UTC(), Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("grant assignment: %w", err)
	}

	role, err := domain.ParseRole(row.Role)
	if err != nil {
		return nil, fmt.Errorf("stored role is invalid: %w", err)
	}
	accountID, err := domainAccountID(row.AccountID)
	if err != nil {
		return nil, fmt.Errorf("stored assignment is invalid: %w", err)
	}
	record := &application.RoleGrantRecord{
		AccountID: accountID,
		Role:      role,
		GrantedAt: row.GrantedAt.Time.UTC(),
	}
	if row.GrantedBy.Valid {
		grantedBy, err := domainAccountID(row.GrantedBy)
		if err != nil {
			return nil, fmt.Errorf("stored assignment is invalid: %w", err)
		}
		record.GrantedBy = grantedBy
	}
	return record, nil
}

// Revoke dates the revocation of an active assignment and reports whether this
// call was the one that dated it. A concurrent demotion therefore resolves as
// false instead of overwriting the instant the first one recorded.
func (r *Repository) Revoke(ctx context.Context, accountID domain.AccountID, revokedAt time.Time) (bool, error) {
	if accountID.IsZero() {
		return false, domain.ErrEmptyAccountID
	}
	account, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return false, fmt.Errorf("revoke assignment: %w", err)
	}

	rows, err := r.queriesFor(ctx).RevokeActiveAdminRole(ctx, platformpg.RevokeActiveAdminRoleParams{
		AccountID: account,
		RevokedAt: pgtype.Timestamptz{Time: revokedAt.UTC(), Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("revoke assignment: %w", err)
	}
	return rows == 1, nil
}

// queriesFor joins the caller transaction when the context carries one, so the
// reads and writes of the bootstrap happen on the connection that holds the
// lock.
func (r *Repository) queriesFor(ctx context.Context) *platformpg.Queries {
	if tx, ok := platformpg.TxFromContext(ctx); ok {
		return r.queries.WithTx(tx)
	}
	return r.queries
}

func pgUUIDFromAccountID(id domain.AccountID) (pgtype.UUID, error) {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(id.String()); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid account id format: %w", err)
	}
	return pgUUID, nil
}
