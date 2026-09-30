// Tests of the participation surface over real PostgreSQL and a real HTTP
// server (P18-T07B): the journey is driven the way a browser drives it — the
// page, the double-submit token the page renders, the four transitions — and
// the state is then read back from the database, because the answer of a POST
// is not the proof that a ledger line was written.
package bootstrap_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	arenaspostgres "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentspostgres "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/postgres"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	identitypostgres "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	walletpostgres "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// participationJourney is the composed process serving both journeys over one
// listener, with the disposable database the repositories write to.
type participationJourney struct {
	account       *bootstrap.AccountSurface
	participation *bootstrap.ParticipationSurface
	server        *httptest.Server
	pool          *pgxpool.Pool
	clock         clockseed.System
}

// newParticipationJourney composes the account and participation surfaces the
// way `arena server` does: one pool, one security boundary, both mounted on the
// platform router, served by a real listener.
func newParticipationJourney(t *testing.T) *participationJourney {
	t.Helper()

	database := dbtest.New(t)
	pool := database.Pool.Pool()
	clock := clockseed.NewClock()
	random := clockseed.NewRandom()

	manager, err := security.New(security.Options{Env: config.EnvTest, Clock: clock, Random: random})
	if err != nil {
		t.Fatalf("security.New() error = %v", err)
	}

	options := bootstrap.Options{
		Env:      config.EnvTest,
		Logger:   slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool:     pool,
		Clock:    clock,
		Random:   random,
		Assets:   manifestFixture(t),
		Security: manager,
	}

	account, err := bootstrap.ComposeAccount(options)
	if err != nil {
		t.Fatalf("ComposeAccount() error = %v", err)
	}
	options.CursorSecret = []byte(cursorSecret)
	participation, err := bootstrap.ComposeParticipation(options)
	if err != nil {
		t.Fatalf("ComposeParticipation() error = %v", err)
	}

	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clock)
	mux, err := httpserver.NewMuxWith(
		ids, locale.NewResolver(), securityheaders.Config{},
		[]httpserver.Surface{account.Surface(), participation.Surface()},
	)
	if err != nil {
		t.Fatalf("httpserver.NewMuxWith() error = %v", err)
	}

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &participationJourney{account: account, participation: participation, server: server, pool: pool, clock: clock}
}

// hiddenField matches one hidden input of a rendered form, so the test can read
// the values the page generated — the CSRF token, the attempt key and the
// change to attribute — instead of inventing them.
func hiddenField(name string) *regexp.Regexp {
	return regexp.MustCompile(`name="` + name + `" value="([^"]+)"`)
}

// signedIn registers, confirms and signs in one account through the account
// journey, and returns the identifier the database assigned to it.
func signedIn(t *testing.T, journey *participationJourney, client *http.Client, email, password string) string {
	t.Helper()

	register := url.Values{
		"email":      {email},
		"password":   {password},
		"csrf_token": {csrfToken(t, openPage(t, client, journey.server, "/register"))},
	}
	if response := submit(t, client, journey.server, "/register", register); response.StatusCode != http.StatusOK {
		t.Fatalf("POST /register(%s) status = %d, want 200", email, response.StatusCode)
	}

	address, err := identitydomain.ParseEmail(email)
	if err != nil {
		t.Fatalf("ParseEmail(%q) error = %v", email, err)
	}
	token, found := journey.account.LocalSink().LastTokenForEmail(address)
	if !found {
		t.Fatalf("registration of %s issued no verification token", email)
	}

	verify := url.Values{
		"token":      {token},
		"csrf_token": {csrfToken(t, openPage(t, client, journey.server, "/verify"))},
	}
	if response := submit(t, client, journey.server, "/verify", verify); response.StatusCode != http.StatusOK {
		t.Fatalf("POST /verify(%s) status = %d, want 200", email, response.StatusCode)
	}

	login := url.Values{
		"email":      {email},
		"password":   {password},
		"csrf_token": {csrfToken(t, openPage(t, client, journey.server, "/login"))},
	}
	if response := submit(t, client, journey.server, "/login", login); response.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /login(%s) status = %d, want 303", email, response.StatusCode)
	}

	stored, err := identitypostgres.NewRepository(journey.pool).GetAccountByEmail(context.Background(), address)
	if err != nil {
		t.Fatalf("GetAccountByEmail(%q) error = %v", email, err)
	}
	return stored.ID().String()
}

