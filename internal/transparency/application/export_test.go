package application_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/transparency/application"
	"github.com/AlexandreZanata/Regnovum/internal/transparency/domain"
)

const exportCursorSecret = "0123456789abcdef0123456789abcdef"

var exportInstant = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

// fakeExportRepository emulates the bounded keyset repository so the use
// case can be observed without a database: it records the requested limits
// and positions and never returns more rows than asked.
type fakeExportRepository struct {
	header       *application.ExportHeader
	headerErr    error
	arguments    []application.ExportArgument
	argumentsErr error

	limits []int
	afters []*application.ExportPosition
	calls  int
}

func (f *fakeExportRepository) GetArenaExportHeader(_ context.Context, _ string) (*application.ExportHeader, error) {
	if f.headerErr != nil {
		return nil, f.headerErr
	}
	return f.header, nil
}

func (f *fakeExportRepository) ListArenaExportArguments(_ context.Context, _ string, after *application.ExportPosition, limit int) ([]application.ExportArgument, error) {
	f.calls++
	f.limits = append(f.limits, limit)
	f.afters = append(f.afters, after)
	if f.argumentsErr != nil {
		return nil, f.argumentsErr
	}

	start := 0
	if after != nil {
		for i, argument := range f.arguments {
			if argument.ID == after.ArgumentID {
				start = i + 1
				break
			}
		}
	}
	end := start + limit
	if end > len(f.arguments) {
		end = len(f.arguments)
	}
	return append([]application.ExportArgument{}, f.arguments[start:end]...), nil
}

func mustExportUseCase(t *testing.T, repository application.ArenaExportRepository) *application.GetArenaExportUseCase {
	t.Helper()
	codec, err := application.NewExportCursorCodec([]byte(exportCursorSecret))
	if err != nil {
		t.Fatalf("NewExportCursorCodec: %v", err)
	}
	uc, err := application.NewGetArenaExportUseCase(repository, codec)
	if err != nil {
		t.Fatalf("NewGetArenaExportUseCase: %v", err)
	}
	return uc
}

func exportHeader(participants int64) *application.ExportHeader {
	return &application.ExportHeader{
		Arena: application.ExportArena{
			ID:          "018f6b2a-0000-7000-8000-0000000000a1",
			Slug:        "export-probe-arena",
			Statement:   "Export probe statement",
			Category:    "technology",
			Language:    "pt-BR",
			Status:      "published",
			PublishedAt: exportInstant,
		},
		Positions: application.ExportPositions{
			Initial:         application.ExportDistribution{Agree: 3, Disagree: 1, Undecided: 1},
			Current:         application.ExportDistribution{Agree: 2, Disagree: 2, Undecided: 1},
			Participants:    participants,
			PositionChanges: 7,
		},
		Influence: application.ExportInfluence{ValidAttributions: 9, InfluencedAuthors: 4},
	}
}

func exportArguments(count int) []application.ExportArgument {
	arguments := make([]application.ExportArgument, 0, count)
	for i := 0; i < count; i++ {
		content := "export probe argument"
		arguments = append(arguments, application.ExportArgument{
			ID:        fmt.Sprintf("018f6b2a-0000-7000-8000-%012d", i+1),
			Relation:  "support",
			Content:   &content,
			Status:    "published",
			CreatedAt: exportInstant.Add(time.Duration(i) * time.Minute),
		})
	}
	return arguments
}

func TestExportSchemaVersionIsPinned(t *testing.T) {
	t.Parallel()

	if domain.ExportSchemaVersion != 1 {
		t.Fatalf("ExportSchemaVersion = %d, want 1", domain.ExportSchemaVersion)
	}
}

