// Tests of the Arena participation journey (P18-T06).
//
// Every test drives the surface the way a browser without JavaScript does: a
// read that renders a document, then a form POST that carries the double submit
// in its body and the cookies of the open page. Nothing here executes the client
// module, which is the point — the journey has to be complete, and useful,
// before the module runs; the module only adds what a document cannot (the local
// position choice and the busy state of a submission already on its way).
//
// The four transitions are exercised through fakes of the application ports, so
// the suite proves what the adapter decides — which document is read, which
// value is validated against which vocabulary, which refusal becomes a message
// on a field instead of a page — without a database.
package html_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	adapterhtml "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/html"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	persuasionapp "github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	positionsapp "github.com/AlexandreZanata/Regnovum/internal/positions/application"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// participationSlug is the Arena every test drives.
const participationSlug = "arena-de-teste"

// sessionToken is the opaque cookie the fake validator accepts.
const sessionToken = "session-token"

// arenaIDFixture is the Arena identifier every fake answers with.
const arenaIDFixture = "00000000-0000-0000-0000-0000000000a1"

// accountIDFixture is the account the fake session validator resolves.
const accountIDFixture = "00000000-0000-0000-0000-0000000000b1"

// participationManifest resolves the six assets the page loads.
func participationManifest() assets.Manifest {
	names := []string{
		"pages/arena.js",
		"styles/reset.css",
		"styles/tokens.css",
		"styles/base.css",
		"styles/primitives.css",
		"styles/arena.css",
	}
	records := make(map[string]assets.Record, len(names))
	for _, name := range names {
		records[name] = assets.Record{Path: "/assets/" + strings.ReplaceAll(name, "/", "-"), SHA256: strings.Repeat("c", 64)}
	}
	return assets.Manifest{Version: 1, Assets: records}
}

// participationArena is a published Arena, reconstituted the way the adapter
// reads one from storage.
func participationArena(t *testing.T) *arenasdomain.Arena {
	t.Helper()

	statement, err := arenasdomain.ParseStatement("O debate público melhora com argumentos verificáveis.", arenasdomain.DefaultStatementPolicy())
	if err != nil {
		t.Fatalf("ParseStatement() error = %v", err)
	}
	category, err := arenasdomain.ParseCategory("tecnologia")
	if err != nil {
		t.Fatalf("ParseCategory() error = %v", err)
	}
	language, err := arenasdomain.ParseLanguage("pt-BR")
	if err != nil {
		t.Fatalf("ParseLanguage() error = %v", err)
	}
	slug, err := arenasdomain.ParseSlug(participationSlug)
	if err != nil {
		t.Fatalf("ParseSlug() error = %v", err)
	}
	published := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	arena, err := arenasdomain.ReconstituteArena(
		arenasdomain.ArenaID(arenaIDFixture),
		arenasdomain.CreatorID("00000000-0000-0000-0000-0000000000b2"),
		statement,
		arenasdomain.Context{},
		category,
		language,
		arenasdomain.ArenaStatusPublished,
		slug,
		2,
		published,
		&published,
		nil,
	)
	if err != nil {
		t.Fatalf("ReconstituteArena() error = %v", err)
	}
	return arena
}

// ownerPosition is the stored projection of the account the fake session
// resolves.
func ownerPosition(t *testing.T) *positionsdomain.DebatePosition {
	t.Helper()

	initial, err := positionsdomain.ParsePosition(positionsdomain.PositionAgree)
	if err != nil {
		t.Fatalf("ParsePosition(agree) error = %v", err)
	}
	current, err := positionsdomain.ParsePosition(positionsdomain.PositionDisagree)
	if err != nil {
		t.Fatalf("ParsePosition(disagree) error = %v", err)
	}
	stored, err := positionsdomain.ReconstituteDebatePosition(
		mustPositionsArenaID(t),
		mustPositionsAccountID(t),
		initial,
		current,
		2,
		time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("ReconstituteDebatePosition() error = %v", err)
	}
	return stored
}

func mustPositionsArenaID(t *testing.T) positionsdomain.ArenaID {
	t.Helper()
	id, err := positionsdomain.ParseArenaID(arenaIDFixture)
	if err != nil {
		t.Fatalf("ParseArenaID() error = %v", err)
	}
	return id
}

func mustPositionsAccountID(t *testing.T) positionsdomain.AccountID {
	t.Helper()
	id, err := positionsdomain.ParseAccountID(accountIDFixture)
	if err != nil {
		t.Fatalf("ParseAccountID() error = %v", err)
	}
	return id
}

// fakeArenaDocument answers the Arena of the slug, or the refusal of the test.
type fakeArenaDocument struct {
	arena *arenasdomain.Arena
	err   error
}

func (f *fakeArenaDocument) Execute(_ context.Context, _ string) (*arenasdomain.Arena, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.arena, nil
}

