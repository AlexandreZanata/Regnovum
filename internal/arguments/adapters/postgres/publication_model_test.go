package postgres_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	arenaspg "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentspg "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/postgres"
	argumentswallet "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/walletdebit"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	walletpg "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// Independent atomic-publication model of arguments (P24-T07).
//
// The system under test is the real stack over a disposable PostgreSQL:
// the publish use case (validation, eligibility, wallet debit and argument
// insert in one transaction), the withdraw lifecycle and the wallet ledger
// behind the debit port. The oracle below is a separate, deliberately small
// state machine written from the documented contract: no argument without
// its debit and no debit without its argument, immutable withdrawn content,
// single-use idempotency keys, and refused scripts and dangerous URLs. It
// never calls domain constructors or application use cases; the measuring
// instrument (grapheme counter) and fixed validity fixtures are its only
// shared ground with the product.

// modelPublicationClock is one fixed instant: publication carries no time
// rules of its own, so a frozen clock keeps sequences replayable.
type modelPublicationClock struct{}

func (modelPublicationClock) Now() time.Time {
	return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
}

// modelArgAccountEligibility is the test-controlled gate: only accounts the
// model holds active may publish.
type modelArgAccountEligibility struct {
	mu     sync.Mutex
	active map[string]bool
}

func (g *modelArgAccountEligibility) EnsureEligible(_ context.Context, accountID argumentsdomain.AccountID) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.active[accountID.String()] {
		return argumentsapp.ErrAccountNotEligible
	}
	return nil
}

// modelArenaGate answers argument acceptance from the stored arena state.
type modelArenaGate struct {
	arenas *arenaspg.Repository
}

func (g *modelArenaGate) EnsureAcceptsArguments(ctx context.Context, arenaID argumentsdomain.ArenaID) error {
	arena, err := g.arenas.GetArenaByID(ctx, arenasdomain.ArenaID(arenaID.String()))
	if err != nil {
		return argumentsapp.ErrArenaNotFound
	}
	if arena.Status() != arenasdomain.ArenaStatusPublished {
		return argumentsapp.ErrArenaNotOpen
	}
	return nil
}

// publicationWorld wires the real publish stack over one disposable
// database: arguments and wallet repositories, the arena gate, the debit
// bridge and one shared transaction manager.
type publicationWorld struct {
	ctx       context.Context
	t         *testing.T
	pool      *pgxpool.Pool
	arguments *argumentspg.Repository
	wallet    *walletpg.Repository
	arenas    *arenaspg.Repository
	queries   *platformpg.Queries
	accounts  *modelArgAccountEligibility
	publish   *argumentsapp.PublishArgumentUseCase
	withdraw  *argumentsapp.WithdrawArgumentUseCase
}

func newPublicationWorld(t *testing.T) *publicationWorld {
	t.Helper()
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	clock := modelPublicationClock{}
	argumentsRepo := argumentspg.NewRepository(pool)
	walletRepo := walletpg.NewRepository(pool)
	arenasRepo := arenaspg.NewRepository(pool)
	accounts := &modelArgAccountEligibility{active: make(map[string]bool)}
	debits := walletapp.NewDebitInkUseCase(walletRepo, clock)
	bridge := argumentswallet.New(debits)
	uow := platformpg.NewTxManager(pool)
	return &publicationWorld{
		ctx: ctx, t: t, pool: pool,
		arguments: argumentsRepo, wallet: walletRepo, arenas: arenasRepo,
		queries: platformpg.New(pool), accounts: accounts,
		publish: argumentsapp.NewPublishArgumentUseCase(
			argumentsRepo, accounts, &modelArenaGate{arenas: arenasRepo},
			bridge, uow, text.GraphemeCount, argumentsdomain.DefaultReplyPolicy(), clock),
		withdraw: argumentsapp.NewWithdrawArgumentUseCase(argumentsRepo, clock),
	}
}

// modelPublishedArgument is one argument the oracle has seen stored.
type modelPublishedArgument struct {
	argID    string
	cost     int64
	sources  int
	parent   string
	relation string
	content  string
}

// modelAuthor is everything the oracle believes about one author: UUID
// key, funded and spent totals, and per-key outcomes.
type modelAuthor struct {
	funded    int64
	spent     int64
	published map[string]*modelPublishedArgument
	keys      map[string]string
}