func TestExportSuppressesSmallSamples(t *testing.T) {
	t.Parallel()

	small := &fakeExportRepository{header: exportHeader(domain.LowCountThreshold - 1)}
	page, err := mustExportUseCase(t, small).Execute(context.Background(), application.GetArenaExportQuery{ArenaID: "arena"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !page.Positions.Suppressed || page.Positions.Participants != 0 || page.Positions.PositionChanges != 0 {
		t.Fatalf("small sample must publish no counts: %+v", page.Positions)
	}
	if page.Positions.Initial != (application.ExportDistribution{}) || page.Positions.Current != (application.ExportDistribution{}) {
		t.Fatalf("small sample must publish no distributions: %+v", page.Positions)
	}

	boundary := &fakeExportRepository{header: exportHeader(domain.LowCountThreshold)}
	page, err = mustExportUseCase(t, boundary).Execute(context.Background(), application.GetArenaExportQuery{ArenaID: "arena"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if page.Positions.Suppressed || page.Positions.Participants != domain.LowCountThreshold || page.Positions.PositionChanges != 7 {
		t.Fatalf("boundary sample must publish exact aggregates: %+v", page.Positions)
	}
}

func TestExportPagesArgumentsWithinBounds(t *testing.T) {
	t.Parallel()

	repository := &fakeExportRepository{header: exportHeader(10), arguments: exportArguments(5)}
	uc := mustExportUseCase(t, repository)

	var collected []string
	cursor := ""
	pages := 0
	for {
		page, err := uc.Execute(context.Background(), application.GetArenaExportQuery{
			ArenaID: "arena", Cursor: cursor, Limit: 2,
		})
		if err != nil {
			t.Fatalf("Execute page %d: %v", pages, err)
		}
		if len(page.Arguments) > 2 {
			t.Fatalf("page %d delivered %d arguments, want at most 2", pages, len(page.Arguments))
		}
		for _, argument := range page.Arguments {
			collected = append(collected, argument.ID)
		}
		pages++
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	if pages != 3 || len(collected) != 5 {
		t.Fatalf("traversal delivered %d pages and %d arguments, want 3 and 5", pages, len(collected))
	}
	for _, limit := range repository.limits {
		if limit != 3 {
			t.Fatalf("repository was asked for limit %d, want the bounded lookahead 3", limit)
		}
	}
	if repository.afters[0] != nil {
		t.Fatalf("first page must not carry a position: %+v", repository.afters[0])
	}
	if repository.afters[1] == nil || repository.afters[2] == nil {
		t.Fatal("following pages must carry the signed position")
	}
}

func TestExportClampsPageLimit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		limit     int
		wantAsked int
	}{
		{name: "default", limit: 0, wantAsked: application.DefaultExportPageLimit + 1},
		{name: "negative defaults", limit: -5, wantAsked: application.DefaultExportPageLimit + 1},
		{name: "explicit", limit: 7, wantAsked: 8},
		{name: "maximum", limit: application.MaxExportPageLimit, wantAsked: application.MaxExportPageLimit + 1},
		{name: "above maximum clamps", limit: 100000, wantAsked: application.MaxExportPageLimit + 1},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repository := &fakeExportRepository{header: exportHeader(10)}
			if _, err := mustExportUseCase(t, repository).Execute(context.Background(), application.GetArenaExportQuery{
				ArenaID: "arena", Limit: testCase.limit,
			}); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if len(repository.limits) != 1 || repository.limits[0] != testCase.wantAsked {
				t.Fatalf("repository limits = %v, want [%d]", repository.limits, testCase.wantAsked)
			}
		})
	}
}

func TestExportRejectsForgedCursorAndUnknownArena(t *testing.T) {
	t.Parallel()

	repository := &fakeExportRepository{header: exportHeader(10)}
	uc := mustExportUseCase(t, repository)

	if _, err := uc.Execute(context.Background(), application.GetArenaExportQuery{ArenaID: "arena", Cursor: "forged.cursor"}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("forged cursor error = %v, want ErrInvalidCursor", err)
	}
	if repository.calls != 0 {
		t.Fatalf("forged cursor must not touch the repository (%d calls)", repository.calls)
	}
	if _, err := uc.Execute(context.Background(), application.GetArenaExportQuery{ArenaID: "   "}); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("empty arena error = %v, want ErrArenaNotFound", err)
	}

	missing := &fakeExportRepository{headerErr: application.ErrArenaNotFound}
	if _, err := mustExportUseCase(t, missing).Execute(context.Background(), application.GetArenaExportQuery{ArenaID: "arena"}); !errors.Is(err, application.ErrArenaNotFound) {
		t.Fatalf("unknown arena error = %v, want ErrArenaNotFound", err)
	}
}