// fakePorts record what the surface asked the application to do.
type fakePorts struct {
	position   *positionsdomain.DebatePosition
	positionOK bool
	aggregate  *positionsapp.PositionAggregate
	changes    []positionsapp.PositionChangeRecord
	arguments  map[string][]argumentsapp.PublicArgument

	confirmCalls     []positionsapp.ConfirmInitialPositionCommand
	changeCalls      []positionsapp.ChangePositionCommand
	publishCalls     []argumentsapp.PublishArgumentCommand
	attributionCalls []persuasionapp.RecordAttributionsCommand

	confirmErr     error
	changeErr      error
	publishErr     error
	attributionErr error
	publishReplay  bool
}

func (f *fakePorts) MyPositionExecute(_ context.Context, _ positionsapp.GetMyPositionQuery) (*positionsdomain.DebatePosition, error) {
	if !f.positionOK {
		return nil, positionsapp.ErrPositionNotFound
	}
	return f.position, nil
}

func (f *fakePorts) ConfirmExecute(_ context.Context, command positionsapp.ConfirmInitialPositionCommand) (*positionsapp.ConfirmInitialPositionResult, error) {
	f.confirmCalls = append(f.confirmCalls, command)
	if f.confirmErr != nil {
		return nil, f.confirmErr
	}
	return &positionsapp.ConfirmInitialPositionResult{}, nil
}

func (f *fakePorts) ChangeExecute(_ context.Context, command positionsapp.ChangePositionCommand) (*positionsapp.ChangePositionResult, error) {
	f.changeCalls = append(f.changeCalls, command)
	if f.changeErr != nil {
		return nil, f.changeErr
	}
	return &positionsapp.ChangePositionResult{ChangeID: "change-1"}, nil
}

func (f *fakePorts) AggregateExecute(_ context.Context, _ positionsapp.GetPositionAggregateQuery) (*positionsapp.PositionAggregate, error) {
	if f.aggregate == nil {
		return nil, errors.New("no aggregate fixture")
	}
	return f.aggregate, nil
}

func (f *fakePorts) ChangesExecute(_ context.Context, _ positionsapp.ListPositionChangesQuery) ([]positionsapp.PositionChangeRecord, error) {
	return f.changes, nil
}

func (f *fakePorts) ArgumentsExecute(_ context.Context, query argumentsapp.ListArenaArgumentsQuery) (*argumentsapp.ArgumentPage, error) {
	page := &argumentsapp.ArgumentPage{Arguments: f.arguments[query.Relation]}
	return page, nil
}

func (f *fakePorts) PublishExecute(_ context.Context, command argumentsapp.PublishArgumentCommand) (*argumentsapp.PublishArgumentResult, error) {
	f.publishCalls = append(f.publishCalls, command)
	if f.publishErr != nil {
		return nil, f.publishErr
	}
	return &argumentsapp.PublishArgumentResult{Replayed: f.publishReplay}, nil
}

func (f *fakePorts) AttributionExecute(_ context.Context, command persuasionapp.RecordAttributionsCommand) (*persuasionapp.RecordAttributionsResult, error) {
	f.attributionCalls = append(f.attributionCalls, command)
	if f.attributionErr != nil {
		return nil, f.attributionErr
	}
	return &persuasionapp.RecordAttributionsResult{}, nil
}

// The eight ports share one recording fake, and each port is its own type:
// the surface declares an interface per operation, so a single struct cannot
// stand in for eight of them — and the compiler is what proves the adapter asks
// the application for exactly these calls.
type (
	fakeMyPosition        struct{ ports *fakePorts }
	fakeConfirmPosition   struct{ ports *fakePorts }
	fakeChangePosition    struct{ ports *fakePorts }
	fakePositionAggregate struct{ ports *fakePorts }
	fakePositionChanges   struct{ ports *fakePorts }
	fakeArenaArguments    struct{ ports *fakePorts }
	fakePublishArgument   struct{ ports *fakePorts }
	fakeAttributions      struct{ ports *fakePorts }
)

func (f fakeMyPosition) Execute(ctx context.Context, query positionsapp.GetMyPositionQuery) (*positionsdomain.DebatePosition, error) {
	return f.ports.MyPositionExecute(ctx, query)
}

func (f fakeConfirmPosition) Execute(ctx context.Context, command positionsapp.ConfirmInitialPositionCommand) (*positionsapp.ConfirmInitialPositionResult, error) {
	return f.ports.ConfirmExecute(ctx, command)
}

func (f fakeChangePosition) Execute(ctx context.Context, command positionsapp.ChangePositionCommand) (*positionsapp.ChangePositionResult, error) {
	return f.ports.ChangeExecute(ctx, command)
}

func (f fakePositionAggregate) Execute(ctx context.Context, query positionsapp.GetPositionAggregateQuery) (*positionsapp.PositionAggregate, error) {
	return f.ports.AggregateExecute(ctx, query)
}

func (f fakePositionChanges) Execute(ctx context.Context, query positionsapp.ListPositionChangesQuery) ([]positionsapp.PositionChangeRecord, error) {
	return f.ports.ChangesExecute(ctx, query)
}