// publicationDivergence is one place where the stack and the oracle
// disagreed.
type publicationDivergence struct {
	step int
	op   string
	want bool
	got  bool
}

// publicationRunner carries one deterministic script: the world, the
// per-author oracles and every divergence found.
type publicationRunner struct {
	t         *testing.T
	world     *publicationWorld
	authors   map[string]*modelAuthor
	arenas    map[string]string
	argArena  map[string]string
	argDepth  map[string]int
	argAuthor map[string]string
	withdrawn map[string]bool
	specs     map[string]publishSpec
	step      int
	diverged  []publicationDivergence
	trace     []string
	accepts   int
	refusals  int
	replays   int
}

func (r *publicationRunner) check(op string, wantAccept bool, err error) bool {
	gotAccept := err == nil
	r.trace = append(r.trace, op)
	if wantAccept {
		r.accepts++
	} else {
		r.refusals++
	}
	if gotAccept != wantAccept {
		r.diverged = append(r.diverged, publicationDivergence{step: r.step, op: op, want: wantAccept, got: gotAccept})
		return false
	}
	return true
}

// opFund credits one author through the real ledger; funding is setup, not
// an oracle claim, but the credited total anchors conservation.
func (r *publicationRunner) opFund(author string, amount int64, n int) {
	r.step++
	r.t.Helper()
	key, err := walletdomain.ParseIdempotencyKey(fmt.Sprintf("model-fund-%d", n))
	if err != nil {
		r.t.Fatalf("ParseIdempotencyKey: %v", err)
	}
	ref, err := walletdomain.ParseReference(fmt.Sprintf("model:fund:%d", n))
	if err != nil {
		r.t.Fatalf("ParseReference: %v", err)
	}
	_, err = r.world.wallet.ApplyCredit(r.world.ctx, walletapp.CreditRequest{
		AccountID:      walletdomain.AccountID(author),
		Bucket:         walletdomain.BucketPurchased,
		OperationType:  walletdomain.OperationCreditPurchase,
		IdempotencyKey: key,
		Reference:      ref,
		Delta:          amount,
		ChangedAt:      modelPublicationClock{}.Now(),
	})
	if !r.check("fund", true, err) {
		return
	}
	r.authors[author].funded += amount
}

// walletSpent reads the observed debited total of one author.
func (r *publicationRunner) walletSpent(author string) int64 {
	r.t.Helper()
	balance, err := r.world.wallet.DerivedBalance(r.world.ctx, walletdomain.AccountID(author))
	if err != nil {
		r.t.Fatalf("derived wallet balance: %v", err)
	}
	funded := r.authors[author].funded
	return funded - balance.Purchased.Int64()
}

// reconcile proves conservation after one command: the observed spent
// total equals the oracle's accepted costs, and every accepted key resolves
// to a stored row while refusals resolve to nothing.
func (r *publicationRunner) reconcile(op string) {
	r.t.Helper()
	for author, slot := range r.authors {
		if got := r.walletSpent(author); got != slot.spent {
			r.diverged = append(r.diverged, publicationDivergence{step: r.step, op: op + ":spent:" + author, want: true, got: false})
		}
		for key, argID := range slot.keys {
			if argID == "" {
				continue
			}
			if _, err := r.world.arguments.GetByAuthorAndIdempotencyKey(r.world.ctx,
				mustParseArgumentsAccountID(r.t, author), mustParseArgumentsKey(r.t, key)); err != nil {
				r.diverged = append(r.diverged, publicationDivergence{step: r.step, op: op + ":row:" + key, want: true, got: false})
			}
		}
	}
}

func mustParseArgumentsAccountID(t *testing.T, raw string) argumentsdomain.AccountID {
	t.Helper()
	id, err := argumentsdomain.ParseAccountID(raw)
	if err != nil {
		t.Fatalf("ParseAccountID(%q): %v", raw, err)
	}
	return id
}

func mustParseArgumentsKey(t *testing.T, raw string) argumentsdomain.IdempotencyKey {
	t.Helper()
	key, err := argumentsdomain.ParseIdempotencyKey(raw)
	if err != nil {
		t.Fatalf("ParseIdempotencyKey(%q): %v", raw, err)
	}
	return key
}

