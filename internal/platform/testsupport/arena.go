package testsupport

import (
	"time"

	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// The synthetic Arena text. It satisfies the versioned statement policy and
// says nothing about any real person, place or claim — a fixture that reads
// like a debate is still a fixture.
const (
	defaultStatement = "O debate público melhora quando os argumentos podem ser verificados."
	defaultContext   = "Cenário sintético de teste, sem relação com pessoas ou fatos reais."
	// defaultSlugPrefix is the readable half of the address a published Arena
	// receives. The slug itself carries a token per call, because the schema
	// keeps a unique index on it: two Arenas of one scenario must be two Arenas
	// and not one collision.
	defaultSlugPrefix = "arena-de-teste"
	// namedSlug is the address the transition test and the negative controls
	// name explicitly. It is deliberately not the default a published Arena
	// gets.
	namedSlug = "arena-de-teste-explicita"
	// defaultCategory and defaultLanguage are the pair the migrations seed and
	// the catalogs render, so a scenario does not invent a vocabulary the
	// product would not accept.
	defaultCategory = "technology"
	defaultLanguage = "pt-BR"
)

// ArenaOption varies the Arena a builder makes. Every option carries a value
// the domain still validates, so an option can be wrong but never silent.
type ArenaOption func(*arenaSpec)

type arenaSpec struct {
	statement string
	context   string
	category  string
	language  string
	slug      string
	closesIn  time.Duration
}

// WithArenaStatement builds the Arena around a specific statement. The policy
// judges it like any other, so a statement that is too short or too long is
// refused here.
func WithArenaStatement(statement string) ArenaOption {
	return func(spec *arenaSpec) { spec.statement = statement }
}

// WithArenaContext builds the Arena with a specific context.
func WithArenaContext(context string) ArenaOption {
	return func(spec *arenaSpec) { spec.context = context }
}

// WithArenaCategory builds the Arena in a specific category.
func WithArenaCategory(category string) ArenaOption {
	return func(spec *arenaSpec) { spec.category = category }
}

// WithArenaLanguage builds the Arena in a specific language.
func WithArenaLanguage(language string) ArenaOption {
	return func(spec *arenaSpec) { spec.language = language }
}

// WithArenaSlug publishes the Arena under a specific slug, for the scenario that
// reads it back or collides with it on purpose. An empty value means "keep the
// default", which is unique per call.
func WithArenaSlug(slug string) ArenaOption {
	return func(spec *arenaSpec) { spec.slug = slug }
}

// WithArenaClosingIn schedules the close of a published Arena this far ahead of
// the scenario's instant, which is how a test crosses the deadline without
// waiting for it.
func WithArenaClosingIn(closesIn time.Duration) ArenaOption {
	return func(spec *arenaSpec) { spec.closesIn = closesIn }
}

// ArenaDraft builds a draft: statement, context, category and language set, no
// slug and no publication instant, which is the only shape the aggregate
// accepts for that status.
func (b *Builder) ArenaDraft(creator *identitydomain.Account, options ...ArenaOption) (*arenasdomain.Arena, error) {
	b.t.Helper()
	if creator == nil {
		return nil, arenasdomain.ErrEmptyCreatorID
	}
	spec, err := b.arenaSpec(options...)
	if err != nil {
		return nil, err
	}
	return arenasdomain.ReconstituteArena(
		arenasdomain.ArenaID(b.Identifier()),
		arenasdomain.CreatorID(creator.ID().String()),
		spec.parsed.statement,
		spec.parsed.context,
		spec.parsed.category,
		spec.parsed.language,
		arenasdomain.ArenaStatusDraft,
		arenasdomain.Slug{},
		1,
		b.Now(),
		nil,
		nil,
	)
}

// ArenaPublished builds a draft and publishes it through the aggregate's own
// transition, so the publication instant, the slug and the version are the ones
// the product would have written.
func (b *Builder) ArenaPublished(creator *identitydomain.Account, options ...ArenaOption) (*arenasdomain.Arena, error) {
	b.t.Helper()
	spec, err := b.arenaSpec(options...)
	if err != nil {
		return nil, err
	}
	arena, err := b.ArenaDraft(creator, options...)
	if err != nil {
		return nil, err
	}
	slug, err := arenasdomain.ParseSlug(spec.slug)
	if err != nil {
		return nil, err
	}
	if err := arena.Publish(slug, b.Now()); err != nil {
		return nil, err
	}
	if spec.closesIn > 0 {
		if err := arena.ScheduleClose(b.Now().Add(spec.closesIn)); err != nil {
			return nil, err
		}
	}
	return arena, nil
}

// ClosedArena builds a published Arena that has been closed, which is the state
// every scenario on the refusing side of participation needs.
func (b *Builder) ClosedArena(creator *identitydomain.Account, options ...ArenaOption) (*arenasdomain.Arena, error) {
	b.t.Helper()
	arena, err := b.ArenaPublished(creator, options...)
	if err != nil {
		return nil, err
	}
	if err := arena.Close(); err != nil {
		return nil, err
	}
	return arena, nil
}

// arenaValues is the parsed form of a spec, kept apart so that the parsing of
// every field happens exactly once and before any Arena exists.
type arenaValues struct {
	statement arenasdomain.Statement
	context   arenasdomain.Context
	category  arenasdomain.Category
	language  arenasdomain.Language
}

type resolvedArena struct {
	slug string
	// closesIn is the distance between the publication and the close.
	closesIn time.Duration
	parsed   arenaValues
}

// arenaSpec resolves the options and parses every value through the domain, so
// a builder either answers a fully valid Arena or the error that names what the
// caller asked for is not acceptable.
func (b *Builder) arenaSpec(options ...ArenaOption) (resolvedArena, error) {
	b.t.Helper()
	spec := arenaSpec{
		statement: defaultStatement,
		context:   defaultContext,
		category:  defaultCategory,
		language:  defaultLanguage,
	}
	for _, option := range options {
		option(&spec)
	}
	if spec.slug == "" {
		spec.slug = defaultSlugPrefix + "-" + b.shortToken()
	}

	policy := arenasdomain.DefaultStatementPolicy()
	statement, err := arenasdomain.ParseStatement(spec.statement, policy)
	if err != nil {
		return resolvedArena{}, err
	}
	context, err := arenasdomain.ParseContext(spec.context, policy)
	if err != nil {
		return resolvedArena{}, err
	}
	category, err := arenasdomain.ParseCategory(spec.category)
	if err != nil {
		return resolvedArena{}, err
	}
	language, err := arenasdomain.ParseLanguage(spec.language)
	if err != nil {
		return resolvedArena{}, err
	}
	return resolvedArena{
		slug:     spec.slug,
		closesIn: spec.closesIn,
		parsed:   arenaValues{statement: statement, context: context, category: category, language: language},
	}, nil
}