func (f fakeArenaArguments) Execute(ctx context.Context, query argumentsapp.ListArenaArgumentsQuery) (*argumentsapp.ArgumentPage, error) {
	return f.ports.ArgumentsExecute(ctx, query)
}

func (f fakePublishArgument) Execute(ctx context.Context, command argumentsapp.PublishArgumentCommand) (*argumentsapp.PublishArgumentResult, error) {
	return f.ports.PublishExecute(ctx, command)
}

func (f fakeAttributions) Execute(ctx context.Context, command persuasionapp.RecordAttributionsCommand) (*persuasionapp.RecordAttributionsResult, error) {
	return f.ports.AttributionExecute(ctx, command)
}

// participation is the composed surface under test.
type participation struct {
	mux     *http.ServeMux
	ports   *fakePorts
	manager *security.Manager
}

func newParticipation(t *testing.T, mutate func(*adapterhtml.ParticipationConfig)) *participation {
	t.Helper()

	templates, err := adapterhtml.NewParticipationTemplates(participationManifest())
	if err != nil {
		t.Fatalf("NewParticipationTemplates() error = %v", err)
	}
	manager, err := security.New(security.Options{
		Env:    config.EnvTest,
		Clock:  clockseed.NewClock(),
		Random: clockseed.NewRandom(),
	})
	if err != nil {
		t.Fatalf("security.New() error = %v", err)
	}

	ports := &fakePorts{arguments: map[string][]argumentsapp.PublicArgument{}}
	built := &participation{ports: ports, manager: manager}

	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		if rawToken != sessionToken {
			return security.AuthIdentity{}, errors.New("unknown session")
		}
		return security.AuthIdentity{AccountID: accountIDFixture, SessionID: "session-1"}, nil
	})

	config := adapterhtml.ParticipationConfig{
		GetDocument:        &fakeArenaDocument{arena: participationArena(t)},
		MyPosition:         fakeMyPosition{ports},
		ConfirmPosition:    fakeConfirmPosition{ports},
		ChangePosition:     fakeChangePosition{ports},
		Aggregate:          fakePositionAggregate{ports},
		PositionChanges:    fakePositionChanges{ports},
		Arguments:          fakeArenaArguments{ports},
		PublishArgument:    fakePublishArgument{ports},
		RecordAttributions: fakeAttributions{ports},
		Security:           manager,
		SessionValidator:   validator,
		Random:             clockseed.NewRandom(),
		Templates:          templates,
		MaxAttributions:    3,
	}
	if mutate != nil {
		mutate(&config)
	}

	handler, err := adapterhtml.NewParticipationHandler(config)
	if err != nil {
		t.Fatalf("NewParticipationHandler() error = %v", err)
	}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	built.mux = mux
	return built
}

// pageSession is one open document: the document it rendered, the token it
// carried and the cookies it set.
type pageSession struct {
	document string
	token    string
	cookies  []*http.Cookie
}

// get performs one read, optionally carrying the session cookie.
func (p *participation) get(t *testing.T, target string, signedIn bool) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Accept", "text/html")
	if signedIn {
		request.AddCookie(&http.Cookie{Name: security.DefaultSessionCookieName, Value: sessionToken})
	}
	recorder := httptest.NewRecorder()
	p.mux.ServeHTTP(recorder, request)
	return recorder
}

// open performs the read that renders the page and mints the CSRF cookie.
func (p *participation) open(t *testing.T, signedIn bool) pageSession {
	t.Helper()

	recorder := p.get(t, "/arenas/"+participationSlug, signedIn)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /arenas/%s = %d, want 200: %s", participationSlug, recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	document := recorder.Body.String()
	if !signedIn {
		// A visitor with no session has no form on the page, so the read has
		// nothing to protect and mints no token.
		if token := csrfField(t, document); token != "" {
			t.Fatalf("the anonymous page rendered a form token, so it is not the page this test believes it is: %q", token)
		}
		return pageSession{document: document, cookies: cookies}
	}
	for _, cookie := range cookies {
		if cookie.Name == security.DefaultCSRFCookieName {
			return pageSession{document: document, token: csrfField(t, document), cookies: cookies}
		}
	}
	t.Fatal("the page set no CSRF cookie, so the forms it rendered could never be submitted")
	return pageSession{}
}

