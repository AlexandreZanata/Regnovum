package html

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	persuasionapp "github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	persuasiondomain "github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/observability"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/requestid"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	"github.com/AlexandreZanata/Regnovum/internal/platform/websurface"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
	positionsapp "github.com/AlexandreZanata/Regnovum/internal/positions/application"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// The closed vocabularies and the control names of the participation forms. A
// value that is not listed here is refused with the message of its field, so the
// page never forwards anything it did not render itself.
const (
	positionField    = "position"
	relationField    = "relation"
	contentField     = "content"
	attributionField = "argument_ids"
	changeIDField    = "change_id"
	attemptField     = "attempt"
)

// The query parameters of the page. `reveal` asks for the aggregate, which the
// page never derives unasked, and `ok` carries the code of the transition that
// just happened: only the closed set below is rendered, so an unknown code
// produces no notice and nothing the URL carries is ever reflected into the
// document.
const (
	revealParam = "reveal"
	revealValue = "1"
	noticeParam = "ok"
)

// The codes of the transitions that redirect back to the page, and the catalog
// message of each. The message carries no value from the request: the page
// renders the stored state right below it.
var noticeKeys = map[string]string{
	"position_confirmed":   "arenas.participation.notice.position_confirmed",
	"position_changed":     "arenas.participation.notice.position_changed",
	"argument_published":   "arenas.participation.notice.argument_published",
	"attribution_recorded": "arenas.participation.notice.attribution_recorded",
}

// The bounded reads of the page.
const (
	// listLimit bounds the list of one relation. The page shows the newest
	// page of each relation; walking further belongs to the JSON API, which
	// owns the keyset cursor.
	listLimit = 20
	// excerptRunes bounds the excerpt of an argument used as an attribution
	// option.
	excerptRunes = 140
)

// The operations of the journey, as ports oriented to this consumer. The
// application's use cases satisfy them; the surface declares what it calls and
// nothing else, so a test can drive the journey without a persistence stack.
type (
	// ArenaDocument resolves the Arena a request names, refusing with the
	// application's ErrArenaNotFound and ErrArenaGone.
	ArenaDocument interface {
		Execute(ctx context.Context, rawSlug string) (*arenasdomain.Arena, error)
	}
	// MyPosition answers the authenticated owner's position projection.
	MyPosition interface {
		Execute(ctx context.Context, query positionsapp.GetMyPositionQuery) (*positionsdomain.DebatePosition, error)
	}
	// ConfirmInitialPosition records the first confirmed position.
	ConfirmInitialPosition interface {
		Execute(ctx context.Context, command positionsapp.ConfirmInitialPositionCommand) (*positionsapp.ConfirmInitialPositionResult, error)
	}
	// ChangePosition records one accepted position change.
	ChangePosition interface {
		Execute(ctx context.Context, command positionsapp.ChangePositionCommand) (*positionsapp.ChangePositionResult, error)
	}
	// PositionAggregate derives the privacy-safe public aggregate.
	PositionAggregate interface {
		Execute(ctx context.Context, query positionsapp.GetPositionAggregateQuery) (*positionsapp.PositionAggregate, error)
	}
	// ListPositionChanges answers the authenticated owner's change history.
	ListPositionChanges interface {
		Execute(ctx context.Context, query positionsapp.ListPositionChangesQuery) ([]positionsapp.PositionChangeRecord, error)
	}
	// ArenaArguments answers the public per-relation list of arguments.
	ArenaArguments interface {
		Execute(ctx context.Context, query argumentsapp.ListArenaArgumentsQuery) (*argumentsapp.ArgumentPage, error)
	}
	// PublishArgument publishes one argument, debiting INK atomically.
	PublishArgument interface {
		Execute(ctx context.Context, command argumentsapp.PublishArgumentCommand) (*argumentsapp.PublishArgumentResult, error)
	}
	// RecordAttributions records the influence attribution of one change.
	RecordAttributions interface {
		Execute(ctx context.Context, command persuasionapp.RecordAttributionsCommand) (*persuasionapp.RecordAttributionsResult, error)
	}
)

// ParticipationConfig aggregates the use cases and platform services of the
// Arena participation journey.
type ParticipationConfig struct {
	GetDocument        ArenaDocument
	MyPosition         MyPosition
	ConfirmPosition    ConfirmInitialPosition
	ChangePosition     ChangePosition
	Aggregate          PositionAggregate
	PositionChanges    ListPositionChanges
	Arguments          ArenaArguments
	PublishArgument    PublishArgument
	RecordAttributions RecordAttributions
	Security           *security.Manager
	// SessionValidator resolves the session cookie into an identity. It is
	// the adapter the account module already exposes, so the two surfaces
	// authenticate through one implementation.
	SessionValidator security.SessionValidator
	// Random is the entropy source of the attempt key one rendered
	// publication form carries. It is injected, never read from the package:
	// the architecture test refuses a direct read (ADR-012).
	Random    ports.Random
	RateLimit ratelimit.Protector
	Templates *ParticipationTemplates
	// MaxAttributions is the largest selection one change may credit. The
	// number belongs to the persuasion policy and is configured there; this
	// surface only refuses to render a form that would always be refused.
	MaxAttributions int
	// Analytics receives the allowlisted product events of the journey. It
	// is optional: a test drives the journey without one, and a process
	// composed without a provider records nothing.
	Analytics observability.EventSink
}

