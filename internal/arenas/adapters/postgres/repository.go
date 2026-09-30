// Package postgres is the PostgreSQL outbound adapter of the arenas module.
// It implements the private draft repository against the immutable Arena
// schema (migration 00012): every statement is scoped by the creator, and
// writes use the optimistic version check. Drafts never touch the Arena
// Pass ledger.
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

	"github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

const (
	pgUniqueViolation    = "23505"
	slugUniqueConstraint = "arenas_slug_unique"
)

// Repository implements the arenas application ports using PostgreSQL.
type Repository struct {
	pool    *pgxpool.Pool
	queries *platformpg.Queries
}

var _ application.ArenaRepository = (*Repository)(nil)

// NewRepository creates a PostgreSQL repository adapter for arenas.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{
		pool:    pool,
		queries: platformpg.New(pool),
	}
}

// queriesFor binds the queries to the caller transaction when one is
// carried by the context (P07-T05): the publication runs the pass
// consumption and the Arena transition in one transaction. Without a shared
// transaction the repository stays autocommit, one statement per call.
func (r *Repository) queriesFor(ctx context.Context) *platformpg.Queries {
	if tx, ok := platformpg.TxFromContext(ctx); ok {
		return r.queries.WithTx(tx)
	}
	return r.queries
}

// CreateArena stores a new private draft.
func (r *Repository) CreateArena(ctx context.Context, request application.CreateArenaRequest) (*domain.Arena, error) {
	creatorUUID, err := pgUUIDFromCreatorID(request.CreatorID)
	if err != nil {
		return nil, fmt.Errorf("create arena: %w", err)
	}

	row, err := r.queriesFor(ctx).CreateArena(ctx, platformpg.CreateArenaParams{
		CreatorID: creatorUUID,
		Statement: request.Statement.String(),
		Context:   textFromContext(request.Context),
		Category:  request.Category.String(),
		Language:  request.Language.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("create arena: %w", err)
	}
	return mapArenaRow(row)
}

// GetArenaForCreator returns the Arena owned by the creator; foreign and
// missing ids are indistinguishable.
func (r *Repository) GetArenaForCreator(ctx context.Context, arenaID domain.ArenaID, creatorID domain.CreatorID) (*domain.Arena, error) {
	arenaUUID, creatorUUID, err := arenaScope(arenaID, creatorID)
	if err != nil {
		return nil, err
	}

	row, err := r.queriesFor(ctx).GetArenaForCreator(ctx, platformpg.GetArenaForCreatorParams{
		ID:        arenaUUID,
		CreatorID: creatorUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrArenaNotFound
		}
		return nil, fmt.Errorf("get arena for creator: %w", err)
	}
	return mapArenaRow(row)
}

// ListArenaDraftsForCreator returns the creator's drafts, newest first.
func (r *Repository) ListArenaDraftsForCreator(ctx context.Context, creatorID domain.CreatorID) ([]domain.Arena, error) {
	creatorUUID, err := pgUUIDFromCreatorID(creatorID)
	if err != nil {
		return nil, fmt.Errorf("list arena drafts: %w", err)
	}

	rows, err := r.queriesFor(ctx).ListArenaDraftsForCreator(ctx, creatorUUID)
	if err != nil {
		return nil, fmt.Errorf("list arena drafts: %w", err)
	}

	drafts := make([]domain.Arena, 0, len(rows))
	for _, row := range rows {
		arena, err := mapArenaRow(row)
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, *arena)
	}
	return drafts, nil
}

// UpdateArenaDraft replaces the draft fields under the optimistic version
// check; a stale version affects no row.
func (r *Repository) UpdateArenaDraft(ctx context.Context, arenaID domain.ArenaID, creatorID domain.CreatorID, update application.DraftUpdate) (*domain.Arena, error) {
	arenaUUID, creatorUUID, err := arenaScope(arenaID, creatorID)
	if err != nil {
		return nil, err
	}

	row, err := r.queriesFor(ctx).UpdateArenaDraft(ctx, platformpg.UpdateArenaDraftParams{
		ID:        arenaUUID,
		CreatorID: creatorUUID,
		Statement: update.Statement.String(),
		Context:   textFromContext(update.Context),
		Category:  update.Category.String(),
		Language:  update.Language.String(),
		Version:   update.ExpectedVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, r.diagnoseDraftMiss(ctx, arenaUUID, creatorUUID, update.ExpectedVersion)
		}
		return nil, fmt.Errorf("update arena draft: %w", err)
	}
	return mapArenaRow(row)
}