// post submits fields the way the rendered form does: url-encoded, in the body,
// with the token and the cookies of the open page.
func (p *participation) post(t *testing.T, session pageSession, action string, fields url.Values, without string) *httptest.ResponseRecorder {
	t.Helper()

	if without != "csrf_token" && session.token != "" {
		fields.Set("csrf_token", session.token)
	}
	request := httptest.NewRequest(http.MethodPost, action, strings.NewReader(fields.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if without != "cookie" {
		for _, cookie := range session.cookies {
			request.AddCookie(cookie)
		}
	}
	if without != "session" && session.token != "" {
		request.AddCookie(&http.Cookie{Name: security.DefaultSessionCookieName, Value: sessionToken})
	}
	recorder := httptest.NewRecorder()
	p.mux.ServeHTTP(recorder, request)
	return recorder
}

// submit drives one transition end to end: the read that mints the cookie, then
// the POST that carries the token.
func (p *participation) submit(t *testing.T, action string, fields url.Values, without string) *httptest.ResponseRecorder {
	t.Helper()

	session := p.open(t, without != "session")
	return p.post(t, session, action, fields, without)
}

var csrfPattern = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// csrfField reads the double-submit token a rendered page carries.
func csrfField(t *testing.T, document string) string {
	t.Helper()
	match := csrfPattern.FindStringSubmatch(document)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

// --- the page -------------------------------------------------------------

// TestParticipationPageForVisitorKeepsTheChoiceLocal is the visitor contract: the
// page renders, it invites the local choice without submitting anything, it
// offers the two ways into the account journey, and it does not publish a single
// aggregate count until someone asks for it.
func TestParticipationPageForVisitorKeepsTheChoiceLocal(t *testing.T) {
	t.Parallel()

	built := newParticipation(t, nil)
	built.ports.aggregate = &positionsapp.PositionAggregate{Total: 42, CheckedAt: time.Now().UTC()}

	recorder := built.get(t, "/arenas/"+participationSlug, false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if cache := recorder.Header().Get("Cache-Control"); !strings.Contains(cache, "no-store") {
		t.Errorf("Cache-Control = %q, want a private, never-stored document", cache)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", contentType)
	}

	document := recorder.Body.String()
	for _, marker := range []string{
		`lang="pt-BR"`,                        // the content language is the interface locale
		"O debate público melhora",            // the statement
		`data-ga-choice-group="true"`,         // the local choice
		`data-ga-choice="agree"`,              // one button per position
		`data-ga-choice="disagree"`,           //
		`data-ga-choice="undecided"`,          //
		`aria-pressed="false"`,                // nothing is chosen before the browser chooses
		`href="/login"`,                       // the two ways into the account journey
		`href="/register"`,                    //
		"?reveal=1",                           // the aggregate is asked for, never derived unasked
		`href="/d/` + participationSlug + `"`, // the canonical public document
	} {
		if !strings.Contains(document, marker) {
			t.Errorf("the visitor page is missing %s", marker)
		}
	}
	if strings.Contains(document, "<form") {
		t.Error("the visitor page rendered a form, but a visitor submits nothing")
	}
	if strings.Contains(document, "Participantes elegíveis") {
		t.Error("the visitor page published the aggregate without being asked")
	}
	if strings.Contains(document, "Sua posição atual") {
		t.Error("the visitor page rendered the owner state, which needs a session")
	}
}

// TestParticipationAggregateIsRevealedOnRequest proves the two answers of the
// same read: the counts when the person asks for them, and the note of a
// suppressed sample instead of counts that were never derived.
func TestParticipationAggregateIsRevealedOnRequest(t *testing.T) {
	t.Parallel()

	built := newParticipation(t, nil)
	checkedAt := time.Date(2026, 9, 20, 8, 30, 0, 0, time.UTC)
	built.ports.aggregate = &positionsapp.PositionAggregate{
		Current:   positionsapp.PositionDistribution{Agree: 7, Disagree: 5, Undecided: 1},
		Initial:   positionsapp.PositionDistribution{Agree: 9, Disagree: 3, Undecided: 1},
		Total:     13,
		CheckedAt: checkedAt,
	}

	revealed := built.get(t, "/arenas/"+participationSlug+"?reveal=1", false)
	document := revealed.Body.String()
	for _, marker := range []string{"Participantes elegíveis: 13", "A favor: 7", "Contra: 5", "Sem posição: 1", "A favor: 9", "Apurado em 2026-09-20T08:30:00Z"} {
		if !strings.Contains(document, marker) {
			t.Errorf("the revealed aggregate is missing %q", marker)
		}
	}

	built.ports.aggregate = &positionsapp.PositionAggregate{Suppressed: true, CheckedAt: checkedAt}
	suppressed := built.get(t, "/arenas/"+participationSlug+"?reveal=1", false)
	suppressedDocument := suppressed.Body.String()
	if !strings.Contains(suppressedDocument, "A amostra é pequena demais") {
		t.Error("a suppressed aggregate must render the note")
	}
	for _, marker := range []string{"Participantes elegíveis", "A favor:"} {
		if strings.Contains(suppressedDocument, marker) {
			t.Errorf("a suppressed aggregate must publish no count at all, found %q", marker)
		}
	}
}

// TestParticipationPageForOwnerShowsTheStoredState is the owner contract: the
// page reports the stored position, preselects the current one in the change
// form, offers the publication form with its attempt key, and offers the
// attribution form bound to the change that exists.
func TestParticipationPageForOwnerShowsTheStoredState(t *testing.T) {
	t.Parallel()

	built := newParticipation(t, nil)
	built.ports.position = ownerPosition(t)
	built.ports.positionOK = true
	built.ports.changes = []positionsapp.PositionChangeRecord{{ID: "change-1"}}
	built.ports.arguments[argumentsdomain.RelationSupport] = []argumentsapp.PublicArgument{
		publicArgument(t, "argument-1", "support", "Um argumento verificável.", 2),
	}

	recorder := built.get(t, "/arenas/"+participationSlug, true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	document := recorder.Body.String()

	for _, marker := range []string{
		"Posição inicial: A favor",
		"Posição atual: Contra",
		`action="/arenas/` + participationSlug + `/position/change"`,
		`value="disagree" checked="checked"`, // the change form starts on the stored position
		`action="/arenas/` + participationSlug + `/arguments"`,
		`name="attempt"`,                    // the retry key travels with the form
		`name="change_id" value="change-1"`, // the attribution form is bound to the change
		`type="checkbox"`,                   // the selection is a checkbox group
		"Um argumento verificável.",
		`action="/logout"`, // the account links give way to the sign-out form
	} {
		if !strings.Contains(document, marker) {
			t.Errorf("the owner page is missing %q", marker)
		}
	}
	if strings.Contains(document, "Confirmar posição inicial") {
		t.Error("an owner with a confirmed position must not be offered the initial confirmation again")
	}
	if strings.Contains(document, `href="/login"`) {
		t.Error("a signed-in page must not invite the person to sign in")
	}
}

// TestParticipationPageForOwnerWithoutPositionOffersTheConfirmation is the other
// half of the owner contract: no stored projection means the confirmation form,
// and nothing else of the owner surface.
func TestParticipationPageForOwnerWithoutPositionOffersTheConfirmation(t *testing.T) {
	t.Parallel()

	built := newParticipation(t, nil)
	built.ports.positionOK = false

	recorder := built.get(t, "/arenas/"+participationSlug, true)
	document := recorder.Body.String()

	if !strings.Contains(document, "Confirmar posição inicial") {
		t.Error("an owner without a position must be offered the confirmation")
	}
	if !strings.Contains(document, `action="/arenas/`+participationSlug+`/position"`) {
		t.Error("the confirmation form must post to the confirmation route")
	}
	for _, marker := range []string{"Mudar posição", "Publicar argumento", "O que influenciou"} {
		if strings.Contains(document, marker) {
			t.Errorf("an owner without a position must not be offered %q", marker)
		}
	}
}

// TestParticipationPageReportsAnArenaThatIsNotThere proves the two documents
// that are not the page: a missing Arena and a removed one.
func TestParticipationPageReportsAnArenaThatIsNotThere(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		failure  error
		status   int
		expected string
	}{
		{name: "not found", failure: arenasapp.ErrArenaNotFound, status: http.StatusNotFound, expected: "Arena não encontrada"},
		{name: "gone", failure: arenasapp.ErrArenaGone, status: http.StatusGone, expected: "Arena removida"},
	}

	for _, testCase := range cases {
		built := newParticipation(t, func(config *adapterhtml.ParticipationConfig) {
			config.GetDocument = &fakeArenaDocument{err: testCase.failure}
		})
		recorder := built.get(t, "/arenas/"+participationSlug, false)
		if recorder.Code != testCase.status {
			t.Errorf("%s: GET = %d, want %d", testCase.name, recorder.Code, testCase.status)
		}
		if !strings.Contains(recorder.Body.String(), testCase.expected) {
			t.Errorf("%s: the document does not carry %q: %s", testCase.name, testCase.expected, recorder.Body.String())
		}
	}
}

// --- the transitions ------------------------------------------------------

// TestConfirmPositionRedirectsAndRecordsTheSubmittedChoice is the happy path of
// the first transition: the use case receives the arena, the account and the
// position, and the answer is a redirect a reload cannot repeat.
func TestConfirmPositionRedirectsAndRecordsTheSubmittedChoice(t *testing.T) {
	t.Parallel()

	built := newParticipation(t, nil)
	recorder := built.submit(t, "/arenas/"+participationSlug+"/position", url.Values{"position": {"agree"}}, "")

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d, want 303: %s", recorder.Code, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); location != "/arenas/"+participationSlug+"?ok=position_confirmed" {
		t.Errorf("Location = %q, want the page with the code of the transition", location)
	}
	if len(built.ports.confirmCalls) != 1 {
		t.Fatalf("the use case was called %d times, want once", len(built.ports.confirmCalls))
	}
	call := built.ports.confirmCalls[0]
	if call.AccountID != accountIDFixture || call.ArenaID != arenaIDFixture || call.Position != "agree" {
		t.Errorf("the use case received %+v", call)
	}

	// The page the redirect lands on reports the transition, and only the
	// closed vocabulary of the notices is rendered.
	notice := built.get(t, recorder.Header().Get("Location"), false)
	if !strings.Contains(notice.Body.String(), "Posição inicial confirmada.") {
		t.Error("the page after the transition must report it")
	}
	if unknown := built.get(t, "/arenas/"+participationSlug+"?ok=%3Cscript%3E", false); strings.Contains(unknown.Body.String(), "ga-toast") {
		t.Error("an unknown notice code must render nothing, so the query string is never reflected")
	}
}

// TestConfirmPositionRefusesAChoiceOutsideTheVocabulary proves the adapter
// validates the submitted document before the use case is called, and that the
// refusal reaches the person as a message on the field plus a summary that links
// to it.
func TestConfirmPositionRefusesAChoiceOutsideTheVocabulary(t *testing.T) {
	t.Parallel()

	built := newParticipation(t, nil)
	recorder := built.submit(t, "/arenas/"+participationSlug+"/position", url.Values{"position": {"talvez"}}, "")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST = %d, want 400", recorder.Code)
	}
	if len(built.ports.confirmCalls) != 0 {
		t.Error("a choice outside the vocabulary must not reach the use case")
	}
	document := recorder.Body.String()
	for _, marker := range []string{
		"Escolha uma das posições listadas.",
		`role="alert"`,
		`<ga-error-summary`,
		`href="#position-agree"`, // the summary links to a control a keyboard can reach
	} {
		if !strings.Contains(document, marker) {
			t.Errorf("the refused page is missing %q", marker)
		}
	}
}

// TestConfirmPositionReportsAnImmutableChoiceOnTheForm proves the one refusal of
// this transition that is about the person's own answer: it is shown on the
// form, with the status that says the request conflicts with the stored state.
func TestConfirmPositionReportsAnImmutableChoiceOnTheForm(t *testing.T) {
	t.Parallel()

	built := newParticipation(t, nil)
	built.ports.confirmErr = positionsapp.ErrInitialPositionAlreadySet

	recorder := built.submit(t, "/arenas/"+participationSlug+"/position", url.Values{"position": {"agree"}}, "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("POST = %d, want 409", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "A posição inicial já foi confirmada com outro valor.") {
		t.Errorf("the page does not carry the message of the refusal: %s", recorder.Body.String())
	}
}

// TestChangePositionReportsAVersionConflict is the other conflict of the surface:
// two tabs, one stored chain.
func TestChangePositionReportsAVersionConflict(t *testing.T) {
	t.Parallel()

	built := newParticipation(t, nil)
	built.ports.changeErr = positionsapp.ErrVersionConflict
	built.ports.position = ownerPosition(t)
	built.ports.positionOK = true

	recorder := built.submit(t, "/arenas/"+participationSlug+"/position/change", url.Values{"position": {"agree"}}, "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("POST = %d, want 409", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "Sua posição mudou em outro lugar.") {
		t.Errorf("the page does not carry the message of the refusal: %s", recorder.Body.String())
	}
	if len(built.ports.changeCalls) != 1 || built.ports.changeCalls[0].Position != "agree" {
		t.Errorf("the use case received %+v", built.ports.changeCalls)
	}
}

// TestPublishArgumentCarriesTheAttemptKeyOfTheForm proves the property that keeps
// a double submission from debiting INK twice: the key the page rendered is the
// key the application receives, unchanged, on both submissions.
func TestPublishArgumentCarriesTheAttemptKeyOfTheForm(t *testing.T) {
	t.Parallel()

	built := newParticipation(t, nil)
	built.ports.position = ownerPosition(t)
	built.ports.positionOK = true
	session := built.open(t, true)
	attempt := hiddenValue(t, session, "attempt")

	fields := url.Values{"relation": {"support"}, "content": {"Um argumento verificável."}, "attempt": {attempt}}
	first := built.post(t, session, "/arenas/"+participationSlug+"/arguments", fields, "")
	second := built.post(t, session, "/arenas/"+participationSlug+"/arguments", url.Values{"relation": {"support"}, "content": {"Um argumento verificável."}, "attempt": {attempt}}, "")

	for _, recorder := range []*httptest.ResponseRecorder{first, second} {
		if recorder.Code != http.StatusSeeOther {
			t.Fatalf("POST = %d, want 303: %s", recorder.Code, recorder.Body.String())
		}
	}
	if len(built.ports.publishCalls) != 2 {
		t.Fatalf("the use case was called %d times, want twice", len(built.ports.publishCalls))
	}
	for _, call := range built.ports.publishCalls {
		if call.IdempotencyKey != attempt {
			t.Errorf("the use case received attempt key %q, want the one the page rendered (%q)", call.IdempotencyKey, attempt)
		}
		if call.AccountID != accountIDFixture || call.ArenaID != arenaIDFixture {
			t.Errorf("the use case received %+v", call)
		}
	}
	if first.Header().Get("Location") == "" || second.Header().Get("Location") == "" {
		t.Error("both answers must carry the browser back to the page")
	}
}

// TestPublishArgumentReportsAnEmptyBalanceOnTheField proves the refusal that
// belongs to the person's own submission is shown where it can be acted on,
// instead of as a page that says nothing about the argument.
func TestPublishArgumentReportsAnEmptyBalanceOnTheField(t *testing.T) {
	t.Parallel()

	built := newParticipation(t, nil)
	built.ports.publishErr = argumentsapp.ErrInsufficientInk

	recorder := built.submit(t, "/arenas/"+participationSlug+"/arguments", url.Values{"relation": {"support"}, "content": {"Um argumento."}, "attempt": {"attempt-1"}}, "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("POST = %d, want 409", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "Seu saldo de INK não cobre esta publicação.") {
		t.Errorf("the page does not carry the message of the refusal: %s", recorder.Body.String())
	}
}

// TestPublishArgumentRefusesAContentTheDomainWouldRefuse proves the adapter
// classifies a domain refusal of the document as a message on the content, which
// is the field the person can fix.
//
// A blank content never reaches this path: the surface refuses it as a missing
// field before the use case is called, which the empty-field test above proves.
func TestPublishArgumentRefusesAContentTheDomainWouldRefuse(t *testing.T) {
	t.Parallel()

	built := newParticipation(t, nil)
	built.ports.publishErr = argumentsdomain.ErrContentTooLong

	recorder := built.submit(t, "/arenas/"+participationSlug+"/arguments", url.Values{"relation": {"context"}, "content": {"Um argumento verificável."}, "attempt": {"attempt-1"}}, "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("POST = %d, want 409", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "Escreva um argumento de até 3000 grafemas.") {
		t.Errorf("the page does not carry the message of the content limit: %s", recorder.Body.String())
	}
}

// TestAttributionRefusesMoreThanThePolicyAllows proves the browser stops at the
// limit the policy declares, and that the limit is the configured one instead of
// a number this surface invented.
func TestAttributionRefusesMoreThanThePolicyAllows(t *testing.T) {
	t.Parallel()

	built := newParticipation(t, func(config *adapterhtml.ParticipationConfig) {
		config.MaxAttributions = 2
	})

	fields := url.Values{
		"change_id":    {"change-1"},
		"argument_ids": {"argument-1", "argument-2", "argument-3"},
	}
	recorder := built.submit(t, "/arenas/"+participationSlug+"/attributions", fields, "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST = %d, want 400", recorder.Code)
	}
	if len(built.ports.attributionCalls) != 0 {
		t.Error("a selection above the policy must not reach the use case")
	}
	if !strings.Contains(recorder.Body.String(), "Escolha no máximo 2 argumentos.") {
		t.Errorf("the page does not carry the message of the limit: %s", recorder.Body.String())
	}
}

// TestAttributionRecordsTheSelectionForTheChange proves the happy path of the
// last transition, including the change identifier the form carried as a hidden
// field.
func TestAttributionRecordsTheSelectionForTheChange(t *testing.T) {
	t.Parallel()

	built := newParticipation(t, nil)
	fields := url.Values{
		"change_id":    {"change-1"},
		"argument_ids": {"argument-1", "argument-2"},
	}
	recorder := built.submit(t, "/arenas/"+participationSlug+"/attributions", fields, "")

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d, want 303: %s", recorder.Code, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); location != "/arenas/"+participationSlug+"?ok=attribution_recorded" {
		t.Errorf("Location = %q", location)
	}
	if len(built.ports.attributionCalls) != 1 {
		t.Fatalf("the use case was called %d times, want once", len(built.ports.attributionCalls))
	}
	call := built.ports.attributionCalls[0]
	if call.AccountID != accountIDFixture || call.ChangeID != "change-1" || len(call.ArgumentIDs) != 2 {
		t.Errorf("the use case received %+v", call)
	}
}

