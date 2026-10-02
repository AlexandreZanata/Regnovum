package domain

import (
	"strings"
	"time"
)

// KingdomClass is the closed vocabulary of the constitutional and
// economic data classes the retention policy governs: sealed
// evidence, reports, contracts, escrow, manifest and cutoff,
// rankings, reigns and economic records. The values are the same
// strings storage uses, so a recorded run and the policy that
// produced it can never drift apart.
type KingdomClass string

const (
	// KingdomClassSealedProof covers sealed evidence envelopes: the
	// sealed bytes stay while identifiers are stripped on expiry,
	// so defense and audit keep the proof without the person.
	KingdomClassSealedProof KingdomClass = "prova-selada"
	// KingdomClassReport covers severe-case reports: identifiers
	// are stripped on expiry while the fact stays for audit.
	KingdomClassReport KingdomClass = "denuncia"
	// KingdomClassContract covers bound contracts: kept as
	// evidence under obligation, never purged.
	KingdomClassContract KingdomClass = "contrato"
	// KingdomClassEscrow covers escrow custody: kept as financial
	// evidence, never purged, so the ledger never breaks.
	KingdomClassEscrow KingdomClass = "escrow"
	// KingdomClassManifestCutoff covers season manifests and
	// cutoff snapshots: kept as the evidence of what each book
	// sealed.
	KingdomClassManifestCutoff KingdomClass = "manifesto-corte"
	// KingdomClassRanking covers wealth and competition rankings:
	// the public alias stays while the exact balance is dropped
	// on expiry, under distinct permissions.
	KingdomClassRanking KingdomClass = "ranking"
	// KingdomClassReigns covers reign sequences: kept as
	// institutional history, never purged.
	KingdomClassReigns KingdomClass = "reinados"
	// KingdomClassEconomicRecords covers economic records and
	// receipts: kept as ledger evidence, never purged.
	KingdomClassEconomicRecords KingdomClass = "registros-economicos"
)

// ParseKingdomClass resolves a stored kingdom class value.
// Matching is exact: no trimming, no case folding.
func ParseKingdomClass(value string) (KingdomClass, bool) {
	switch KingdomClass(value) {
	case KingdomClassSealedProof, KingdomClassReport, KingdomClassContract,
		KingdomClassEscrow, KingdomClassManifestCutoff, KingdomClassRanking,
		KingdomClassReigns, KingdomClassEconomicRecords:
		return KingdomClass(value), true
	default:
		return "", false
	}
}

// KingdomSchedule is one row of the kingdom retention policy:
// which class is governed, what disposal applies, for which
// purpose and legal basis, after how long per jurisdiction, and
// why. Deadlines travel per jurisdiction: there is no universal
// horizon, and a jurisdiction without a window refuses instead
// of borrowing another one's.
type KingdomSchedule struct {
	// Class is the governed data class.
	Class KingdomClass
	// Action is what disposal does with the class.
	Action RetentionAction
	// Purpose is the declared processing purpose.
	Purpose string
	// Basis is the legal basis of the processing.
	Basis string
	// Windows maps one jurisdiction code (for example "BR") to
	// the period after a record became terminal before disposal
	// applies. Empty for indefinite classes.
	Windows map[string]time.Duration
	// Indefinite marks a class retained without a disposal
	// horizon under obligation: it never yields a cutoff.
	Indefinite bool
	// ReasonCode is the stable justification published with the policy.
	ReasonCode string
}

