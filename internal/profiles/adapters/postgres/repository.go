// Package postgres is the PostgreSQL outbound adapter of the profiles
// module. It implements the application repositories against the app schema
// created by migration 00005 and never exposes rows beyond the domain model.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

const (
	pgUniqueViolation            = "23505"
	usernameNormalizedConstraint = "profiles_username_normalized_unique"
)

// Repository implements the profiles application ports using PostgreSQL.
type Repository struct {
	pool    *pgxpool.Pool
	queries *platformpg.Queries
}

var (
	_ application.ProfileRepository                  = (*Repository)(nil)
	_ application.ProfileQueryRepository             = (*Repository)(nil)
	_ application.CommunicationPreferencesRepository = (*Repository)(nil)
	_ application.AccountEligibility                 = (*Repository)(nil)
)

// NewRepository creates a PostgreSQL repository adapter for profiles.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{
		pool:    pool,
		queries: platformpg.New(pool),
	}
}

// queriesFor binds the queries to the caller transaction when one is
// carried by the context (P07-T05): the deletion workflow joins the shared
// transaction so anonymization and evidence commit or roll back together.
// Without a shared transaction the repository stays autocommit.
func (r *Repository) queriesFor(ctx context.Context) *platformpg.Queries {
	if tx, ok := platformpg.TxFromContext(ctx); ok {
		return r.queries.WithTx(tx)
	}
	return r.queries
}

// CreateProfileWithUsernameHistory atomically creates the profile and its
// first username history entry.
func (r *Repository) CreateProfileWithUsernameHistory(
	ctx context.Context,
	accountID domain.AccountID,
	username domain.Username,
	locale domain.Locale,
	changedAt time.Time,
) (*domain.Profile, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, fmt.Errorf("create profile: %w", application.ErrProfileNotFound)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	row, err := qtx.CreateProfile(ctx, platformpg.CreateProfileParams{
		AccountID:          pgUUID,
		Username:           username.String(),
		UsernameNormalized: username.Normalized(),
		InterfaceLocale:    locale.String(),
	})
	if err != nil {
		return nil, mapProfileWriteError(err)
	}

	if err := qtx.CreateUsernameHistoryEntry(ctx, platformpg.CreateUsernameHistoryEntryParams{
		AccountID:          pgUUID,
		Username:           username.String(),
		UsernameNormalized: username.Normalized(),
		ChangedAt:          timestamptz(changedAt),
	}); err != nil {
		return nil, fmt.Errorf("create username history entry: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return mapProfileRow(row)
}

// GetProfileByAccountID retrieves the profile owned by an account.
func (r *Repository) GetProfileByAccountID(ctx context.Context, accountID domain.AccountID) (*domain.Profile, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, application.ErrProfileNotFound
	}

	row, err := r.queries.GetProfileByAccountID(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrProfileNotFound
		}
		return nil, fmt.Errorf("get profile by account id: %w", err)
	}
	return mapProfileRow(row)
}

// LastUsernameChangeAt returns the most recent username audit instant, or
// the zero time when the account has no history yet.
func (r *Repository) LastUsernameChangeAt(ctx context.Context, accountID domain.AccountID) (time.Time, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return time.Time{}, application.ErrProfileNotFound
	}

	changedAt, err := r.queries.GetLastUsernameChangeAt(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("get last username change: %w", err)
	}
	if !changedAt.Valid {
		return time.Time{}, nil
	}
	return changedAt.Time, nil
}