// publishedArena seeds one published Arena owned by the creator. Creating it
// through the repository and not through the use case is deliberate: publishing
// requires a billing pass, and this test is about participation, not about
// selling.
func publishedArena(t *testing.T, journey *participationJourney, creatorID, slug string) *arenasdomain.Arena {
	t.Helper()

	statement, err := arenasdomain.ParseStatement(
		"O debate público melhora quando os argumentos podem ser verificados.",
		arenasdomain.DefaultStatementPolicy(),
	)
	if err != nil {
		t.Fatalf("ParseStatement() error = %v", err)
	}
	// The categories are the ones the migration seeds; the slug is the
	// stored identifier and the display name comes from the catalogs.
	category, err := arenasdomain.ParseCategory("technology")
	if err != nil {
		t.Fatalf("ParseCategory() error = %v", err)
	}
	language, err := arenasdomain.ParseLanguage("pt-BR")
	if err != nil {
		t.Fatalf("ParseLanguage() error = %v", err)
	}
	parsedSlug, err := arenasdomain.ParseSlug(slug)
	if err != nil {
		t.Fatalf("ParseSlug() error = %v", err)
	}

	repository := arenaspostgres.NewRepository(journey.pool)
	draft, err := repository.CreateArena(context.Background(), arenasapp.CreateArenaRequest{
		CreatorID: arenasdomain.CreatorID(creatorID),
		Statement: statement,
		Category:  category,
		Language:  language,
	})
	if err != nil {
		t.Fatalf("CreateArena() error = %v", err)
	}

	published, err := repository.PublishArenaDraft(
		context.Background(), draft.ID(), draft.CreatorID(), parsedSlug, journey.clock.Now(), draft.Version(),
	)
	if err != nil {
		t.Fatalf("PublishArenaDraft() error = %v", err)
	}
	return published
}