// DeleteArenaDraft removes a draft owned by the creator.
func (r *Repository) DeleteArenaDraft(ctx context.Context, arenaID domain.ArenaID, creatorID domain.CreatorID) error {
	arenaUUID, creatorUUID, err := arenaScope(arenaID, creatorID)
	if err != nil {
		return err
	}

	rows, err := r.queriesFor(ctx).DeleteArenaDraft(ctx, platformpg.DeleteArenaDraftParams{
		ID:        arenaUUID,
		CreatorID: creatorUUID,
	})
	if err != nil {
		return fmt.Errorf("delete arena draft: %w", err)
	}
	if rows == 1 {
		return nil
	}
	return r.diagnoseDraftMiss(ctx, arenaUUID, creatorUUID, 0)
}

// PublishArenaDraft transitions the draft to published under the optimistic
// version check. It runs inside the publication transaction so the Arena row
// and the consumed pass commit together.
func (r *Repository) PublishArenaDraft(ctx context.Context, arenaID domain.ArenaID, creatorID domain.CreatorID, slug domain.Slug, publishedAt time.Time, expectedVersion int32) (*domain.Arena, error) {
	arenaUUID, creatorUUID, err := arenaScope(arenaID, creatorID)
	if err != nil {
		return nil, err
	}

	row, err := r.queriesFor(ctx).PublishArenaDraft(ctx, platformpg.PublishArenaDraftParams{
		ID:          arenaUUID,
		CreatorID:   creatorUUID,
		Slug:        pgtype.Text{String: slug.String(), Valid: true},
		PublishedAt: pgtype.Timestamptz{Time: publishedAt.UTC(), Valid: true},
		Version:     expectedVersion,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == slugUniqueConstraint {
			return nil, application.ErrSlugConflict
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, r.diagnosePublishMiss(ctx, arenaUUID, creatorUUID, expectedVersion)
		}
		return nil, fmt.Errorf("publish arena draft: %w", err)
	}
	return mapArenaRow(row)
}

// diagnosePublishMiss explains why the publication affected no row: missing
// or foreign Arena, a non-draft state, or a concurrent publication/version
// change.
func (r *Repository) diagnosePublishMiss(ctx context.Context, arenaUUID, creatorUUID pgtype.UUID, expectedVersion int32) error {
	state, err := r.queriesFor(ctx).GetArenaStateForCreator(ctx, platformpg.GetArenaStateForCreatorParams{
		ID:        arenaUUID,
		CreatorID: creatorUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrArenaNotFound
		}
		return fmt.Errorf("diagnose arena publication: %w", err)
	}

	if state.Status == string(domain.ArenaStatusPublished) {
		// A concurrent publication won: the retry resolves the replay.
		return application.ErrVersionConflict
	}
	if state.Status != string(domain.ArenaStatusDraft) {
		return domain.ErrInvalidStatusChange
	}
	if state.Version != expectedVersion {
		return application.ErrVersionConflict
	}
	return application.ErrVersionConflict
}

// GetArenaByID returns any Arena by identifier; moderation is not scoped by
// the creator.
func (r *Repository) GetArenaByID(ctx context.Context, arenaID domain.ArenaID) (*domain.Arena, error) {
	arenaUUID, err := pgUUIDFromArenaID(arenaID)
	if err != nil {
		return nil, application.ErrArenaNotFound
	}

	row, err := r.queriesFor(ctx).GetArenaByID(ctx, arenaUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrArenaNotFound
		}
		return nil, fmt.Errorf("get arena by id: %w", err)
	}
	return mapArenaRow(row)
}

// CloseArena performs the published→closed transition requested by the
// creator under the optimistic version check.
func (r *Repository) CloseArena(ctx context.Context, arenaID domain.ArenaID, creatorID domain.CreatorID, expectedVersion int32) (*domain.Arena, error) {
	arenaUUID, creatorUUID, err := arenaScope(arenaID, creatorID)
	if err != nil {
		return nil, err
	}

	row, err := r.queriesFor(ctx).CloseArena(ctx, platformpg.CloseArenaParams{
		ID:        arenaUUID,
		CreatorID: creatorUUID,
		Version:   expectedVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, r.diagnoseCloseMiss(ctx, arenaUUID, creatorUUID, expectedVersion)
		}
		return nil, fmt.Errorf("close arena: %w", err)
	}
	return mapArenaRow(row)
}