// ApplyUsernameChange atomically applies a planned username change and
// appends its audit entry.
func (r *Repository) ApplyUsernameChange(ctx context.Context, accountID domain.AccountID, change domain.UsernameChange) (*domain.Profile, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, application.ErrProfileNotFound
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	row, err := qtx.UpdateProfileUsername(ctx, platformpg.UpdateProfileUsernameParams{
		AccountID:          pgUUID,
		Username:           change.Current.String(),
		UsernameNormalized: change.Current.Normalized(),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrProfileNotFound
		}
		return nil, mapProfileWriteError(err)
	}

	if err := qtx.CreateUsernameHistoryEntry(ctx, platformpg.CreateUsernameHistoryEntryParams{
		AccountID:          pgUUID,
		Username:           change.Current.String(),
		UsernameNormalized: change.Current.Normalized(),
		ChangedAt:          timestamptz(change.ChangedAt),
	}); err != nil {
		return nil, fmt.Errorf("create username history entry: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return mapProfileRow(row)
}

// UpdateProfileLocale replaces the interface locale preference.
func (r *Repository) UpdateProfileLocale(ctx context.Context, accountID domain.AccountID, locale domain.Locale, updatedAt time.Time) (*domain.Profile, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, application.ErrProfileNotFound
	}

	row, err := r.queries.UpdateProfileLocale(ctx, platformpg.UpdateProfileLocaleParams{
		AccountID:       pgUUID,
		InterfaceLocale: locale.String(),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrProfileNotFound
		}
		return nil, fmt.Errorf("update profile locale: %w", err)
	}
	return mapProfileRow(row)
}

// UpdateProfileTimezone replaces the optional IANA timezone preference; the
// zero timezone clears it.
func (r *Repository) UpdateProfileTimezone(ctx context.Context, accountID domain.AccountID, timezone domain.Timezone, updatedAt time.Time) (*domain.Profile, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, application.ErrProfileNotFound
	}

	stored := pgtype.Text{}
	if !timezone.IsZero() {
		stored = pgtype.Text{String: timezone.String(), Valid: true}
	}

	row, err := r.queries.UpdateProfileTimezone(ctx, platformpg.UpdateProfileTimezoneParams{
		AccountID: pgUUID,
		Timezone:  stored,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrProfileNotFound
		}
		return nil, fmt.Errorf("update profile timezone: %w", err)
	}
	return mapProfileRow(row)
}

// GetPublicProfileByUsername returns the explicit public projection of the
// profile that owns the normalized username. It deliberately selects only
// username, interface locale and creation instant: no email, no account
// identifier, no payment, antifraud or moderation data.
func (r *Repository) GetPublicProfileByUsername(ctx context.Context, normalizedUsername string) (*application.PublicProfile, error) {
	row, err := r.queries.GetPublicProfileByUsername(ctx, normalizedUsername)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrProfileNotFound
		}
		return nil, fmt.Errorf("get public profile: %w", err)
	}
	return &application.PublicProfile{
		Username:        row.Username,
		InterfaceLocale: row.InterfaceLocale,
		CreatedAt:       row.CreatedAt.Time.UTC(),
	}, nil
}

// GetPrivateProfileByAccountID returns the owner projection of a profile.
func (r *Repository) GetPrivateProfileByAccountID(ctx context.Context, accountID domain.AccountID) (*application.PrivateProfile, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, application.ErrProfileNotFound
	}

	row, err := r.queries.GetProfileByAccountID(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrProfileNotFound
		}
		return nil, fmt.Errorf("get private profile: %w", err)
	}
	return &application.PrivateProfile{
		Username:        row.Username,
		InterfaceLocale: row.InterfaceLocale,
		CreatedAt:       row.CreatedAt.Time.UTC(),
		UpdatedAt:       row.UpdatedAt.Time.UTC(),
	}, nil
}

// PreferencesFor returns the explicit communication preferences of the
// account: the interface locale owned by app.profiles joined with the stored
// opt-ins. A missing opt-in row resolves to the conservative default (false).
func (r *Repository) PreferencesFor(ctx context.Context, accountID domain.AccountID) (*application.CommunicationPreferences, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, application.ErrProfileNotFound
	}

	row, err := r.queries.GetCommunicationPreferencesByAccountID(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrProfileNotFound
		}
		return nil, fmt.Errorf("get communication preferences: %w", err)
	}
	return mapCommunicationPreferences(row.AccountID, row.InterfaceLocale, row.MarketingOptIn)
}