// ParticipationHandler serves the browser participation journey of one Arena:
// the page at /arenas/{slug} and the four transitions a person submits from it.
type ParticipationHandler struct {
	getDocument     ArenaDocument
	myPosition      MyPosition
	confirm         ConfirmInitialPosition
	change          ChangePosition
	aggregate       PositionAggregate
	changes         ListPositionChanges
	arguments       ArenaArguments
	publish         PublishArgument
	attributions    RecordAttributions
	security        *security.Manager
	sessions        security.SessionValidator
	random          ports.Random
	rateLimit       ratelimit.Protector
	templates       *ParticipationTemplates
	maxAttributions int
	analytics       observability.EventSink
}

// NewParticipationHandler validates the composition and builds the handler. It
// fails closed: a journey served without a security manager would render forms
// whose double submit nobody checks, and one served without the attribution
// limit would render a form the policy refuses, so neither combination reaches
// the mux.
func NewParticipationHandler(config ParticipationConfig) (*ParticipationHandler, error) {
	missing := make([]string, 0, 14)
	for name, dependency := range map[string]any{
		"get arena document":  config.GetDocument,
		"my position":         config.MyPosition,
		"confirm position":    config.ConfirmPosition,
		"change position":     config.ChangePosition,
		"position aggregate":  config.Aggregate,
		"position changes":    config.PositionChanges,
		"arena arguments":     config.Arguments,
		"publish argument":    config.PublishArgument,
		"record attributions": config.RecordAttributions, "security manager": config.Security,
		"session validator": config.SessionValidator,
		"random source":     config.Random,
		"templates":         config.Templates,
	} {
		if websurface.Missing(dependency) {
			missing = append(missing, name)
		}
	}
	if config.MaxAttributions < 1 {
		missing = append(missing, "attribution limit")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("arena participation html: missing dependencies: %s", strings.Join(missing, ", "))
	}

	return &ParticipationHandler{
		getDocument:     config.GetDocument,
		myPosition:      config.MyPosition,
		confirm:         config.ConfirmPosition,
		change:          config.ChangePosition,
		aggregate:       config.Aggregate,
		changes:         config.PositionChanges,
		arguments:       config.Arguments,
		publish:         config.PublishArgument,
		attributions:    config.RecordAttributions,
		security:        config.Security,
		sessions:        config.SessionValidator,
		random:          config.Random,
		rateLimit:       config.RateLimit,
		templates:       config.Templates,
		maxAttributions: config.MaxAttributions,
		analytics:       config.Analytics,
	}, nil
}

// capture records one allowlisted product event of the journey. The adapter
// supplies only the facts it owns — the account the transition belonged to,
// the request correlation and the negotiated locale — and the observability
// package decides what may travel. Extra properties must be admitted by the
// event's allowlist or the whole event is refused as a programming error.
func (h *ParticipationHandler) capture(request *http.Request, name, accountID string, properties map[string]any) {
	if h.analytics == nil {
		return
	}
	if properties == nil {
		properties = make(map[string]any, 1)
	}
	properties["locale"] = locale.FromContext(request.Context()).String()
	h.analytics.Capture(observability.Event{
		Name:       name,
		AccountID:  accountID,
		RequestID:  requestid.FromRequest(request),
		Properties: properties,
	})
}

// RegisterRoutes wires the journey into the provided ServeMux.
//
// The read is optionally authenticated: a visitor without a session reads the
// page and chooses a position locally, and only the transitions require one.
// Every transition is a POST, resolves the session, requires it, proves the
// double submit and is bounded by the throttle of its action; the refusal
// translation turns whatever those middlewares answer into the same kind of
// page, so a spent budget or an expired session never reaches a person as a
// problem document.
func (h *ParticipationHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /arenas/{slug}", h.authenticated(http.HandlerFunc(h.ShowParticipation)))
	mux.Handle("POST /arenas/{slug}/position", h.mutation(ratelimit.ActionPositionConfirm, h.SubmitPosition))
	mux.Handle("POST /arenas/{slug}/position/change", h.mutation(ratelimit.ActionPositionChange, h.SubmitPositionChange))
	mux.Handle("POST /arenas/{slug}/arguments", h.mutation(ratelimit.ActionArgumentPublish, h.SubmitArgument))
	// Recording attributions carries no policy of its own, and this surface
	// never invents one: the route is authenticated, double-submit protected
	// and owner-only, and what it may record is bounded by the attribution
	// policy of the change rather than by a repetition budget.
	mux.Handle("POST /arenas/{slug}/attributions", h.mutation("", h.SubmitAttributions))
}

// authenticated resolves the session of a read. A visitor without one reads the
// page as well: the identity is simply absent from the context.
func (h *ParticipationHandler) authenticated(next http.Handler) http.Handler {
	return h.security.AuthenticateMiddleware(h.sessions)(next)
}

// mutation composes the middleware of one mutating browser route.
func (h *ParticipationHandler) mutation(action ratelimit.Action, handler http.HandlerFunc) http.Handler {
	next := http.Handler(handler)
	if action != "" && h.rateLimit != nil {
		next = h.rateLimit.Protect(action, next)
	}
	next = h.security.CSRFMiddleware()(next)
	next = h.security.RequireAuthMiddleware()(next)
	next = h.authenticated(next)
	return websurface.Refusals(h.presentRefusal, next)
}

// submission is the prelude every mutation shares: the Arena the request names,
// the authenticated account and the submitted document, already read inside the
// route budget.
type submission struct {
	arena     *arenasdomain.Arena
	slug      string
	accountID string
	form      url.Values
}

