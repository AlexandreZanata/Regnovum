package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

const (
	signalsModeratorRaw  = "018f6b2a-0000-7000-8000-0000000000e1"
	signalsSubjectAuthor = "018f6b2a-0000-7000-8000-0000000000e2"
	signalsCounterpart   = "018f6b2a-0000-7000-8000-0000000000e3"
)

// fakeSignalFacts is a deterministic AbuseSignalFactRepository double that
// records the window it was asked for.
type fakeSignalFacts struct {
	facts       *domain.SignalFacts
	err         error
	calls       int
	subject     string
	windowStart time.Time
	windowEnd   time.Time
}

func (f *fakeSignalFacts) LoadAbuseSignalFacts(_ context.Context, subject domain.AuthorID, windowStart, windowEnd time.Time) (*domain.SignalFacts, error) {
	f.calls++
	f.subject = subject.String()
	f.windowStart = windowStart
	f.windowEnd = windowEnd
	if f.err != nil {
		return nil, f.err
	}
	return f.facts, nil
}

// fakeModerationAuthorizer answers the moderation port deterministically.
type fakeModerationAuthorizer struct {
	err    error
	calls  int
	actors []string
}

func (a *fakeModerationAuthorizer) EnsureModerator(_ context.Context, actor domain.ModeratorID) error {
	a.calls++
	a.actors = append(a.actors, actor.String())
	return a.err
}

func mustSignalsFact(t *testing.T) domain.SignalFacts {
	t.Helper()
	counterpart, err := domain.ParseAttributorID(signalsCounterpart)
	if err != nil {
		t.Fatalf("ParseAttributorID: %v", err)
	}
	return domain.SignalFacts{
		Reciprocity: []domain.ReciprocityFact{{Counterpart: counterpart, Inbound: 2, Outbound: 2}},
		Concentration: []domain.ConcentrationFact{
			{Attributor: counterpart, Events: 8},
		},
	}
}

func signalsQuery() application.AssessAttributionSignalsQuery {
	return application.AssessAttributionSignalsQuery{
		ActorAccountID: signalsModeratorRaw,
		SubjectID:      signalsSubjectAuthor,
	}
}

func TestGetAttributionSignalsAssessesTheInjectedWindow(t *testing.T) {
	t.Parallel()

	facts := &fakeSignalFacts{facts: ptrSignalsFacts(mustSignalsFact(t))}
	authorizer := &fakeModerationAuthorizer{}
	policy := domain.DefaultSignalPolicy()
	useCase := application.NewGetAttributionSignalsUseCase(facts, authorizer, policy, fixedClock{instant: testInstant})

	assessment, err := useCase.Execute(context.Background(), signalsQuery())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if authorizer.calls != 1 || authorizer.actors[0] != signalsModeratorRaw {
		t.Fatalf("authorizer calls = %d for %v, want one authorization of the actor", authorizer.calls, authorizer.actors)
	}
	if facts.calls != 1 {
		t.Fatalf("fact calls = %d, want one read", facts.calls)
	}
	if facts.subject != signalsSubjectAuthor {
		t.Fatalf("subject = %q, want the assessed author", facts.subject)
	}
	// The window ends at the injected clock and spans exactly the policy
	// window, so an assessment is reproducible from its document.
	if !facts.windowEnd.Equal(testInstant) {
		t.Fatalf("window end = %s, want the injected instant", facts.windowEnd)
	}
	if got := facts.windowEnd.Sub(facts.windowStart); got != policy.Window {
		t.Fatalf("window = %v, want the policy window %v", got, policy.Window)
	}
	if assessment.PolicyVersion != policy.Version || assessment.Window != policy.Window {
		t.Fatalf("assessment = %+v, want the policy revision and window", assessment)
	}
	if !assessment.AssessedAt.Equal(testInstant) {
		t.Fatalf("AssessedAt = %s, want the injected instant", assessment.AssessedAt)
	}
	if !assessment.Has(domain.SignalReciprocity) || !assessment.Has(domain.SignalConcentration) {
		t.Fatalf("signals = %+v, want the reciprocity and concentration signals", assessment.Signals)
	}
	if assessment.Subject.String() != signalsSubjectAuthor {
		t.Fatalf("subject = %q, want the assessed author", assessment.Subject.String())
	}
}