// RestrictArena applies the moderation restriction to a published or closed
// Arena under the optimistic version check.
func (r *Repository) RestrictArena(ctx context.Context, arenaID domain.ArenaID, expectedVersion int32) (*domain.Arena, error) {
	arenaUUID, err := pgUUIDFromArenaID(arenaID)
	if err != nil {
		return nil, application.ErrArenaNotFound
	}

	row, err := r.queriesFor(ctx).RestrictArena(ctx, platformpg.RestrictArenaParams{
		ID:      arenaUUID,
		Version: expectedVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, r.diagnoseModerationMiss(ctx, arenaUUID, expectedVersion, domain.ArenaStatusRestricted, domain.ArenaStatusPublished, domain.ArenaStatusClosed)
		}
		return nil, fmt.Errorf("restrict arena: %w", err)
	}
	return mapArenaRow(row)
}

// RemoveArena applies the moderation removal to a published, closed or
// restricted Arena under the optimistic version check.
func (r *Repository) RemoveArena(ctx context.Context, arenaID domain.ArenaID, expectedVersion int32) (*domain.Arena, error) {
	arenaUUID, err := pgUUIDFromArenaID(arenaID)
	if err != nil {
		return nil, application.ErrArenaNotFound
	}

	row, err := r.queriesFor(ctx).RemoveArena(ctx, platformpg.RemoveArenaParams{
		ID:      arenaUUID,
		Version: expectedVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, r.diagnoseModerationMiss(ctx, arenaUUID, expectedVersion, domain.ArenaStatusRemoved, domain.ArenaStatusPublished, domain.ArenaStatusClosed, domain.ArenaStatusRestricted)
		}
		return nil, fmt.Errorf("remove arena: %w", err)
	}
	return mapArenaRow(row)
}

// ListPublicArenas returns one keyset page of the public feed, newest
// first. The query itself limits candidates to publicly visible statuses:
// drafts and removed Arenas can never appear, whatever the filters.
func (r *Repository) ListPublicArenas(ctx context.Context, filter application.ArenaFeedFilter, after *application.FeedPosition, limit int) ([]domain.Arena, error) {
	params := platformpg.ListPublicArenasPageParams{PageLimit: int32(limit)}
	if filter.Language != nil {
		params.LanguageFilter = pgtype.Text{String: filter.Language.String(), Valid: true}
	}
	if filter.Category != nil {
		params.CategoryFilter = pgtype.Text{String: filter.Category.String(), Valid: true}
	}
	if filter.Status != nil {
		params.StatusFilter = pgtype.Text{String: filter.Status.String(), Valid: true}
	}
	if after != nil {
		var afterID pgtype.UUID
		if err := afterID.Scan(after.ArenaID); err != nil {
			return nil, application.ErrInvalidCursor
		}
		params.AfterPublishedAt = pgtype.Timestamptz{Time: after.PublishedAt.UTC(), Valid: true}
		params.AfterID = afterID
	}

	rows, err := r.queriesFor(ctx).ListPublicArenasPage(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list public arenas: %w", err)
	}

	arenas := make([]domain.Arena, 0, len(rows))
	for _, row := range rows {
		arena, err := mapArenaRow(row)
		if err != nil {
			return nil, err
		}
		arenas = append(arenas, *arena)
	}
	return arenas, nil
}

// GetPublicArenaBySlug resolves a public Arena address; drafts and removed
// Arenas are not found.
func (r *Repository) GetPublicArenaBySlug(ctx context.Context, slug domain.Slug) (*domain.Arena, error) {
	row, err := r.queriesFor(ctx).GetPublicArenaBySlug(ctx, pgtype.Text{String: slug.String(), Valid: true})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrArenaNotFound
		}
		return nil, fmt.Errorf("get public arena by slug: %w", err)
	}
	return mapArenaRow(row)
}

// GetArenaStatusBySlug reports the stored status of the Arena holding the
// slug, including removed (P08-T08); drafts never hold a slug.
func (r *Repository) GetArenaStatusBySlug(ctx context.Context, slug domain.Slug) (domain.ArenaStatus, error) {
	status, err := r.queriesFor(ctx).GetArenaStatusBySlug(ctx, pgtype.Text{String: slug.String(), Valid: true})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", application.ErrArenaNotFound
		}
		return "", fmt.Errorf("get arena status by slug: %w", err)
	}
	return domain.ArenaStatus(status), nil
}

// diagnoseCloseMiss explains why the closing affected no row.
func (r *Repository) diagnoseCloseMiss(ctx context.Context, arenaUUID, creatorUUID pgtype.UUID, expectedVersion int32) error {
	state, err := r.queriesFor(ctx).GetArenaStateForCreator(ctx, platformpg.GetArenaStateForCreatorParams{
		ID:        arenaUUID,
		CreatorID: creatorUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrArenaNotFound
		}
		return fmt.Errorf("diagnose arena closing: %w", err)
	}
	if state.Status != string(domain.ArenaStatusPublished) {
		return domain.ErrInvalidStatusChange
	}
	return application.ErrVersionConflict
}

