package domain

import (
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// CloseRunState names the lifecycle of one closing barrier run:
// closing drains, sealed is terminal history, failed parks for
// resume. A blocked seal is an outcome, not a state: the run stays
// closing until obligations resolve or a takeover retries.
type CloseRunState string

const (
	// CloseStateClosing drains admitted work under the current
	// generation and lease.
	CloseStateClosing CloseRunState = "closing"
	// CloseStateSealed is terminal: the book sealed with no open
	// obligations and a conserved snapshot.
	CloseStateSealed CloseRunState = "sealed"
	// CloseStateFailed parks a run that hit a worker error: the
	// cursor is kept, the failure is stored, and a resumed worker
	// replays from the cursor.
	CloseStateFailed CloseRunState = "failed"
)

// ParseCloseRunState validates a run state token.
func ParseCloseRunState(raw string) (CloseRunState, error) {
	state := CloseRunState(raw)
	switch state {
	case CloseStateClosing, CloseStateSealed, CloseStateFailed:
		return state, nil
	default:
		return "", ErrInvalidSeason
	}
}

// String returns the stored run state value.
func (s CloseRunState) String() string { return string(s) }

// maxCloseDetailRunes bounds stored failure details: enough for a
// machine code and a short cause, never a dump.
const maxCloseDetailRunes = 1024

// ParseCloseOwner validates one opaque worker identity: exact
// match, no control characters, bounded length.
func ParseCloseOwner(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidSeason
	}
	if utf8.RuneCountInString(raw) > maxSeasonRunes {
		return "", ErrInvalidSeason
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidSeason
		}
	}
	return raw, nil
}

// CloseDrain is the fence token every closing write carries: the
// book, the generation and the owner. It travels as values so the
// economy and commerce drain paths can verify it without importing
// this module back: scalars, never a cycle.
type CloseDrain struct {
	Season     string
	Generation int64
	Owner      string
}

// Valid checks the token shape: a named book, a generation from 1
// and an opaque owner. Lease and currency are judged against the
// stored run on the database clock, never here.
func (d CloseDrain) Valid() error {
	if strings.TrimSpace(d.Season) == "" {
		return ErrInvalidSeason
	}
	if utf8.RuneCountInString(d.Season) > maxSeasonRunes {
		return ErrInvalidSeason
	}
	if d.Generation < 1 {
		return ErrInvalidSeason
	}
	if _, err := ParseCloseOwner(d.Owner); err != nil {
		return err
	}
	return nil
}

// CloseRun is one fenced closing barrier: the book, its monotonic
// generation, the lease, the cutoff, the checkpoint cursors, the
// progress hints and the conservation snapshot. Cursors order
// batches for determinism only: commit order is never inferred from
// keys or timestamps, the barrier orders admitted work by commit
// visibility in the database.
type CloseRun struct {
	Season             string
	Generation         int64
	LeaseOwner         string
	LeasedUntil        time.Time
	State              CloseRunState
	CutoffAt           time.Time
	LastHoldKey        string
	LastEscrowKey      string
	Drained            int64
	Blocked            int64
	SnapshotMilli      int64
	SnapshotLegs       int64
	SnapshotIntentions int64
}

// BeginRequest carries one barrier opening: the book with its end,
// the database now, the worker, the lease length and the
// conservation snapshot read in the same transaction.
type BeginRequest struct {
	Season             string
	Owner              string
	EndsAt             time.Time
	Now                time.Time
	LeaseTTL           time.Duration
	SnapshotMilli      int64
	SnapshotLegs       int64
	SnapshotIntentions int64
}