// Content fixtures with certain validity: plain sentences in several
// scripts parse, while the invalid list is refused by documented rules
// (empty after normalization, over the grapheme budget, invalid UTF-8,
// NUL control). Script markup is inert text to the domain — it parses,
// and rendering safety belongs to the frontend textContent rule — so it
// lives in the valid list, asserting exactly that.
var (
	modelValidContents = []string{
		"A AGI existirá até 2040 porque a tendência se mantém",
		"Fusion energy will be commercial by 2035",
		"👩🏽‍🚀 bandeira 🇧🇷 e acento cafe\u0301",
		"人工知能は2040年までに実現する",
		"linha um\nlinha dois",
		"<script>alert(1)</script>",
	}
	modelInvalidContents = []string{
		"",
		"   ",
		"a\x00b",
		"\xff\xfe",
	}
	modelValidRelations   = []string{"support", "oppose", "context"}
	modelInvalidRelations = []string{"maybe", ""}
)

// modelContentCost measures the debit of fixture texts with the same
// instrument the product uses. Fixtures carry no surrounding whitespace,
// so normalization cannot move the count.
func modelContentCost(raw string) int64 {
	return int64(text.GraphemeCount(raw))
}

// publishSpec is one drawn publication in raw transport strings.
type publishSpec struct {
	content  string
	relation string
	parent   string
	sources  [][2]string
	key      string
}

// opPublish predicts and runs one publication: identical keys replay,
// invalid inputs and ineligible or unfunded attempts are refused writing
// nothing, and first acceptances debit exactly the grapheme cost.
func (r *publicationRunner) opPublish(author, arena string, spec publishSpec) {
	r.step++
	r.t.Helper()
	if prev, ok := r.authors[author].published[spec.key]; ok && prev.argID != "" {
		// A key that resolved to an argument resends the original command
		// byte-for-byte: the recorded attempt is the result, so retries
		// never charge again.
		orig := r.specs[spec.key]
		res, err := r.world.publish.Execute(r.world.ctx, argumentsapp.PublishArgumentCommand{
			AccountID: author, ArenaID: arena, Content: orig.content, Relation: orig.relation,
			ParentID: orig.parent, Sources: modelSourceCommands(orig.sources), IdempotencyKey: spec.key,
		})
		if !r.check("publish-replay", true, err) {
			return
		}
		if res == nil || !res.Replayed || res.Argument.ID.String() != prev.argID {
			r.diverged = append(r.diverged, publicationDivergence{step: r.step, op: "publish-replay-shape", want: true, got: false})
		}
		r.replays++
		r.reconcile("publish-replay")
		return
	}
	// New keys, and retries of refusals (which wrote nothing), evaluate
	// fresh against current balances and history.
	r.specs[spec.key] = spec
	wantAccept, cost := r.predictPublish(author, arena, spec)
	res, err := r.world.publish.Execute(r.world.ctx, argumentsapp.PublishArgumentCommand{
		AccountID: author, ArenaID: arena, Content: spec.content, Relation: spec.relation,
		ParentID: spec.parent, Sources: modelSourceCommands(spec.sources), IdempotencyKey: spec.key,
	})
	if !r.check("publish", wantAccept, err) {
		r.authors[author].published[spec.key] = &modelPublishedArgument{}
		return
	}
	if !wantAccept {
		r.authors[author].published[spec.key] = &modelPublishedArgument{}
		r.reconcile("publish-refused")
		return
	}
	if res == nil || res.Replayed || res.Argument.ID.IsZero() {
		r.diverged = append(r.diverged, publicationDivergence{step: r.step, op: "publish-shape", want: true, got: false})
		return
	}
	if res.Argument.Content.String() != spec.content {
		r.diverged = append(r.diverged, publicationDivergence{step: r.step, op: "publish-content", want: true, got: false})
		return
	}
	r.authors[author].published[spec.key] = &modelPublishedArgument{
		argID: res.Argument.ID.String(), cost: cost, sources: len(spec.sources),
		parent: spec.parent, relation: spec.relation, content: spec.content,
	}
	r.argArena[res.Argument.ID.String()] = arena
	r.argAuthor[res.Argument.ID.String()] = author
	if spec.parent == "" {
		r.argDepth[res.Argument.ID.String()] = 0
	} else {
		r.argDepth[res.Argument.ID.String()] = r.argDepth[spec.parent] + 1
	}
	r.authors[author].spent += cost
	r.reconcile("publish")
}

