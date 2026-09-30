package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

const signalsWindow = 30 * 24 * time.Hour

// signalHarness wires the restricted assessment over one isolated database.
type signalHarness struct {
	pool      *pgxpool.Pool
	repo      *postgres.Repository
	subject   pgtype.UUID
	arena     pgtype.UUID
	peerArena pgtype.UUID
	now       time.Time
}

func newSignalHarness(t *testing.T) *signalHarness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	h := &signalHarness{
		pool: pool,
		repo: postgres.NewRepository(pool),
		now:  time.Now().UTC(),
	}
	h.subject = mustReputationAccount(t, ctx, q, pool, "signals-subject@arena.example.com", "active", true)
	h.arena = mustReputationArena(t, ctx, pool, h.subject, "signals-subject-arena", "technology", "pt-BR")
	h.peerArena = mustReputationArena(t, ctx, pool, h.subject, "signals-peer-arena", "science", "pt-BR")
	return h
}

// credit records one attribution of argumentID by attributorID through one
// more change of the account in the given Arena, with explicit instants so the
// window filter is exercised on purpose.
func (h *signalHarness) credit(t *testing.T, ctx context.Context, arenaID, attributorID, argumentID, moderatorID pgtype.UUID, version int32, to string, changedAt, createdAt time.Time, status string) {
	t.Helper()
	from := "agree"
	if to == "agree" {
		from = "disagree"
	}
	changeID := mustPersuasionChangeAt(t, ctx, h.pool, arenaID, attributorID, from, to, version, changedAt)
	h.attribute(t, ctx, changeID, attributorID, argumentID, moderatorID, createdAt, status)
}

