package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

const (
	signalSubjectRaw   = "018f6b2a-0000-7000-8000-0000000000d1"
	signalRingPeerRaw  = "018f6b2a-0000-7000-8000-0000000000d2"
	signalFarmerRaw    = "018f6b2a-0000-7000-8000-0000000000d3"
	signalFlipperRaw   = "018f6b2a-0000-7000-8000-0000000000d4"
	signalCasualRaw    = "018f6b2a-0000-7000-8000-0000000000d5"
	signalAssessmentAt = "2026-09-17T18:00:00Z"
)

func mustAttributor(t *testing.T, raw string) domain.AttributorID {
	t.Helper()
	id, err := domain.ParseAttributorID(raw)
	if err != nil {
		t.Fatalf("ParseAttributorID(%q): %v", raw, err)
	}
	return id
}

func mustSignalSubject(t *testing.T) domain.AuthorID {
	t.Helper()
	id, err := domain.ParseAuthorID(signalSubjectRaw)
	if err != nil {
		t.Fatalf("ParseAuthorID: %v", err)
	}
	return id
}

// assess runs the default policy over the facts and returns the signals.
func assess(t *testing.T, facts domain.SignalFacts) []domain.Signal {
	t.Helper()
	signals, err := domain.DefaultSignalPolicy().Assess(mustSignalSubject(t), facts)
	if err != nil {
		t.Fatalf("Assess() error = %v", err)
	}
	for _, signal := range signals {
		if err := signal.Validate(); err != nil {
			t.Fatalf("produced signal is invalid: %v (%+v)", err, signal)
		}
	}
	return signals
}

func signalKinds(signals []domain.Signal) map[domain.SignalKind]domain.Signal {
	kinds := make(map[domain.SignalKind]domain.Signal, len(signals))
	for _, signal := range signals {
		if _, repeated := kinds[signal.Kind]; repeated {
			return nil
		}
		kinds[signal.Kind] = signal
	}
	return kinds
}

func TestDefaultSignalPolicyIsValid(t *testing.T) {
	t.Parallel()

	policy := domain.DefaultSignalPolicy()
	if !policy.IsValid() {
		t.Fatal("the default policy must be valid")
	}
	if policy.Version == "" || policy.Window <= 0 {
		t.Fatalf("policy = %+v, want a versioned revision and a positive window", policy)
	}
}

func TestSignalPolicyValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*domain.SignalPolicy)
	}{
		{name: "missing version", mutate: func(p *domain.SignalPolicy) { p.Version = "" }},
		{name: "missing window", mutate: func(p *domain.SignalPolicy) { p.Window = 0 }},
		{name: "negative window", mutate: func(p *domain.SignalPolicy) { p.Window = -time.Hour }},
		{name: "reciprocity threshold below one", mutate: func(p *domain.SignalPolicy) { p.MinReciprocityEvents = 0 }},
		{name: "concentration threshold below one", mutate: func(p *domain.SignalPolicy) { p.MinConcentrationEvents = 0 }},
		{name: "share above the whole", mutate: func(p *domain.SignalPolicy) { p.ConcentrationShareBasisPoints = 10_001 }},
		{name: "share below one", mutate: func(p *domain.SignalPolicy) { p.ConcentrationShareBasisPoints = 0 }},
		{name: "single change cannot alternate", mutate: func(p *domain.SignalPolicy) { p.MinAlternationChanges = 1 }},
		{name: "no reversals required", mutate: func(p *domain.SignalPolicy) { p.MinAlternationReversals = 0 }},
		{name: "more reversals than changes", mutate: func(p *domain.SignalPolicy) {
			p.MinAlternationChanges = 3
			p.MinAlternationReversals = 3
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := domain.DefaultSignalPolicy()
			test.mutate(&policy)
			if policy.IsValid() {
				t.Fatalf("policy %+v must be invalid", policy)
			}
			if _, err := policy.Assess(mustSignalSubject(t), domain.SignalFacts{}); !errors.Is(err, domain.ErrInvalidSignalPolicy) {
				t.Fatalf("Assess() error = %v, want ErrInvalidSignalPolicy", err)
			}
		})
	}
}

