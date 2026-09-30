package postgres_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	arenaspg "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentspg "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/postgres"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	persuasionpg "github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/postgres"
	persuasionapp "github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	persuasiondomain "github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	positionspg "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/postgres"
	positionsapp "github.com/AlexandreZanata/Regnovum/internal/positions/application"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// Independent participation model of arenas, positions and persuasion
// (P24-T06).
//
// The system under test is the real stack over a disposable PostgreSQL:
// arena drafts through closing, position confirmation and changes,
// argument rows as attribution targets, attributions with the three-argument
// limit, aggregates with the privacy threshold, and derived reputations.
// The oracle below is a separate, deliberately small state machine written
// from the documented rules: pre-position privacy, immutable initials,
// version chains, reconstructed aggregates, single attributions and derived
// reputations. It never calls domain constructors or application use cases;
// identifiers are learned from observed outputs, while the TRANSITION RULES
// are the oracle's own.

// modelParticipationClock is one explicit clock: it answers the instant it
// was told and moves only when the sequence moves it, so created-before
// relations are total and replayable. It starts frozen; every command ticks
// it forward by one second.
type modelParticipationClock struct {
	mu      sync.Mutex
	instant time.Time
}

func newModelParticipationClock() *modelParticipationClock {
	return &modelParticipationClock{instant: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
}

func (c *modelParticipationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.instant
}

func (c *modelParticipationClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.instant = c.instant.Add(duration)
}

// modelAccountEligibility is the test-controlled gate for positioning: only
// accounts the model holds active may confirm. Production answers this from
// identity; the model moves it explicitly to prove ineligible accounts stay
// out of the aggregate.
type modelAccountEligibility struct {
	mu     sync.Mutex
	active map[string]bool
}

func newModelAccountEligibility() *modelAccountEligibility {
	return &modelAccountEligibility{active: make(map[string]bool)}
}

func (g *modelAccountEligibility) EnsureEligible(_ context.Context, accountID positionsdomain.AccountID) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.active[accountID.String()] {
		return positionsapp.ErrAccountNotEligible
	}
	return nil
}

func (g *modelAccountEligibility) setActive(accountID string, active bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active[accountID] = active
}

func (g *modelAccountEligibility) isActive(accountID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active[accountID]
}

// modelArenaGate answers position acceptance from the stored arena state:
// published arenas accept, drafts and closed ones refuse.
type modelArenaGate struct {
	arenas *arenaspg.Repository
}

func (g *modelArenaGate) EnsureAcceptsPositions(ctx context.Context, arenaID positionsdomain.ArenaID) error {
	arena, err := g.arenas.GetArenaByID(ctx, arenasdomain.ArenaID(arenaID.String()))
	if err != nil {
		return err
	}
	if arena.Status() != arenasdomain.ArenaStatusPublished {
		return positionsapp.ErrArenaNotOpen
	}
	return nil
}

// participationWorld wires the real cross-module stack over one disposable
// database.
type participationWorld struct {
	ctx        context.Context
	t          *testing.T
	pool       *pgxpool.Pool
	clock      *modelParticipationClock
	arenas     *arenaspg.Repository
	positions  *positionspg.Repository
	arguments  *argumentspg.Repository
	persuasion *persuasionpg.Repository
	queries    *platformpg.Queries
	uow        *platformpg.TxManager
	accounts   *modelAccountEligibility
	arenaGate  *modelArenaGate
	confirm    *positionsapp.ConfirmInitialPositionUseCase
	change     *positionsapp.ChangePositionUseCase
	aggregate  *positionsapp.GetPositionAggregateUseCase
	aggregate3 *positionsapp.GetPositionAggregateUseCase
	record     *persuasionapp.RecordAttributionsUseCase
	reputation *persuasionapp.GetAuthorReputationUseCase
}

func newParticipationWorld(t *testing.T) *participationWorld {
	t.Helper()
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	clock := newModelParticipationClock()
	arenasRepo := arenaspg.NewRepository(pool)
	positionsRepo := positionspg.NewRepository(pool)
	argumentsRepo := argumentspg.NewRepository(pool)
	persuasionRepo := persuasionpg.NewRepository(pool)
	queries := platformpg.New(pool)
	accounts := newModelAccountEligibility()
	gate := &modelArenaGate{arenas: arenasRepo}
	uow := platformpg.NewTxManager(pool)
	return &participationWorld{
		ctx: ctx, t: t, pool: pool, clock: clock,
		arenas: arenasRepo, positions: positionsRepo, arguments: argumentsRepo,
		persuasion: persuasionRepo, queries: queries, uow: uow,
		accounts: accounts, arenaGate: gate,
		confirm:   positionsapp.NewConfirmInitialPositionUseCase(positionsRepo, accounts, gate, clock),
		change:    positionsapp.NewChangePositionUseCase(positionsRepo, gate, uow, clock),
		aggregate: positionsapp.NewGetPositionAggregateUseCase(positionsRepo, positionsdomain.DefaultAggregatePolicy(), clock),
		aggregate3: positionsapp.NewGetPositionAggregateUseCase(positionsRepo,
			positionsdomain.AggregatePolicy{Version: "model-3", MinParticipants: 3}, clock),
		record:     persuasionapp.NewRecordAttributionsUseCase(persuasionRepo, persuasiondomain.DefaultEligibilityPolicy(), uow),
		reputation: persuasionapp.NewGetAuthorReputationUseCase(persuasionRepo, clock),
	}
}