// attribute inserts one attribution row of an existing change; an invalidated
// attribution always carries its decision record (P11-T04).
func (h *signalHarness) attribute(t *testing.T, ctx context.Context, changeID, attributorID, argumentID, moderatorID pgtype.UUID, createdAt time.Time, status string) {
	t.Helper()
	if status == "valid" {
		if _, err := h.pool.Exec(ctx, `
			INSERT INTO app.persuasion_attributions (position_change_id, attributor_id, argument_id, status, created_at)
			VALUES ($1, $2, $3, 'valid', $4)`, changeID, attributorID, argumentID, createdAt); err != nil {
			t.Fatalf("insert attribution: %v", err)
		}
		return
	}
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO app.persuasion_attributions (
			position_change_id, attributor_id, argument_id, status, created_at,
			invalidated_at, moderation_reason, moderated_by, moderated_at)
		VALUES ($1, $2, $3, 'invalid', $4, $4, 'atribuição fraudulenta', $5, $4)`,
		changeID, attributorID, argumentID, createdAt, moderatorID); err != nil {
		t.Fatalf("insert invalidated attribution: %v", err)
	}
}

// change records one more position change with an explicit direction, used to
// shape a chain without an attribution.
func (h *signalHarness) change(t *testing.T, ctx context.Context, arenaID, accountID pgtype.UUID, from, to string, version int32, changedAt time.Time) pgtype.UUID {
	t.Helper()
	return mustPersuasionChangeAt(t, ctx, h.pool, arenaID, accountID, from, to, version, changedAt)
}

// buildKnownFixture seeds the documented patterns: a mutual ring, a
// concentrated account, a flipper whose reversals straddle the window
// boundary, an ineligible account that is still observed and two closed cases
// (invalidated and out of window) that never count.
func (h *signalHarness) buildKnownFixture(t *testing.T) (ringPeer, farmer, flipper, suspended, closedCase, oldActor pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	q := platformpg.New(h.pool)

	ringPeer = mustReputationAccount(t, ctx, q, h.pool, "signals-ring-peer@arena.example.com", "active", true)
	farmer = mustReputationAccount(t, ctx, q, h.pool, "signals-farmer@arena.example.com", "active", true)
	flipper = mustReputationAccount(t, ctx, q, h.pool, "signals-flipper@arena.example.com", "active", true)
	// Suspended and unverified accounts are exactly the population
	// coordinated manipulation uses: the facts must include them so review is
	// not blind, while the official metrics keep excluding them (BR §7).
	suspended = mustReputationAccount(t, ctx, q, h.pool, "signals-suspended@arena.example.com", "suspended", true)
	closedCase = mustReputationAccount(t, ctx, q, h.pool, "signals-closed@arena.example.com", "active", true)
	oldActor = mustReputationAccount(t, ctx, q, h.pool, "signals-old@arena.example.com", "active", true)

	firstArgument := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.subject, "Argumento do sujeito auditado", "published", h.now.Add(-90*24*time.Hour))
	secondArgument := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.subject, "Segundo argumento do sujeito auditado", "published", h.now.Add(-90*24*time.Hour))
	peerArgument := mustPersuasionArgument(t, ctx, h.pool, h.peerArena, ringPeer, "Argumento do par recíproco", "published", h.now.Add(-90*24*time.Hour))

	inWindow := h.now.Add(-24 * time.Hour)
	outOfWindow := h.now.Add(-90 * 24 * time.Hour)

	// The ring: two events in each direction (THR-PERS-01 shape).
	h.credit(t, ctx, h.arena, ringPeer, firstArgument, h.subject, 2, "disagree", inWindow, inWindow, "valid")
	h.credit(t, ctx, h.arena, ringPeer, secondArgument, h.subject, 3, "disagree", inWindow, inWindow, "valid")
	h.credit(t, ctx, h.peerArena, h.subject, peerArgument, h.subject, 2, "disagree", inWindow, inWindow, "valid")
	h.credit(t, ctx, h.peerArena, h.subject, peerArgument, h.subject, 3, "disagree", inWindow, inWindow, "valid")

	// The concentration: one account holding most of the subject's
	// attributions in the window. Its chain cycles through the three
	// positions, which never repeats a position two steps back: six changes
	// and no reversal.
	cycle := []string{"agree", "disagree", "undecided", "agree", "disagree", "undecided"}
	for i, to := range cycle {
		h.credit(t, ctx, h.arena, farmer, firstArgument, h.subject, int32(2+i), to, inWindow, inWindow, "valid")
	}

	// The flipper: a chain whose first reversal compares against a change
	// from before the window, so the predecessor of an in-window change has
	// to be read outside it.
	windowStart := h.now.Add(-signalsWindow)
	h.change(t, ctx, h.arena, flipper, "agree", "undecided", 2, windowStart.Add(-2*time.Hour))
	h.change(t, ctx, h.arena, flipper, "undecided", "agree", 3, windowStart.Add(time.Hour))
	h.change(t, ctx, h.arena, flipper, "agree", "undecided", 4, windowStart.Add(2*time.Hour))
	h.change(t, ctx, h.arena, flipper, "undecided", "agree", 5, windowStart.Add(3*time.Hour))
	h.credit(t, ctx, h.arena, flipper, firstArgument, h.subject, 6, "disagree", windowStart.Add(4*time.Hour), inWindow, "valid")

	// Ineligible but observed.
	h.credit(t, ctx, h.arena, suspended, firstArgument, h.subject, 2, "disagree", inWindow, inWindow, "valid")
	// A closed case: invalidated attributions never feed a signal.
	h.credit(t, ctx, h.arena, closedCase, firstArgument, h.subject, 2, "disagree", inWindow, inWindow, "invalid")
	// An old case: behaviour outside the window never contributes.
	h.credit(t, ctx, h.arena, oldActor, firstArgument, h.subject, 2, "disagree", outOfWindow, outOfWindow, "valid")

	return ringPeer, farmer, flipper, suspended, closedCase, oldActor
}

func (h *signalHarness) facts(t *testing.T) *domain.SignalFacts {
	t.Helper()
	subjectID, err := domain.ParseAuthorID(uuidText(h.subject))
	if err != nil {
		t.Fatalf("ParseAuthorID: %v", err)
	}
	facts, err := h.repo.LoadAbuseSignalFacts(context.Background(), subjectID, h.now.Add(-signalsWindow), h.now)
	if err != nil {
		t.Fatalf("LoadAbuseSignalFacts: %v", err)
	}
	return facts
}

func concentrationByAccount(facts *domain.SignalFacts) map[string]int64 {
	counts := make(map[string]int64, len(facts.Concentration))
	for _, fact := range facts.Concentration {
		counts[fact.Attributor.String()] = fact.Events
	}
	return counts
}

func alternationByAccount(facts *domain.SignalFacts) map[string]domain.AlternationFact {
	alternations := make(map[string]domain.AlternationFact, len(facts.Alternation))
	for _, fact := range facts.Alternation {
		alternations[fact.Account.String()] = fact
	}
	return alternations
}

// TestAbuseSignalFactsAggregateTheKnownPatterns proves the aggregation the
// policy judges: the ring appears once with both directions counted, every
// account credited inside the window is counted (eligible or not), and closed
// or old cases never appear.
func TestAbuseSignalFactsAggregateTheKnownPatterns(t *testing.T) {
	t.Parallel()
	h := newSignalHarness(t)
	ringPeer, farmer, flipper, suspended, closedCase, oldActor := h.buildKnownFixture(t)

	facts := h.facts(t)

	if len(facts.Reciprocity) != 1 {
		t.Fatalf("reciprocity facts = %+v, want exactly the ring pair", facts.Reciprocity)
	}
	pair := facts.Reciprocity[0]
	if pair.Counterpart.String() != uuidText(ringPeer) {
		t.Fatalf("counterpart = %q, want the ring peer", pair.Counterpart.String())
	}
	if pair.Inbound != 2 || pair.Outbound != 2 {
		t.Fatalf("pair = %+v, want two events in each direction", pair)
	}

	concentration := concentrationByAccount(facts)
	if concentration[uuidText(farmer)] != 6 || concentration[uuidText(ringPeer)] != 2 ||
		concentration[uuidText(flipper)] != 1 || concentration[uuidText(suspended)] != 1 {
		t.Fatalf("concentration = %+v, want the four accounts credited inside the window", concentration)
	}
	if len(concentration) != 4 {
		t.Fatalf("concentration = %+v, want exactly the four in-window accounts", concentration)
	}
	if _, ok := concentration[uuidText(closedCase)]; ok {
		t.Fatal("an invalidated attribution must not feed the counts")
	}
	if _, ok := concentration[uuidText(oldActor)]; ok {
		t.Fatal("an attribution outside the window must not feed the counts")
	}

	alternation := alternationByAccount(facts)
	if got := alternation[uuidText(farmer)]; got.Changes != 6 || got.Reversals != 0 {
		t.Fatalf("farmer alternation = %+v, want six changes and no reversal", got)
	}
	// The first reversal compares against a change from before the window: the
	// chain must be read whole, not only inside the window.
	if got := alternation[uuidText(flipper)]; got.Changes != 4 || got.Reversals != 2 {
		t.Fatalf("flipper alternation = %+v, want four in-window changes and two reversals", got)
	}
	if got := alternation[uuidText(ringPeer)]; got.Changes != 2 || got.Reversals != 0 {
		t.Fatalf("ring peer alternation = %+v, want two changes and no reversal", got)
	}
	if len(alternation) != 4 {
		t.Fatalf("alternation = %+v, want exactly the four in-window accounts", alternation)
	}
}

// TestAbuseSignalAssessmentOverKnownFixture proves the end-to-end restricted
// assessment: the known patterns produce exactly the three documented signals,
// with the counts that crossed the thresholds.
func TestAbuseSignalAssessmentOverKnownFixture(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newSignalHarness(t)
	ringPeer, farmer, flipper, _, _, _ := h.buildKnownFixture(t)

	useCase := application.NewGetAttributionSignalsUseCase(
		h.repo,
		allowSignalModerator{},
		domain.DefaultSignalPolicy(),
		clockseed.NewClock(),
	)

	assessment, err := useCase.Execute(ctx, application.AssessAttributionSignalsQuery{
		ActorAccountID: uuidText(h.subject),
		SubjectID:      uuidText(h.subject),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if err := assessment.Validate(); err != nil {
		t.Fatalf("assessment is incoherent: %v", err)
	}
	if assessment.PolicyVersion != domain.DefaultSignalPolicy().Version {
		t.Fatalf("PolicyVersion = %q, want the injected revision", assessment.PolicyVersion)
	}
	if assessment.Window != signalsWindow {
		t.Fatalf("Window = %v, want the policy window", assessment.Window)
	}
	if len(assessment.Signals) != 3 {
		t.Fatalf("signals = %+v, want the concentration, the alternation and the ring", assessment.Signals)
	}

	// Deterministic order: kind, then counterpart.
	if assessment.Signals[0].Kind != domain.SignalConcentration || assessment.Signals[0].Counterpart.String() != uuidText(farmer) {
		t.Fatalf("first signal = %+v, want the concentration of the farmer", assessment.Signals[0])
	}
	if assessment.Signals[0].DominantEvents != 6 || assessment.Signals[0].ShareBasisPoints != 6_000 {
		t.Fatalf("concentration = %+v, want 6 of 10 events in basis points", assessment.Signals[0])
	}
	if assessment.Signals[1].Kind != domain.SignalRapidAlternation || assessment.Signals[1].Counterpart.String() != uuidText(flipper) {
		t.Fatalf("second signal = %+v, want the rapid alternation of the flipper", assessment.Signals[1])
	}
	if assessment.Signals[1].Changes != 4 || assessment.Signals[1].Reversals != 2 {
		t.Fatalf("alternation = %+v, want the observed changes and reversals", assessment.Signals[1])
	}
	if assessment.Signals[2].Kind != domain.SignalReciprocity || assessment.Signals[2].Counterpart.String() != uuidText(ringPeer) {
		t.Fatalf("third signal = %+v, want the reciprocity ring", assessment.Signals[2])
	}
	if assessment.Signals[2].MutualEvents != 4 {
		t.Fatalf("reciprocity = %+v, want both directions summed", assessment.Signals[2])
	}
}

// TestAbuseSignalsStaySilentOnBenignBehaviour is the integration proof of the
// documented false positives: a single mutual exchange, a young Arena held by
// one participant and steady decisions across an Arena produce no signal, in
// the database as in the domain.
func TestAbuseSignalsStaySilentOnBenignBehaviour(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newSignalHarness(t)
	q := platformpg.New(h.pool)

	benign := mustReputationAccount(t, ctx, q, h.pool, "signals-benign@arena.example.com", "active", true)
	casual := mustReputationAccount(t, ctx, q, h.pool, "signals-casual@arena.example.com", "active", true)
	steady := mustReputationAccount(t, ctx, q, h.pool, "signals-steady@arena.example.com", "active", true)

	benignArena := mustReputationArena(t, ctx, h.pool, benign, "signals-benign-arena", "philosophy", "pt-BR")
	benignArgument := mustPersuasionArgument(t, ctx, h.pool, benignArena, benign, "Argumento do autor benigno", "published", h.now.Add(-90*24*time.Hour))
	casualArgument := mustPersuasionArgument(t, ctx, h.pool, benignArena, casual, "Argumento do par casual", "published", h.now.Add(-90*24*time.Hour))

	inWindow := h.now.Add(-24 * time.Hour)

	// One exchange in each direction: ordinary social behaviour.
	h.credit(t, ctx, benignArena, casual, benignArgument, benign, 2, "disagree", inWindow, inWindow, "valid")
	h.credit(t, ctx, benignArena, benign, casualArgument, benign, 2, "disagree", inWindow, inWindow, "valid")

	// A steady reader deciding across the Arena: five changes cycling through
	// the three positions, which never repeats a position two steps back.
	steadyChain := []struct {
		from, to string
	}{
		{"undecided", "agree"},
		{"agree", "disagree"},
		{"disagree", "undecided"},
		{"undecided", "agree"},
		{"agree", "disagree"},
	}
	firstSteady := h.change(t, ctx, benignArena, steady, steadyChain[0].from, steadyChain[0].to, 2, h.now.Add(-3*time.Hour))
	h.attribute(t, ctx, firstSteady, steady, benignArgument, benign, inWindow, "valid")
	for i, step := range steadyChain[1:] {
		h.change(t, ctx, benignArena, steady, step.from, step.to, int32(3+i), h.now.Add(-time.Duration(4-i)*30*time.Minute))
	}

	useCase := application.NewGetAttributionSignalsUseCase(
		h.repo,
		allowSignalModerator{},
		domain.DefaultSignalPolicy(),
		clockseed.NewClock(),
	)
	subjectID, err := domain.ParseAuthorID(uuidText(benign))
	if err != nil {
		t.Fatalf("ParseAuthorID: %v", err)
	}
	assessment, err := useCase.Execute(ctx, application.AssessAttributionSignalsQuery{
		ActorAccountID: uuidText(benign),
		SubjectID:      uuidText(benign),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(assessment.Signals) != 0 {
		t.Fatalf("signals = %+v, want none for benign behaviour", assessment.Signals)
	}

	// The silence is not vacuous: the facts really were observed, and each one
	// stayed below its threshold.
	facts, err := h.repo.LoadAbuseSignalFacts(ctx, subjectID, h.now.Add(-signalsWindow), h.now)
	if err != nil {
		t.Fatalf("LoadAbuseSignalFacts: %v", err)
	}
	if len(facts.Reciprocity) != 1 || facts.Reciprocity[0].Inbound != 1 || facts.Reciprocity[0].Outbound != 1 {
		t.Fatalf("reciprocity = %+v, want the single mutual exchange observed", facts.Reciprocity)
	}
	if got := concentrationByAccount(facts); got[uuidText(casual)] != 1 || got[uuidText(steady)] != 1 {
		t.Fatalf("concentration = %+v, want one event per account", got)
	}
	if got := alternationByAccount(facts); got[uuidText(steady)].Changes != 5 || got[uuidText(steady)].Reversals != 0 {
		t.Fatalf("alternation = %+v, want five changes without a single reversal", got)
	}
}

func TestAbuseSignalFactsHandleMissingAndMalformedSubjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newSignalHarness(t)

	t.Run("unknown subject has empty facts", func(t *testing.T) {
		unknown, err := domain.ParseAuthorID("018f6b2a-0000-7000-8000-0000000000aa")
		if err != nil {
			t.Fatalf("ParseAuthorID: %v", err)
		}
		facts, err := h.repo.LoadAbuseSignalFacts(ctx, unknown, h.now.Add(-signalsWindow), h.now)
		if err != nil {
			t.Fatalf("a well-formed but unknown subject must be an empty assessment, got %v", err)
		}
		if len(facts.Reciprocity) != 0 || len(facts.Concentration) != 0 || len(facts.Alternation) != 0 {
			t.Fatalf("facts = %+v, want empty slices", facts)
		}
		if err := facts.Validate(); err != nil {
			t.Fatalf("empty facts must be coherent: %v", err)
		}
	})

	t.Run("malformed identifier", func(t *testing.T) {
		malformed, err := domain.ParseAuthorID("not-a-uuid")
		if err != nil {
			t.Fatalf("ParseAuthorID: %v", err)
		}
		if _, err := h.repo.LoadAbuseSignalFacts(ctx, malformed, h.now.Add(-signalsWindow), h.now); !errors.Is(err, application.ErrInvalidAuthorID) {
			t.Fatalf("error = %v, want ErrInvalidAuthorID", err)
		}
	})

	t.Run("non-positive window is refused", func(t *testing.T) {
		subjectID, err := domain.ParseAuthorID(uuidText(h.subject))
		if err != nil {
			t.Fatalf("ParseAuthorID: %v", err)
		}
		if _, err := h.repo.LoadAbuseSignalFacts(ctx, subjectID, h.now, h.now); !errors.Is(err, domain.ErrInvalidSignalFacts) {
			t.Fatalf("error = %v, want ErrInvalidSignalFacts", err)
		}
	})
}

// allowSignalModerator authorizes every actor: the moderation role store is
// exercised by its own adapter, not here.
type allowSignalModerator struct{}

func (allowSignalModerator) EnsureModerator(context.Context, domain.ModeratorID) error { return nil }
