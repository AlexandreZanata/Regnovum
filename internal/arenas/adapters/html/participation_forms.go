package html

import (
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/websurface"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
	positionsapp "github.com/AlexandreZanata/Regnovum/internal/positions/application"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// argumentOptionLimit bounds how many arguments of one relation become
// attribution options. It is a bounded read of a page a person acts on, and
// walking past it belongs to the JSON API.
const argumentOptionLimit = 20

// attemptKeyBytes is the entropy of one attempt key: 128 bits, rendered as
// printable ASCII well inside the bound the domain accepts (128 characters).
const attemptKeyBytes = 16

// relations is the closed vocabulary of the argument relations, in the order the
// page lists them. It is the domain's own list, so the page can never render a
// relation the domain would refuse.
var relations = argumentsdomain.SupportedRelations()

// confirmForm is the form that records the initial position. The initial choice
// is immutable, so the form starts unselected: nothing the page shows may
// suggest that the first answer was already taken.
func (h *ParticipationHandler) confirmForm(w http.ResponseWriter, r *http.Request, slug string, failures submissionErrors, checked positionsdomain.Position) (*PositionFormData, error) {
	base, err := h.formBase(w, r, formSpec{
		action:    arenaPath(slug) + "/position",
		heading:   "arenas.participation.position.confirm_heading",
		intro:     "arenas.participation.position.confirm_intro",
		submit:    "arenas.participation.position.confirm_submit",
		busy:      "arenas.participation.position.confirm_busy",
		errorID:   positionField + "-form-error",
		failures:  failures,
		forForm:   formPosition,
		summaryOn: true,
	})
	if err != nil {
		return nil, err
	}
	group, err := h.positionChoices(r, positionField, checked, true)
	if err != nil {
		return nil, err
	}
	group = h.decorateGroup(group, failures, formPosition)
	base.Summary = summaryOfGroup(group, failures)
	return &PositionFormData{FormData: base, Group: group}, nil
}

// changeForm is the form that records one change of the position.
func (h *ParticipationHandler) changeForm(w http.ResponseWriter, r *http.Request, slug string, failures submissionErrors, current positionsdomain.Position) (*PositionFormData, error) {
	base, err := h.formBase(w, r, formSpec{
		action:    arenaPath(slug) + "/position/change",
		heading:   "arenas.participation.position.change_heading",
		intro:     "arenas.participation.position.change_intro",
		submit:    "arenas.participation.position.change_submit",
		busy:      "arenas.participation.position.change_busy",
		errorID:   positionField + "-form-error",
		failures:  failures,
		forForm:   formChange,
		summaryOn: true,
	})
	if err != nil {
		return nil, err
	}
	// The form starts on the stored position: a change is a deliberate move
	// away from it, and preselecting anything else would turn a stray submit
	// into a silent change.
	group, err := h.positionChoices(r, positionField, current, true)
	if err != nil {
		return nil, err
	}
	group = h.decorateGroup(group, failures, formChange)
	base.Summary = summaryOfGroup(group, failures)
	return &PositionFormData{FormData: base, Group: group}, nil
}

// publishForm is the form that publishes one argument. The attempt key is
// generated when the page is rendered, so a double submission of the same
// document resolves the argument that was already recorded instead of debiting
// INK twice.
func (h *ParticipationHandler) publishForm(w http.ResponseWriter, r *http.Request, slug string, failures submissionErrors) (*PublishFormData, error) {
	attempt, err := newAttemptKey(h.random)
	if err != nil {
		return nil, err
	}
	base, err := h.formBase(w, r, formSpec{
		action:   arenaPath(slug) + "/arguments",
		heading:  "arenas.participation.arguments.publish_heading",
		intro:    "arenas.participation.arguments.publish_intro",
		submit:   "arenas.participation.arguments.publish_submit",
		busy:     "arenas.participation.arguments.publish_busy",
		errorID:  contentField + "-form-error",
		failures: failures,
		forForm:  formPublish,
		hidden:   []HiddenData{{Name: attemptField, Value: attempt}},
	})
	if err != nil {
		return nil, err
	}
	relations, err := h.relationChoices(r, argumentsdomain.Relation{})
	if err != nil {
		return nil, err
	}
	relations = h.decorateGroup(relations, failures, formPublish)

	label, err := websurface.Localized(r, "arenas.participation.field.content_label", nil)
	if err != nil {
		return nil, err
	}
	hint, err := websurface.Localized(r, "arenas.participation.field.content_hint", map[string]string{
		"max": strconv.Itoa(argumentsdomain.MaxGraphemeCost),
	})
	if err != nil {
		return nil, err
	}

	form := &PublishFormData{
		FormData:      base,
		Relations:     relations,
		ContentName:   contentField,
		ContentLabel:  label,
		ContentHint:   hint,
		ContentID:     contentField + controlIDSuffix,
		ContentHintID: contentField + hintIDSuffix,
	}
	form.ContentErrorID = contentField + errorIDSuffix
	if failures.Form == formPublish {
		form.ContentError = failures.Field[contentField]
	}
	switch {
	case form.ContentHint == "":
		form.ContentDescribed = form.ContentErrorID
	case form.ContentError == "":
		form.ContentDescribed = form.ContentHintID
	default:
		form.ContentDescribed = form.ContentHintID + " " + form.ContentErrorID
	}
	form.Summary = summaryOfGroup(relations, failures)
	if form.ContentError != "" {
		form.Summary = append(form.Summary, SummaryData{Target: form.ContentID, Message: form.ContentError})
	}
	return form, nil
}

// attributionForm is the form that records what influenced the most recent
// change of the owner. It exists only when there is a change to attribute, and
// its options are the arguments the page already listed: nothing here reads a
// list the person cannot see.
func (h *ParticipationHandler) attributionForm(w http.ResponseWriter, r *http.Request, slug, arenaID, accountID string, failures submissionErrors, groups []RelationGroupData) (*AttributionFormData, error) {
	changes, err := h.changes.Execute(r.Context(), positionsChangeQuery(accountID, arenaID))
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return nil, nil
	}
	options := attributionOptions(groups)
	if len(options) == 0 {
		// Nothing the page listed can be credited, so a form would offer an
		// empty selection that could never be submitted.
		return nil, nil
	}

	base, err := h.formBase(w, r, formSpec{
		action:   arenaPath(slug) + "/attributions",
		heading:  "arenas.participation.attribution.heading",
		intro:    "arenas.participation.attribution.intro",
		submit:   "arenas.participation.attribution.submit",
		busy:     "arenas.participation.attribution.busy",
		errorID:  attributionField + "-form-error",
		failures: failures,
		forForm:  formAttribution,
		hidden:   []HiddenData{{Name: changeIDField, Value: changes[0].ID}},
	})
	if err != nil {
		return nil, err
	}
	group, err := h.attributionGroup(r, options, failures)
	if err != nil {
		return nil, err
	}
	base.Summary = summaryOfGroup(group, failures)
	return &AttributionFormData{FormData: base, Options: group}, nil
}