// TestReciprocityRingIsReported is the known fixture of the collusion shape
// (THR-PERS-01): two accounts crediting each other repeatedly.
func TestReciprocityRingIsReported(t *testing.T) {
	t.Parallel()

	peer := mustAttributor(t, signalRingPeerRaw)
	signals := assess(t, domain.SignalFacts{Reciprocity: []domain.ReciprocityFact{
		{Counterpart: peer, Inbound: 3, Outbound: 2},
	}})

	kinds := signalKinds(signals)
	signal, ok := kinds[domain.SignalReciprocity]
	if !ok {
		t.Fatalf("signals = %+v, want a reciprocity signal", signals)
	}
	if signal.Counterpart != peer {
		t.Fatalf("counterpart = %q, want the mutual account", signal.Counterpart.String())
	}
	if signal.MutualEvents != 5 {
		t.Fatalf("MutualEvents = %d, want both directions summed (5)", signal.MutualEvents)
	}
	if signal.Window != domain.DefaultSignalPolicy().Window {
		t.Fatalf("Window = %v, want the policy window", signal.Window)
	}
}

// TestReciprocityFalsePositiveStaysUnreported documents the benign case: one
// exchange in each direction is ordinary social behaviour, not a ring.
func TestReciprocityFalsePositiveStaysUnreported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		fact domain.ReciprocityFact
	}{
		{name: "single exchange", fact: domain.ReciprocityFact{Counterpart: mustAttributor(t, signalRingPeerRaw), Inbound: 1, Outbound: 1}},
		{name: "repeated one way only", fact: domain.ReciprocityFact{Counterpart: mustAttributor(t, signalCasualRaw), Inbound: 4, Outbound: 1}},
		{name: "repeated the other way only", fact: domain.ReciprocityFact{Counterpart: mustAttributor(t, signalCasualRaw), Inbound: 1, Outbound: 4}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			signals := assess(t, domain.SignalFacts{Reciprocity: []domain.ReciprocityFact{test.fact}})
			if len(signals) != 0 {
				t.Fatalf("signals = %+v, want none for %+v", signals, test.fact)
			}
		})
	}
}

// TestConcentrationFarmIsReported is the known fixture of one account holding
// a dominant share of the subject's attributions (METRICS §4).
func TestConcentrationFarmIsReported(t *testing.T) {
	t.Parallel()

	farmer := mustAttributor(t, signalFarmerRaw)
	signals := assess(t, domain.SignalFacts{Concentration: []domain.ConcentrationFact{
		{Attributor: farmer, Events: 6},
		{Attributor: mustAttributor(t, signalCasualRaw), Events: 4},
	}})

	kinds := signalKinds(signals)
	signal, ok := kinds[domain.SignalConcentration]
	if !ok {
		t.Fatalf("signals = %+v, want a concentration signal", signals)
	}
	if signal.Counterpart != farmer || signal.DominantEvents != 6 {
		t.Fatalf("signal = %+v, want the dominant account with its event count", signal)
	}
	// 6 of 10 is 60%: integer basis points, never a float.
	if signal.ShareBasisPoints != 6_000 {
		t.Fatalf("ShareBasisPoints = %d, want 6000", signal.ShareBasisPoints)
	}
}

// TestConcentrationFalsePositivesStayUnreported documents the benign cases: a
// young Arena where one participant holds everything, and two participants
// holding an equal half.
func TestConcentrationFalsePositivesStayUnreported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		facts []domain.ConcentrationFact
	}{
		{
			name: "one participant holding a young arena",
			facts: []domain.ConcentrationFact{
				{Attributor: mustAttributor(t, signalCasualRaw), Events: 3},
			},
		},
		{
			name: "two participants sharing equally",
			facts: []domain.ConcentrationFact{
				{Attributor: mustAttributor(t, signalFarmerRaw), Events: 5},
				{Attributor: mustAttributor(t, signalCasualRaw), Events: 5},
			},
		},
		{
			name: "below the configured share",
			facts: []domain.ConcentrationFact{
				{Attributor: mustAttributor(t, signalFarmerRaw), Events: 5},
				{Attributor: mustAttributor(t, signalCasualRaw), Events: 4},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			signals := assess(t, domain.SignalFacts{Concentration: test.facts})
			if len(signals) != 0 {
				t.Fatalf("signals = %+v, want none for %+v", signals, test.facts)
			}
		})
	}
}

// TestRapidAlternationIsReported is the known fixture of repeated reversals by
// the same account (METRICS §4).
func TestRapidAlternationIsReported(t *testing.T) {
	t.Parallel()

	flipper := mustAttributor(t, signalFlipperRaw)
	signals := assess(t, domain.SignalFacts{Alternation: []domain.AlternationFact{
		{Account: flipper, Changes: 4, Reversals: 2},
	}})

	kinds := signalKinds(signals)
	signal, ok := kinds[domain.SignalRapidAlternation]
	if !ok {
		t.Fatalf("signals = %+v, want a rapid alternation signal", signals)
	}
	if signal.Counterpart != flipper || signal.Changes != 4 || signal.Reversals != 2 {
		t.Fatalf("signal = %+v, want the flipping account with its counts", signal)
	}
}