func TestGetAttributionSignalsIsModeratorOnly(t *testing.T) {
	t.Parallel()

	t.Run("denied actor never reads a fact", func(t *testing.T) {
		facts := &fakeSignalFacts{facts: ptrSignalsFacts(mustSignalsFact(t))}
		authorizer := &fakeModerationAuthorizer{err: application.ErrNotAuthorized}
		useCase := application.NewGetAttributionSignalsUseCase(facts, authorizer, domain.DefaultSignalPolicy(), fixedClock{instant: testInstant})

		if _, err := useCase.Execute(context.Background(), signalsQuery()); !errors.Is(err, application.ErrNotAuthorized) {
			t.Fatalf("Execute() error = %v, want ErrNotAuthorized", err)
		}
		if facts.calls != 0 {
			t.Fatal("an unauthorized caller must not observe whether a signal exists")
		}
	})

	t.Run("storage failure during authorization propagates", func(t *testing.T) {
		facts := &fakeSignalFacts{facts: ptrSignalsFacts(mustSignalsFact(t))}
		authorizer := &fakeModerationAuthorizer{err: errors.New("role store offline")}
		useCase := application.NewGetAttributionSignalsUseCase(facts, authorizer, domain.DefaultSignalPolicy(), fixedClock{instant: testInstant})

		_, err := useCase.Execute(context.Background(), signalsQuery())
		if err == nil || errors.Is(err, application.ErrNotAuthorized) {
			t.Fatalf("Execute() error = %v, want the storage failure, not a denial", err)
		}
		if facts.calls != 0 {
			t.Fatal("no fact may be read when authorization could not be answered")
		}
	})
}

func TestGetAttributionSignalsValidatesInputsBeforePorts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query application.AssessAttributionSignalsQuery
	}{
		{name: "missing actor", query: application.AssessAttributionSignalsQuery{SubjectID: signalsSubjectAuthor}},
		{name: "blank actor", query: application.AssessAttributionSignalsQuery{ActorAccountID: "   ", SubjectID: signalsSubjectAuthor}},
		{name: "missing subject", query: application.AssessAttributionSignalsQuery{ActorAccountID: signalsModeratorRaw}},
		{name: "blank subject", query: application.AssessAttributionSignalsQuery{ActorAccountID: signalsModeratorRaw, SubjectID: " "}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := &fakeSignalFacts{}
			authorizer := &fakeModerationAuthorizer{}
			useCase := application.NewGetAttributionSignalsUseCase(facts, authorizer, domain.DefaultSignalPolicy(), fixedClock{instant: testInstant})

			if _, err := useCase.Execute(context.Background(), test.query); err == nil {
				t.Fatal("Execute() error = nil, want a validation failure")
			}
			if facts.calls != 0 || authorizer.calls != 0 {
				t.Fatal("a malformed query must not touch the ports")
			}
		})
	}

	t.Run("invalid policy", func(t *testing.T) {
		policy := domain.DefaultSignalPolicy()
		policy.Version = ""
		facts := &fakeSignalFacts{}
		authorizer := &fakeModerationAuthorizer{}
		useCase := application.NewGetAttributionSignalsUseCase(facts, authorizer, policy, fixedClock{instant: testInstant})

		if _, err := useCase.Execute(context.Background(), signalsQuery()); !errors.Is(err, domain.ErrInvalidSignalPolicy) {
			t.Fatalf("Execute() error = %v, want ErrInvalidSignalPolicy", err)
		}
		if facts.calls != 0 || authorizer.calls != 0 {
			t.Fatal("an invalid policy must not touch the ports")
		}
	})
}

func TestGetAttributionSignalsHandlesEmptyAndFailingFacts(t *testing.T) {
	t.Parallel()

	t.Run("a subject without facts is a clean assessment", func(t *testing.T) {
		facts := &fakeSignalFacts{}
		useCase := application.NewGetAttributionSignalsUseCase(facts, &fakeModerationAuthorizer{}, domain.DefaultSignalPolicy(), fixedClock{instant: testInstant})

		assessment, err := useCase.Execute(context.Background(), signalsQuery())
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if len(assessment.Signals) != 0 {
			t.Fatalf("signals = %+v, want none", assessment.Signals)
		}
		if err := assessment.Validate(); err != nil {
			t.Fatalf("empty assessment is incoherent: %v", err)
		}
	})

	t.Run("storage failure propagates", func(t *testing.T) {
		facts := &fakeSignalFacts{err: errors.New("facts offline")}
		useCase := application.NewGetAttributionSignalsUseCase(facts, &fakeModerationAuthorizer{}, domain.DefaultSignalPolicy(), fixedClock{instant: testInstant})

		if _, err := useCase.Execute(context.Background(), signalsQuery()); err == nil {
			t.Fatal("Execute() error = nil, want the storage failure")
		}
	})

	t.Run("malformed facts are refused", func(t *testing.T) {
		facts := &fakeSignalFacts{facts: &domain.SignalFacts{Alternation: []domain.AlternationFact{{Changes: 4, Reversals: 2}}}}
		useCase := application.NewGetAttributionSignalsUseCase(facts, &fakeModerationAuthorizer{}, domain.DefaultSignalPolicy(), fixedClock{instant: testInstant})

		if _, err := useCase.Execute(context.Background(), signalsQuery()); !errors.Is(err, domain.ErrInvalidSignalFacts) {
			t.Fatalf("Execute() error = %v, want ErrInvalidSignalFacts", err)
		}
	})
}

func ptrSignalsFacts(facts domain.SignalFacts) *domain.SignalFacts {
	return &facts
}
