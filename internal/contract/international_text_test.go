package contract_test

// P29-T02 — hostile international corpus across the five user-text
// surfaces: username, Arena statement, argument content, source
// description/URL and search query.
//
// One shared corpus proves the documented rule of each surface instead
// of five disconnected unit lists: canonical equivalents cost the same
// (grapheme clusters, never bytes or runes), prohibited invisible
// controls are refused everywhere with the surface's documented error,
// allowed international text round-trips byte-identical (nothing is
// silently NFC-normalized), and every limit uses its documented unit.
// Identifiers (username) stay ASCII by design, so confusables are
// rejected there while the same text is inert content elsewhere.

import (
	"context"
	"errors"
	"strings"
	"testing"

	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	profilesdomain "github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
	"github.com/AlexandreZanata/Regnovum/internal/search/application"
)

// corpusPolicy is a known-valid statement policy; the product policy is
// versioned separately, and this matrix only needs coherent bounds.
func corpusPolicy() arenasdomain.StatementPolicy {
	return arenasdomain.StatementPolicy{Version: "t02", MinLength: 10, MaxLength: 40, ContextMaxLength: 100}
}

// TestInternationalCorpusCostIsStable proves canonical equivalents never
// diverge in price: NFC and NFD are the same user-perceived text, and a
// ZWJ sequence is one cluster no matter how many bytes it occupies.
func TestInternationalCorpusCostIsStable(t *testing.T) {
	t.Parallel()

	const nfc, nfd = "café", "café"
	if text.GraphemeCount(nfc) != 4 || text.GraphemeCount(nfd) != 4 {
		t.Fatalf("NFC/NFD costs = %d/%d, want 4/4", text.GraphemeCount(nfc), text.GraphemeCount(nfd))
	}
	if text.GraphemeCount("👩🏽‍🚀") != 1 || text.GraphemeCount("🇧🇷") != 1 {
		t.Fatal("ZWJ sequence and flag pair must cost exactly one cluster each")
	}

	nfcContent, err := argumentsdomain.ParseContent("café na arena", text.GraphemeCount)
	if err != nil {
		t.Fatalf("NFC content rejected: %v", err)
	}
	nfdContent, err := argumentsdomain.ParseContent("café na arena", text.GraphemeCount)
	if err != nil {
		t.Fatalf("NFD content rejected: %v", err)
	}
	if nfcContent.GraphemeCost() != nfdContent.GraphemeCost() {
		t.Fatalf("NFC/NFD costs diverge: %d vs %d", nfcContent.GraphemeCost(), nfdContent.GraphemeCost())
	}
	// Nothing is silently normalized: each form persists byte-identical.
	if nfcContent.String() != "café na arena" || nfdContent.String() != "café na arena" {
		t.Fatalf("forms were not preserved: %q vs %q", nfcContent.String(), nfdContent.String())
	}
}

// TestInternationalCorpusAllowedSurfaces proves permitted international
// text — combining marks, ZWJ, RTL script, mixed scripts, CJK —
// round-trips byte-identical on every surface that accepts it.
func TestInternationalCorpusAllowedSurfaces(t *testing.T) {
	t.Parallel()

	allowed := []string{
		"A fusão nuclear move a arena",
		"A caféina nuclear move a arena",
		"Fusion 👩🏽‍🚀 energy will be commercial",
		"الذكاء الاصطناعي يغير العالم",
		"AGI 人工知能 e fusão nuclear",
	}
	for _, raw := range allowed {
		statement, err := arenasdomain.ParseStatement(raw, corpusPolicy())
		if err != nil {
			t.Errorf("statement %q rejected: %v", raw, err)
			continue
		}
		if statement.String() != raw {
			t.Errorf("statement = %q, want byte-identical %q", statement.String(), raw)
		}
		content, err := argumentsdomain.ParseContent(raw, text.GraphemeCount)
		if err != nil {
			t.Errorf("content %q rejected: %v", raw, err)
			continue
		}
		if content.String() != raw {
			t.Errorf("content = %q, want byte-identical %q", content.String(), raw)
		}
	}

	description, err := argumentsdomain.ParseSource("https://example.com/relatorio", "Fonte com café, 人工知能 e 👩🏽‍🚀")
	if err != nil {
		t.Fatalf("international source rejected: %v", err)
	}
	if description.Description() != "Fonte com café, 人工知能 e 👩🏽‍🚀" {
		t.Errorf("description = %q, want byte-identical preservation", description.Description())
	}

	username, err := profilesdomain.ParseUsername("arena-123")
	if err != nil {
		t.Fatalf("ASCII handle rejected: %v", err)
	}
	if username.Normalized() != "arena-123" {
		t.Errorf("normalized = %q, want arena-123", username.Normalized())
	}
}