// modelPosition is one account's projected chain in one arena.
type modelPosition struct {
	initial string
	current string
	version int32
	changes []string
}

// modelArgument is one attribution target the oracle has seen stored,
// with the sequence step that timestamps it for the late rule.
type modelArgument struct {
	author      string
	createdStep int
}

// modelArena is everything the oracle believes about one arena.
type modelArena struct {
	id          string
	creator     string
	status      string
	version     int32
	positions   map[string]*modelPosition
	arguments   map[string]*modelArgument
	attributed  map[string]map[string]bool
	changeSteps map[string]int
	changeOrder []string
}

// participationDivergence is one place where the stack and the oracle
// disagreed.
type participationDivergence struct {
	step int
	op   string
	want bool
	got  bool
}

// participationRunner carries one deterministic journey over two arenas:
// the world, the per-arena oracles and every divergence found.
type participationRunner struct {
	t        *testing.T
	world    *participationWorld
	arenas   map[string]*modelArena
	step     int
	diverged []participationDivergence
	trace    []string
	accepts  int
	refusals int
	replays  int
	// mutantCountIneligible makes the oracle count suspended accounts in
	// aggregates: the harness must flag the privacy violation.
	mutantCountIneligible bool
}

func (r *participationRunner) check(op string, wantAccept bool, err error) bool {
	gotAccept := err == nil
	r.trace = append(r.trace, op)
	if wantAccept {
		r.accepts++
	} else {
		r.refusals++
	}
	if gotAccept != wantAccept {
		r.diverged = append(r.diverged, participationDivergence{step: r.step, op: op, want: wantAccept, got: gotAccept})
		return false
	}
	return true
}

// modelStatement builds a valid arena statement deterministically.
func modelStatement(t *testing.T, seq, n int) arenasdomain.Statement {
	t.Helper()
	statement, err := arenasdomain.ParseStatement(
		fmt.Sprintf("A AGI existira ate %d em %d cenarios distintos", 2040+seq, n),
		arenasdomain.DefaultStatementPolicy())
	if err != nil {
		t.Fatalf("ParseStatement: %v", err)
	}
	return statement
}

func modelCategory(t *testing.T) arenasdomain.Category {
	t.Helper()
	// The slug must exist in the seeded category table, not merely parse:
	// the value object accepts vocabulary the database never seeded.
	category, err := arenasdomain.ParseCategory("technology")
	if err != nil {
		t.Fatalf("ParseCategory: %v", err)
	}
	return category
}

func modelLanguage(t *testing.T, seq int) arenasdomain.Language {
	t.Helper()
	raw := "pt-BR"
	if seq%2 == 1 {
		raw = "en-US"
	}
	language, err := arenasdomain.ParseLanguage(raw)
	if err != nil {
		t.Fatalf("ParseLanguage: %v", err)
	}
	return language
}

func modelArenaContext(t *testing.T, seq int) arenasdomain.Context {
	t.Helper()
	contextText, err := arenasdomain.ParseContext(fmt.Sprintf("Contexto da arena %d", seq), arenasdomain.DefaultStatementPolicy())
	if err != nil {
		t.Fatalf("ParseContext: %v", err)
	}
	return contextText
}

// opCreateDraft creates one draft arena through the real repository.
func (r *participationRunner) opCreateDraft(creator string, seq, n int) string {
	r.step++
	r.t.Helper()
	arena, err := r.world.arenas.CreateArena(r.world.ctx, arenasapp.CreateArenaRequest{
		CreatorID: arenasdomain.CreatorID(creator),
		Statement: modelStatement(r.t, seq, n),
		Context:   modelArenaContext(r.t, seq),
		Category:  modelCategory(r.t),
		Language:  modelLanguage(r.t, seq),
	})
	if !r.check("create-draft", true, err) || arena == nil {
		return ""
	}
	id := arena.ID().String()
	r.arenas[id] = &modelArena{id: id, creator: creator, status: "draft", version: 1,
		positions: make(map[string]*modelPosition), arguments: make(map[string]*modelArgument),
		attributed: make(map[string]map[string]bool), changeSteps: make(map[string]int)}
	return id
}

// opPublish moves one draft to published; only the creator publishes and
// only a draft publishes, so anything else is refused writing nothing.
func (r *participationRunner) opPublish(arenaID, creator string) {
	r.step++
	r.t.Helper()
	st, known := r.arenas[arenaID]
	wantAccept := known && st.status == "draft" && st.creator == creator
	var version int32
	if known {
		version = st.version
	}
	slug, err := arenasdomain.ParseSlug(fmt.Sprintf("arena-model-%s", arenaID))
	if err != nil {
		r.t.Fatalf("ParseSlug: %v", err)
	}
	arena, err := r.world.arenas.PublishArenaDraft(r.world.ctx,
		arenasdomain.ArenaID(arenaID), arenasdomain.CreatorID(creator), slug,
		r.world.clock.Now(), version)
	if !r.check("publish", wantAccept, err) || !wantAccept {
		return
	}
	if arena == nil || arena.Status() != arenasdomain.ArenaStatusPublished {
		r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "publish-shape", want: true, got: false})
		return
	}
	st.status = "published"
	st.version = arena.Version()
}