// TestAlternationFalsePositivesStayUnreported documents the benign cases:
// reading and deciding once in several Arenas is engagement, not farming.
func TestAlternationFalsePositivesStayUnreported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		fact domain.AlternationFact
	}{
		{name: "several changes without any reversal", fact: domain.AlternationFact{Account: mustAttributor(t, signalCasualRaw), Changes: 5, Reversals: 0}},
		{name: "one reversal in a short burst", fact: domain.AlternationFact{Account: mustAttributor(t, signalCasualRaw), Changes: 2, Reversals: 1}},
		{name: "many changes with a single reversal", fact: domain.AlternationFact{Account: mustAttributor(t, signalCasualRaw), Changes: 4, Reversals: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			signals := assess(t, domain.SignalFacts{Alternation: []domain.AlternationFact{test.fact}})
			if len(signals) != 0 {
				t.Fatalf("signals = %+v, want none for %+v", signals, test.fact)
			}
		})
	}
}

// TestAssessmentIsDeterministicAndOrdered proves two things a reviewer
// depends on: the same facts always produce the same signals, and the
// document is ordered by kind and counterpart regardless of the order the
// adapter returned the facts in.
func TestAssessmentIsDeterministicAndOrdered(t *testing.T) {
	t.Parallel()

	firstPeer := mustAttributor(t, signalRingPeerRaw)
	secondPeer := mustAttributor(t, signalFlipperRaw)
	facts := domain.SignalFacts{
		Reciprocity: []domain.ReciprocityFact{
			{Counterpart: secondPeer, Inbound: 2, Outbound: 2},
			{Counterpart: firstPeer, Inbound: 3, Outbound: 3},
		},
		Concentration: []domain.ConcentrationFact{
			{Attributor: mustAttributor(t, signalFarmerRaw), Events: 9},
			{Attributor: mustAttributor(t, signalCasualRaw), Events: 1},
		},
	}

	first := assess(t, facts)
	second := assess(t, facts)
	if len(first) != len(second) {
		t.Fatalf("assessments differ in size: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("signal %d differs between runs: %+v vs %+v", i, first[i], second[i])
		}
	}

	// Three signals are expected here: the concentration, the mutual credit
	// and the rapid alternation of a flipping account would need facts of its
	// own, so only the first two kinds appear.
	if len(first) != 3 {
		t.Fatalf("signals = %+v, want the concentration plus the two reciprocity rings", first)
	}
	if first[0].Kind != domain.SignalConcentration || first[0].Counterpart != mustAttributor(t, signalFarmerRaw) {
		t.Fatalf("first signal = %+v, want the dominant concentration first", first[0])
	}
	// Both rings are reciprocity signals, ordered by counterpart identifier:
	// signalRingPeerRaw sorts before signalFlipperRaw.
	if first[1].Kind != domain.SignalReciprocity || first[1].Counterpart != firstPeer {
		t.Fatalf("second signal = %+v, want the first reciprocity ring by counterpart", first[1])
	}
	if first[2].Kind != domain.SignalReciprocity || first[2].Counterpart != secondPeer {
		t.Fatalf("third signal = %+v, want the other ring ordered by counterpart", first[2])
	}
}

func TestSignalValidationRequiresItsKindCounts(t *testing.T) {
	t.Parallel()

	counterpart := mustAttributor(t, signalRingPeerRaw)
	window := domain.DefaultSignalPolicy().Window

	valid := []domain.Signal{
		{Kind: domain.SignalReciprocity, Counterpart: counterpart, Window: window, MutualEvents: 4},
		{Kind: domain.SignalConcentration, Counterpart: counterpart, Window: window, DominantEvents: 6, ShareBasisPoints: 6_000},
		{Kind: domain.SignalRapidAlternation, Counterpart: counterpart, Window: window, Changes: 4, Reversals: 2},
	}
	for _, signal := range valid {
		if err := signal.Validate(); err != nil {
			t.Fatalf("valid signal rejected: %v (%+v)", err, signal)
		}
	}

	invalid := []domain.Signal{
		{Kind: "score", Counterpart: counterpart, Window: window, MutualEvents: 1},
		{Kind: domain.SignalReciprocity, Window: window, MutualEvents: 1},
		{Kind: domain.SignalReciprocity, Counterpart: counterpart, Window: window},
		{Kind: domain.SignalReciprocity, Counterpart: counterpart, Window: window, MutualEvents: 1, DominantEvents: 2},
		{Kind: domain.SignalConcentration, Counterpart: counterpart, Window: window, DominantEvents: 6},
		{Kind: domain.SignalConcentration, Counterpart: counterpart, Window: window, DominantEvents: 6, ShareBasisPoints: 10_001},
		{Kind: domain.SignalRapidAlternation, Counterpart: counterpart, Window: window, Changes: 4},
		{Kind: domain.SignalRapidAlternation, Counterpart: counterpart, Window: window, Reversals: 2},
		{Kind: domain.SignalRapidAlternation, Counterpart: counterpart, Window: window, Changes: 2, Reversals: 3},
		{Kind: domain.SignalReciprocity, Counterpart: counterpart, Window: 0, MutualEvents: 1},
	}
	for _, signal := range invalid {
		if err := signal.Validate(); !errors.Is(err, domain.ErrInvalidSignal) {
			t.Fatalf("invalid signal accepted: %+v (error = %v)", signal, err)
		}
	}
}