// --- composition ----------------------------------------------------------

// TestTransitionsRequireASessionAndTheDoubleSubmit is the security contract of
// the four routes: an anonymous caller is refused with the page of the
// unauthorized kind, and an authenticated one without the token is refused with
// the message that tells the person what to do about it.
func TestTransitionsRequireASessionAndTheDoubleSubmit(t *testing.T) {
	t.Parallel()

	built := newParticipation(t, nil)

	for _, action := range []string{
		"/arenas/" + participationSlug + "/position",
		"/arenas/" + participationSlug + "/position/change",
		"/arenas/" + participationSlug + "/arguments",
		"/arenas/" + participationSlug + "/attributions",
	} {
		anonymous := built.submit(t, action, url.Values{"position": {"agree"}}, "session")
		if anonymous.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without a session = %d, want 401", action, anonymous.Code)
		}
		if !strings.Contains(anonymous.Body.String(), "Autenticação é necessária") {
			t.Errorf("POST %s must answer the localized refusal of the unauthorized kind: %s", action, anonymous.Body.String())
		}
		if contentType := anonymous.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Errorf("POST %s answered %q, want a page instead of a problem document", action, contentType)
		}

		unsigned := built.submit(t, action, url.Values{}, "csrf_token")
		if unsigned.Code != http.StatusForbidden {
			t.Errorf("POST %s without the token = %d, want 403", action, unsigned.Code)
		}
		if !strings.Contains(unsigned.Body.String(), "Recarregue a página e envie o formulário de novo.") {
			t.Errorf("POST %s must explain the expired form: %s", action, unsigned.Body.String())
		}
	}

	if len(built.ports.confirmCalls)+len(built.ports.changeCalls)+len(built.ports.publishCalls)+len(built.ports.attributionCalls) != 0 {
		t.Error("a refused transition must not reach the application")
	}
}