// opClose closes one published arena; only the creator closes and only a
// published arena closes, so anything else is refused writing nothing.
func (r *participationRunner) opClose(arenaID, creator string) {
	r.step++
	r.t.Helper()
	st, known := r.arenas[arenaID]
	wantAccept := known && st.status == "published" && st.creator == creator
	var version int32
	if known {
		version = st.version
	}
	arena, err := r.world.arenas.CloseArena(r.world.ctx,
		arenasdomain.ArenaID(arenaID), arenasdomain.CreatorID(creator), version)
	if !r.check("close", wantAccept, err) || !wantAccept {
		return
	}
	if arena == nil || arena.Status() != arenasdomain.ArenaStatusClosed {
		r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "close-shape", want: true, got: false})
		return
	}
	st.status = "closed"
	st.version = arena.Version()
}

// opConfirm records one first position: a fresh confirmation writes,
// the same position replays, and anything else is refused — the initial
// choice is immutable history, and closed arenas and ineligible accounts
// take nothing.
func (r *participationRunner) opConfirm(arenaID, account, position string) {
	r.step++
	r.t.Helper()
	st, known := r.arenas[arenaID]
	wantAccept := false
	if known {
		if existing, ok := st.positions[account]; ok {
			wantAccept = existing.initial == position
		} else if st.status == "published" && r.world.accounts.isActive(account) && modelPositionValid(position) {
			wantAccept = true
		}
	}
	res, err := r.world.confirm.Execute(r.world.ctx, positionsapp.ConfirmInitialPositionCommand{
		AccountID: account, ArenaID: arenaID, Position: position,
	})
	if !r.check("confirm", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if res == nil || res.Position == nil {
		r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "confirm-shape", want: true, got: false})
		return
	}
	if _, ok := st.positions[account]; !ok {
		st.positions[account] = &modelPosition{initial: position, current: position, version: 1}
		if res.Replayed {
			r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "confirm-replay-flag", want: true, got: false})
		}
	} else if !res.Replayed {
		r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "confirm-replay-flag", want: true, got: false})
	} else {
		r.replays++
	}
	if got := res.Position.Version(); got != st.positions[account].version {
		r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "confirm-version", want: true, got: false})
	}
}

// opChange records one position change: a new value advances the chain by
// exactly one version, while the same value, a closed arena or a missing
// projection is refused writing nothing.
func (r *participationRunner) opChange(arenaID, account, position string) string {
	r.step++
	r.t.Helper()
	st, known := r.arenas[arenaID]
	wantAccept := false
	if known {
		if existing, ok := st.positions[account]; ok && st.status == "published" && existing.current != position && modelPositionValid(position) {
			wantAccept = true
		}
	}
	res, err := r.world.change.Execute(r.world.ctx, positionsapp.ChangePositionCommand{
		AccountID: account, ArenaID: arenaID, Position: position,
	})
	if !r.check("change", wantAccept, err) {
		return ""
	}
	if !wantAccept {
		return ""
	}
	if res == nil || res.Position == nil || res.ChangeID == "" {
		r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "change-shape", want: true, got: false})
		return ""
	}
	existing := st.positions[account]
	existing.current = position
	existing.version++
	existing.changes = append(existing.changes, res.ChangeID)
	st.changeSteps[res.ChangeID] = r.step
	st.changeOrder = append(st.changeOrder, res.ChangeID)
	st.changeSteps[res.ChangeID] = r.step
	if res.Position.Version() != existing.version || res.Position.CurrentPosition().String() != position {
		r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "change-projection", want: true, got: false})
	}
	return res.ChangeID
}

// modelArgumentContent builds one valid argument body deterministically.
func modelArgumentContent(t *testing.T, seq, n int) argumentsdomain.Content {
	t.Helper()
	content, err := argumentsdomain.ParseContent(
		fmt.Sprintf("O argumento %d da sequencia %d sustenta a afirmacao com razao", n, seq),
		text.GraphemeCount)
	if err != nil {
		t.Fatalf("ParseContent: %v", err)
	}
	return content
}

// opPublishArgument stores one argument row as a future attribution target.
// Publication cost belongs to P24-T07; here arguments are only targets, so
// the repository insert is the honest seam.
func (r *participationRunner) opPublishArgument(arenaID, author string, seq, n int) string {
	r.step++
	r.t.Helper()
	st := r.arenas[arenaID]
	key, err := argumentsdomain.ParseIdempotencyKey(fmt.Sprintf("model-arg-%d-%d", seq, n))
	if err != nil {
		r.t.Fatalf("ParseIdempotencyKey: %v", err)
	}
	relation, err := argumentsdomain.ParseRelation("support")
	if err != nil {
		r.t.Fatalf("ParseRelation: %v", err)
	}
	published, inserted, err := r.world.arguments.CreateArgument(r.world.ctx, argumentsapp.CreateArgumentRequest{
		ArenaID:        mustParseArgArenaID(r.t, arenaID),
		AuthorID:       mustParseArgAccountID(r.t, author),
		Relation:       relation,
		Content:        modelArgumentContent(r.t, seq, n),
		IdempotencyKey: key,
		CreatedAt:      r.world.clock.Now(),
	})
	if !r.check("publish-argument", true, err) || published == nil {
		return ""
	}
	if !inserted {
		r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "publish-argument-fresh", want: true, got: false})
		return ""
	}
	id := published.ID.String()
	st.arguments[id] = &modelArgument{author: author, createdStep: r.step}
	return id
}

func mustParseArgArenaID(t *testing.T, raw string) argumentsdomain.ArenaID {
	t.Helper()
	id, err := argumentsdomain.ParseArenaID(raw)
	if err != nil {
		t.Fatalf("ParseArenaID(%q): %v", raw, err)
	}
	return id
}