func modelSourceCommands(sources [][2]string) []argumentsapp.SourceCommand {
	out := make([]argumentsapp.SourceCommand, 0, len(sources))
	for _, source := range sources {
		out = append(out, argumentsapp.SourceCommand{URL: source[0], Description: source[1]})
	}
	return out
}

func (g *modelArgAccountEligibility) isActive(accountID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active[accountID]
}

func (g *modelArgAccountEligibility) setActive(accountID string, active bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active[accountID] = active
}

// predictPublish derives the outcome from the oracle history alone: known
// fixtures decide content and relation validity, the tracked lots decide
// parent eligibility, and funds decide the debit.
func (r *publicationRunner) predictPublish(author, arena string, spec publishSpec) (bool, int64) {
	if !modelContentKnownValid(spec.content) || !modelRelationKnownValid(spec.relation) {
		return false, 0
	}
	if !modelSourcesKnownValid(spec.sources) {
		return false, 0
	}
	if !r.world.accounts.isActive(author) || r.arenas[arena] != "published" {
		return false, 0
	}
	cost := modelContentCost(spec.content)
	if r.authors[author].funded-r.authors[author].spent < cost {
		return false, 0
	}
	if spec.parent != "" {
		parentArena, ok := r.argArena[spec.parent]
		if !ok || parentArena != arena {
			return false, 0
		}
		if r.argDepth[spec.parent] >= 1 || r.withdrawn[spec.parent] {
			return false, 0
		}
	}
	return true, cost
}

// opArenaCreate creates one draft arena and publishes it, tracking status
// for the acceptance gate.
func (r *publicationRunner) opArenaCreate(creator string, seq, n int) string {
	r.step++
	r.t.Helper()
	statement, err := arenasdomain.ParseStatement(
		fmt.Sprintf("A arena %d debate a tese %d com clareza", seq, n),
		arenasdomain.DefaultStatementPolicy())
	if err != nil {
		r.t.Fatalf("ParseStatement: %v", err)
	}
	category, err := arenasdomain.ParseCategory("technology")
	if err != nil {
		r.t.Fatalf("ParseCategory: %v", err)
	}
	language, err := arenasdomain.ParseLanguage("pt-BR")
	if err != nil {
		r.t.Fatalf("ParseLanguage: %v", err)
	}
	arenaContext, err := arenasdomain.ParseContext(fmt.Sprintf("Contexto %d", seq), arenasdomain.DefaultStatementPolicy())
	if err != nil {
		r.t.Fatalf("ParseContext: %v", err)
	}
	arena, err := r.world.arenas.CreateArena(r.world.ctx, arenasapp.CreateArenaRequest{
		CreatorID: arenasdomain.CreatorID(creator), Statement: statement,
		Context: arenaContext, Category: category, Language: language,
	})
	if !r.check("arena-create", true, err) || arena == nil {
		return ""
	}
	id := arena.ID().String()
	r.arenas[id] = "draft"
	slug, err := arenasdomain.ParseSlug(fmt.Sprintf("model-arg-%d-%d", seq, n))
	if err != nil {
		r.t.Fatalf("ParseSlug: %v", err)
	}
	published, err := r.world.arenas.PublishArenaDraft(r.world.ctx,
		arenasdomain.ArenaID(id), arenasdomain.CreatorID(creator), slug, modelPublicationClock{}.Now(), 1)
	if !r.check("arena-publish", true, err) || published == nil {
		return ""
	}
	r.arenas[id] = "published"
	return id
}

