// Package postgres is the outbound PostgreSQL adapter of the arguments
// module (P10-T04). It joins the caller transaction when the context
// carries one, so the argument, its sources and the INK debit commit or
// roll back together; without a shared transaction each statement stays
// autocommit.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// Repository implements the arguments application ports using PostgreSQL.
type Repository struct {
	pool    *pgxpool.Pool
	queries *platformpg.Queries
}

var _ application.ArgumentRepository = (*Repository)(nil)

// NewRepository creates a PostgreSQL repository adapter for arguments.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{
		pool:    pool,
		queries: platformpg.New(pool),
	}
}

// queriesFor binds the queries to the caller transaction when one is
// carried by the context, so publication writes commit or roll back as one
// unit with the wallet debit.
func (r *Repository) queriesFor(ctx context.Context) *platformpg.Queries {
	if tx, ok := platformpg.TxFromContext(ctx); ok {
		return r.queries.WithTx(tx)
	}
	return r.queries
}

// CreateArgument inserts the argument under the author idempotency key.
func (r *Repository) CreateArgument(ctx context.Context, request application.CreateArgumentRequest) (*application.PublishedArgument, bool, error) {
	arenaParam, ok := uuidParam(request.ArenaID.String())
	if !ok {
		return nil, false, application.ErrArenaNotFound
	}
	authorParam, ok := uuidParam(request.AuthorID.String())
	if !ok {
		return nil, false, application.ErrAccountNotFound
	}
	parentParam := pgtype.UUID{}
	if !request.ParentID.IsZero() {
		parentParam, ok = uuidParam(request.ParentID.String())
		if !ok {
			return nil, false, application.ErrParentNotFound
		}
	}

	row, err := r.queriesFor(ctx).CreateArgument(ctx, platformpg.CreateArgumentParams{
		ArenaID:        arenaParam,
		AuthorID:       authorParam,
		ParentID:       parentParam,
		Relation:       request.Relation.String(),
		Content:        request.Content.String(),
		ContentHash:    request.Content.Hash().String(),
		GraphemeCost:   int32(request.Content.GraphemeCost()),
		IdempotencyKey: request.IdempotencyKey.String(),
		CreatedAt:      pgtype.Timestamptz{Time: request.CreatedAt.UTC(), Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The author already used this idempotency key: the caller
			// resolves the stored argument as a replay.
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("create argument: %w", err)
	}

	created, err := mapArgument(row.ID, row.ArenaID, row.AuthorID, row.ParentID, row.Relation, row.Content, row.ContentHash, row.GraphemeCost, row.Status, row.CreatedAt.Time, row.WithdrawnAt)
	if err != nil {
		return nil, false, err
	}
	return created, true, nil
}

// CreateArgumentSource attaches one structured source to an argument.
func (r *Repository) CreateArgumentSource(ctx context.Context, argumentID domain.ArgumentID, source domain.Source, at time.Time) error {
	argumentParam, ok := uuidParam(argumentID.String())
	if !ok {
		return application.ErrArgumentNotFound
	}

	description := pgtype.Text{}
	if source.HasDescription() {
		description = pgtype.Text{String: source.Description(), Valid: true}
	}

	if _, err := r.queriesFor(ctx).CreateArgumentSource(ctx, platformpg.CreateArgumentSourceParams{
		ArgumentID:  argumentParam,
		Url:         source.URL(),
		Description: description,
		CreatedAt:   pgtype.Timestamptz{Time: at.UTC(), Valid: true},
	}); err != nil {
		return fmt.Errorf("create argument source: %w", err)
	}
	return nil
}

// GetByAuthorAndIdempotencyKey resolves the argument recorded under one
// attempt key.
func (r *Repository) GetByAuthorAndIdempotencyKey(ctx context.Context, authorID domain.AccountID, key domain.IdempotencyKey) (*application.PublishedArgument, error) {
	authorParam, ok := uuidParam(authorID.String())
	if !ok {
		return nil, application.ErrArgumentNotFound
	}

	row, err := r.queriesFor(ctx).GetArgumentByAuthorAndKey(ctx, platformpg.GetArgumentByAuthorAndKeyParams{
		AuthorID:       authorParam,
		IdempotencyKey: key.String(),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrArgumentNotFound
		}
		return nil, fmt.Errorf("get argument by key: %w", err)
	}
	return mapArgument(row.ID, row.ArenaID, row.AuthorID, row.ParentID, row.Relation, row.Content, row.ContentHash, row.GraphemeCost, row.Status, row.CreatedAt.Time, row.WithdrawnAt)
}

// GetParent returns one argument together with its derived depth.
func (r *Repository) GetParent(ctx context.Context, argumentID domain.ArgumentID) (*application.PublishedArgument, int, error) {
	argumentParam, ok := uuidParam(argumentID.String())
	if !ok {
		return nil, 0, application.ErrArgumentNotFound
	}

	row, err := r.queriesFor(ctx).GetParentArgument(ctx, argumentParam)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, application.ErrArgumentNotFound
		}
		return nil, 0, fmt.Errorf("get parent argument: %w", err)
	}
	parent, err := mapArgument(row.ID, row.ArenaID, row.AuthorID, row.ParentID, row.Relation, row.Content, row.ContentHash, row.GraphemeCost, row.Status, row.CreatedAt.Time, row.WithdrawnAt)
	if err != nil {
		return nil, 0, err
	}
	return parent, int(row.Depth), nil
}