// begin reads the prelude. It reports false after answering the request.
func (h *ParticipationHandler) begin(w http.ResponseWriter, r *http.Request) (submission, bool) {
	arena, ok := h.arena(w, r)
	if !ok {
		return submission{}, false
	}
	identity, signed := security.FromContext(r.Context())
	if !signed {
		// RequireAuthMiddleware answers first on the composed surface; this is
		// the defensive path of a handler mounted without it, and it refuses
		// exactly the same way.
		h.presentRefusal(w, r, websurface.Refusal{Status: http.StatusUnauthorized, Kind: apperr.KindUnauthorized})
		return submission{}, false
	}
	form, ok := websurface.Form(w, r)
	if !ok {
		return submission{}, false
	}
	return submission{arena: arena, slug: arena.Slug().String(), accountID: identity.AccountID, form: form}, true
}

// arena resolves the Arena of the request. A removed Arena answers 410, one that
// does not exist answers 404, and both answers are the documents the public
// Arena surface already renders.
func (h *ParticipationHandler) arena(w http.ResponseWriter, r *http.Request) (*arenasdomain.Arena, bool) {
	arena, err := h.getDocument.Execute(r.Context(), r.PathValue("slug"))
	switch {
	case errors.Is(err, application.ErrArenaNotFound):
		h.renderNotice(w, r, http.StatusNotFound, "", "arenas.document.not_found.title", "arenas.document.not_found.detail")
		return nil, false
	case errors.Is(err, application.ErrArenaGone):
		h.renderNotice(w, r, http.StatusGone, "", "arenas.document.gone.title", "arenas.document.gone.detail")
		return nil, false
	case err != nil:
		h.fail(w, r, err)
		return nil, false
	}
	return arena, true
}

// ShowParticipation renders the page of the Arena.
func (h *ParticipationHandler) ShowParticipation(w http.ResponseWriter, r *http.Request) {
	arena, ok := h.arena(w, r)
	if !ok {
		return
	}
	h.renderPage(w, r, arena, submissionErrors{}, http.StatusOK)
}

// SubmitPosition confirms the initial position and redirects back to the page.
//
// The initial choice is immutable, so the one refusal worth showing on the form
// is the conflict of confirming it twice with different values; everything else
// the application refuses (an Arena that closed while the page was open, an
// account that is no longer eligible) becomes the localized refusal page of its
// kind.
func (h *ParticipationHandler) SubmitPosition(w http.ResponseWriter, r *http.Request) {
	input, ok := h.begin(w, r)
	if !ok {
		return
	}

	position, failures, err := h.readPosition(r, formPosition, input.form)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if len(failures.Field) > 0 {
		h.renderPage(w, r, input.arena, failures, http.StatusBadRequest)
		return
	}

	result, err := h.confirm.Execute(r.Context(), positionsapp.ConfirmInitialPositionCommand{
		AccountID: input.accountID,
		ArenaID:   input.arena.ID().String(),
		Position:  position.String(),
	})
	if err != nil {
		if errors.Is(err, positionsapp.ErrInitialPositionAlreadySet) {
			failures.Message, err = websurface.Localized(r, "arenas.participation.errors.immutable_position", nil)
			if err != nil {
				h.fail(w, r, err)
				return
			}
			h.renderPage(w, r, input.arena, failures, http.StatusConflict)
			return
		}
		h.fail(w, r, err)
		return
	}
	if result != nil && !result.Replayed {
		h.capture(r, observability.EventArenaPositionConfirmed, input.accountID, nil)
	}

	h.redirect(w, r, input.slug, "position_confirmed")
}

// SubmitPositionChange records one position change and redirects back to the
// page.
func (h *ParticipationHandler) SubmitPositionChange(w http.ResponseWriter, r *http.Request) {
	input, ok := h.begin(w, r)
	if !ok {
		return
	}

	position, failures, err := h.readPosition(r, formChange, input.form)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if len(failures.Field) > 0 {
		h.renderPage(w, r, input.arena, failures, http.StatusBadRequest)
		return
	}

	_, err = h.change.Execute(r.Context(), positionsapp.ChangePositionCommand{
		AccountID: input.accountID,
		ArenaID:   input.arena.ID().String(),
		Position:  position.String(),
	})
	if err != nil {
		if errors.Is(err, positionsapp.ErrVersionConflict) {
			failures.Message, err = websurface.Localized(r, "arenas.participation.errors.version_conflict", nil)
			if err != nil {
				h.fail(w, r, err)
				return
			}
			h.renderPage(w, r, input.arena, failures, http.StatusConflict)
			return
		}
		h.fail(w, r, err)
		return
	}
	h.capture(r, observability.EventArenaPositionChanged, input.accountID, nil)

	h.redirect(w, r, input.slug, "position_changed")
}