// SetMarketingOptIn atomically upserts the explicit marketing consent and
// appends its audit entry.
func (r *Repository) SetMarketingOptIn(ctx context.Context, accountID domain.AccountID, optIn bool, changedAt time.Time) (*application.CommunicationPreferences, error) {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, application.ErrProfileNotFound
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	if _, err := qtx.UpsertCommunicationPreferences(ctx, platformpg.UpsertCommunicationPreferencesParams{
		AccountID:      pgUUID,
		MarketingOptIn: optIn,
	}); err != nil {
		return nil, fmt.Errorf("upsert communication preferences: %w", err)
	}

	if err := qtx.CreateCommunicationPreferenceHistoryEntry(ctx, platformpg.CreateCommunicationPreferenceHistoryEntryParams{
		AccountID:      pgUUID,
		MarketingOptIn: optIn,
		ChangedAt:      timestamptz(changedAt),
	}); err != nil {
		return nil, fmt.Errorf("create communication preference history entry: %w", err)
	}

	row, err := qtx.GetCommunicationPreferencesByAccountID(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrProfileNotFound
		}
		return nil, fmt.Errorf("reload communication preferences: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return mapCommunicationPreferences(row.AccountID, row.InterfaceLocale, row.MarketingOptIn)
}

func mapCommunicationPreferences(accountID pgtype.UUID, interfaceLocale string, marketingOptIn bool) (*application.CommunicationPreferences, error) {
	locale, err := domain.ParseLocale(interfaceLocale)
	if err != nil {
		return nil, fmt.Errorf("stored interface locale is invalid: %w", err)
	}
	return &application.CommunicationPreferences{
		AccountID:       domain.AccountID(uuidToString(accountID)),
		InterfaceLocale: locale,
		MarketingOptIn:  marketingOptIn,
	}, nil
}

// EnsureEligible asserts the account exists, is active and has a verified
// email before it may own or mutate a profile.
func (r *Repository) EnsureEligible(ctx context.Context, accountID domain.AccountID) error {
	pgUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return application.ErrAccountNotEligible
	}

	eligible, err := r.queries.IsAccountEligibleForProfile(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrAccountNotEligible
		}
		return fmt.Errorf("check account eligibility: %w", err)
	}
	if !eligible.Bool || !eligible.Valid {
		return application.ErrAccountNotEligible
	}
	return nil
}

func mapProfileRow(row platformpg.AppProfile) (*domain.Profile, error) {
	username, err := domain.ParseUsername(row.Username)
	if err != nil {
		return nil, fmt.Errorf("stored username is invalid: %w", err)
	}
	if username.Normalized() != row.UsernameNormalized {
		return nil, errors.New("stored username does not match its normalized form")
	}

	locale, err := domain.ParseLocale(row.InterfaceLocale)
	if err != nil {
		return nil, fmt.Errorf("stored interface locale is invalid: %w", err)
	}

	timezone := domain.Timezone{}
	if row.Timezone.Valid {
		timezone, err = domain.ParseTimezone(row.Timezone.String)
		if err != nil {
			return nil, fmt.Errorf("stored timezone is invalid: %w", err)
		}
	}

	return domain.ReconstituteProfile(
		domain.AccountID(uuidToString(row.AccountID)),
		username,
		locale,
		timezone,
		row.CreatedAt.Time,
		row.UpdatedAt.Time,
	)
}

func mapProfileWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		if pgErr.ConstraintName == usernameNormalizedConstraint {
			return application.ErrUsernameTaken
		}
		return application.ErrProfileAlreadyExists
	}
	return fmt.Errorf("write profile: %w", err)
}

func pgUUIDFromAccountID(id domain.AccountID) (pgtype.UUID, error) {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(id.String()); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid account id format: %w", err)
	}
	return pgUUID, nil
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()}
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
