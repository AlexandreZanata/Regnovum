package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/transparency/application"
)

var _ application.ArenaExportRepository = (*Repository)(nil)

// GetArenaExportHeader implements the approved read-only export projection:
// one statement resolves the publicly readable Arena and every aggregate
// count. Unknown, draft and removed Arenas are ErrArenaNotFound, exactly as
// other public Arena reads.
func (r *Repository) GetArenaExportHeader(ctx context.Context, arenaID string) (*application.ExportHeader, error) {
	arenaUUID, err := exportUUID(arenaID)
	if err != nil {
		return nil, application.ErrArenaNotFound
	}

	row, err := r.queries.GetArenaExportHeader(ctx, arenaUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrArenaNotFound
		}
		return nil, fmt.Errorf("get arena export header: %w", err)
	}

	header, err := mapExportHeader(row)
	if err != nil {
		return nil, err
	}
	return header, nil
}

// ListArenaExportArguments returns one bounded page (the caller asks for
// limit+1 rows) of published and withdrawn arguments, oldest first. The
// content of withdrawn arguments is withheld here, mirroring the public
// argument adapter; sources are read in one batched statement for the
// published arguments of the page only, so the read never grows with the
// Arena.
func (r *Repository) ListArenaExportArguments(ctx context.Context, arenaID string, after *application.ExportPosition, limit int) ([]application.ExportArgument, error) {
	arenaUUID, err := exportUUID(arenaID)
	if err != nil {
		return nil, application.ErrArenaNotFound
	}

	params := platformpg.ListArenaExportArgumentsParams{
		ArenaID:   arenaUUID,
		PageLimit: int32(limit),
	}
	if after != nil {
		afterUUID, err := exportUUID(after.ArgumentID)
		if err != nil {
			return nil, application.ErrInvalidCursor
		}
		params.AfterCreatedAt = pgtype.Timestamptz{Time: after.CreatedAt.UTC(), Valid: true}
		params.AfterID = afterUUID
	}

	rows, err := r.queries.ListArenaExportArguments(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list arena export arguments: %w", err)
	}

	arguments := make([]application.ExportArgument, 0, len(rows))
	publishedIDs := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		argument, err := mapExportArgument(row)
		if err != nil {
			return nil, err
		}
		if argument.Status == "published" {
			publishedIDs = append(publishedIDs, row.ID)
		}
		arguments = append(arguments, argument)
	}

	if len(publishedIDs) == 0 {
		return arguments, nil
	}

	sourceRows, err := r.queries.ListExportArgumentSources(ctx, publishedIDs)
	if err != nil {
		return nil, fmt.Errorf("list arena export sources: %w", err)
	}

	sources := make(map[string][]application.ExportSource, len(publishedIDs))
	for _, row := range sourceRows {
		argumentID := exportUUIDString(row.ArgumentID)
		if argumentID == "" {
			return nil, errors.New("list arena export sources: stored source has no argument")
		}
		source := application.ExportSource{URL: row.Url}
		if row.Description.Valid {
			description := row.Description.String
			source.Description = &description
		}
		sources[argumentID] = append(sources[argumentID], source)
	}
	for i := range arguments {
		if arguments[i].Status != "published" {
			continue
		}
		if argumentSources, ok := sources[arguments[i].ID]; ok {
			arguments[i].Sources = argumentSources
		}
	}
	return arguments, nil
}

// mapExportHeader converts the stored row into the public header projection.
// A public Arena is guaranteed by the statement to carry a slug and a
// publication instant; a violated guarantee is a storage corruption and
// fails loudly instead of publishing an incomplete document.
func mapExportHeader(row platformpg.GetArenaExportHeaderRow) (*application.ExportHeader, error) {
	arenaID := exportUUIDString(row.ID)
	if arenaID == "" || !row.Slug.Valid || row.Slug.String == "" || !row.PublishedAt.Valid {
		return nil, errors.New("get arena export header: stored public arena is incomplete")
	}

	closesAt := exportTimePtr(row.ClosesAt)

	return &application.ExportHeader{
		Arena: application.ExportArena{
			ID:          arenaID,
			Slug:        row.Slug.String,
			Statement:   row.Statement,
			Context:     row.Context.String,
			Category:    row.Category,
			Language:    row.Language,
			Status:      row.Status,
			PublishedAt: row.PublishedAt.Time.UTC(),
			ClosesAt:    closesAt,
		},
		Positions: application.ExportPositions{
			Initial: application.ExportDistribution{
				Agree:     row.InitialAgree,
				Disagree:  row.InitialDisagree,
				Undecided: row.InitialUndecided,
			},
			Current: application.ExportDistribution{
				Agree:     row.CurrentAgree,
				Disagree:  row.CurrentDisagree,
				Undecided: row.CurrentUndecided,
			},
			Participants:    row.ParticipantsTotal,
			PositionChanges: row.PositionChanges,
		},
		Influence: application.ExportInfluence{
			ValidAttributions: row.ValidAttributions,
			InfluencedAuthors: row.InfluencedAuthors,
		},
	}, nil
}

// mapExportArgument converts one stored argument into the public export
// projection: content only survives while the argument is published.
func mapExportArgument(row platformpg.ListArenaExportArgumentsRow) (application.ExportArgument, error) {
	argumentID := exportUUIDString(row.ID)
	if argumentID == "" || !row.CreatedAt.Valid {
		return application.ExportArgument{}, errors.New("list arena export arguments: stored argument is incomplete")
	}

	argument := application.ExportArgument{
		ID:          argumentID,
		Relation:    row.Relation,
		Status:      row.Status,
		CreatedAt:   row.CreatedAt.Time.UTC(),
		WithdrawnAt: exportTimePtr(row.WithdrawnAt),
		Influence: application.ExportArgumentInfluence{
			ValidAttributions: row.ValidAttributions,
			DistinctPeople:    row.DistinctPeople,
		},
	}
	if parentID := exportUUIDString(row.ParentID); parentID != "" {
		argument.ParentID = parentID
	}
	if argument.Status == "published" {
		content := row.Content
		argument.Content = &content
	}
	return argument, nil
}

// exportUUID parses one canonical UUID text.
func exportUUID(value string) (pgtype.UUID, error) {
	var parsed pgtype.UUID
	if err := parsed.Scan(value); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid uuid format: %w", err)
	}
	return parsed, nil
}

// exportUUIDString renders a UUID back in canonical lower-case form; an
// invalid value renders empty so callers can reject it.
func exportUUIDString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	bytes := value.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		bytes[0], bytes[1], bytes[2], bytes[3], bytes[4], bytes[5], bytes[6], bytes[7],
		bytes[8], bytes[9], bytes[10], bytes[11], bytes[12], bytes[13], bytes[14], bytes[15])
}

// exportTimePtr returns the UTC instant or nil when unset.
func exportTimePtr(instant pgtype.Timestamptz) *time.Time {
	if !instant.Valid {
		return nil
	}
	copied := instant.Time.UTC()
	return &copied
}
