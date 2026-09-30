package testsupport

import (
	"strings"
	"time"

	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	moderationdomain "github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

// The synthetic moderation values of a scenario: a spam report on an argument
// and a warning justified by a cited rule. They name no person and quote no
// user content.
const (
	defaultReportTarget  = "argument"
	defaultReportReason  = "spam"
	defaultReportContext = "Cenário sintético: o alvo repete o mesmo link em sequência."
	defaultRule          = "anti-spam"
	defaultJustification = "Cenário sintético: o alvo repete o mesmo link em sequência."
)

// ReportOption varies the report a builder makes.
type ReportOption func(*reportSpec)

type reportSpec struct {
	reason  string
	context string
}

// WithReportReason files the report under a specific reason of the closed
// vocabulary.
func WithReportReason(reason string) ReportOption {
	return func(spec *reportSpec) { spec.reason = reason }
}

// WithReportContext files the report with a specific context, which is what a
// scenario reading it back asserts on.
func WithReportContext(context string) ReportOption {
	return func(spec *reportSpec) { spec.context = context }
}

// Report builds the command that files one report: the reporter, the target
// type, the target identifier, a reason from the closed vocabulary and a
// context within the bound the domain documents.
//
// The context is judged here because the domain documents the bound and the use
// case would otherwise be the first to notice; a scenario that asks for an
// oversized context is answered with the domain's refusal.
func (b *Builder) Report(reporter *identitydomain.Account, target, targetID string, options ...ReportOption) (moderationapp.FileReportCommand, error) {
	b.t.Helper()
	if reporter == nil {
		return moderationapp.FileReportCommand{}, moderationdomain.ErrEmptyAccountID
	}
	if strings.TrimSpace(targetID) == "" {
		return moderationapp.FileReportCommand{}, moderationdomain.ErrEmptyTargetID
	}
	spec := reportSpec{reason: defaultReportReason, context: defaultReportContext}
	for _, option := range options {
		option(&spec)
	}

	targetType, err := moderationdomain.ParseTargetType(target)
	if err != nil {
		return moderationapp.FileReportCommand{}, err
	}
	reason, err := moderationdomain.ParseReason(spec.reason)
	if err != nil {
		return moderationapp.FileReportCommand{}, err
	}
	if trimmed := strings.TrimSpace(spec.context); trimmed == "" || len([]rune(trimmed)) > moderationdomain.MaxReportContextLength {
		return moderationapp.FileReportCommand{}, moderationdomain.ErrInvalidContext
	}

	return moderationapp.FileReportCommand{
		Reporter: moderationdomain.AccountID(reporter.ID().String()),
		Target:   targetType.String(),
		TargetID: targetID,
		Reason:   string(reason),
		Context:  spec.context,
	}, nil
}

// DecisionOption varies the decision a builder makes.
type DecisionOption func(*decisionSpec)

type decisionSpec struct {
	rule          string
	justification string
	expiresAt     *time.Time
}

// WithDecisionRule cites a specific rule reference in the decision.
func WithDecisionRule(rule string) DecisionOption {
	return func(spec *decisionSpec) { spec.rule = rule }
}

// WithDecisionJustification writes a specific justification.
func WithDecisionJustification(justification string) DecisionOption {
	return func(spec *decisionSpec) { spec.justification = justification }
}

// WithDecisionExpiry sets the instant a time-boxed measure ends, which is how a
// scenario decides a suspension that outlives the decision.
func WithDecisionExpiry(expiresAt time.Time) DecisionOption {
	return func(spec *decisionSpec) {
		instant := expiresAt.UTC()
		spec.expiresAt = &instant
	}
}

// Decision is one moderation decision, as the reviewer's surface states it: the
// action, the cited rule, the justification and the expiry the action requires
// or forbids.
type Decision struct {
	Action        moderationdomain.Action
	Rule          string
	Justification string
	ExpiresAt     *time.Time
}

// Decision builds a reviewable decision over a target: the action decides
// whether an expiry is required, forbidden or optional, so the expiry is
// resolved by the domain instead of being chosen by the caller.
//
// A target the action may not sanction is refused here, which is what keeps a
// scenario from deciding something the product's own policy forbids.
func (b *Builder) Decision(target moderationdomain.TargetType, action string, options ...DecisionOption) (Decision, error) {
	b.t.Helper()
	spec := decisionSpec{rule: defaultRule, justification: defaultJustification}
	for _, option := range options {
		option(&spec)
	}

	parsedAction, err := moderationdomain.ParseAction(action)
	if err != nil {
		return Decision{}, err
	}
	if !moderationdomain.SanctionAllowed(target, parsedAction) {
		return Decision{}, moderationdomain.ErrTargetActionMismatch
	}
	rule, err := moderationdomain.ParseRule(spec.rule)
	if err != nil {
		return Decision{}, err
	}
	justification, err := moderationdomain.ParseJustification(spec.justification)
	if err != nil {
		return Decision{}, err
	}
	expiry, err := moderationdomain.DecisionExpiry(parsedAction, spec.expiresAt, b.Now())
	if err != nil {
		return Decision{}, err
	}

	return Decision{
		Action:        parsedAction,
		Rule:          rule,
		Justification: justification,
		ExpiresAt:     expiry,
	}, nil
}