// GetForAuthor returns one argument scoped to its author.
func (r *Repository) GetForAuthor(ctx context.Context, argumentID domain.ArgumentID, authorID domain.AccountID) (*application.PublishedArgument, error) {
	argumentParam, ok := uuidParam(argumentID.String())
	if !ok {
		return nil, application.ErrArgumentNotFound
	}
	authorParam, ok := uuidParam(authorID.String())
	if !ok {
		return nil, application.ErrArgumentNotFound
	}

	row, err := r.queriesFor(ctx).GetArgumentForAuthor(ctx, platformpg.GetArgumentForAuthorParams{
		ArgumentID: argumentParam,
		AuthorID:   authorParam,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrArgumentNotFound
		}
		return nil, fmt.Errorf("get argument for author: %w", err)
	}
	return mapArgument(row.ID, row.ArenaID, row.AuthorID, row.ParentID, row.Relation, row.Content, row.ContentHash, row.GraphemeCost, row.Status, row.CreatedAt.Time, row.WithdrawnAt)
}

// WithdrawArgument moves a published argument to withdrawn under the author
// scope, recording the withdrawal instant once.
func (r *Repository) WithdrawArgument(ctx context.Context, argumentID domain.ArgumentID, authorID domain.AccountID, at time.Time) (*application.PublishedArgument, bool, error) {
	argumentParam, ok := uuidParam(argumentID.String())
	if !ok {
		return nil, false, application.ErrArgumentNotFound
	}
	authorParam, ok := uuidParam(authorID.String())
	if !ok {
		return nil, false, application.ErrArgumentNotFound
	}

	row, err := r.queriesFor(ctx).WithdrawArgument(ctx, platformpg.WithdrawArgumentParams{
		ArgumentID:  argumentParam,
		AuthorID:    authorParam,
		WithdrawnAt: pgtype.Timestamptz{Time: at.UTC(), Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The status moved concurrently: the caller re-reads.
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("withdraw argument: %w", err)
	}
	updated, err := mapArgument(row.ID, row.ArenaID, row.AuthorID, row.ParentID, row.Relation, row.Content, row.ContentHash, row.GraphemeCost, row.Status, row.CreatedAt.Time, row.WithdrawnAt)
	if err != nil {
		return nil, false, err
	}
	return updated, true, nil
}

// mapArgument rebuilds the stored projection from row fields, validating
// the same invariants the domain enforces (including the content hash).
func mapArgument(
	id, arenaID, authorID, parentID pgtype.UUID,
	relation, content, contentHash string,
	graphemeCost int32,
	status string,
	createdAt time.Time,
	withdrawnAt pgtype.Timestamptz,
) (*application.PublishedArgument, error) {
	argumentID, err := domain.ParseArgumentID(uuidToString(id))
	if err != nil {
		return nil, fmt.Errorf("stored argument id is invalid: %w", err)
	}
	arena, err := domain.ParseArenaID(uuidToString(arenaID))
	if err != nil {
		return nil, fmt.Errorf("stored arena id is invalid: %w", err)
	}
	author, err := domain.ParseAccountID(uuidToString(authorID))
	if err != nil {
		return nil, fmt.Errorf("stored author id is invalid: %w", err)
	}
	parent := domain.ArgumentID{}
	if parentID.Valid {
		parent, err = domain.ParseArgumentID(uuidToString(parentID))
		if err != nil {
			return nil, fmt.Errorf("stored parent id is invalid: %w", err)
		}
	}
	storedRelation, err := domain.ParseRelation(relation)
	if err != nil {
		return nil, fmt.Errorf("stored relation is invalid: %w", err)
	}
	hash, err := domain.ParseContentHash(contentHash)
	if err != nil {
		return nil, fmt.Errorf("stored content hash is invalid: %w", err)
	}
	storedContent, err := domain.ReconstituteContent(content, int(graphemeCost), hash)
	if err != nil {
		return nil, fmt.Errorf("stored content is invalid: %w", err)
	}

	var withdrawn *time.Time
	if withdrawnAt.Valid {
		instant := withdrawnAt.Time
		withdrawn = &instant
	}

	return &application.PublishedArgument{
		ID:          argumentID,
		ArenaID:     arena,
		AuthorID:    author,
		ParentID:    parent,
		Relation:    storedRelation,
		Content:     storedContent,
		Status:      status,
		CreatedAt:   createdAt,
		WithdrawnAt: withdrawn,
	}, nil
}

// uuidParam parses a canonical UUID string into its database parameter.
func uuidParam(raw string) (pgtype.UUID, bool) {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		return pgtype.UUID{}, false
	}
	return id, id.Valid
}

// uuidToString renders a database UUID in canonical form.
func uuidToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

// ListArenaArguments returns published top-level arguments of one relation
// in one Arena, newest first, strictly older than the position.
func (r *Repository) ListArenaArguments(ctx context.Context, arenaID domain.ArenaID, relation domain.Relation, after *application.ArgumentPosition, limit int) ([]application.PublicArgument, error) {
	arenaParam, ok := uuidParam(arenaID.String())
	if !ok {
		return nil, application.ErrArgumentNotFound
	}
	afterCreatedAt, afterID, err := argumentCursorParams(after)
	if err != nil {
		return nil, err
	}

	rows, err := r.queriesFor(ctx).ListArenaArgumentsPage(ctx, platformpg.ListArenaArgumentsPageParams{
		ArenaID:        arenaParam,
		Relation:       relation.String(),
		AfterCreatedAt: afterCreatedAt,
		AfterID:        afterID,
		PageLimit:      int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list arena arguments: %w", err)
	}

	arguments := make([]application.PublicArgument, 0, len(rows))
	for _, row := range rows {
		argument, err := mapPublicArgument(row.ID, row.ArenaID, row.ParentID, row.Relation, row.Content, row.ContentHash, row.GraphemeCost, row.Status, row.CreatedAt.Time, row.ReplyCount)
		if err != nil {
			return nil, err
		}
		arguments = append(arguments, *argument)
	}
	return arguments, nil
}

// ListReplies returns published replies of one parent, newest first,
// strictly older than the position.
func (r *Repository) ListReplies(ctx context.Context, parentID domain.ArgumentID, after *application.ArgumentPosition, limit int) ([]application.PublicArgument, error) {
	parentParam, ok := uuidParam(parentID.String())
	if !ok {
		return nil, application.ErrArgumentNotFound
	}
	afterCreatedAt, afterID, err := argumentCursorParams(after)
	if err != nil {
		return nil, err
	}

	rows, err := r.queriesFor(ctx).ListRepliesPage(ctx, platformpg.ListRepliesPageParams{
		ParentID:       parentParam,
		AfterCreatedAt: afterCreatedAt,
		AfterID:        afterID,
		PageLimit:      int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list replies: %w", err)
	}

	arguments := make([]application.PublicArgument, 0, len(rows))
	for _, row := range rows {
		// Replies carry no derived count: the depth policy forbids
		// grandchildren, so the reply count is structurally zero.
		argument, err := mapPublicArgument(row.ID, row.ArenaID, row.ParentID, row.Relation, row.Content, row.ContentHash, row.GraphemeCost, row.Status, row.CreatedAt.Time, 0)
		if err != nil {
			return nil, err
		}
		arguments = append(arguments, *argument)
	}
	return arguments, nil
}

// GetPublicArgument resolves one argument for the public surface.
func (r *Repository) GetPublicArgument(ctx context.Context, argumentID domain.ArgumentID) (*application.PublicArgument, error) {
	argumentParam, ok := uuidParam(argumentID.String())
	if !ok {
		return nil, application.ErrArgumentNotFound
	}

	row, err := r.queriesFor(ctx).GetPublicArgument(ctx, argumentParam)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrArgumentNotFound
		}
		return nil, fmt.Errorf("get public argument: %w", err)
	}
	return mapPublicArgument(row.ID, row.ArenaID, row.ParentID, row.Relation, row.Content, row.ContentHash, row.GraphemeCost, row.Status, row.CreatedAt.Time, row.ReplyCount)
}

// argumentCursorParams renders the optional keyset position.
func argumentCursorParams(after *application.ArgumentPosition) (pgtype.Timestamptz, pgtype.UUID, error) {
	if after == nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}, nil
	}
	afterID, ok := uuidParam(after.ArgumentID)
	if !ok {
		return pgtype.Timestamptz{}, pgtype.UUID{}, application.ErrInvalidCursor
	}
	return pgtype.Timestamptz{Time: after.CreatedAt.UTC(), Valid: true}, afterID, nil
}