func mustParseArgAccountID(t *testing.T, raw string) argumentsdomain.AccountID {
	t.Helper()
	id, err := argumentsdomain.ParseAccountID(raw)
	if err != nil {
		t.Fatalf("ParseAccountID(%q): %v", raw, err)
	}
	return id
}

// opRecordAttribution records up to three arguments against one change of
// the caller's own account, predicting the outcome from the oracle history
// alone. Unknown, foreign-change, duplicate-in-request, over-limit, self,
// cross-arena and late arguments are refused; an identical selection
// replays without duplicating rows.
func (r *participationRunner) opRecordAttribution(arenaID, account, changeID string, argIDs []string) {
	r.step++
	r.t.Helper()
	st := r.arenas[arenaID]
	// The change must be one of the caller's recorded changes.
	changeOwner := ""
	for acct, pos := range st.positions {
		for _, id := range pos.changes {
			if id == changeID {
				changeOwner = acct
			}
		}
	}
	credited := st.attributed[changeID]
	if credited == nil {
		credited = make(map[string]bool)
	}
	// Predict from history alone.
	wantAccept := true
	if changeOwner == "" || changeOwner != account {
		wantAccept = false
	}
	seen := make(map[string]bool)
	fresh := 0
	if wantAccept {
		for _, id := range argIDs {
			arg, ok := st.arguments[id]
			if !ok || arg.author == account || seen[id] {
				wantAccept = false
				break
			}
			seen[id] = true
			if credited[id] {
				continue
			}
			fresh++
			if arg.createdStep >= st.changeSteps[changeID] {
				wantAccept = false
				break
			}
		}
	}
	if wantAccept && len(credited)+fresh > 3 {
		wantAccept = false
	}
	res, err := r.world.record.Execute(r.world.ctx, persuasionapp.RecordAttributionsCommand{
		AccountID: account, ChangeID: changeID, ArgumentIDs: argIDs,
	})
	if !r.check("record-attribution", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if res == nil {
		r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "attribution-shape", want: true, got: false})
		return
	}
	wantReplay := fresh == 0 && len(credited) > 0
	if res.Replayed != wantReplay {
		r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "attribution-replay-flag", want: true, got: false})
	} else if wantReplay {
		r.replays++
	}
	if st.attributed[changeID] == nil {
		st.attributed[changeID] = make(map[string]bool)
	}
	for id := range seen {
		st.attributed[changeID][id] = true
	}
}

// opReadAggregate reads both aggregate views and compares with the oracle
// recomputation from normalized history: initial and current distributions
// over eligible accounts only, with the privacy threshold applied.
func (r *participationRunner) opReadAggregate(arenaID string) {
	r.step++
	r.t.Helper()
	st := r.arenas[arenaID]
	got, err := r.world.aggregate.Execute(r.world.ctx, positionsapp.GetPositionAggregateQuery{ArenaID: arenaID})
	if !r.check("read-aggregate", true, err) || got == nil {
		return
	}
	got3, err := r.world.aggregate3.Execute(r.world.ctx, positionsapp.GetPositionAggregateQuery{ArenaID: arenaID})
	if !r.check("read-aggregate-3", true, err) || got3 == nil {
		return
	}
	wantInitial := map[string]int64{"agree": 0, "disagree": 0, "undecided": 0}
	wantCurrent := map[string]int64{"agree": 0, "disagree": 0, "undecided": 0}
	var eligible int64
	for account, pos := range st.positions {
		if !r.world.accounts.isActive(account) && !r.mutantCountIneligible {
			continue
		}
		eligible++
		wantInitial[pos.initial]++
		wantCurrent[pos.current]++
	}
	for _, view := range []struct {
		name      string
		observed  *positionsapp.PositionAggregate
		threshold int64
	}{
		{"aggregate-10", got, 10},
		{"aggregate-3", got3, 3},
	} {
		if eligible < view.threshold {
			if !view.observed.Suppressed || view.observed.Total != 0 {
				r.diverged = append(r.diverged, participationDivergence{step: r.step, op: view.name + ":suppressed", want: true, got: false})
			}
			continue
		}
		if view.observed.Suppressed {
			r.diverged = append(r.diverged, participationDivergence{step: r.step, op: view.name + ":published", want: true, got: false})
			continue
		}
		if view.observed.Total != eligible {
			r.diverged = append(r.diverged, participationDivergence{step: r.step, op: view.name + ":total", want: true, got: false})
		}
		if view.observed.Initial.Agree != wantInitial["agree"] || view.observed.Initial.Disagree != wantInitial["disagree"] || view.observed.Initial.Undecided != wantInitial["undecided"] {
			r.diverged = append(r.diverged, participationDivergence{step: r.step, op: view.name + ":initial", want: true, got: false})
		}
		if view.observed.Current.Agree != wantCurrent["agree"] || view.observed.Current.Disagree != wantCurrent["disagree"] || view.observed.Current.Undecided != wantCurrent["undecided"] {
			r.diverged = append(r.diverged, participationDivergence{step: r.step, op: view.name + ":current", want: true, got: false})
		}
		// The aggregate carries no account identifiers by construction.
		if view.observed.Total != view.observed.Initial.Total() || view.observed.Total != view.observed.Current.Total() {
			r.diverged = append(r.diverged, participationDivergence{step: r.step, op: view.name + ":coherent", want: true, got: false})
		}
	}
}