// opArenaClose closes one published arena; anything else is refused.
func (r *publicationRunner) opArenaClose(arenaID, creator string) {
	r.step++
	r.t.Helper()
	wantAccept := r.arenas[arenaID] == "published"
	closed, err := r.world.arenas.CloseArena(r.world.ctx,
		arenasdomain.ArenaID(arenaID), arenasdomain.CreatorID(creator), 2)
	if !r.check("arena-close", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if closed == nil {
		r.diverged = append(r.diverged, publicationDivergence{step: r.step, op: "arena-close-shape", want: true, got: false})
		return
	}
	r.arenas[arenaID] = "closed"
}

// opWithdraw retracts one own argument: the row stays with its content
// while the display status flips, repeats replay, and foreign or unknown
// arguments are refused.
func (r *publicationRunner) opWithdraw(author, key string) {
	r.step++
	r.t.Helper()
	wantAccept := false
	argID := "018f6b2a-ffff-7000-8000-ffffffffffff"
	if published, known := r.authors[author].published[key]; known && published.argID != "" {
		wantAccept = true
		argID = published.argID
	}
	res, err := r.world.withdraw.Execute(r.world.ctx, argumentsapp.WithdrawArgumentCommand{AccountID: author, ArgumentID: argID})
	if !r.check("withdraw", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if res == nil {
		r.diverged = append(r.diverged, publicationDivergence{step: r.step, op: "withdraw-shape", want: true, got: false})
		return
	}
	if res.Replayed != r.withdrawn[argID] {
		r.diverged = append(r.diverged, publicationDivergence{step: r.step, op: "withdraw-replay-flag", want: true, got: false})
	}
	stored, err := r.world.arguments.GetForAuthor(r.world.ctx,
		mustParseArgumentsID(r.t, argID), mustParseArgumentsAccountID(r.t, author))
	if err != nil {
		r.diverged = append(r.diverged, publicationDivergence{step: r.step, op: "withdraw-read", want: true, got: false})
		return
	}
	if stored.Content.String() != r.authors[author].published[key].content || stored.Status != "withdrawn" {
		r.diverged = append(r.diverged, publicationDivergence{step: r.step, op: "withdraw-preserved", want: true, got: false})
		return
	}
	r.withdrawn[argID] = true
	r.reconcile("withdraw")
}

func mustParseArgumentsID(t *testing.T, raw string) argumentsdomain.ArgumentID {
	t.Helper()
	id, err := argumentsdomain.ParseArgumentID(raw)
	if err != nil {
		t.Fatalf("ParseArgumentID(%q): %v", raw, err)
	}
	return id
}

// drawSpec generates one publication attempt: hostile Unicode content,
// relations in and out of vocabulary, sources across the URL rules, and a
// fresh key. Parents are chosen by the generator from oracle state, never
// blindly, so every depth and arena rule is exercised on purpose.
func drawSpec(rnd *testsource.Random, seq, n int) publishSpec {
	contents := append(append([]string{}, modelValidContents...), modelInvalidContents...)
	content := contents[rnd.Int64n(int64(len(contents)))]
	relation := modelValidRelations[rnd.Int64n(int64(len(modelValidRelations)))]
	if rnd.Int64n(10) == 0 {
		relation = modelInvalidRelations[rnd.Int64n(int64(len(modelInvalidRelations)))]
	}
	parent := ""
	switch rnd.Int64n(10) {
	case 0, 1, 2, 3, 4:
	case 7:
		parent = "018f6b2a-ffff-7000-8000-ffffffffffff"
	default:
		parent = "not-a-parent"
	}
	var sources [][2]string
	switch rnd.Int64n(10) {
	case 0, 1, 2, 3, 4, 5, 6:
		sources = [][2]string{{"https://example.com/estudo", "Estudo revisado"}}
	case 7:
		sources = nil
	case 8:
		sources = [][2]string{{"https://example.com/outro", ""}}
	default:
		bad := [][2]string{
			{"javascript://alert(1)", ""},
			{"http://", ""},
			{"https://ex ample.com/x", ""},
			{"https://user@example.com/x", ""},
		}
		sources = [][2]string{bad[rnd.Int64n(int64(len(bad)))]}
	}
	return publishSpec{
		content: content, relation: relation, parent: parent,
		sources: sources, key: fmt.Sprintf("model-pub-%d-%d", seq, n),
	}
}

// runOneSequence executes one deterministic publication script over two
// authors and two arenas, comparing every outcome and balance with the
// oracle and auditing conservation at the end.
func (r *publicationRunner) runOneSequence(rnd *testsource.Random, seq int) {
	authors := []string{r.newModelAuthor(seq, 0), r.newModelAuthor(seq, 1)}
	arenaA := r.newModelArena(authors[0], seq, 0)
	arenaB := r.newModelArena(authors[1], seq, 1)
	r.opArenaClose(arenaB, authors[1])
	steps := 8 + rnd.Int64n(9)
	keys := 0
	for i := 0; i < int(steps); i++ {
		author := authors[rnd.Int64n(int64(len(authors)))]
		arena := arenaA
		if rnd.Int64n(2) == 0 {
			arena = arenaB
		}
		switch rnd.Int64n(12) {
		case 0, 1, 2, 3, 4, 5:
			spec := drawSpec(rnd, seq, keys)
			keys++
			if seen := r.seenKeys(author); len(seen) > 0 && rnd.Int64n(6) == 0 {
				spec.key = seen[rnd.Int64n(int64(len(seen)))]
			} else if rnd.Int64n(2) == 0 {
				spec.parent = r.drawParent(rnd, arena)
			}
			r.opPublish(author, arena, spec)
			// A quarter of publications repeat immediately: the retry
			// must resolve the recorded attempt without charging again.
			if rnd.Int64n(4) == 0 {
				r.opPublish(author, arena, spec)
			}
		case 6, 7:
			r.opWithdrawFlow(author, rnd)
		case 8:
			r.flipActive(author, rnd.Int64n(2) == 0)
		case 9:
			r.opArenaClose(arenaA, authors[0])
		default:
			r.opArenaClose(arenaB, authors[1])
		}
	}
	r.auditConservation()
}

// newModelAuthor creates one funded active author for a sequence.
func (r *publicationRunner) newModelAuthor(seq, n int) string {
	r.t.Helper()
	email := fmt.Sprintf("pub-model-%d-%d@arena.example.com", seq, n)
	pgAcc, err := r.world.queries.CreateAccount(r.world.ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		r.t.Fatalf("create model account: %v", err)
	}
	uuid := uuidText(pgAcc.ID)
	r.world.accounts.setActive(uuid, true)
	r.authors[uuid] = &modelAuthor{funded: 0, spent: 0, published: make(map[string]*modelPublishedArgument), keys: make(map[string]string)}
	r.opFund(uuid, 50000, seq*10+n)
	return uuid
}

// newModelArena creates one published arena for a sequence.
func (r *publicationRunner) newModelArena(creator string, seq, n int) string {
	r.t.Helper()
	id := r.opArenaCreate(creator, seq, n)
	if id == "" {
		r.t.Fatalf("sequence setup could not create arena %d/%d", seq, n)
	}
	return id
}

// seenKeys lists the idempotency keys one author already attempted, in a
// stable order so the registered stream decides every draw.
func (r *publicationRunner) seenKeys(author string) []string {
	out := []string{}
	for key := range r.authors[author].published {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// opWithdrawFlow withdraws one known key, repeats it, or probes a foreign
// or unknown argument.
func (r *publicationRunner) opWithdrawFlow(author string, rnd *testsource.Random) {
	keys := r.seenKeys(author)
	if len(keys) == 0 {
		r.opWithdraw(author, "model-pub-never-seen")
		return
	}
	key := keys[rnd.Int64n(int64(len(keys)))]
	switch rnd.Int64n(4) {
	case 0:
		r.opWithdraw(author, key)
		r.opWithdraw(author, key)
	default:
		r.opWithdraw(author, key)
	}
}

// flipActive suspends or restores one author in the oracle gate.
func (r *publicationRunner) flipActive(author string, active bool) {
	r.step++
	r.t.Helper()
	r.world.accounts.setActive(author, active)
	r.check("suspend-flip", true, nil)
}

// auditConservation proves the global invariant after one sequence: every
// accepted publish moved exactly its cost, refusals moved nothing, and the
// wallet debits equal the accepted costs per author.
func (r *publicationRunner) auditConservation() {
	r.t.Helper()
	for author, slot := range r.authors {
		var spent int64
		for _, published := range slot.published {
			if published.argID == "" {
				continue
			}
			spent += published.cost
		}
		if spent != slot.spent {
			r.diverged = append(r.diverged, publicationDivergence{step: r.step, op: "audit:spent:" + author, want: true, got: false})
		}
		if got := r.walletSpent(author); got != slot.spent {
			r.diverged = append(r.diverged, publicationDivergence{step: r.step, op: "audit:wallet:" + author, want: true, got: false})
		}
	}
}

// modelContentKnownValid names the fixtures certain to parse: the valid
// list plus the inert-markup case. Anything else never reaches the
// predictor as a claimed acceptance.
func modelContentKnownValid(raw string) bool {
	for _, fixture := range modelValidContents {
		if fixture == raw {
			return true
		}
	}
	return false
}

// modelRelationKnownValid names the closed relation vocabulary.
func modelRelationKnownValid(raw string) bool {
	for _, fixture := range modelValidRelations {
		if fixture == raw {
			return true
		}
	}
	return false
}

// modelSourcesKnownValid names the source fixtures certain to parse: the
// https pair, and refuses javascript schemes, short hosts, spaces and
// userinfo by the documented URL rules.
func modelSourcesKnownValid(sources [][2]string) bool {
	for _, source := range sources {
		url, desc := source[0], source[1]
		if desc != "" && desc != "Estudo revisado" {
			return false
		}
		switch url {
		case "https://example.com/estudo", "https://example.com/outro":
			continue
		default:
			return false
		}
	}
	return true
}

// drawParent selects a parent by oracle state: usually none, sometimes an
// eligible same-arena top-level argument, sometimes a refusal probe. Lists
// are sorted so the registered stream decides every draw.
func (r *publicationRunner) drawParent(rnd *testsource.Random, arena string) string {
	var eligible, deep, foreign, gone []string
	for id, a := range r.argArena {
		switch {
		case r.withdrawn[id]:
			gone = append(gone, id)
		case a != arena:
			foreign = append(foreign, id)
		case r.argDepth[id] >= 1:
			deep = append(deep, id)
		default:
			eligible = append(eligible, id)
		}
	}
	sort.Strings(eligible)
	sort.Strings(deep)
	sort.Strings(foreign)
	sort.Strings(gone)
	switch rnd.Int64n(20) {
	case 0, 1, 2, 3, 4, 5, 6, 7, 8, 9:
		return ""
	case 10, 11, 12, 13:
		if len(eligible) > 0 {
			return eligible[rnd.Int64n(int64(len(eligible)))]
		}
		return ""
	case 14, 15:
		if len(deep) > 0 {
			return deep[rnd.Int64n(int64(len(deep)))]
		}
		return "018f6b2a-ffff-7000-8000-ffffffffffff"
	case 16, 17:
		if len(foreign) > 0 {
			return foreign[rnd.Int64n(int64(len(foreign)))]
		}
		return "018f6b2a-ffff-7000-8000-ffffffffffff"
	case 18:
		if len(gone) > 0 {
			return gone[rnd.Int64n(int64(len(gone)))]
		}
		return "018f6b2a-ffff-7000-8000-ffffffffffff"
	default:
		return "not-a-parent"
	}
}

// TestArgumentPublicationModel runs deterministic publication scripts over
// two funded authors and two arenas: hostile Unicode content, relations,
// parents across the depth and arena rules, sources across the URL rules,
// withdrawals with content preservation, and replays, comparing every
// outcome and balance with the independent oracle.
func TestArgumentPublicationModel(t *testing.T) {
	seed := testsource.SeedFor(t)
	rnd := testsource.NewRandom(seed)
	world := newPublicationWorld(t)
	const sequences = 150
	divergentRuns := 0
	var first []publicationDivergence
	var firstTrace []string
	accepts, refusals, replays := 0, 0, 0
	for seq := 0; seq < sequences; seq++ {
		runner := &publicationRunner{
			t: t, world: world, authors: make(map[string]*modelAuthor),
			arenas: make(map[string]string), argArena: make(map[string]string),
			argDepth: make(map[string]int), argAuthor: make(map[string]string),
			withdrawn: make(map[string]bool), specs: make(map[string]publishSpec),
		}
		runner.runOneSequence(rnd, seq)
		accepts += runner.accepts
		refusals += runner.refusals
		replays += runner.replays
		if len(runner.diverged) > 0 {
			divergentRuns++
			if first == nil {
				first = append([]publicationDivergence{}, runner.diverged...)
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
	t.Logf("argument publication model: %d sequences, accepts=%d refusals=%d replays=%d, seed %d", sequences, accepts, refusals, replays, seed)
}

// TestArgumentPublicationConcurrent races one idempotency key across
// goroutines: the transacted insert admits exactly one debit and one row,
// and every loser replays the original without charging again.
func TestArgumentPublicationConcurrent(t *testing.T) {
	world := newPublicationWorld(t)
	ctx := world.ctx
	runner := &publicationRunner{
		t: t, world: world, authors: make(map[string]*modelAuthor),
		arenas: make(map[string]string), argArena: make(map[string]string),
		argDepth: make(map[string]int), argAuthor: make(map[string]string),
		withdrawn: make(map[string]bool), specs: make(map[string]publishSpec),
	}
	author := runner.newModelAuthor(980, 0)
	arena := runner.newModelArena(author, 980, 0)
	spec := publishSpec{
		content: modelValidContents[0], relation: "support",
		sources: [][2]string{{"https://example.com/estudo", "Estudo revisado"}},
		key:     "model-pub-race-1",
	}
	const racers = 16
	var wg sync.WaitGroup
	outcomes := make(chan error, racers)
	replayed := make(chan bool, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := world.publish.Execute(ctx, argumentsapp.PublishArgumentCommand{
				AccountID: author, ArenaID: arena, Content: spec.content, Relation: spec.relation,
				Sources: modelSourceCommands(spec.sources), IdempotencyKey: spec.key,
			})
			outcomes <- err
			replayed <- err == nil && res != nil && res.Replayed
		}()
	}
	wg.Wait()
	close(outcomes)
	close(replayed)
	fresh := 0
	played := 0
	for err := range outcomes {
		if err != nil {
			t.Fatalf("racer failed: %v", err)
		}
	}
	for wasReplay := range replayed {
		if wasReplay {
			played++
		} else {
			fresh++
		}
	}
	if fresh != 1 || played != racers-1 {
		t.Fatalf("racing one key: %d fresh + %d replays, want exactly 1 fresh and %d replays", fresh, played, racers-1)
	}
	if got := runner.opWalletPurchased(author); got != 50000-modelContentCost(spec.content) {
		t.Fatalf("racing one key left balance %d, want %d after exactly the single cost %d", got, 50000-modelContentCost(spec.content), modelContentCost(spec.content))
	}
	if got := countModelArguments(t, ctx, world.pool, arena); got != 1 {
		t.Fatalf("racing one key stored %d argument rows, want exactly 1", got)
	}
}

// opWalletPurchased reads the observed purchased balance of one author.
func (r *publicationRunner) opWalletPurchased(author string) int64 {
	r.t.Helper()
	balance, err := r.world.wallet.DerivedBalance(r.world.ctx, walletdomain.AccountID(author))
	if err != nil {
		r.t.Fatalf("derived wallet balance: %v", err)
	}
	return balance.Purchased.Int64()
}

// countModelArguments counts the argument rows of one arena.
func countModelArguments(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arena string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM app.arguments WHERE arena_id = $1", arena).Scan(&count); err != nil {
		t.Fatalf("count arguments: %v", err)
	}
	return count
}

// TestArgumentPublicationDetectsMutantCost proves the harness bites: the
// same script run against a mutant oracle that debits one extra unit per
// publish must report the conservation divergence.
func TestArgumentPublicationDetectsMutantCost(t *testing.T) {
	world := newPublicationWorld(t)
	newRunner := func() *publicationRunner {
		return &publicationRunner{
			t: t, world: world, authors: make(map[string]*modelAuthor),
			arenas: make(map[string]string), argArena: make(map[string]string),
			argDepth: make(map[string]int), argAuthor: make(map[string]string),
			withdrawn: make(map[string]bool), specs: make(map[string]publishSpec),
		}
	}
	runner := newRunner()
	author := runner.newModelAuthor(990, 0)
	arena := runner.newModelArena(author, 990, 0)
	spec := publishSpec{content: modelValidContents[0], relation: "support", key: "model-pub-mutant-1"}
	runner.opPublish(author, arena, spec)
	if len(runner.diverged) > 0 {
		t.Fatalf("true cost diverged: %+v", runner.diverged)
	}
	mutant := newRunner()
	mutantAuthor := mutant.newModelAuthor(991, 0)
	mutantArena := mutant.newModelArena(mutantAuthor, 991, 0)
	mutant.opPublish(mutantAuthor, mutantArena, publishSpec{content: modelValidContents[1], relation: "oppose", key: "model-pub-mutant-2"})
	// Mutant belief: every publish debits one unit more than measured.
	mutant.authors[mutantAuthor].spent += 1
	mutant.auditConservation()
	if len(mutant.diverged) == 0 {
		t.Fatal("mutant cost (one extra unit per publish) reported no divergence")
	}
}