// TestInternationalCorpusProhibitedControlsRejected proves invisible
// controls never pass: bidi overrides, C0 controls, DEL and invalid
// UTF-8 fail with each surface's documented error, and confusable
// non-ASCII bytes never become an identifier.
func TestInternationalCorpusProhibitedControlsRejected(t *testing.T) {
	t.Parallel()

	bidi, nul, invalid := "texto\u202Ereordenado", "a\x00b", string([]byte{0xff, 0xfe})

	for _, raw := range []string{bidi, nul, invalid} {
		if _, err := arenasdomain.ParseStatement("álbum "+raw+" longo", corpusPolicy()); !errors.Is(err, arenasdomain.ErrInvalidStatement) {
			t.Errorf("statement %q error = %v, want ErrInvalidStatement", raw, err)
		}
		if _, err := argumentsdomain.ParseContent(raw+" conteúdo", text.GraphemeCount); !errors.Is(err, argumentsdomain.ErrInvalidContent) {
			t.Errorf("content %q error = %v, want ErrInvalidContent", raw, err)
		}
	}

	for _, raw := range []string{"аrena", "‮arena", "arena​", "a\x7Fb"} {
		if _, err := profilesdomain.ParseUsername(raw); !errors.Is(err, profilesdomain.ErrUsernameNonASCII) && !errors.Is(err, profilesdomain.ErrInvalidUsernameFormat) {
			t.Errorf("username %q error = %v, want NonASCII/Format rejection", raw, err)
		}
	}
	if _, err := profilesdomain.ParseUsername(invalid); !errors.Is(err, profilesdomain.ErrInvalidUsernameFormat) {
		t.Errorf("username invalid-UTF8 error = %v, want ErrInvalidUsernameFormat", err)
	}

	if _, err := argumentsdomain.ParseSource("https://example.com/x", "texto\u0007controle"); !errors.Is(err, argumentsdomain.ErrInvalidContent) {
		t.Errorf("source description control error = %v, want ErrInvalidContent", err)
	}
	if _, err := argumentsdomain.ParseSource("https://ex\u00E4mple.com/x", ""); !errors.Is(err, argumentsdomain.ErrInvalidSourceURL) {
		t.Errorf("IDN hostname error = %v, want ErrInvalidSourceURL", err)
	}
}

// hostileSearchRepo records the queries a use case forwards: hostile
// syntax must arrive as inert parameter text, never interpolated SQL.
type hostileSearchRepo struct{ queries []string }

func (fake *hostileSearchRepo) SearchArenas(ctx context.Context, query, language string, after *application.Cursor, limit int) ([]application.ArenaResult, error) {
	fake.queries = append(fake.queries, query)
	return nil, nil
}

// TestInternationalCorpusSearchBoundary proves the search validation
// edge: interface text (including bidi and tsquery metacharacters)
// forwards verbatim as a bound parameter, while NUL, emptiness and the
// 200-byte ceiling fail before any query is built.
func TestInternationalCorpusSearchBoundary(t *testing.T) {
	t.Parallel()

	codec, err := application.NewCursorCodec([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	repo := &hostileSearchRepo{}
	useCase, err := application.NewArenaSearchUseCase(repo, codec)
	if err != nil {
		t.Fatal(err)
	}

	forwarded := []string{
		"fusão nuclear",
		"texto\u202Ereordenado",
		`arena' & (fusão | "energia"):* \`,
		"الذكاء الاصطناعي",
		strings.Repeat("é", 100),
	}
	for _, query := range forwarded {
		if _, err := useCase.Execute(context.Background(), query, "pt-BR", "", 1); err != nil {
			t.Errorf("query %q rejected: %v", query, err)
		}
	}
	if len(repo.queries) != len(forwarded) {
		t.Fatalf("forwarded %d queries, want %d", len(repo.queries), len(forwarded))
	}
	for index, query := range forwarded {
		if repo.queries[index] != query {
			t.Errorf("query[%d] = %q, want verbatim %q", index, repo.queries[index], query)
		}
	}

	for _, query := range []string{"", "   ", "a\x00b", strings.Repeat("é", 101)} {
		if _, err := useCase.Execute(context.Background(), query, "pt-BR", "", 1); !errors.Is(err, application.ErrInvalidQuery) {
			t.Errorf("query %q error = %v, want ErrInvalidQuery", query, err)
		}
	}
}

// TestInternationalCorpusLimitsUseDocumentedUnits proves every bound
// measures what its surface documents: clusters for argument cost,
// runes for statement and description, bytes for username and search.
func TestInternationalCorpusLimitsUseDocumentedUnits(t *testing.T) {
	t.Parallel()

	// 3000 ZWJ clusters occupy tens of kilobytes yet cost exactly 3000.
	heavy, err := argumentsdomain.ParseContent(strings.Repeat("👩🏽‍🚀", argumentsdomain.MaxGraphemeCost), text.GraphemeCount)
	if err != nil {
		t.Fatalf("3000-cluster content rejected: %v", err)
	}
	if heavy.GraphemeCost() != argumentsdomain.MaxGraphemeCost {
		t.Fatalf("cost = %d, want clusters not bytes", heavy.GraphemeCost())
	}

	// 40 runes of two-byte text are 80 bytes: the statement bound counts runes.
	if _, err := arenasdomain.ParseStatement(strings.Repeat("é", 40), corpusPolicy()); err != nil {
		t.Errorf("40-rune statement rejected: %v", err)
	}
	// 500 CJK runes are 1500 bytes: the description bound counts runes.
	if _, err := argumentsdomain.ParseSource("https://example.com/x", strings.Repeat("知", argumentsdomain.SourceDescriptionMaxLength)); err != nil {
		t.Errorf("500-rune description rejected: %v", err)
	}
	if _, err := argumentsdomain.ParseSource("https://example.com/x", strings.Repeat("知", argumentsdomain.SourceDescriptionMaxLength+1)); !errors.Is(err, argumentsdomain.ErrSourceDescriptionTooLong) {
		t.Errorf("501-rune description error = %v, want ErrSourceDescriptionTooLong", err)
	}
	// Usernames measure bytes: multibyte input exhausts the budget faster.
	if _, err := profilesdomain.ParseUsername(strings.Repeat("a", profilesdomain.UsernameMaxLength)); err != nil {
		t.Errorf("30-byte username rejected: %v", err)
	}
	if _, err := profilesdomain.ParseUsername(strings.Repeat("a", profilesdomain.UsernameMaxLength+1)); !errors.Is(err, profilesdomain.ErrUsernameTooLong) {
		t.Errorf("31-byte username error = %v, want ErrUsernameTooLong", err)
	}
}