// opReadReputation reads one author's derived reputation and compares with
// the oracle recomputation: distinct attributors counted once per arena
// and valid attributions counted as events.
func (r *participationRunner) opReadReputation(author string) {
	r.step++
	r.t.Helper()
	got, err := r.world.reputation.Execute(r.world.ctx, persuasionapp.GetAuthorReputationQuery{AuthorID: author})
	if !r.check("read-reputation", true, err) || got == nil {
		return
	}
	wantPeople := map[string]map[string]bool{}
	wantValid := map[string]int64{}
	for _, st := range r.arenas {
		for changeID, args := range st.attributed {
			owner := ""
			for acct, pos := range st.positions {
				for _, id := range pos.changes {
					if id == changeID {
						owner = acct
					}
				}
			}
			// The projection counts only attributions of currently
			// active verified attributors: suspending one removes
			// their past attributions from every headline.
			if !r.world.accounts.isActive(owner) {
				continue
			}
			for argID := range args {
				arg := st.arguments[argID]
				if arg.author != author {
					continue
				}
				if wantPeople[st.id] == nil {
					wantPeople[st.id] = make(map[string]bool)
				}
				wantPeople[st.id][owner] = true
				wantValid[st.id]++
			}
		}
	}
	var wantHeadline int64
	for _, people := range wantPeople {
		wantHeadline += int64(len(people))
	}
	if got.InfluencedPeople() != wantHeadline {
		r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "reputation-headline", want: true, got: false})
	}
	observedValid := make(map[string]int64)
	for _, arena := range got.Arenas {
		observedValid[arena.ArenaID.String()] = arena.ValidAttributions
		if int64(len(wantPeople[arena.ArenaID.String()])) != arena.DistinctPeople {
			r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "reputation-distinct", want: true, got: false})
		}
	}
	for arenaID, want := range wantValid {
		if observedValid[arenaID] != want {
			r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "reputation-valid", want: true, got: false})
		}
	}
	for arenaID := range observedValid {
		if _, ok := wantValid[arenaID]; !ok {
			r.diverged = append(r.diverged, participationDivergence{step: r.step, op: "reputation-extra-arena", want: true, got: false})
		}
	}
}

// modelPositionValues is the closed vocabulary the generator draws from,
// plus one fixed refusal fixture the contract never accepts.
// modelPositionValid is the generator-side vocabulary: the three
// documented values parse, the fixed refusal fixture does not.
func modelPositionValid(raw string) bool {
	switch raw {
	case "agree", "disagree", "undecided":
		return true
	default:
		return false
	}
}

var modelPositionValues = []string{"agree", "disagree", "undecided", "sideways"}

// modelAccount is one buyer the sequences share across both arenas,
// carrying the platform row identity alongside the oracle key.
type modelAccount struct {
	uuid  string
	pg    pgtype.UUID
	email string
}