// SubmitArgument publishes one argument and redirects back to the page.
//
// The attempt key of the publication travels in the form, generated when the
// page was rendered: a double submission of the same document resolves the
// argument that was already recorded instead of debiting INK twice. The balance
// is the application's business — this surface only shows the refusal when it is
// not enough.
func (h *ParticipationHandler) SubmitArgument(w http.ResponseWriter, r *http.Request) {
	input, ok := h.begin(w, r)
	if !ok {
		return
	}

	relation, failures, err := h.readRelation(r, input.form)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	content := strings.TrimSpace(input.form.Get(contentField))
	if content == "" {
		message, err := websurface.Localized(r, "arenas.participation.errors.required", nil)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		failures.Field[contentField] = message
	}
	if len(failures.Field) > 0 {
		h.renderPage(w, r, input.arena, failures, http.StatusBadRequest)
		return
	}

	result, err := h.publish.Execute(r.Context(), argumentsapp.PublishArgumentCommand{
		AccountID:      input.accountID,
		ArenaID:        input.arena.ID().String(),
		Relation:       relation.String(),
		Content:        content,
		IdempotencyKey: strings.TrimSpace(input.form.Get(attemptField)),
	})
	if err != nil {
		classified, ok, classifyErr := h.publicationFailure(r, err)
		if classifyErr != nil {
			h.fail(w, r, classifyErr)
			return
		}
		if !ok {
			h.fail(w, r, err)
			return
		}
		failures.Field = classified
		h.renderPage(w, r, input.arena, failures, http.StatusConflict)
		return
	}
	if result != nil && !result.Replayed {
		h.capture(r, observability.EventArenaArgumentPublished, input.accountID, nil)
	}

	h.redirect(w, r, input.slug, "argument_published")
}

// SubmitAttributions records the influence attributed to one position change.
func (h *ParticipationHandler) SubmitAttributions(w http.ResponseWriter, r *http.Request) {
	input, ok := h.begin(w, r)
	if !ok {
		return
	}

	changeID := strings.TrimSpace(input.form.Get(changeIDField))
	selection := make([]string, 0, len(input.form[attributionField]))
	for _, value := range input.form[attributionField] {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			selection = append(selection, trimmed)
		}
	}

	failures := submissionErrors{Form: formAttribution, Field: map[string]string{}}
	switch {
	case changeID == "":
		message, err := websurface.Localized(r, "arenas.participation.errors.change_not_found", nil)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		failures.Message = message
	case len(selection) > h.maxAttributions:
		message, err := websurface.Localized(r, "arenas.participation.errors.too_many_attributions", map[string]string{"max": strconv.Itoa(h.maxAttributions)})
		if err != nil {
			h.fail(w, r, err)
			return
		}
		failures.Message = message
	case len(selection) == 0:
		message, err := websurface.Localized(r, "arenas.participation.errors.required", nil)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		failures.Field[attributionField] = message
	}
	if failures.Message != "" || len(failures.Field) > 0 {
		h.renderPage(w, r, input.arena, failures, http.StatusBadRequest)
		return
	}

	result, err := h.attributions.Execute(r.Context(), persuasionapp.RecordAttributionsCommand{
		AccountID:   input.accountID,
		ChangeID:    changeID,
		ArgumentIDs: selection,
	})
	if err != nil {
		classified, ok, classifyErr := h.attributionFailure(r, err)
		if classifyErr != nil {
			h.fail(w, r, classifyErr)
			return
		}
		if !ok {
			h.fail(w, r, err)
			return
		}
		h.renderPage(w, r, input.arena, classified, http.StatusConflict)
		return
	}
	if result != nil && !result.Replayed {
		h.capture(r, observability.EventArenaInfluenceAssigned, input.accountID, map[string]any{
			"attributed_count": len(result.ArgumentIDs),
		})
	}

	h.redirect(w, r, input.slug, "attribution_recorded")
}

// readPosition validates the submitted position against the closed vocabulary of
// the form. The form the failure belongs to is a parameter because the same
// vocabulary serves the confirmation and the change, and a message rendered on
// the wrong form is a message nobody sees.
func (h *ParticipationHandler) readPosition(r *http.Request, formName string, form url.Values) (positionsdomain.Position, submissionErrors, error) {
	failures := submissionErrors{Form: formName, Field: map[string]string{}}
	raw := strings.TrimSpace(form.Get(positionField))
	key := ""
	switch {
	case raw == "":
		key = "arenas.participation.errors.required"
	default:
		position, err := positionsdomain.ParsePosition(raw)
		if err == nil {
			return position, failures, nil
		}
		key = "arenas.participation.errors.invalid_choice"
	}
	message, err := websurface.Localized(r, key, nil)
	if err != nil {
		return positionsdomain.Position{}, failures, err
	}
	failures.Field[positionField] = message
	return positionsdomain.Position{}, failures, nil
}

// readRelation validates the submitted relation against the closed vocabulary of
// the form.
func (h *ParticipationHandler) readRelation(r *http.Request, form url.Values) (argumentsdomain.Relation, submissionErrors, error) {
	failures := submissionErrors{Form: formPublish, Field: map[string]string{}}
	raw := strings.TrimSpace(form.Get(relationField))
	key := ""
	switch {
	case raw == "":
		key = "arenas.participation.errors.required"
	default:
		relation, err := argumentsdomain.ParseRelation(raw)
		if err == nil {
			return relation, failures, nil
		}
		key = "arenas.participation.errors.invalid_relation"
	}
	message, err := websurface.Localized(r, key, nil)
	if err != nil {
		return argumentsdomain.Relation{}, failures, err
	}
	failures.Field[relationField] = message
	return argumentsdomain.Relation{}, failures, nil
}

// publicationFailure classifies the failures of a publication that belong on the
// form. It reports false when the failure is not one of them, which leaves the
// refusal page of its kind to answer.
func (h *ParticipationHandler) publicationFailure(r *http.Request, failure error) (map[string]string, bool, error) {
	switch {
	case errors.Is(failure, argumentsapp.ErrInsufficientInk):
		message, err := websurface.Localized(r, "arenas.participation.errors.insufficient_ink", nil)
		if err != nil {
			return nil, false, err
		}
		return map[string]string{contentField: message}, true, nil
	case isContentFailure(failure):
		// The content itself was refused (empty after normalization, or above
		// the grapheme ceiling). The relation was validated here against the
		// closed vocabulary, so the message belongs to the content.
		message, err := websurface.Localized(r, "arenas.participation.errors.invalid_content", map[string]string{
			"max": strconv.Itoa(argumentsdomain.MaxGraphemeCost),
		})
		if err != nil {
			return nil, false, err
		}
		return map[string]string{contentField: message}, true, nil
	default:
		return nil, false, nil
	}
}