// TestNewParticipationHandlerFailsClosed proves the composition guard: a surface
// without the security manager, the session validator or the attribution limit
// never reaches the mux.
func TestNewParticipationHandlerFailsClosed(t *testing.T) {
	t.Parallel()

	templates, err := adapterhtml.NewParticipationTemplates(participationManifest())
	if err != nil {
		t.Fatalf("NewParticipationTemplates() error = %v", err)
	}
	manager, err := security.New(security.Options{Env: config.EnvTest, Clock: clockseed.NewClock(), Random: clockseed.NewRandom()})
	if err != nil {
		t.Fatalf("security.New() error = %v", err)
	}
	ports := &fakePorts{}
	validator := security.SessionValidatorFunc(func(context.Context, string) (security.AuthIdentity, error) {
		return security.AuthIdentity{}, errors.New("unused")
	})
	// A composition that passes a typed nil implementation reaches the guard as
	// a non-nil interface, which is exactly the case a plain == nil test misses.
	var typedNil *fakePublishArgument

	complete := func() adapterhtml.ParticipationConfig {
		return adapterhtml.ParticipationConfig{
			GetDocument:        &fakeArenaDocument{},
			MyPosition:         fakeMyPosition{ports},
			ConfirmPosition:    fakeConfirmPosition{ports},
			ChangePosition:     fakeChangePosition{ports},
			Aggregate:          fakePositionAggregate{ports},
			PositionChanges:    fakePositionChanges{ports},
			Arguments:          fakeArenaArguments{ports},
			PublishArgument:    fakePublishArgument{ports},
			RecordAttributions: fakeAttributions{ports},
			Security:           manager,
			SessionValidator:   validator,
			Random:             clockseed.NewRandom(),
			Templates:          templates,
			MaxAttributions:    3,
		}
	}

	missing := complete()
	missing.Security = nil
	if _, err := adapterhtml.NewParticipationHandler(missing); err == nil || !strings.Contains(err.Error(), "security manager") {
		t.Errorf("the guard must name the missing security manager, got %v", err)
	}

	entropy := complete()
	entropy.Random = nil
	if _, err := adapterhtml.NewParticipationHandler(entropy); err == nil || !strings.Contains(err.Error(), "random source") {
		t.Errorf("the guard must name the missing entropy source, got %v", err)
	}

	limit := complete()
	limit.MaxAttributions = 0
	if _, err := adapterhtml.NewParticipationHandler(limit); err == nil || !strings.Contains(err.Error(), "attribution limit") {
		t.Errorf("the guard must refuse a surface without an attribution limit, got %v", err)
	}

	typed := complete()
	typed.PublishArgument = typedNil
	if _, err := adapterhtml.NewParticipationHandler(typed); err == nil {
		t.Error("the guard must refuse a typed nil use case, which would panic inside a request")
	} else if !strings.Contains(err.Error(), "publish argument") {
		t.Errorf("the report does not name what is missing: %v", err)
	}

	if _, err := adapterhtml.NewParticipationHandler(complete()); err != nil {
		t.Errorf("the complete composition was refused: %v", err)
	}
}