// setupSequence creates two draft arenas and three active verified buyers,
// publishes both arenas, and returns the handles the script uses.
func (r *participationRunner) setupSequence(seq int) ([2]string, []*modelAccount) {
	r.t.Helper()
	var arenas [2]string
	var accounts []*modelAccount
	for i := 0; i < 3; i++ {
		email := fmt.Sprintf("ent-part-%d-%d@arena.example.com", seq, i)
		pgAcc, err := r.world.queries.CreateAccount(r.world.ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
		if err != nil {
			r.t.Fatalf("create model account: %v", err)
		}
		if _, err := r.world.queries.SetEmailVerified(r.world.ctx, pgAcc.ID); err != nil {
			r.t.Fatalf("verify model account: %v", err)
		}
		var pgID pgtype.UUID
		if err := pgID.Scan(uuidString(pgAcc.ID)); err != nil {
			r.t.Fatalf("scan account uuid: %v", err)
		}
		uuid := uuidString(pgAcc.ID)
		r.world.accounts.setActive(uuid, true)
		accounts = append(accounts, &modelAccount{uuid: uuid, pg: pgID, email: email})
	}
	arenas[0] = r.opCreateDraft(accounts[0].uuid, seq, 0)
	arenas[1] = r.opCreateDraft(accounts[1].uuid, seq, 1)
	r.opPublish(arenas[0], accounts[0].uuid)
	r.opPublish(arenas[1], accounts[1].uuid)
	return arenas, accounts
}

// opSuspend flips one buyer between active and suspended in both the
// stored row and the oracle gates, so aggregates and reputations must drop
// and restore them.
func (r *participationRunner) opSuspend(account *modelAccount, active bool) {
	r.step++
	r.t.Helper()
	status := "active"
	if !active {
		status = "suspended"
	}
	if _, err := r.world.queries.UpdateAccountStatus(r.world.ctx, platformpg.UpdateAccountStatusParams{ID: account.pg, Status: status}); err != nil {
		r.t.Fatalf("update account status: %v", err)
	}
	r.world.accounts.setActive(account.uuid, active)
	if !r.check("suspend", true, nil) {
		return
	}
	r.auditAll()
}

// auditAll re-reads every aggregate and reputation the last commands could
// have moved: exclusion and derivation are recomputed, never cached.
func (r *participationRunner) auditAll() {
	r.t.Helper()
	for id := range r.arenas {
		r.opReadAggregate(id)
	}
	authors := make(map[string]bool)
	for _, st := range r.arenas {
		for _, arg := range st.arguments {
			authors[arg.author] = true
		}
	}
	for author := range authors {
		r.opReadReputation(author)
	}
}

// runOneSequence executes one deterministic journey over two arenas.
func (r *participationRunner) runOneSequence(rnd *testsource.Random, seq int) {
	arenas, accounts := r.setupSequence(seq)
	steps := 8 + rnd.Int64n(9)
	argSeq := 0
	for i := 0; i < int(steps); i++ {
		arenaID := arenas[rnd.Int64n(int64(len(arenas)))]
		account := accounts[rnd.Int64n(int64(len(accounts)))]
		position := modelPositionValues[rnd.Int64n(int64(len(modelPositionValues)))]
		if rnd.Int64n(20) == 0 {
			position = "sideways"
		}
		switch rnd.Int64n(12) {
		case 0, 1:
			r.opConfirm(arenaID, account.uuid, position)
			// A third of confirmations repeat immediately: the retry
			// must resolve the recorded projection without writing.
			if rnd.Int64n(3) == 0 {
				r.opConfirm(arenaID, account.uuid, position)
			}
		case 2, 3:
			changeID := r.opChange(arenaID, account.uuid, position)
			_ = changeID
		case 4:
			author := accounts[rnd.Int64n(int64(len(accounts)))].uuid
			r.opPublishArgument(arenaID, author, seq, argSeq)
			argSeq++
		case 5:
			r.opAttributeFlow(arenaID, account.uuid, rnd, seq)
		case 6:
			r.opReadAggregate(arenaID)
		case 7:
			r.opReadReputation(accounts[rnd.Int64n(int64(len(accounts)))].uuid)
		case 8:
			r.opSuspend(accounts[rnd.Int64n(int64(len(accounts)))], rnd.Int64n(2) == 0)
		case 9:
			r.opPublish(arenaID, account.uuid)
		case 10:
			r.opClose(arenaID, account.uuid)
		default:
			r.opConfirm("018f6b2a-0000-7000-8000-000000000000", account.uuid, position)
		}
		r.world.clock.Advance(time.Second)
	}
	r.auditAll()
}

// opAttributeFlow attributes eligible arguments of the arena's latest
// change, then probes one refusal: an unknown argument, a foreign change,
// or a duplicate inside the request.
func (r *participationRunner) opAttributeFlow(arenaID, account string, rnd *testsource.Random, seq int) {
	r.t.Helper()
	st := r.arenas[arenaID]
	if len(st.changeOrder) == 0 {
		return
	}
	changeID := st.changeOrder[len(st.changeOrder)-1]
	eligible := []string{}
	for id, arg := range st.arguments {
		if arg.author == account {
			continue
		}
		if st.attributed[changeID] != nil && st.attributed[changeID][id] {
			continue
		}
		eligible = append(eligible, id)
	}
	sort.Strings(eligible)
	pick := []string{}
	for i := 0; i < 3 && len(eligible) > 0; i++ {
		pick = append(pick, eligible[rnd.Int64n(int64(len(eligible)))])
	}
	// Deduplicate the draw while preserving order: the request itself must
	// carry each argument once.
	unique := pick[:0]
	seenPick := make(map[string]bool)
	for _, id := range pick {
		if !seenPick[id] {
			seenPick[id] = true
			unique = append(unique, id)
		}
	}
	r.opRecordAttribution(arenaID, account, changeID, unique)
	// Half the attributions repeat identically: the retry must resolve
	// the recorded set without duplicating rows.
	if len(unique) > 0 && rnd.Int64n(2) == 0 {
		r.opRecordAttribution(arenaID, account, changeID, unique)
	}
	switch rnd.Int64n(4) {
	case 0:
		r.opRecordAttribution(arenaID, account, changeID, []string{"018f6b2a-ffff-7000-8000-ffffffffffff"})
	case 1:
		r.opRecordAttribution(arenaID, account, "018f6b2a-ffff-7000-8000-ffffffffffff", unique)
	case 2:
		if len(unique) > 0 {
			r.opRecordAttribution(arenaID, account, changeID, append(append([]string{}, unique...), unique[0]))
		}
	}
	_ = seq
}

// TestParticipationModel runs deterministic participation journeys over
// two real arenas per sequence: drafts through closing, confirmations,
// changes, arguments, attributions, aggregate and reputation reads, with
// every outcome and every reconstructed projection compared against the
// independent oracle.
func TestParticipationModel(t *testing.T) {
	seed := testsource.SeedFor(t)
	rnd := testsource.NewRandom(seed)
	world := newParticipationWorld(t)
	const sequences = 120
	divergentRuns := 0
	var first []participationDivergence
	var firstTrace []string
	accepts, refusals, replays := 0, 0, 0
	for seq := 0; seq < sequences; seq++ {
		runner := &participationRunner{t: t, world: world, arenas: make(map[string]*modelArena)}
		runner.runOneSequence(rnd, seq)
		accepts += runner.accepts
		refusals += runner.refusals
		replays += runner.replays
		if len(runner.diverged) > 0 {
			divergentRuns++
			if first == nil {
				first = append([]participationDivergence{}, runner.diverged...)
				if len(first) > 5 {
					first = first[:5]
				}
				firstTrace = append([]string{}, runner.trace...)
				if len(firstTrace) > 10 {
					firstTrace = firstTrace[len(firstTrace)-10:]
				}
			}
		}
	}
	if accepts == 0 || refusals == 0 || replays == 0 {
		t.Fatalf("model never exercised a full path: accepts=%d refusals=%d replays=%d", accepts, refusals, replays)
	}
	if divergentRuns > 0 {
		t.Fatalf("%d of %d sequences diverged, first: %+v trace: %v", divergentRuns, sequences, first, firstTrace)
	}
	t.Logf("participation model: %d sequences, accepts=%d refusals=%d replays=%d, seed %d", sequences, accepts, refusals, replays, seed)
}

// TestParticipationConcurrent races changes, confirmations and attributions:
// version chains stay contiguous, the three-argument limit holds, confirms
// stay idempotent, and the aggregate still reconstructs from history.
// newRaceAccounts creates verified active buyers outside the scripted
// generator, for the concurrency and mutant probes.
func newRaceAccounts(t *testing.T, world *participationWorld, prefix string, n int) []*modelAccount {
	t.Helper()
	ctx := world.ctx
	accounts := make([]*modelAccount, 0, n)
	for i := 0; i < n; i++ {
		email := fmt.Sprintf("%s-%d@arena.example.com", prefix, i)
		pgAcc, err := world.queries.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		if _, err := world.queries.SetEmailVerified(ctx, pgAcc.ID); err != nil {
			t.Fatalf("verify account: %v", err)
		}
		uuid := uuidString(pgAcc.ID)
		world.accounts.setActive(uuid, true)
		var pgID pgtype.UUID
		if err := pgID.Scan(uuid); err != nil {
			t.Fatalf("scan uuid: %v", err)
		}
		accounts = append(accounts, &modelAccount{uuid: uuid, pg: pgID, email: email})
	}
	return accounts
}

// seedRaceArguments stores attribution targets outside the scripted
// generator and returns their identifiers in creation order.
func seedRaceArguments(t *testing.T, world *participationWorld, arenaID, author string, n int) []string {
	t.Helper()
	ctx := world.ctx
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		key, err := argumentsdomain.ParseIdempotencyKey(fmt.Sprintf("race-arg-%d", i))
		if err != nil {
			t.Fatalf("ParseIdempotencyKey: %v", err)
		}
		relation, err := argumentsdomain.ParseRelation("support")
		if err != nil {
			t.Fatalf("ParseRelation: %v", err)
		}
		content, err := argumentsdomain.ParseContent(fmt.Sprintf("Racing argument %d sustains the claim", i), text.GraphemeCount)
		if err != nil {
			t.Fatalf("ParseContent: %v", err)
		}
		published, _, err := world.arguments.CreateArgument(ctx, argumentsapp.CreateArgumentRequest{
			ArenaID:        mustParseArgArenaID(t, arenaID),
			AuthorID:       mustParseArgAccountID(t, author),
			Relation:       relation,
			Content:        content,
			IdempotencyKey: key,
			CreatedAt:      world.clock.Now(),
		})
		if err != nil {
			t.Fatalf("seed race argument: %v", err)
		}
		ids = append(ids, published.ID.String())
	}
	return ids
}