// attributionFailure classifies the failures of an attribution that belong on the
// form.
func (h *ParticipationHandler) attributionFailure(r *http.Request, failure error) (submissionErrors, bool, error) {
	failures := submissionErrors{Form: formAttribution, Field: map[string]string{}}
	key := ""
	switch {
	case errors.Is(failure, persuasionapp.ErrChangeNotFound):
		key = "arenas.participation.errors.change_not_found"
	case errors.Is(failure, persuasionapp.ErrArgumentNotFound):
		key = "arenas.participation.errors.argument_not_found"
	case errors.Is(failure, persuasiondomain.ErrTooManyAttributions):
		key = "arenas.participation.errors.too_many_attributions"
	default:
		return failures, false, nil
	}
	message, err := websurface.Localized(r, key, map[string]string{"max": strconv.Itoa(h.maxAttributions)})
	if err != nil {
		return failures, false, err
	}
	failures.Message = message
	return failures, true, nil
}

// isContentFailure reports whether the publication was refused by the domain
// rules of the content itself. The relation was validated against the closed
// vocabulary before the call, so a domain failure of a document that carried
// only a relation and a content is about the content.
func isContentFailure(failure error) bool {
	var domainFailure argumentsdomain.DomainError
	return errors.As(failure, &domainFailure)
}

// pageForms names the form a failed submission belongs to, so the page marks the
// right one.
const (
	formNone        = ""
	formPosition    = "position"
	formChange      = "change"
	formPublish     = "publish"
	formAttribution = "attribution"
)

// submissionErrors carries what a failed submission must show: which form
// failed, the message of each field and the message of the refusal itself.
type submissionErrors struct {
	Form    string
	Field   map[string]string
	Message string
}

// stranded are the messages of a failure whose form the page cannot render. A
// transition that needs a stored position (a change, a publication, an
// attribution) has no form to receive its message when that state is gone, and
// a refusal the person never sees is a refusal that looks like a lost
// submission. The messages become the notices of the page instead, in a fixed
// order so the rendering is deterministic.
func (f submissionErrors) stranded() []string {
	switch f.Form {
	case formChange, formPublish, formAttribution:
	default:
		return nil
	}
	names := make([]string, 0, len(f.Field))
	for name := range f.Field {
		names = append(names, name)
	}
	sort.Strings(names)
	messages := make([]string, 0, len(names)+1)
	if f.Message != "" {
		messages = append(messages, f.Message)
	}
	for _, name := range names {
		messages = append(messages, f.Field[name])
	}
	return messages
}