// creditInk gives one account a balance through the wallet use case, the same
// path a purchase or a free cycle takes.
func creditInk(t *testing.T, journey *participationJourney, accountID string, amount int64, key string) {
	t.Helper()

	credits := walletapp.NewCreditInkUseCase(walletpostgres.NewRepository(journey.pool), journey.clock)
	_, err := credits.Execute(context.Background(), walletapp.CreditInkCommand{
		AccountID:      accountID,
		Bucket:         string(walletdomain.BucketFree),
		OperationType:  walletdomain.OperationCreditFree.String(),
		Amount:         amount,
		Reference:      "journey-credit",
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("CreditInk(%s) error = %v", accountID, err)
	}
}

// inkBalance reads the derived balance of one account from the database.
func inkBalance(t *testing.T, journey *participationJourney, accountID string) int64 {
	t.Helper()

	balance, err := walletpostgres.NewRepository(journey.pool).DerivedBalance(
		context.Background(), walletdomain.AccountID(accountID),
	)
	if err != nil {
		t.Fatalf("DerivedBalance(%s) error = %v", accountID, err)
	}
	return balance.Free.Int64()
}

// TestTheParticipationJourneyIsServedAndChargesInk is the validation of the
// task: over real PostgreSQL, the page of an Arena and its four transitions are
// served, a publication stores the argument and debits INK — read back from the
// database, not inferred from the response.
func TestTheParticipationJourneyIsServedAndChargesInk(t *testing.T) {
	t.Parallel()

	journey := newParticipationJourney(t)
	const slug = "jornada-de-participacao"

	// Two people: one publishes the argument that the other will credit, since
	// the domain refuses attributing an argument to its own author.
	author := browser(t)
	participant := browser(t)
	authorID := signedIn(t, journey, author, "author@example.test", "correct horse battery staple")
	participantID := signedIn(t, journey, participant, "participant@example.test", "correct horse battery staple")

	publishedArena(t, journey, participantID, slug)
	creditInk(t, journey, authorID, 1000, "credit-author")
	creditInk(t, journey, participantID, 1000, "credit-participant")

	// 1. The page of a published Arena is served to a signed-in person, with
	// the forms it will submit.
	page := openPage(t, participant, journey.server, "/arenas/"+slug)
	if !strings.Contains(page, `action="/arenas/`+slug+`"`) && !strings.Contains(page, slug+`/position`) {
		t.Errorf("the page does not carry the participation forms of %s", slug)
	}

	// 2. The argument of the other person, published through the same surface:
	// this is the transition that charges INK. The publication form only
	// exists once the person has a position, so the confirmation comes first.
	confirmedByAuthor := submit(t, author, journey.server, "/arenas/"+slug+"/position", url.Values{
		"position":   {"undecided"},
		"csrf_token": {csrfToken(t, openPage(t, author, journey.server, "/arenas/"+slug))},
	})
	if confirmedByAuthor.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(confirmedByAuthor.Body)
		t.Fatalf("POST /arenas/%s/position (author) status = %d, want 303 (body: %.300s)", slug, confirmedByAuthor.StatusCode, body)
	}

	authorPage := openPage(t, author, journey.server, "/arenas/"+slug)
	authorBalance := inkBalance(t, journey, authorID)
	argument := "O argumento publicado aqui precisa custar INK."
	cost := int64(text.GraphemeCount(argument))
	published := submit(t, author, journey.server, "/arenas/"+slug+"/arguments", url.Values{
		"relation":   {"support"},
		"content":    {argument},
		"attempt":    {attemptKey(t, authorPage)},
		"csrf_token": {csrfToken(t, authorPage)},
	})
	if published.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(published.Body)
		t.Fatalf("POST /arenas/%s/arguments status = %d, want 303 (body: %.300s)", slug, published.StatusCode, body)
	}
	if after := inkBalance(t, journey, authorID); after != authorBalance-cost {
		t.Errorf("the author balance went from %d to %d, want the publication of %d graphemes to cost %d INK",
			authorBalance, after, cost, cost)
	}

	// The argument exists in the database, with the content that was charged.
	arenaID, err := argumentsdomain.ParseArenaID(arenaIDFor(t, journey, slug))
	if err != nil {
		t.Fatalf("ParseArenaID() error = %v", err)
	}
	relation, err := argumentsdomain.ParseRelation(argumentsdomain.RelationSupport)
	if err != nil {
		t.Fatalf("ParseRelation() error = %v", err)
	}
	storedArguments, err := argumentspostgres.NewRepository(journey.pool).ListArenaArguments(
		context.Background(), arenaID, relation, nil, 10,
	)
	if err != nil {
		t.Fatalf("ListArenaArguments() error = %v", err)
	}
	if len(storedArguments) != 1 {
		t.Fatalf("the Arena holds %d arguments, want the one just published", len(storedArguments))
	}
	if storedArguments[0].Content == nil || storedArguments[0].Content.String() != argument {
		t.Errorf("the stored argument does not carry the published content")
	}
	if cost != int64(storedArguments[0].Content.GraphemeCost()) {
		t.Errorf("the stored cost is %d, want the %d graphemes that were charged",
			storedArguments[0].Content.GraphemeCost(), cost)
	}

	// The argument must predate the change it will be credited by.
	time.Sleep(2 * time.Millisecond)

	// 3. The initial position, confirmed once and immutable afterwards.
	confirmed := submit(t, participant, journey.server, "/arenas/"+slug+"/position", url.Values{
		"position":   {"agree"},
		"csrf_token": {csrfToken(t, openPage(t, participant, journey.server, "/arenas/"+slug))},
	})
	if confirmed.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(confirmed.Body)
		t.Fatalf("POST /arenas/%s/position status = %d, want 303 (body: %.300s)", slug, confirmed.StatusCode, body)
	}

	// 4. One change of that position, which is what an attribution names.
	changed := submit(t, participant, journey.server, "/arenas/"+slug+"/position/change", url.Values{
		"position":   {"disagree"},
		"csrf_token": {csrfToken(t, openPage(t, participant, journey.server, "/arenas/"+slug))},
	})
	if changed.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(changed.Body)
		t.Fatalf("POST /arenas/%s/position/change status = %d, want 303 (body: %.300s)", slug, changed.StatusCode, body)
	}

	// 5. The attribution of the change to the argument of the other person. The
	// page carries both the change and the argument it lists, so the test reads
	// them instead of guessing identifiers.
	attributionPage := openPage(t, participant, journey.server, "/arenas/"+slug)
	change := hiddenField("change_id").FindStringSubmatch(attributionPage)
	if len(change) != 2 {
		t.Fatalf("the page renders no change to attribute: %.400s", attributionPage)
	}
	selected := hiddenField("argument_ids").FindStringSubmatch(attributionPage)
	if len(selected) != 2 {
		t.Fatalf("the page lists no argument to credit: %.400s", attributionPage)
	}

	attributed := submit(t, participant, journey.server, "/arenas/"+slug+"/attributions", url.Values{
		"change_id":    {change[1]},
		"argument_ids": {selected[1]},
		"csrf_token":   {csrfToken(t, attributionPage)},
	})
	if attributed.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(attributed.Body)
		t.Fatalf("POST /arenas/%s/attributions status = %d, want 303 (body: %.300s)", slug, attributed.StatusCode, body)
	}

	// Nothing the participant published: the balance of the author is the one
	// that moved, and the participant still holds what was credited.
	if balance := inkBalance(t, journey, participantID); balance != 1000 {
		t.Errorf("the participant balance is %d, want the credited 1000 untouched", balance)
	}
}

