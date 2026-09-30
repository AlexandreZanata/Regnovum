package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/search/application"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

var _ application.ArenaSearchRepository = (*Repository)(nil)
var _ application.ArgumentSearchRepository = (*Repository)(nil)

func (r *Repository) SearchArenas(ctx context.Context, query, language string, after *application.Cursor, limit int) ([]application.ArenaResult, error) {
	var score *float64
	var at *time.Time
	var id *string
	if after != nil {
		score = &after.Score
		instant := after.At.UTC()
		at = &instant
		id = &after.ID
	}

	// Selecting the configuration statically when a language filter is present
	// keeps the expression identical to the partial GIN index. The unfiltered
	// path remains language-aware through the CASE expression.
	vector := "CASE WHEN a.language = 'pt-BR' THEN 'portuguese'::regconfig ELSE 'english'::regconfig END"
	languageClause := ""
	if language == "pt-BR" {
		vector = "'portuguese'::regconfig"
		languageClause = "AND a.language = $2"
	} else if language == "en-US" {
		vector = "'english'::regconfig"
		languageClause = "AND a.language = $2"
	} else {
		// Unfiltered search stays language-aware through the CASE
		// expression and matches every language explicitly: binding $2
		// without referencing it breaks the query the same way an
		// empty filter broke it before (500 on a documented read).
		languageClause = "AND ($2 = '' OR a.language = $2)"
	}
	queryText := fmt.Sprintf(`
		WITH ranked AS (
			SELECT a.id, a.slug, a.statement, a.category, a.language, a.published_at,
			       ts_rank_cd(to_tsvector(%s, a.statement || ' ' || COALESCE(a.context, '')),
			           websearch_to_tsquery(%s, $1))::double precision AS score
			FROM app.arenas a
			WHERE a.status IN ('published', 'closed', 'restricted')
			  %s
			  AND to_tsvector(%s, a.statement || ' ' || COALESCE(a.context, '')) @@
			      websearch_to_tsquery(%s, $1)
		)
		SELECT id, slug, statement, category, language, published_at, score
		FROM ranked
		WHERE ($3::double precision IS NULL OR (score, published_at, id) < ($3::double precision, $4::timestamptz, $5::uuid))
		ORDER BY score DESC, published_at DESC, id DESC
		LIMIT $6`, vector, vector, languageClause, vector, vector)
	rows, err := r.pool.Query(ctx, queryText, query, language, score, at, id, limit)
	if err != nil {
		return nil, fmt.Errorf("search arenas: %w", err)
	}
	defer rows.Close()
	items := make([]application.ArenaResult, 0, limit)
	for rows.Next() {
		var item application.ArenaResult
		var published pgtype.Timestamptz
		if err := rows.Scan(&item.ID, &item.Slug, &item.Statement, &item.Category, &item.Language, &published, &item.Score); err != nil {
			return nil, fmt.Errorf("scan arena search result: %w", err)
		}
		if !published.Valid {
			return nil, fmt.Errorf("scan arena search result: missing publication time")
		}
		item.PublishedAt = published.Time.UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate arena search results: %w", err)
	}
	return items, nil
}

func (r *Repository) SearchArguments(ctx context.Context, query, language string, after *application.Cursor, limit int) ([]application.ArgumentResult, error) {
	var score *float64
	var at *time.Time
	var id *string
	if after != nil {
		score = &after.Score
		instant := after.At.UTC()
		at = &instant
		id = &after.ID
	}
	rows, err := r.pool.Query(ctx, `
		WITH ranked AS (
			SELECT arg.id, arg.arena_id, arg.relation, arg.content, arena.language, arg.created_at,
			       CASE WHEN arena.language = 'pt-BR' THEN
				   ts_rank_cd(to_tsvector('portuguese'::regconfig, arg.content), websearch_to_tsquery('portuguese'::regconfig, $1))
				ELSE
				   ts_rank_cd(to_tsvector('english'::regconfig, arg.content), websearch_to_tsquery('english'::regconfig, $1))
			       END::double precision AS score
			FROM app.arguments arg
			JOIN app.arenas arena ON arena.id = arg.arena_id
			WHERE arg.status = 'published'
			  AND arena.status IN ('published', 'closed', 'restricted')
			  AND ($2 = '' OR arena.language = $2)
			  AND (
				  (arena.language = 'pt-BR' AND to_tsvector('portuguese'::regconfig, arg.content) @@ websearch_to_tsquery('portuguese'::regconfig, $1))
				  OR (arena.language = 'en-US' AND to_tsvector('english'::regconfig, arg.content) @@ websearch_to_tsquery('english'::regconfig, $1))
			  )
		)
		SELECT id, arena_id, relation, content, language, created_at, score
		FROM ranked
		WHERE ($3::double precision IS NULL OR (score, created_at, id) < ($3::double precision, $4::timestamptz, $5::uuid))
		ORDER BY score DESC, created_at DESC, id DESC
		LIMIT $6`, query, language, score, at, id, limit)
	if err != nil {
		return nil, fmt.Errorf("search arguments: %w", err)
	}
	defer rows.Close()
	items := make([]application.ArgumentResult, 0, limit)
	for rows.Next() {
		var item application.ArgumentResult
		var created pgtype.Timestamptz
		if err := rows.Scan(&item.ID, &item.ArenaID, &item.Relation, &item.Content, &item.Language, &created, &item.Score); err != nil {
			return nil, fmt.Errorf("scan argument search result: %w", err)
		}
		if !created.Valid {
			return nil, fmt.Errorf("scan argument search result: missing creation time")
		}
		item.CreatedAt = created.Time.UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate argument search results: %w", err)
	}
	return items, nil
}