// renderPage assembles and writes the page. The status is the one the answer
// deserves: a rendered read is 200, a refused submission is 400 or 409, and the
// page itself is never publicly cacheable.
func (h *ParticipationHandler) renderPage(w http.ResponseWriter, r *http.Request, arena *arenasdomain.Arena, failures submissionErrors, status int) {
	page, err := h.buildPage(w, r, arena, failures)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	body, err := websurface.Document(func(out io.Writer) error {
		return h.templates.RenderPage(out, page)
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	websurface.WritePrivate(w, status, body)
}

// redirect answers a transition with a redirect back to the page. The code of
// the notice travels in the query string and is resolved from the closed set
// above, so the page that follows a transition is a read of the stored state and
// a reload cannot submit the same document twice.
func (h *ParticipationHandler) redirect(w http.ResponseWriter, r *http.Request, slug, code string) {
	httpcache.Private(w)
	http.Redirect(w, r, arenaPath(slug)+"?"+noticeParam+"="+url.QueryEscape(code), http.StatusSeeOther)
}

// renderNotice renders an outcome or refusal document. It is used for the two
// answers that are not the page itself: an Arena that does not exist, and a
// platform refusal.
//
// The keys are parameters instead of one prefix, because not every pair nests:
// the shared error vocabulary is nested by kind (errors.<kind>.title) while the
// double submit of this surface carries its own pair, and composing a key from a
// prefix is how a page starts rendering the raw key it could not find.
func (h *ParticipationHandler) renderNotice(w http.ResponseWriter, r *http.Request, status int, slug, titleKey, detailKey string) {
	heading, err := websurface.Localized(r, titleKey, nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	detail, err := websurface.Localized(r, detailKey, nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	actions, err := h.noticeActions(r, slug)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	pageTitle, err := websurface.Localized(r, "arenas.participation.refusal_page_title", nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	body, err := websurface.Document(func(out io.Writer) error {
		return h.templates.RenderNotice(out, NoticeData{
			ParticipationChrome: ParticipationChrome{
				Lang:      websurface.Locale(r),
				PageTitle: pageTitle,
				Brand:     "",
				NavLabel:  "",
			},
			Heading: heading,
			Detail:  detail,
			Actions: actions,
		})
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	websurface.WritePrivate(w, status, body)
}

// // presentRefusal renders one platform refusal as a page: the shared error
// vocabulary by kind, except for the double submit, whose message has to tell
// the person what to do about it.
func (h *ParticipationHandler) presentRefusal(w http.ResponseWriter, r *http.Request, refusal websurface.Refusal) {
	title, detail := refusalKeys(refusal.Kind, refusal.CSRF)
	h.renderNotice(w, r, refusal.Status, r.PathValue("slug"), title, detail)
}

// refusalKeys resolves the catalog pair of one refusal.
func refusalKeys(kind apperr.Kind, csrf bool) (string, string) {
	if csrf {
		return "arenas.participation.errors.csrf_title", "arenas.participation.errors.csrf_detail"
	}
	return "errors." + string(kind) + ".title", "errors." + string(kind) + ".detail"
}

// noticeActions lists the ways out of a refusal or an outcome document.
func (h *ParticipationHandler) noticeActions(r *http.Request, slug string) ([]ParticipationLink, error) {
	links := []struct {
		key  string
		href string
	}{
		{key: "arenas.participation.nav.arena", href: arenaPath(slug)},
		{key: "arenas.participation.nav.document", href: documentPath(slug)},
	}
	actions := make([]ParticipationLink, 0, len(links))
	for _, link := range links {
		label, err := websurface.Localized(r, link.key, nil)
		if err != nil {
			return nil, err
		}
		actions = append(actions, ParticipationLink{Label: label, Href: link.href})
	}
	return actions, nil
}

// fail answers an unexpected failure as a problem document. The refusal
// translation of the mutating routes turns it into the localized page, so a
// server fault reaches a person as a page and a program as RFC 9457.
func (h *ParticipationHandler) fail(w http.ResponseWriter, r *http.Request, failure error) {
	_ = httperror.WriteProblem(w, r, failure)
}

// arenaPath is the address of the participation page of one Arena.
func arenaPath(slug string) string {
	return "/arenas/" + slug
}

// documentPath is the address of the public document of one Arena.
func documentPath(slug string) string {
	return "/d/" + slug
}

// buildPage assembles the page from the stored state.
//
// Every region is derived from what the person is allowed to see: the aggregate
// only when it was asked for, the position forms from the stored projection, the
// attribution form from the history of the owner, and the anonymous block from
// the absence of a session. Nothing here formats a number into prose: counts and
// instants travel as they are stored (I18N_STANDARD §5), and every phrase comes
// from the catalog.
func (h *ParticipationHandler) buildPage(w http.ResponseWriter, r *http.Request, arena *arenasdomain.Arena, failures submissionErrors) (ParticipationPageData, error) {
	slug := arena.Slug().String()
	statement := arena.Statement().String()
	pageTitle, err := websurface.Localized(r, "arenas.participation.page_title", map[string]string{"subject": statement})
	if err != nil {
		return ParticipationPageData{}, err
	}
	chrome, err := h.chrome(w, r, slug, pageTitle)
	if err != nil {
		return ParticipationPageData{}, err
	}

	page := ParticipationPageData{
		ParticipationChrome: chrome,
		ArenaID:             arena.ID().String(),
		Statement:           statement,
		Context:             arena.Context().String(),
	}
	if page.StatusLabel, err = h.statusLabel(r, arena); err != nil {
		return ParticipationPageData{}, err
	}
	if page.CategoryLabel, err = websurface.Localized(r, "arenas.participation.meta.category", map[string]string{"category": arena.Category().String()}); err != nil {
		return ParticipationPageData{}, err
	}
	if page.LanguageLabel, err = websurface.Localized(r, "arenas.participation.meta.language", map[string]string{"language": arena.Language().String()}); err != nil {
		return ParticipationPageData{}, err
	}
	if published := arena.PublishedAt(); published != nil {
		instant := published.UTC().Format(time.RFC3339)
		page.PublishedAt = instant
		if page.PublishedLabel, err = websurface.Localized(r, "arenas.participation.meta.published", map[string]string{"instant": instant}); err != nil {
			return ParticipationPageData{}, err
		}
	}
	if page.Notices, err = h.notices(r); err != nil {
		return ParticipationPageData{}, err
	}
	if page.RevealLabel, err = websurface.Localized(r, "arenas.participation.aggregate.reveal", nil); err != nil {
		return ParticipationPageData{}, err
	}
	if page.AggregateHeading, err = websurface.Localized(r, "arenas.participation.aggregate.heading", nil); err != nil {
		return ParticipationPageData{}, err
	}
	page.RevealHref = arenaPath(slug) + "?" + revealParam + "=" + revealValue
	if r.URL.Query().Get(revealParam) == revealValue {
		if page.Aggregate, err = h.aggregateView(r, arena); err != nil {
			return ParticipationPageData{}, err
		}
	}
	if page.PositionHeading, err = websurface.Localized(r, "arenas.participation.position.heading", nil); err != nil {
		return ParticipationPageData{}, err
	}

	groups, err := h.argumentGroups(r, arena)
	if err != nil {
		return ParticipationPageData{}, err
	}
	page.Arguments = groups

	identity, signed := security.FromContext(r.Context())
	if !signed {
		page.Anonymous, err = h.anonymousPosition(r)
		if err != nil {
			return ParticipationPageData{}, err
		}
		return page, nil
	}

	position, err := h.myPosition.Execute(r.Context(), positionsapp.GetMyPositionQuery{
		AccountID: identity.AccountID,
		ArenaID:   arena.ID().String(),
	})
	switch {
	case errors.Is(err, positionsapp.ErrPositionNotFound):
		position = nil
	case err != nil:
		return ParticipationPageData{}, err
	}

	if position == nil {
		page.Confirm, err = h.confirmForm(w, r, slug, failures, positionsdomain.Position{})
		if err != nil {
			return ParticipationPageData{}, err
		}
		page.Notices = append(page.Notices, failures.stranded()...)
		return page, nil
	}

	if page.State, err = h.positionState(r, position); err != nil {
		return ParticipationPageData{}, err
	}
	if page.Change, err = h.changeForm(w, r, slug, failures, position.CurrentPosition()); err != nil {
		return ParticipationPageData{}, err
	}
	if page.Publish, err = h.publishForm(w, r, slug, failures); err != nil {
		return ParticipationPageData{}, err
	}
	if page.Attribution, err = h.attributionForm(w, r, slug, arena.ID().String(), identity.AccountID, failures, groups); err != nil {
		return ParticipationPageData{}, err
	}
	return page, nil
}

// chrome is the head and header of one page: the request locale, the title and
// the account links, with the sign-out form in place of the account links for a
// person who is signed in.
func (h *ParticipationHandler) chrome(w http.ResponseWriter, r *http.Request, slug, pageTitle string) (ParticipationChrome, error) {
	brand, err := websurface.Localized(r, "arenas.participation.brand", nil)
	if err != nil {
		return ParticipationChrome{}, err
	}
	navLabel, err := websurface.Localized(r, "arenas.participation.nav.label", nil)
	if err != nil {
		return ParticipationChrome{}, err
	}
	document, err := websurface.Localized(r, "arenas.participation.nav.document", nil)
	if err != nil {
		return ParticipationChrome{}, err
	}

	chrome := ParticipationChrome{
		Lang:      websurface.Locale(r),
		PageTitle: pageTitle,
		Brand:     brand,
		NavLabel:  navLabel,
		Nav:       []ParticipationLink{{Label: document, Href: documentPath(slug)}},
	}

	_, signed := security.FromContext(r.Context())
	if !signed {
		account, err := h.accountLinks(r)
		if err != nil {
			return ParticipationChrome{}, err
		}
		chrome.Nav = append(chrome.Nav, account...)
		return chrome, nil
	}

	token, err := websurface.CSRF(h.security, w, r)
	if err != nil {
		return ParticipationChrome{}, err
	}
	label, err := websurface.Localized(r, "arenas.participation.nav.signout", nil)
	if err != nil {
		return ParticipationChrome{}, err
	}
	chrome.SignOut = &SignOutData{
		Action: "/logout",
		Field:  websurface.FormField,
		Token:  token,
		Label:  label,
	}
	return chrome, nil
}

// statusLabel is the localized public status of the Arena. The document surface
// owns the vocabulary and this page reuses it, so one status never reads two
// ways.
func (h *ParticipationHandler) statusLabel(r *http.Request, arena *arenasdomain.Arena) (string, error) {
	label, err := websurface.Localized(r, "arenas.document.status."+arena.Status().String(), nil)
	if err != nil {
		return "", err
	}
	return websurface.Localized(r, "arenas.participation.meta.status", map[string]string{"status": label})
}

// notices resolves the transitions a redirect reported. An unknown code
// produces nothing: the URL is never rendered, only its closed vocabulary is.
func (h *ParticipationHandler) notices(r *http.Request) ([]string, error) {
	key, ok := noticeKeys[r.URL.Query().Get(noticeParam)]
	if !ok {
		return nil, nil
	}
	message, err := websurface.Localized(r, key, nil)
	if err != nil {
		return nil, err
	}
	return []string{message}, nil
}

// aggregateView is the revealed public aggregate. A suppressed aggregate carries
// no count at all: the application publishes none, and printing a zero next to
// the note would be a number nobody derived.
func (h *ParticipationHandler) aggregateView(r *http.Request, arena *arenasdomain.Arena) (*AggregateData, error) {
	result, err := h.aggregate.Execute(r.Context(), positionsapp.GetPositionAggregateQuery{ArenaID: arena.ID().String()})
	if err != nil {
		return nil, err
	}

	heading, err := websurface.Localized(r, "arenas.participation.aggregate.heading", nil)
	if err != nil {
		return nil, err
	}
	view := &AggregateData{Heading: heading}
	if view.CheckedLabel, err = websurface.Localized(r, "arenas.participation.aggregate.checked", map[string]string{
		"instant": result.CheckedAt.UTC().Format(time.RFC3339),
	}); err != nil {
		return nil, err
	}
	if result.Suppressed {
		view.SuppressedNote, err = websurface.Localized(r, "arenas.participation.aggregate.suppressed", nil)
		return view, err
	}

	if view.CurrentHeading, err = websurface.Localized(r, "arenas.participation.aggregate.current", nil); err != nil {
		return nil, err
	}
	if view.InitialHeading, err = websurface.Localized(r, "arenas.participation.aggregate.initial", nil); err != nil {
		return nil, err
	}
	if view.TotalLabel, err = websurface.Localized(r, "arenas.participation.aggregate.total", map[string]string{
		"total": strconv.FormatInt(result.Total, 10),
	}); err != nil {
		return nil, err
	}
	if view.Current, err = h.distributionRows(r, result.Current); err != nil {
		return nil, err
	}
	if view.Initial, err = h.distributionRows(r, result.Initial); err != nil {
		return nil, err
	}
	return view, nil
}

// distributionRows renders the three counted choices of one distribution, in the
// order of the vocabulary.
func (h *ParticipationHandler) distributionRows(r *http.Request, distribution positionsapp.PositionDistribution) ([]DistributionRow, error) {
	counted := map[string]int64{
		positionsdomain.PositionAgree:     distribution.Agree,
		positionsdomain.PositionDisagree:  distribution.Disagree,
		positionsdomain.PositionUndecided: distribution.Undecided,
	}
	positions := positionsdomain.SupportedPositions()
	rows := make([]DistributionRow, 0, len(positions))
	for _, position := range positions {
		label, err := websurface.Localized(r, "arenas.participation.choice."+position.String(), nil)
		if err != nil {
			return nil, err
		}
		rows = append(rows, DistributionRow{Label: label, Count: counted[position.String()]})
	}
	return rows, nil
}

// anonymousPosition is the block of a visitor without a session: the local
// choice, what it means and the two ways into the account journey.
func (h *ParticipationHandler) anonymousPosition(r *http.Request) (*AnonymousPositionData, error) {
	text, err := websurface.Localized(r, "arenas.participation.position.anonymous", nil)
	if err != nil {
		return nil, err
	}
	heading, err := websurface.Localized(r, "arenas.participation.local.heading", nil)
	if err != nil {
		return nil, err
	}
	hint, err := websurface.Localized(r, "arenas.participation.local.hint", nil)
	if err != nil {
		return nil, err
	}
	choices, err := h.positionChoices(r, positionField, positionsdomain.Position{}, false)
	if err != nil {
		return nil, err
	}
	links, err := h.accountLinks(r)
	if err != nil {
		return nil, err
	}
	return &AnonymousPositionData{Text: text, Heading: heading, Hint: hint, Choices: choices.Choices, Links: links}, nil
}

// accountLinks lists the two ways into the account journey, in a fixed order so
// the header of a visitor is the same on every rendering.
func (h *ParticipationHandler) accountLinks(r *http.Request) ([]ParticipationLink, error) {
	candidates := []struct {
		key  string
		href string
	}{
		{key: "arenas.participation.nav.login", href: "/login"},
		{key: "arenas.participation.nav.register", href: "/register"},
	}
	links := make([]ParticipationLink, 0, len(candidates))
	for _, candidate := range candidates {
		label, err := websurface.Localized(r, candidate.key, nil)
		if err != nil {
			return nil, err
		}
		links = append(links, ParticipationLink{Label: label, Href: candidate.href})
	}
	return links, nil
}

// positionState reports the stored projection of the owner.
func (h *ParticipationHandler) positionState(r *http.Request, position *positionsdomain.DebatePosition) (*PositionStateData, error) {
	heading, err := websurface.Localized(r, "arenas.participation.position.heading", nil)
	if err != nil {
		return nil, err
	}
	initial, err := h.positionLabel(r, position.InitialPosition())
	if err != nil {
		return nil, err
	}
	current, err := h.positionLabel(r, position.CurrentPosition())
	if err != nil {
		return nil, err
	}
	initialLine, err := websurface.Localized(r, "arenas.participation.position.initial", map[string]string{"position": initial})
	if err != nil {
		return nil, err
	}
	currentLine, err := websurface.Localized(r, "arenas.participation.position.current", map[string]string{"position": current})
	if err != nil {
		return nil, err
	}
	return &PositionStateData{
		Heading: heading,
		Initial: initialLine,
		Current: currentLine,
	}, nil
}

// positionLabel is the localized label of one position.
func (h *ParticipationHandler) positionLabel(r *http.Request, position positionsdomain.Position) (string, error) {
	return websurface.Localized(r, "arenas.participation.choice."+position.String(), nil)
}

// positionChoices builds the closed vocabulary of positions as radios.
func (h *ParticipationHandler) positionChoices(r *http.Request, name string, checked positionsdomain.Position, withLegend bool) (FieldGroupData, error) {
	group := FieldGroupData{Name: name, ControlID: name, HintID: name + "-hint", ErrorID: name + "-error"}
	if withLegend {
		legend, err := websurface.Localized(r, nameFieldKey(name), nil)
		if err != nil {
			return FieldGroupData{}, err
		}
		group.Legend = legend
	}
	for _, position := range positionsdomain.SupportedPositions() {
		label, err := h.positionLabel(r, position)
		if err != nil {
			return FieldGroupData{}, err
		}
		group.Choices = append(group.Choices, ChoiceData{
			Name:      name,
			Value:     position.String(),
			Label:     label,
			ControlID: name + "-" + position.String(),
			Checked:   position == checked,
		})
	}
	return group, nil
}

// nameFieldKey maps a control name to its catalog label.
func nameFieldKey(name string) string {
	switch name {
	case relationField:
		return "arenas.participation.field.relation_label"
	default:
		return "arenas.participation.field.position_label"
	}
}

// relationChoices builds the closed vocabulary of relations as radios.
func (h *ParticipationHandler) relationChoices(r *http.Request, checked argumentsdomain.Relation) (FieldGroupData, error) {
	legend, err := websurface.Localized(r, nameFieldKey(relationField), nil)
	if err != nil {
		return FieldGroupData{}, err
	}
	group := FieldGroupData{Name: relationField, Legend: legend, ControlID: relationField, HintID: relationField + "-hint", ErrorID: relationField + "-error"}
	for _, relation := range argumentsdomain.SupportedRelations() {
		label, err := websurface.Localized(r, "arenas.participation.relation."+relation.String(), nil)
		if err != nil {
			return FieldGroupData{}, err
		}
		group.Choices = append(group.Choices, ChoiceData{
			Name:      relationField,
			Value:     relation.String(),
			Label:     label,
			ControlID: relationField + "-" + relation.String(),
			Checked:   relation == checked,
		})
	}
	return group, nil
}