// IsValid verifies that a kingdom schedule is coherent: a known
// class and action, a stated purpose, basis and reason, and
// either per-jurisdiction positive windows or an indefinite
// retain. An incoherent schedule is refused instead of being
// enforced approximately.
func (s KingdomSchedule) IsValid() error {
	if _, known := ParseKingdomClass(string(s.Class)); !known {
		return ErrUnknownKingdomClass
	}
	switch s.Action {
	case RetentionActionPurge, RetentionActionAnonymize, RetentionActionRetain:
	default:
		return ErrInvalidRetentionSchedule
	}
	if strings.TrimSpace(s.Purpose) == "" || strings.TrimSpace(s.Basis) == "" {
		return ErrInvalidRetentionSchedule
	}
	if strings.TrimSpace(s.ReasonCode) == "" {
		return ErrInvalidRetentionSchedule
	}
	if s.Indefinite {
		if s.Action != RetentionActionRetain || len(s.Windows) != 0 {
			return ErrInvalidRetentionSchedule
		}
		return nil
	}
	if s.Action == RetentionActionRetain {
		return ErrInvalidRetentionSchedule
	}
	if len(s.Windows) == 0 {
		return ErrInvalidRetentionSchedule
	}
	for jurisdiction, window := range s.Windows {
		if strings.TrimSpace(jurisdiction) == "" || window <= 0 {
			return ErrInvalidRetentionSchedule
		}
	}
	return nil
}

// WindowFor answers the disposal horizon of one jurisdiction. A
// jurisdiction without a window refuses: no fallback borrows
// another jurisdiction's deadline by inference.
func (s KingdomSchedule) WindowFor(jurisdiction string) (time.Duration, bool) {
	if s.Indefinite {
		return 0, false
	}
	window, known := s.Windows[jurisdiction]
	if !known || window <= 0 {
		return 0, false
	}
	return window, true
}

// HasCutoff reports whether the class has a disposal horizon in
// the jurisdiction. A retained class and an unlisted
// jurisdiction have none.
func (s KingdomSchedule) HasCutoff(jurisdiction string) bool {
	_, known := s.WindowFor(jurisdiction)
	return known
}

// DueAt is the instant at which disposal becomes due for a
// record that became terminal at terminalAt in the
// jurisdiction. Without a horizon there is no due instant.
func (s KingdomSchedule) DueAt(terminalAt time.Time, jurisdiction string) (time.Time, bool) {
	window, known := s.WindowFor(jurisdiction)
	if !known {
		return time.Time{}, false
	}
	return terminalAt.UTC().Add(window), true
}

// Due reports whether a record terminal at terminalAt is due at
// now in the jurisdiction. The boundary instant is due.
func (s KingdomSchedule) Due(terminalAt, now time.Time, jurisdiction string) bool {
	dueAt, hasDue := s.DueAt(terminalAt, jurisdiction)
	if !hasDue {
		return false
	}
	return !now.UTC().Before(dueAt)
}

// AuthorizeCollection gates collection: a known class, a stated
// basis and a horizon (or an indefinite obligation) in the
// jurisdiction. Data without class, deadline or basis is
// blocked here, before it exists.
func AuthorizeCollection(class KingdomClass, basis, jurisdiction string, schedules []KingdomSchedule) error {
	var schedule *KingdomSchedule
	for i := range schedules {
		if schedules[i].Class == class {
			schedule = &schedules[i]
			break
		}
	}
	if schedule == nil {
		return ErrUnknownKingdomClass
	}
	if strings.TrimSpace(basis) == "" {
		return ErrMissingCollectionBasis
	}
	if schedule.Indefinite {
		return nil
	}
	if _, known := schedule.WindowFor(jurisdiction); !known {
		return ErrNoJurisdictionWindow
	}
	return nil
}

// KingdomHold is an active legal or contractual hold over one
// kingdom class: the whole class when AccountID is empty,
// otherwise the records of one account. Holds are never
// erased; only their release changes them.
type KingdomHold struct {
	// ID is the stable hold identifier.
	ID string
	// Class is the held data class.
	Class KingdomClass
	// AccountID is the held account, empty for a whole-class hold.
	AccountID string
	// ReasonCode is the stable justification of the hold.
	ReasonCode string
	// PlacedAt is when the hold entered force.
	PlacedAt time.Time
}