// attributionGroup is the checkbox group of the attribution form.
func (h *ParticipationHandler) attributionGroup(r *http.Request, options []ChoiceData, failures submissionErrors) (FieldGroupData, error) {
	legend, err := websurface.Localized(r, "arenas.participation.attribution.heading", nil)
	if err != nil {
		return FieldGroupData{}, err
	}
	hint, err := websurface.Localized(r, "arenas.participation.attribution.intro", map[string]string{
		"max": strconv.Itoa(h.maxAttributions),
	})
	if err != nil {
		return FieldGroupData{}, err
	}
	group := FieldGroupData{
		Name:      attributionField,
		Legend:    legend,
		Hint:      hint,
		ControlID: attributionField,
		HintID:    attributionField + hintIDSuffix,
		ErrorID:   attributionField + errorIDSuffix,
		Limit:     h.maxAttributions,
		Choices:   options,
	}
	return h.decorateGroup(group, failures, formAttribution), nil
}

// attributionOptions turns the listed arguments into the options of the
// attribution form, in the order the page lists them.
func attributionOptions(groups []RelationGroupData) []ChoiceData {
	options := make([]ChoiceData, 0, len(groups)*argumentOptionLimit)
	for _, group := range groups {
		for _, argument := range group.Arguments {
			options = append(options, ChoiceData{
				Name:      attributionField,
				Value:     argument.ID,
				Label:     argument.OptionLabel,
				ControlID: attributionField + "-" + argument.ID,
			})
		}
	}
	return options
}

// argumentGroups lists the newest page of each relation and translates the
// entries. An argument whose content is absent is left out of the list: nothing
// readable can be rendered from it, and the excerpt of an option would be empty.
func (h *ParticipationHandler) argumentGroups(r *http.Request, arena *arenasdomain.Arena) ([]RelationGroupData, error) {
	empty, err := websurface.Localized(r, "arenas.participation.arguments.empty", nil)
	if err != nil {
		return nil, err
	}

	groups := make([]RelationGroupData, 0, len(relations))
	for _, relation := range relations {
		heading, err := websurface.Localized(r, "arenas.participation.relation."+relation.String(), nil)
		if err != nil {
			return nil, err
		}
		group := RelationGroupData{Heading: heading, Empty: empty}

		page, err := h.arguments.Execute(r.Context(), argumentsapp.ListArenaArgumentsQuery{
			ArenaID:  arena.ID().String(),
			Relation: relation.String(),
			Limit:    listLimit,
		})
		if err != nil {
			return nil, err
		}
		for _, argument := range page.Arguments {
			if argument.Content == nil {
				continue
			}
			content := argument.Content.String()
			replies, err := websurface.Localized(r, "arenas.participation.arguments.replies", map[string]string{
				"count": strconv.FormatInt(argument.ReplyCount, 10),
			})
			if err != nil {
				return nil, err
			}
			option, err := websurface.Localized(r, "arenas.participation.attribution.option", map[string]string{
				"relation": heading,
				"excerpt":  excerpt(content, excerptRunes),
			})
			if err != nil {
				return nil, err
			}
			group.Arguments = append(group.Arguments, ArgumentData{
				ID:            argument.ID.String(),
				RelationLabel: heading,
				Content:       content,
				CreatedAt:     argument.CreatedAt.UTC().Format(time.RFC3339),
				RepliesLabel:  replies,
				OptionLabel:   option,
			})
			if len(group.Arguments) >= argumentOptionLimit {
				break
			}
		}
		groups = append(groups, group)
	}
	return groups, nil
}