func TestParticipationConcurrent(t *testing.T) {
	world := newParticipationWorld(t)
	ctx := world.ctx
	runner := &participationRunner{t: t, world: world, arenas: make(map[string]*modelArena)}
	accounts := newRaceAccounts(t, world, "race-part", 3)
	arenaID := runner.opCreateDraft(accounts[0].uuid, 950, 0)
	runner.opPublish(arenaID, accounts[0].uuid)
	if len(runner.diverged) > 0 {
		t.Fatalf("concurrent setup diverged: %+v", runner.diverged)
	}
	for _, acct := range accounts {
		runner.opConfirm(arenaID, acct.uuid, "undecided")
	}
	if len(runner.diverged) > 0 {
		t.Fatalf("concurrent positioning diverged: %+v", runner.diverged)
	}
	const changers = 10
	var wg sync.WaitGroup
	for i := 0; i < changers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			position := "agree"
			if i%2 == 1 {
				position = "disagree"
			}
			_, _ = world.change.Execute(ctx, positionsapp.ChangePositionCommand{AccountID: accounts[0].uuid, ArenaID: arenaID, Position: position})
		}(i)
	}
	wg.Wait()
	history, err := world.positions.ListPositionChanges(ctx,
		mustParsePositionsArenaID(t, arenaID), mustParsePositionsAccountID(t, accounts[0].uuid))
	if err != nil {
		t.Fatalf("list changes: %v", err)
	}
	seenVersions := make(map[int32]bool)
	for _, record := range history {
		seenVersions[record.Change.Version()] = true
	}
	for want := int32(2); want <= int32(len(history)+1); want++ {
		if !seenVersions[want] {
			t.Fatalf("change chain skipped version %d in %d rows", want, len(history))
		}
	}
	// Resync the oracle from the observed tip: the winners are whatever
	// the chain holds, in whatever order they committed.
	projection, err := world.positions.GetByAccountAndArena(ctx,
		mustParsePositionsArenaID(t, arenaID), mustParsePositionsAccountID(t, accounts[0].uuid))
	if err != nil {
		t.Fatalf("read raced projection: %v", err)
	}
	oraclePos := runner.arenas[arenaID].positions[accounts[0].uuid]
	oraclePos.current = projection.CurrentPosition().String()
	oraclePos.version = projection.Version()
	oraclePos.changes = oraclePos.changes[:0]
	for i := len(history) - 1; i >= 0; i-- {
		oraclePos.changes = append(oraclePos.changes, history[i].ID)
	}
	// Eight racing attributions over six arguments: the three-argument
	// limit holds and no pair duplicates, whatever the interleaving.
	raceArgs := seedRaceArguments(t, world, arenaID, accounts[1].uuid, 6)
	raceChangePos := "agree"
	if projection.CurrentPosition().String() == "agree" {
		raceChangePos = "disagree"
	}
	raceChange, err := world.change.Execute(ctx, positionsapp.ChangePositionCommand{AccountID: accounts[0].uuid, ArenaID: arenaID, Position: raceChangePos})
	if err != nil {
		t.Fatalf("seed race change: %v", err)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pick := []string{raceArgs[i%6], raceArgs[(i+1)%6], raceArgs[(i+2)%6]}
			_, _ = world.record.Execute(ctx, persuasionapp.RecordAttributionsCommand{AccountID: accounts[0].uuid, ChangeID: raceChange.ChangeID, ArgumentIDs: pick})
		}(i)
	}
	wg.Wait()
	credited, err := world.persuasion.ListAttributedArgumentIDs(ctx, mustParsePersuasionChangeID(t, raceChange.ChangeID))
	if err != nil {
		t.Fatalf("list raced attributions: %v", err)
	}
	if len(credited) > 3 {
		t.Fatalf("racing attributions stored %d rows, over the three-argument limit", len(credited))
	}
	seenPairs := make(map[string]bool)
	for _, id := range credited {
		if seenPairs[id.String()] {
			t.Fatalf("racing attributions duplicated %q", id.String())
		}
		seenPairs[id.String()] = true
	}
	// Ten racing confirmations of one fresh account: idempotent, one row.
	freshEmail := "race-part-fresh@arena.example.com"
	freshAcc, err := world.queries.CreateAccount(ctx, platformpg.CreateAccountParams{Email: freshEmail, Status: "active"})
	if err != nil {
		t.Fatalf("create fresh account: %v", err)
	}
	if _, err := world.queries.SetEmailVerified(ctx, freshAcc.ID); err != nil {
		t.Fatalf("verify fresh account: %v", err)
	}
	freshUUID := uuidString(freshAcc.ID)
	world.accounts.setActive(freshUUID, true)
	for i := 0; i < changers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = world.confirm.Execute(ctx, positionsapp.ConfirmInitialPositionCommand{AccountID: freshUUID, ArenaID: arenaID, Position: "agree"})
		}()
	}
	wg.Wait()
	// Resync the oracle from every observed projection: races bypass the
	// step oracles by design, so the audit reconciles from stored rows.
	st := runner.arenas[arenaID]
	for _, uuid := range []string{accounts[0].uuid, accounts[1].uuid, accounts[2].uuid, freshUUID} {
		projection, err := world.positions.GetByAccountAndArena(ctx,
			mustParsePositionsArenaID(t, arenaID), mustParsePositionsAccountID(t, uuid))
		if err != nil {
			t.Fatalf("read raced projection of %s: %v", uuid, err)
		}
		pos, ok := st.positions[uuid]
		if !ok {
			pos = &modelPosition{}
			st.positions[uuid] = pos
		}
		pos.initial = projection.InitialPosition().String()
		pos.current = projection.CurrentPosition().String()
		pos.version = projection.Version()
	}
	runner.opReadAggregate(arenaID)
	for _, d := range runner.diverged {
		t.Fatalf("post-race aggregate diverged: %+v", d)
	}
}