func TestSignalAssessmentValidation(t *testing.T) {
	t.Parallel()

	assessedAt, err := time.Parse(time.RFC3339, signalAssessmentAt)
	if err != nil {
		t.Fatalf("parse instant: %v", err)
	}
	policy := domain.DefaultSignalPolicy()
	counterpart := mustAttributor(t, signalRingPeerRaw)
	signal := domain.Signal{Kind: domain.SignalReciprocity, Counterpart: counterpart, Window: policy.Window, MutualEvents: 4}

	valid := domain.SignalAssessment{
		Subject:       mustSignalSubject(t),
		Signals:       []domain.Signal{signal},
		PolicyVersion: policy.Version,
		Window:        policy.Window,
		AssessedAt:    assessedAt,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid assessment rejected: %v", err)
	}
	if !valid.Has(domain.SignalReciprocity) || valid.Has(domain.SignalConcentration) {
		t.Fatalf("Has() = %v/%v, want the reciprocity signal only", valid.Has(domain.SignalReciprocity), valid.Has(domain.SignalConcentration))
	}

	tests := []struct {
		name   string
		mutate func(*domain.SignalAssessment)
	}{
		{name: "missing subject", mutate: func(a *domain.SignalAssessment) { a.Subject = domain.AuthorID{} }},
		{name: "missing instant", mutate: func(a *domain.SignalAssessment) { a.AssessedAt = time.Time{} }},
		{name: "missing policy revision", mutate: func(a *domain.SignalAssessment) { a.PolicyVersion = "" }},
		{name: "missing window", mutate: func(a *domain.SignalAssessment) { a.Window = 0 }},
		{name: "signal outside the window", mutate: func(a *domain.SignalAssessment) { a.Signals[0].Window = policy.Window + time.Hour }},
		{name: "repeated observation", mutate: func(a *domain.SignalAssessment) { a.Signals = append(a.Signals, signal) }},
		{name: "malformed signal", mutate: func(a *domain.SignalAssessment) {
			a.Signals = []domain.Signal{{Kind: domain.SignalReciprocity, Counterpart: counterpart, Window: policy.Window}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assessment := valid
			assessment.Signals = append([]domain.Signal{}, valid.Signals...)
			test.mutate(&assessment)
			if err := assessment.Validate(); err == nil {
				t.Fatalf("assessment %+v must be invalid", assessment)
			}
		})
	}
}

func TestSignalFactsValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		facts domain.SignalFacts
	}{
		{name: "reciprocity without counterpart", facts: domain.SignalFacts{Reciprocity: []domain.ReciprocityFact{{Inbound: 2, Outbound: 2}}}},
		{name: "negative inbound", facts: domain.SignalFacts{Reciprocity: []domain.ReciprocityFact{{Counterpart: mustAttributor(t, signalRingPeerRaw), Inbound: -1, Outbound: 2}}}},
		{name: "concentration without account", facts: domain.SignalFacts{Concentration: []domain.ConcentrationFact{{Events: 4}}}},
		{name: "negative events", facts: domain.SignalFacts{Concentration: []domain.ConcentrationFact{{Attributor: mustAttributor(t, signalFarmerRaw), Events: -4}}}},
		{name: "alternation without account", facts: domain.SignalFacts{Alternation: []domain.AlternationFact{{Changes: 4, Reversals: 2}}}},
		{name: "more reversals than changes", facts: domain.SignalFacts{Alternation: []domain.AlternationFact{{Account: mustAttributor(t, signalFlipperRaw), Changes: 2, Reversals: 3}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.facts.Validate(); !errors.Is(err, domain.ErrInvalidSignalFacts) {
				t.Fatalf("Validate() error = %v, want ErrInvalidSignalFacts", err)
			}
			if _, err := domain.DefaultSignalPolicy().Assess(mustSignalSubject(t), test.facts); !errors.Is(err, domain.ErrInvalidSignalFacts) {
				t.Fatalf("Assess() error = %v, want ErrInvalidSignalFacts", err)
			}
		})
	}
}