// IsClassWide reports whether the hold covers the whole class.
func (h KingdomHold) IsClassWide() bool {
	return strings.TrimSpace(h.AccountID) == ""
}

// Validate verifies that a stored hold is coherent: a known
// class, a non-empty bounded reason code and a placement
// instant.
func (h KingdomHold) Validate() error {
	if _, known := ParseKingdomClass(string(h.Class)); !known {
		return ErrUnknownKingdomClass
	}
	reason := strings.TrimSpace(h.ReasonCode)
	if reason == "" || len(reason) > 100 {
		return ErrInvalidRetentionHold
	}
	if h.PlacedAt.IsZero() {
		return ErrInvalidRetentionHold
	}
	return nil
}

// HeldBy reports whether the holds preserve the record: the
// whole class or exactly its account. A hold preserves only
// what it names, nothing more.
func HeldBy(holds []KingdomHold, class KingdomClass, account string) bool {
	for _, hold := range holds {
		if hold.Class != class {
			continue
		}
		if hold.IsClassWide() || hold.AccountID == account {
			return true
		}
	}
	return false
}

// SettleRequest carries one disposal decision: the schedule,
// the jurisdiction, when the record became terminal, the
// judgment instant, whose record it is, whether the record is
// archived, and the holds in force.
type SettleRequest struct {
	Schedule     KingdomSchedule
	Jurisdiction string
	TerminalAt   time.Time
	Now          time.Time
	Account      string
	Archived     bool
	Holds        []KingdomHold
}

// SettleOutcome is what disposal decided for one record.
type SettleOutcome string

const (
	// SettleWait keeps the record: not due yet.
	SettleWait SettleOutcome = "aguardar"
	// SettleHeld keeps the record under hold: only what the hold
	// names is preserved, the rest proceeds.
	SettleHeld SettleOutcome = "preservado"
	// SettlePurged removes the expired record.
	SettlePurged SettleOutcome = "apagado"
	// SettleAnonymized strips the expired record's identifiers and
	// keeps the fact: archive never blocks it.
	SettleAnonymized SettleOutcome = "anonimizado"
	// SettleRetained keeps the record under obligation: the ledger
	// never breaks for expiry.
	SettleRetained SettleOutcome = "mantido"
)

// String returns the stored outcome value.
func (o SettleOutcome) String() string { return string(o) }

// Settle decides one disposal: held records stay, unheld records
// follow their horizon, retained classes stay under obligation.
// Archive changes nothing: an archived ranking is still
// anonymized on expiry, and economic records never yield.
func Settle(req SettleRequest) (SettleOutcome, error) {
	if err := req.Schedule.IsValid(); err != nil {
		return "", err
	}
	if req.TerminalAt.IsZero() || req.Now.IsZero() {
		return "", ErrInvalidRetentionSchedule
	}
	if HeldBy(req.Holds, req.Schedule.Class, req.Account) {
		return SettleHeld, nil
	}
	if req.Schedule.Indefinite {
		return SettleRetained, nil
	}
	if !req.Schedule.Due(req.TerminalAt, req.Now, req.Jurisdiction) {
		return SettleWait, nil
	}
	switch req.Schedule.Action {
	case RetentionActionPurge:
		return SettlePurged, nil
	case RetentionActionAnonymize:
		return SettleAnonymized, nil
	default:
		return "", ErrInvalidRetentionSchedule
	}
}

// RankingDisclosure is one ranking exposition: the public alias
// anyone may read and the exact balance only the entitled
// reader may read, under distinct permissions.
type RankingDisclosure struct {
	Alias        string
	ExactBalance *int64
}

// AnonymizeRanking drops the exact balance of one expired
// ranking and keeps the public alias: archive and expiry take
// the precise value, never the place in history.
func AnonymizeRanking(disclosure RankingDisclosure) RankingDisclosure {
	return RankingDisclosure{Alias: disclosure.Alias}
}