// positionsChangeQuery is the owner-scoped history read of one Arena. The
// history is private: it is read for the account that owns the session and for
// no one else.
func positionsChangeQuery(accountID, arenaID string) positionsapp.ListPositionChangesQuery {
	return positionsapp.ListPositionChangesQuery{AccountID: accountID, ArenaID: arenaID}
}

// formSpec is the shared shape of the four forms of the page.
type formSpec struct {
	action    string
	heading   string
	intro     string
	submit    string
	busy      string
	errorID   string
	failures  submissionErrors
	forForm   string
	summaryOn bool
	hidden    []HiddenData
}

// formBase assembles the parts every form shares: the action, its title, the
// double-submit token and, when this is the form that failed, the summary and
// the message of the refusal.
func (h *ParticipationHandler) formBase(w http.ResponseWriter, r *http.Request, spec formSpec) (FormData, error) {
	heading, err := websurface.Localized(r, spec.heading, nil)
	if err != nil {
		return FormData{}, err
	}
	intro, err := websurface.Localized(r, spec.intro, map[string]string{"max": strconv.Itoa(h.maxAttributions)})
	if err != nil {
		return FormData{}, err
	}
	submit, err := websurface.Localized(r, spec.submit, nil)
	if err != nil {
		return FormData{}, err
	}
	busy, err := websurface.Localized(r, spec.busy, nil)
	if err != nil {
		return FormData{}, err
	}
	summary, err := websurface.Localized(r, "arenas.participation.errors.summary_title", nil)
	if err != nil {
		return FormData{}, err
	}
	token, err := websurface.CSRF(h.security, w, r)
	if err != nil {
		return FormData{}, err
	}

	base := FormData{
		Action:       spec.action,
		Heading:      heading,
		Intro:        intro,
		Field:        websurface.FormField,
		Token:        token,
		SummaryTitle: summary,
		ErrorID:      spec.errorID,
		Hidden:       spec.hidden,
		SubmitLabel:  submit,
		BusyLabel:    busy,
	}
	if spec.failures.Form == spec.forForm {
		base.Error = spec.failures.Message
	}
	return base, nil
}

// decorateGroup attaches the messages of the failed fields to the group that
// owns them.
func (h *ParticipationHandler) decorateGroup(group FieldGroupData, failures submissionErrors, form string) FieldGroupData {
	if failures.Form != form {
		return group
	}
	group.Error = failures.Field[group.Name]
	switch {
	case group.Error == "" && group.Hint == "":
		group.DescribedBy = group.ErrorID
	case group.Error == "":
		group.DescribedBy = group.HintID
	case group.Hint == "":
		group.DescribedBy = group.ErrorID
	default:
		group.DescribedBy = group.HintID + " " + group.ErrorID
	}
	return group
}

// summaryOfGroup lists the errors of one group in the order the page declares
// them. Each line links to the control that can be fixed: a radio or a checkbox
// is what takes the focus, so the link addresses the first choice of the group
// and not the fieldset, which a keyboard cannot reach.
func summaryOfGroup(group FieldGroupData, failures submissionErrors) []SummaryData {
	if group.Error == "" {
		return nil
	}
	target := group.ControlID
	if len(group.Choices) > 0 {
		target = group.Choices[0].ControlID
	}
	return []SummaryData{{Target: target, Message: group.Error}}
}

// newAttemptKey is the attempt key of one rendered publication form: printable
// ASCII well inside the bound the domain accepts, and unpredictable so two
// renderings of the same page are two different attempts. The randomness comes
// from the injected port, never from the package: reading entropy directly in an
// adapter is what the architecture test refuses (ADR-012).
func newAttemptKey(random ports.Random) (string, error) {
	buffer := make([]byte, attemptKeyBytes)
	if _, err := random.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

// excerpt shortens an argument for a checkbox label. It cuts on rune boundaries
// and never leaves a combining mark dangling at the end, so a grapheme written
// as a base rune plus a mark is either whole or absent. The cut is a display aid
// only: the argument itself is never truncated where it is stored or returned,
// and the full text stays readable through the JSON API.
func excerpt(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= limit {
		return trimmed
	}
	cut := runes[:limit]
	for len(cut) > 0 && unicode.Is(unicode.Mn, cut[len(cut)-1]) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimRight(string(cut), " ") + "…"
}