// hiddenValue reads the value one hidden field of the rendered page carries. The
// attempt key is generated when the page is rendered, so the only way to submit
// the form the way a browser would is to read it back from the document.
func hiddenValue(t *testing.T, session pageSession, name string) string {
	t.Helper()

	pattern := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]+)"`)
	match := pattern.FindStringSubmatch(session.document)
	if len(match) != 2 {
		t.Fatalf("the rendered page carries no %q field:\n%s", name, session.document)
	}
	return match[1]
}

// publicArgument is one published argument of the fake list.
func publicArgument(t *testing.T, id, relation string, content string, replies int64) argumentsapp.PublicArgument {
	t.Helper()

	parsedRelation, err := argumentsdomain.ParseRelation(relation)
	if err != nil {
		t.Fatalf("ParseRelation(%q) error = %v", relation, err)
	}
	counter := func(value string) int { return len([]rune(value)) }
	parsed, err := argumentsdomain.ParseContent(content, counter)
	if err != nil {
		t.Fatalf("ParseContent(%q) error = %v", content, err)
	}
	argumentID, err := argumentsdomain.ParseArgumentID(id)
	if err != nil {
		t.Fatalf("ParseArgumentID(%q) error = %v", id, err)
	}
	arenaID, err := argumentsdomain.ParseArenaID(arenaIDFixture)
	if err != nil {
		t.Fatalf("ParseArenaID(%q) error = %v", arenaIDFixture, err)
	}
	return argumentsapp.PublicArgument{
		ID:         argumentID,
		ArenaID:    arenaID,
		Relation:   parsedRelation,
		Content:    &parsed,
		Status:     "published",
		CreatedAt:  time.Date(2026, 9, 20, 7, 0, 0, 0, time.UTC),
		ReplyCount: replies,
	}
}