// BeginCloseRun opens the barrier in generation 1 once the book end
// is reached on the database clock. Earlier barriers refuse: the
// cutoff is observed, never anticipated.
func BeginCloseRun(req BeginRequest) (CloseRun, error) {
	season, err := parseSeasonToken(req.Season)
	if err != nil {
		return CloseRun{}, err
	}
	owner, err := ParseCloseOwner(req.Owner)
	if err != nil {
		return CloseRun{}, err
	}
	if req.EndsAt.IsZero() || req.Now.IsZero() {
		return CloseRun{}, ErrInvalidSeason
	}
	if req.Now.UTC().Before(req.EndsAt.UTC()) {
		return CloseRun{}, ErrCloseNotDue
	}
	if req.LeaseTTL <= 0 {
		return CloseRun{}, ErrInvalidSeason
	}
	if req.SnapshotMilli < 0 || req.SnapshotLegs < 0 || req.SnapshotIntentions < 0 {
		return CloseRun{}, ErrInvalidSeason
	}
	return CloseRun{
		Season: season, Generation: 1, LeaseOwner: owner,
		LeasedUntil: req.Now.UTC().Add(req.LeaseTTL),
		State:       CloseStateClosing, CutoffAt: req.Now.UTC(),
		SnapshotMilli: req.SnapshotMilli, SnapshotLegs: req.SnapshotLegs,
		SnapshotIntentions: req.SnapshotIntentions,
	}, nil
}

// TakeoverRun moves a lapsed run to the next generation under a new
// owner. A held lease refuses: the book changes hands only past
// leased_until. Sealed runs never reopen, and the generation never
// wraps.
func TakeoverRun(run CloseRun, owner string, now time.Time, ttl time.Duration) (CloseRun, error) {
	next, err := ParseCloseOwner(owner)
	if err != nil {
		return CloseRun{}, err
	}
	if now.IsZero() || ttl <= 0 {
		return CloseRun{}, ErrInvalidSeason
	}
	if run.State == CloseStateSealed {
		return CloseRun{}, ErrInvalidTransition
	}
	if now.UTC().Before(run.LeasedUntil.UTC()) {
		return CloseRun{}, ErrLeaseHeld
	}
	if run.Generation == math.MaxInt64 {
		return CloseRun{}, ErrInvalidSeason
	}
	run.Generation++
	run.LeaseOwner = next
	run.LeasedUntil = now.UTC().Add(ttl)
	run.State = CloseStateClosing
	return run, nil
}

// CheckFence judges one drain write against the stored run: same
// book, same generation, same owner. Older generations stand down,
// foreign owners wait for the lease: every stale or held write
// refuses before any row is read.
func CheckFence(run CloseRun, drain CloseDrain) error {
	if err := drain.Valid(); err != nil {
		return err
	}
	if drain.Season != run.Season {
		return ErrInvalidSeason
	}
	if drain.Generation != run.Generation {
		return ErrStaleGeneration
	}
	if drain.Owner != run.LeaseOwner {
		return ErrLeaseHeld
	}
	if run.State == CloseStateSealed {
		return ErrInvalidTransition
	}
	return nil
}

// AdvanceCursor moves the checkpoint past one processed batch. Keys
// move forward only and deltas never go negative: the cursor is a
// progress hint while Seal decides by live scan, so replays stay
// harmless and crashes resume from the stored cursor.
func AdvanceCursor(run CloseRun, holdKey, escrowKey string, drainedDelta, blockedDelta int64) (CloseRun, error) {
	if drainedDelta < 0 || blockedDelta < 0 {
		return CloseRun{}, ErrInvalidSeason
	}
	if holdKey != "" && holdKey < run.LastHoldKey {
		return CloseRun{}, ErrInvalidSeason
	}
	if escrowKey != "" && escrowKey < run.LastEscrowKey {
		return CloseRun{}, ErrInvalidSeason
	}
	if holdKey != "" {
		run.LastHoldKey = holdKey
	}
	if escrowKey != "" {
		run.LastEscrowKey = escrowKey
	}
	if run.Drained > math.MaxInt64-drainedDelta || run.Blocked > math.MaxInt64-blockedDelta {
		return CloseRun{}, ErrInvalidSeason
	}
	run.Drained += drainedDelta
	run.Blocked += blockedDelta
	return run, nil
}

// CloseFailure carries one parked worker error: the machine code and
// a short cause. The cursor is kept so resume replays from it.
type CloseFailure struct {
	Code   string
	Detail string
}

