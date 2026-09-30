package testsupport

import (
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// The synthetic argument text and its source. The address is reserved for
// documentation and the text argues nothing: a fixture that reads like a
// position is still a fixture.
const (
	defaultArgumentContent = "A verificação das fontes torna o debate público mais honesto."
	defaultSourceURL       = "https://example.test/fonte"
	defaultSourceSummary   = "Documento sintético usado apenas por cenários de teste."
)

// PositionOption varies the position change a builder makes.
type PositionOption func(*positionSpec)

type positionSpec struct {
	from string
	to   string
}

// WithPositionFrom declares the stored position a change starts from, for the
// scenario that reconstitutes a chain.
func WithPositionFrom(position string) PositionOption {
	return func(spec *positionSpec) { spec.from = position }
}

// WithPositionTo declares the position a change targets.
func WithPositionTo(position string) PositionOption {
	return func(spec *positionSpec) { spec.to = position }
}

// PositionChange builds one confirmed transition of the aggregate: a change
// from the stored position to a different one, at the scenario's instant, in
// the version the change advances.
func (b *Builder) PositionChange(arena *arenasdomain.Arena, account *identitydomain.Account, options ...PositionOption) (positionsdomain.PositionChange, error) {
	b.t.Helper()
	if arena == nil {
		return positionsdomain.PositionChange{}, positionsdomain.ErrEmptyArenaID
	}
	if account == nil {
		return positionsdomain.PositionChange{}, positionsdomain.ErrEmptyAccountID
	}
	spec := positionSpec{from: string(positionsdomain.PositionUndecided), to: string(positionsdomain.PositionAgree)}
	for _, option := range options {
		option(&spec)
	}
	from, err := positionsdomain.ParsePosition(spec.from)
	if err != nil {
		return positionsdomain.PositionChange{}, err
	}
	to, err := positionsdomain.ParsePosition(spec.to)
	if err != nil {
		return positionsdomain.PositionChange{}, err
	}
	positionArenaID, err := positionsdomain.ParseArenaID(arena.ID().String())
	if err != nil {
		return positionsdomain.PositionChange{}, err
	}
	positionAccountID, err := positionsdomain.ParseAccountID(account.ID().String())
	if err != nil {
		return positionsdomain.PositionChange{}, err
	}
	// The change is the second version of the aggregate: version one is the
	// confirmed initial position, and the domain refuses a change that does not
	// advance it.
	return positionsdomain.NewPositionChange(
		positionArenaID,
		positionAccountID,
		from,
		to,
		2,
		b.Now(),
	)
}

// ArgumentOption varies the publication attempt a builder makes.
type ArgumentOption func(*argumentSpec)

type argumentSpec struct {
	content  string
	relation string
	parent   string
	sources  []argumentsapp.SourceCommand
}

// WithArgumentContent declares the text of the argument. The domain judges its
// length in grapheme clusters, so an over-long text is refused here.
func WithArgumentContent(content string) ArgumentOption {
	return func(spec *argumentSpec) { spec.content = content }
}

// WithArgumentRelation declares the relation to the Arena statement.
func WithArgumentRelation(relation string) ArgumentOption {
	return func(spec *argumentSpec) { spec.relation = relation }
}

// WithArgumentParent declares the argument this one replies to, which is what a
// threaded scenario needs.
func WithArgumentParent(parentID string) ArgumentOption {
	return func(spec *argumentSpec) { spec.parent = parentID }
}

// WithArgumentSources declares the sources the argument cites.
func WithArgumentSources(sources ...argumentsapp.SourceCommand) ArgumentOption {
	return func(spec *argumentSpec) { spec.sources = sources }
}

// Argument is one validated publication attempt: the command the publish use
// case consumes, together with the domain values it parsed. Handing back both
// is what lets a scenario assert on the parsed content without parsing it a
// second time, and it is why the command here is a value no adapter has to
// re-check.
type Argument struct {
	Command        argumentsapp.PublishArgumentCommand
	Content        argumentsdomain.Content
	Relation       argumentsdomain.Relation
	Sources        []argumentsdomain.Source
	IdempotencyKey argumentsdomain.IdempotencyKey
}

// Argument builds the publication attempt of a participation scenario: a
// non-empty content within the grapheme bound, a relation from the closed
// vocabulary, a parsed source and an idempotency key of the scenario's own.
//
// The content is charged and stored by the product; nothing here writes it, so
// the argument this builder answers is the input of the journey, not its
// outcome.
func (b *Builder) Argument(arena *arenasdomain.Arena, account *identitydomain.Account, options ...ArgumentOption) (Argument, error) {
	b.t.Helper()
	if arena == nil {
		return Argument{}, argumentsdomain.ErrEmptyArenaID
	}
	if account == nil {
		return Argument{}, argumentsdomain.ErrEmptyAccountID
	}
	spec := argumentSpec{
		content:  defaultArgumentContent,
		relation: argumentsdomain.RelationSupport,
		sources:  []argumentsapp.SourceCommand{{URL: defaultSourceURL, Description: defaultSourceSummary}},
	}
	for _, option := range options {
		option(&spec)
	}

	content, err := argumentsdomain.ParseContent(spec.content, text.GraphemeCount)
	if err != nil {
		return Argument{}, err
	}
	relation, err := argumentsdomain.ParseRelation(spec.relation)
	if err != nil {
		return Argument{}, err
	}
	key, err := argumentsdomain.ParseIdempotencyKey(b.id())
	if err != nil {
		return Argument{}, err
	}

	// The sources are parsed into domain values and the command carries the
	// rendered pair, so a source the domain refuses fails the builder instead
	// of the use case.
	sources := make([]argumentsdomain.Source, 0, len(spec.sources))
	commands := make([]argumentsapp.SourceCommand, 0, len(spec.sources))
	for _, candidate := range spec.sources {
		parsed, parseErr := argumentsdomain.ParseSource(candidate.URL, candidate.Description)
		if parseErr != nil {
			return Argument{}, parseErr
		}
		sources = append(sources, parsed)
		commands = append(commands, argumentsapp.SourceCommand{
			URL:         parsed.URL(),
			Description: parsed.Description(),
		})
	}

	return Argument{
		Command: argumentsapp.PublishArgumentCommand{
			AccountID:      account.ID().String(),
			ArenaID:        arena.ID().String(),
			ParentID:       spec.parent,
			Relation:       relation.String(),
			Content:        content.String(),
			Sources:        commands,
			IdempotencyKey: key.String(),
		},
		Content:        content,
		Relation:       relation,
		Sources:        sources,
		IdempotencyKey: key,
	}, nil
}