// diagnoseModerationMiss explains why a moderation write affected no row:
// the target state means a concurrent identical decision (resolved as a
// replay by the retry), a non-moderable state is a transition violation and
// anything else is a concurrent change.
func (r *Repository) diagnoseModerationMiss(ctx context.Context, arenaUUID pgtype.UUID, expectedVersion int32, target domain.ArenaStatus, allowed ...domain.ArenaStatus) error {
	state, err := r.queriesFor(ctx).GetArenaStateByID(ctx, arenaUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrArenaNotFound
		}
		return fmt.Errorf("diagnose arena moderation: %w", err)
	}

	status := domain.ArenaStatus(state.Status)
	if status == target {
		return application.ErrVersionConflict
	}
	for _, candidate := range allowed {
		if status == candidate {
			return application.ErrVersionConflict
		}
	}
	return domain.ErrInvalidStatusChange
}

// diagnoseDraftMiss explains why a scoped draft write affected no row:
// missing or foreign Arena, a non-draft state or a concurrent change.
func (r *Repository) diagnoseDraftMiss(ctx context.Context, arenaUUID, creatorUUID pgtype.UUID, expectedVersion int32) error {
	state, err := r.queriesFor(ctx).GetArenaStateForCreator(ctx, platformpg.GetArenaStateForCreatorParams{
		ID:        arenaUUID,
		CreatorID: creatorUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrArenaNotFound
		}
		return fmt.Errorf("diagnose arena draft write: %w", err)
	}

	if state.Status != string(domain.ArenaStatusDraft) {
		return domain.ErrArenaNotDraft
	}
	if expectedVersion > 0 && state.Version != expectedVersion {
		return application.ErrVersionConflict
	}
	// A draft with the expected version rejected the write: a concurrent
	// transaction changed it between our statements.
	return application.ErrVersionConflict
}

func mapArenaRow(row platformpg.AppArena) (*domain.Arena, error) {
	policy := domain.ReconstitutionPolicy()

	statement, err := domain.ParseStatement(row.Statement, policy)
	if err != nil {
		return nil, fmt.Errorf("stored arena statement is invalid: %w", err)
	}

	context := domain.Context{}
	if row.Context.Valid {
		context, err = domain.ParseContext(row.Context.String, policy)
		if err != nil {
			return nil, fmt.Errorf("stored arena context is invalid: %w", err)
		}
	}

	category, err := domain.ParseCategory(row.Category)
	if err != nil {
		return nil, fmt.Errorf("stored arena category is invalid: %w", err)
	}
	language, err := domain.ParseLanguage(row.Language)
	if err != nil {
		return nil, fmt.Errorf("stored arena language is invalid: %w", err)
	}

	slug := domain.Slug{}
	if row.Slug.Valid {
		slug, err = domain.ParseSlug(row.Slug.String)
		if err != nil {
			return nil, fmt.Errorf("stored arena slug is invalid: %w", err)
		}
	}

	return domain.ReconstituteArena(
		domain.ArenaID(uuidToString(row.ID)),
		domain.CreatorID(uuidToString(row.CreatorID)),
		statement,
		context,
		category,
		language,
		domain.ArenaStatus(row.Status),
		slug,
		row.Version,
		row.CreatedAt.Time,
		timePtr(row.PublishedAt),
		timePtr(row.ClosesAt),
	)
}

func arenaScope(arenaID domain.ArenaID, creatorID domain.CreatorID) (pgtype.UUID, pgtype.UUID, error) {
	arenaUUID, err := pgUUIDFromArenaID(arenaID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, application.ErrArenaNotFound
	}
	creatorUUID, err := pgUUIDFromCreatorID(creatorID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, application.ErrArenaNotFound
	}
	return arenaUUID, creatorUUID, nil
}

func pgUUIDFromArenaID(id domain.ArenaID) (pgtype.UUID, error) {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(id.String()); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid arena id format: %w", err)
	}
	return pgUUID, nil
}

func pgUUIDFromCreatorID(id domain.CreatorID) (pgtype.UUID, error) {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(id.String()); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid creator id format: %w", err)
	}
	return pgUUID, nil
}

func textFromContext(context domain.Context) pgtype.Text {
	if context.IsZero() {
		return pgtype.Text{}
	}
	return pgtype.Text{String: context.String(), Valid: true}
}

func timePtr(instant pgtype.Timestamptz) *time.Time {
	if !instant.Valid {
		return nil
	}
	copied := instant.Time.UTC()
	return &copied
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