// TestTheParticipationSurfaceRefusesAnonymousTransitions proves the composing
// half of the task's validation: the four transitions are mounted (none of them
// is a 404 of an uncomposed route) and they refuse a visitor without a session.
func TestTheParticipationSurfaceRefusesAnonymousTransitions(t *testing.T) {
	t.Parallel()

	journey := newParticipationJourney(t)
	client := browser(t)

	for _, transition := range []string{"position", "position/change", "arguments", "attributions"} {
		path := "/arenas/qualquer-arena/" + transition
		response := submit(t, client, journey.server, path, url.Values{})
		if response.StatusCode == http.StatusNotFound {
			t.Errorf("POST %s answered 404: the transition is not mounted", path)
			continue
		}
		if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s status = %d, want 401 or 403 for a visitor without a session", path, response.StatusCode)
		}
	}
}

// attemptKey reads the idempotency key the page minted for the publication
// form. Using the rendered key is the point: a double submission of the same
// document must resolve the argument already recorded instead of charging twice.
func attemptKey(t *testing.T, document string) string {
	t.Helper()

	matches := hiddenField("attempt").FindStringSubmatch(document)
	if len(matches) != 2 {
		t.Fatalf("the page renders no attempt key: %.400s", document)
	}
	return matches[1]
}

// arenaIDFor reads the identifier of the Arena stored under a slug.
func arenaIDFor(t *testing.T, journey *participationJourney, slug string) string {
	t.Helper()

	parsed, err := arenasdomain.ParseSlug(slug)
	if err != nil {
		t.Fatalf("ParseSlug(%q) error = %v", slug, err)
	}
	arena, err := arenaspostgres.NewRepository(journey.pool).GetPublicArenaBySlug(context.Background(), parsed)
	if err != nil {
		t.Fatalf("GetPublicArenaBySlug(%q) error = %v", slug, err)
	}
	return arena.ID().String()
}

// TestTheSurfaceDeclaresTheRouteCount is a cheap guard on the composition: the
// page plus its four transitions, mounted beside the twelve account pages.
func TestTheSurfaceDeclaresTheRouteCount(t *testing.T) {
	t.Parallel()

	journey := newParticipationJourney(t)
	if got := len(journey.participation.Routes()); got != 5 {
		t.Errorf("the participation surface declares %d routes, want 5", got)
	}
	if got := len(journey.account.Routes()); got != 12 {
		t.Errorf("the account surface declares %d routes, want 12", got)
	}
}