// RecordFailure parks a run that hit a worker error: sealed runs
// never fail, the cursor is kept, and resume replays from it.
func RecordFailure(run CloseRun, failure CloseFailure) (CloseRun, error) {
	if run.State == CloseStateSealed {
		return CloseRun{}, ErrInvalidTransition
	}
	if strings.TrimSpace(failure.Code) == "" || utf8.RuneCountInString(failure.Code) > maxSeasonRunes {
		return CloseRun{}, ErrInvalidSeason
	}
	if utf8.RuneCountInString(failure.Detail) > maxCloseDetailRunes {
		return CloseRun{}, ErrInvalidSeason
	}
	for _, r := range failure.Code + failure.Detail {
		if unicode.IsControl(r) {
			return CloseRun{}, ErrInvalidSeason
		}
	}
	run.State = CloseStateFailed
	return run, nil
}

// SealSnapshot carries one conservation reading: the book Genesis
// S, the journal leg count and the settled intention count. The seal
// compares the barrier reading with the current one.
type SealSnapshot struct {
	Milli      int64
	Legs       int64
	Intentions int64
}

// CanSeal judges the seal: no open escrow, no active hold, the book
// Genesis S byte-equal and legs and intentions never rewound.
// Growth past the snapshot is allowed: admitted in-flight work
// completes once in the old book. Anything else blocks with custody
// preserved: no confiscation, no successor.
func CanSeal(openEscrows, activeHolds int64, snapshot, current SealSnapshot) error {
	if openEscrows < 0 || activeHolds < 0 {
		return ErrInvalidSeason
	}
	if openEscrows > 0 || activeHolds > 0 {
		return ErrCloseBlocked
	}
	if snapshot.Milli != current.Milli {
		return ErrInvalidSeason
	}
	if current.Legs < snapshot.Legs || current.Intentions < snapshot.Intentions {
		return ErrInvalidSeason
	}
	return nil
}

// Mirrored escrow statuses for the closing classification. The
// seasons domain mirrors the commerce vocabulary as strings instead
// of importing it: domains stay disjoint, and the closer reads
// status names from its own scan.
const (
	// CloseEscrowFunded locks buyer funds with no delivery accepted.
	CloseEscrowFunded = "funded"
	// CloseEscrowAccepted records buyer acceptance before any payout.
	CloseEscrowAccepted = "accepted"
	// CloseEscrowReleased paid the provider: terminal, no new effect.
	CloseEscrowReleased = "released"
	// CloseEscrowRefunded returned the principal: terminal.
	CloseEscrowRefunded = "refunded"
	// CloseEscrowExpired marked lapse without moving funds.
	CloseEscrowExpired = "expired"
	// CloseEscrowResolved settled an expiry by decision: terminal.
	CloseEscrowResolved = "resolved"
)

// CloseEscrowOutcome is the drain decision for one escrow: no new
// effect with the receipt preserved, or blocked with custody
// preserved.
type CloseEscrowOutcome string

const (
	// CloseNoEffect preserves a terminal receipt: released, refunded
	// and resolved escrows never move again.
	CloseNoEffect CloseEscrowOutcome = "no-effect"
	// CloseEscrowBlocked preserves custody of a non-terminal escrow:
	// litigation may be pending, terms may miss the accepted clause
	// and no wired disputes source can prove otherwise. Absence of
	// data never proves absence of litigation, so the seal waits.
	CloseEscrowBlocked CloseEscrowOutcome = "blocked"
)

// String returns the stored outcome value.
func (o CloseEscrowOutcome) String() string { return string(o) }

// ClassifyCloseEscrow maps one escrow status to its drain decision.
// Terminal escrows keep their receipt with no new effect. Every
// other status blocks: funded and accepted need a proven absence of
// litigation no wired source can provide, and expired needs a
// competent final persisted resolution the closer is not. Unknown
// statuses block fail-closed.
func ClassifyCloseEscrow(status string) CloseEscrowOutcome {
	switch status {
	case CloseEscrowReleased, CloseEscrowRefunded, CloseEscrowResolved:
		return CloseNoEffect
	default:
		return CloseEscrowBlocked
	}
}