// TestParticipationDetectsMutantAggregate proves the harness bites: an
// oracle that counts suspended accounts must report the privacy violation
// once a positioned account suspends.
func TestParticipationDetectsMutantAggregate(t *testing.T) {
	world := newParticipationWorld(t)
	runner := &participationRunner{t: t, world: world, arenas: make(map[string]*modelArena)}
	accounts := newRaceAccounts(t, world, "mutant-part", 3)
	arenaID := runner.opCreateDraft(accounts[0].uuid, 960, 0)
	runner.opPublish(arenaID, accounts[0].uuid)
	for _, acct := range accounts {
		runner.opConfirm(arenaID, acct.uuid, "agree")
	}
	if len(runner.diverged) > 0 {
		t.Fatalf("mutant setup diverged: %+v", runner.diverged)
	}
	runner.opSuspend(accounts[0], false)
	runner.mutantCountIneligible = true
	runner.opReadAggregate(arenaID)
	if len(runner.diverged) == 0 {
		t.Fatal("mutant aggregate (counts the suspended) reported no divergence")
	}
	found := false
	for _, d := range runner.diverged {
		if len(d.op) >= 9 && d.op[:9] == "aggregate" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mutant divergences %+v do not name the aggregate", runner.diverged)
	}
}

func mustParsePositionsArenaID(t *testing.T, raw string) positionsdomain.ArenaID {
	t.Helper()
	id, err := positionsdomain.ParseArenaID(raw)
	if err != nil {
		t.Fatalf("ParseArenaID(%q): %v", raw, err)
	}
	return id
}

func mustParsePositionsAccountID(t *testing.T, raw string) positionsdomain.AccountID {
	t.Helper()
	id, err := positionsdomain.ParseAccountID(raw)
	if err != nil {
		t.Fatalf("ParseAccountID(%q): %v", raw, err)
	}
	return id
}

func mustParsePersuasionChangeID(t *testing.T, raw string) persuasiondomain.ChangeID {
	t.Helper()
	id, err := persuasiondomain.ParseChangeID(raw)
	if err != nil {
		t.Fatalf("ParseChangeID(%q): %v", raw, err)
	}
	return id
}