// mapPublicArgument rebuilds the cache-safe public projection, applying the
// visibility policy: a withdrawn argument resolves as a retracted
// placeholder (no content), while published content is revalidated against
// its canonical hash.
func mapPublicArgument(
	id, arenaID, parentID pgtype.UUID,
	relation, content, contentHash string,
	graphemeCost int32,
	status string,
	createdAt time.Time,
	replyCount int64,
) (*application.PublicArgument, error) {
	argumentID, err := domain.ParseArgumentID(uuidToString(id))
	if err != nil {
		return nil, fmt.Errorf("stored argument id is invalid: %w", err)
	}
	arena, err := domain.ParseArenaID(uuidToString(arenaID))
	if err != nil {
		return nil, fmt.Errorf("stored arena id is invalid: %w", err)
	}
	parent := domain.ArgumentID{}
	if parentID.Valid {
		parent, err = domain.ParseArgumentID(uuidToString(parentID))
		if err != nil {
			return nil, fmt.Errorf("stored parent id is invalid: %w", err)
		}
	}
	storedRelation, err := domain.ParseRelation(relation)
	if err != nil {
		return nil, fmt.Errorf("stored relation is invalid: %w", err)
	}

	projection := &application.PublicArgument{
		ID:         argumentID,
		ArenaID:    arena,
		ParentID:   parent,
		Relation:   storedRelation,
		Status:     status,
		CreatedAt:  createdAt,
		ReplyCount: replyCount,
	}

	// Retraction policy: only published content is projected; a withdrawn
	// argument keeps its identity and status so the UI can render the
	// placeholder coherently.
	if status == "published" {
		hash, err := domain.ParseContentHash(contentHash)
		if err != nil {
			return nil, fmt.Errorf("stored content hash is invalid: %w", err)
		}
		storedContent, err := domain.ReconstituteContent(content, int(graphemeCost), hash)
		if err != nil {
			return nil, fmt.Errorf("stored content is invalid: %w", err)
		}
		projection.Content = &storedContent
	}

	return projection, nil
}