func TestExportRefusesIncompleteComposition(t *testing.T) {
	t.Parallel()

	codec, err := application.NewExportCursorCodec([]byte(exportCursorSecret))
	if err != nil {
		t.Fatalf("NewExportCursorCodec: %v", err)
	}
	if _, err := application.NewGetArenaExportUseCase(nil, codec); !errors.Is(err, application.ErrInvalidExportConfig) {
		t.Fatalf("nil repository error = %v, want ErrInvalidExportConfig", err)
	}
	if _, err := application.NewGetArenaExportUseCase(&fakeExportRepository{}, nil); !errors.Is(err, application.ErrInvalidExportConfig) {
		t.Fatalf("nil codec error = %v, want ErrInvalidExportConfig", err)
	}
	if _, err := application.NewExportCursorCodec([]byte("short")); !errors.Is(err, application.ErrWeakExportCursorSecret) {
		t.Fatalf("weak secret error = %v, want ErrWeakExportCursorSecret", err)
	}
}

func TestExportCursorRoundTrip(t *testing.T) {
	t.Parallel()

	codec, err := application.NewExportCursorCodec([]byte(exportCursorSecret))
	if err != nil {
		t.Fatalf("NewExportCursorCodec: %v", err)
	}

	argument := exportArguments(1)[0]
	encoded := codec.Encode(argument)
	position, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if position.ArgumentID != argument.ID || !position.CreatedAt.Equal(argument.CreatedAt.UTC()) {
		t.Fatalf("position = %+v, want %s at %s", position, argument.ID, argument.CreatedAt)
	}

	replacement := byte('A')
	if encoded[0] == 'A' {
		replacement = 'B'
	}
	tampered := string(replacement) + encoded[1:]
	if _, err := codec.Decode(tampered); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("tampered cursor error = %v, want ErrInvalidCursor", err)
	}
	if position, err := codec.Decode("   "); err != nil || position != nil {
		t.Fatalf("empty cursor = (%+v, %v), want (nil, nil)", position, err)
	}

	// The printable range edges are valid identifier bytes, while the
	// space and DEL beside them refuse (mutation gate:
	// export_cursor.go:104-105).
	edged := exportArguments(1)[0]
	edged.ID = "edge!~id"
	if position, err := codec.Decode(codec.Encode(edged)); err != nil || position.ArgumentID != edged.ID {
		t.Fatalf("edge cursor = (%+v, %v), want %s decoded", position, err, edged.ID)
	}
	for _, id := range []string{"edge id", "edge\x7fid"} {
		edged.ID = id
		if _, err := codec.Decode(codec.Encode(edged)); !errors.Is(err, application.ErrInvalidCursor) {
			t.Fatalf("identifier %q error = %v, want ErrInvalidCursor", id, err)
		}
	}
}

func TestExportExactPageCarriesNoCursor(t *testing.T) {
	t.Parallel()

	// A page with exactly the requested rows is complete: trimming to the
	// same length must not mint a cursor to a page that does not exist
	// (mutation gate: export.go:186).
	repository := &fakeExportRepository{header: exportHeader(10), arguments: exportArguments(4)}
	uc := mustExportUseCase(t, repository)

	first, err := uc.Execute(context.Background(), application.GetArenaExportQuery{ArenaID: "arena", Limit: 2})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Arguments) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %d arguments/cursor %q, want two with a cursor", len(first.Arguments), first.NextCursor)
	}
	last, err := uc.Execute(context.Background(), application.GetArenaExportQuery{ArenaID: "arena", Cursor: first.NextCursor, Limit: 2})
	if err != nil {
		t.Fatalf("last page: %v", err)
	}
	if len(last.Arguments) != 2 || last.NextCursor != "" {
		t.Fatalf("last page = %d arguments/cursor %q, want two with no cursor", len(last.Arguments), last.NextCursor)
	}
}
