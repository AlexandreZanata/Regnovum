package domain

import (
	"time"
)

// DeadStep is the closed vocabulary of one dead-account effect:
// data first, then third-party custody, refunds, obligations,
// authorized principal with the holder right, and residual only
// with an explicit basis. Later phases execute the adapter once;
// this plan only authorizes, never moves value by itself.
type DeadStep string

const (
	// DeadPreserve exports the required data before any value moves.
	DeadPreserve DeadStep = "dados-preservados"
	// DeadThird returns third-party custody to its owner.
	DeadThird DeadStep = "custodia-terceiros"
	// DeadRefunds settles refunds without erasing fiat debt.
	DeadRefunds DeadStep = "reembolsos"
	// DeadObligations settles recorded obligations.
	DeadObligations DeadStep = "obrigacoes"
	// DeadPrincipal settles the authorized principal with the
	// holder right: to the heir when one applies, else to the
	// account. Bought never confiscates by default.
	DeadPrincipal DeadStep = "principal-direito"
	// DeadResidual moves the remainder only with contrato/lei
	// basis after notice and lapsed appeal.
	DeadResidual DeadStep = "residual"
)

// ParseDeadStep validates a step token. Matching is exact.
func ParseDeadStep(raw string) (DeadStep, error) {
	switch DeadStep(raw) {
	case DeadPreserve, DeadThird, DeadRefunds,
		DeadObligations, DeadPrincipal, DeadResidual:
		return DeadStep(raw), nil
	default:
		return "", ErrInvalidDeadSettlement
	}
}

// String returns the stored step value.
func (s DeadStep) String() string { return string(s) }

// DeadID identifies one dead-account settlement.
type DeadID string

// ParseDeadID validates one settlement token.
func ParseDeadID(raw string) (DeadID, error) {
	if err := checkToken(raw); err != nil {
		return "", ErrInvalidDeadSettlement
	}
	return DeadID(raw), nil
}

// String returns the stored settlement value.
func (d DeadID) String() string { return string(d) }

// checkDeadToken validates one holder, heir, destination or
// export token against the shared shape.
func checkDeadToken(raw string) error {
	if err := checkToken(raw); err != nil {
		return ErrInvalidDeadSettlement
	}
	return nil
}

// isTreasuryDest reports whether destination names the Treasury
// by any accepted spelling: third-party custody, refunds,
// obligations and principal never flow there by default.
func isTreasuryDest(raw string) bool {
	switch raw {
	case "tesouro", "tesouro-livre", "treasury", "treasury/main":
		return true
	default:
		return false
	}
}

// DeadEffect is one recorded settlement effect: the idempotency
// key, the ordered step, how much in minor units, to whom, in
// which season book and when. A replay with the identical key
// returns the settlement unchanged; a divergent payload under a
// recorded key conflicts instead of paying twice.
type DeadEffect struct {
	Key         string
	Step        DeadStep
	AmountMilli int64
	Destination string
	Season      SeasonKey
	At          time.Time
}

// DeadSettlement is one dead-account liquidation: whose account
// in which season book, the initial total with the remainder
// still held, the applicable heir, the fiat debt and defense
// that closure never erases, which steps already ran, and the
// recorded effects. It mints nothing: effects plus remainder
// always equal the total.
type DeadSettlement struct {
	ID          DeadID
	Account     string
	Season      SeasonKey
	TotalMilli  int64
	RemainMilli int64
	Heir        string
	FiatDebt    bool
	DefenseOpen bool
	Sealed      bool
	Preserved   bool
	ThirdDone   bool
	RefundsDone bool
	DebtsDone   bool
	PrincipalOn bool
	Effects     []DeadEffect
}

// OpenDeadRequest carries one settlement opening: the new
// identity, whose account in which book, the initial total, the
// applicable heir, the fiat debt and defense that survive, and
// whether the book already seals.
type OpenDeadRequest struct {
	ID         DeadID
	Account    string
	Season     SeasonKey
	TotalMilli int64
	Heir       string
	FiatDebt   bool
	Defense    bool
	Sealed     bool
}

// PreserveRequest carries one data preservation: the
// idempotency key, the export reference, the source book and
// when it happened.
type PreserveRequest struct {
	Key       string
	ExportRef string
	Season    SeasonKey
	At        time.Time
}

// EffectRequest carries one economic effect: the idempotency
// key, the ordered step, how much to whom in which book and
// when, with the residual basis, notice and appeal deadline
// when the step is residual.
type EffectRequest struct {
	Key         string
	StepRaw     string
	AmountMilli int64
	Destination string
	Season      SeasonKey
	Basis       string
	NoticeAt    time.Time
	AppealDueAt time.Time
	At          time.Time
}

// checkOpenDead validates the opening shape: whole identity and
// holder, a named book, a non-negative total, an heir distinct
// from the holder and never the Treasury, and a live book.
func checkOpenDead(req OpenDeadRequest) error {
	if _, err := ParseDeadID(string(req.ID)); err != nil {
		return err
	}
	if err := checkDeadToken(req.Account); err != nil {
		return err
	}
	if _, err := ParseSeasonKey(string(req.Season)); err != nil {
		return err
	}
	if req.TotalMilli < 0 {
		return ErrInvalidDeadSettlement
	}
	if req.Heir != "" {
		if err := checkDeadToken(req.Heir); err != nil {
			return err
		}
		if req.Heir == req.Account || isTreasuryDest(req.Heir) {
			return ErrInvalidDeadSettlement
		}
	}
	if req.Sealed {
		return ErrBookSealed
	}
	return nil
}

// OpenDeadSettlement opens one dead-account liquidation in its
// source book. Third-party custody stays third-party, bought
// stays with the holder or the heir, fiat debt and defense stay
// open, and the remainder starts whole.
func OpenDeadSettlement(req OpenDeadRequest) (DeadSettlement, error) {
	if err := checkOpenDead(req); err != nil {
		return DeadSettlement{}, err
	}
	return DeadSettlement{
		ID: req.ID, Account: req.Account, Season: req.Season,
		TotalMilli: req.TotalMilli, RemainMilli: req.TotalMilli,
		Heir: req.Heir, FiatDebt: req.FiatDebt, DefenseOpen: req.Defense,
	}, nil
}

// findDeadEffect answers whether key already settled.
func findDeadEffect(set DeadSettlement, key string) (DeadEffect, bool) {
	for _, recorded := range set.Effects {
		if recorded.Key == key {
			return recorded, true
		}
	}
	return DeadEffect{}, false
}

// checkDeadSeason binds every effect to its source book.
func checkDeadSeason(set DeadSettlement, season SeasonKey) error {
	if string(season) == "" {
		return ErrMissingSeason
	}
	if season != set.Season {
		return ErrCrossSeason
	}
	if set.Sealed {
		return ErrBookSealed
	}
	return nil
}

// PreserveDeadData records the required data export before any
// value moves. An identical replay returns the settlement
// unchanged; a divergent export under the recorded key
// conflicts.
func PreserveDeadData(set DeadSettlement, req PreserveRequest) (DeadSettlement, error) {
	if err := checkDeadSeason(set, req.Season); err != nil {
		return DeadSettlement{}, err
	}
	if err := checkDeadToken(req.Key); err != nil {
		return DeadSettlement{}, err
	}
	if err := checkDeadToken(req.ExportRef); err != nil {
		return DeadSettlement{}, err
	}
	if req.At.IsZero() {
		return DeadSettlement{}, ErrInvalidDeadSettlement
	}
	if recorded, found := findDeadEffect(set, req.Key); found {
		if recorded.Step == DeadPreserve && recorded.Destination == req.ExportRef {
			return set, nil
		}
		return DeadSettlement{}, ErrDeadConflict
	}
	if set.Preserved {
		return DeadSettlement{}, ErrDeadOutOfOrder
	}
	set.Preserved = true
	set.Effects = append(set.Effects, DeadEffect{
		Key: req.Key, Step: DeadPreserve,
		Destination: req.ExportRef, Season: req.Season, At: req.At.UTC(),
	})
	return set, nil
}

// checkEffectReplay answers whether key replays the same effect.
func checkEffectReplay(set DeadSettlement, req EffectRequest, step DeadStep) (DeadSettlement, bool, error) {
	recorded, found := findDeadEffect(set, req.Key)
	if !found {
		return set, false, nil
	}
	if recorded.Step == step && recorded.AmountMilli == req.AmountMilli &&
		recorded.Destination == req.Destination && recorded.Season == req.Season {
		return set, true, nil
	}
	return DeadSettlement{}, false, ErrDeadConflict
}

// checkEffectOrder enforces the single ordered sequence.
func checkEffectOrder(set DeadSettlement, step DeadStep) error {
	if !set.Preserved {
		return ErrDeadOutOfOrder
	}
	switch step {
	case DeadThird:
		if set.ThirdDone {
			return ErrDeadOutOfOrder
		}
	case DeadRefunds:
		if !set.ThirdDone || set.RefundsDone {
			return ErrDeadOutOfOrder
		}
	case DeadObligations:
		if !set.RefundsDone || set.DebtsDone {
			return ErrDeadOutOfOrder
		}
	case DeadPrincipal:
		if !set.DebtsDone || set.PrincipalOn {
			return ErrDeadOutOfOrder
		}
	case DeadResidual:
		if !set.PrincipalOn {
			return ErrDeadOutOfOrder
		}
		for _, recorded := range set.Effects {
			if recorded.Step == DeadResidual {
				return ErrDeadOutOfOrder
			}
		}
	default:
		return ErrInvalidDeadSettlement
	}
	return nil
}

// checkEffectFunds refuses non-positive amounts and amounts
// beyond the remainder. The remainder never goes negative and
// no step mints: the caller subtracts only what remains.
func checkEffectFunds(set DeadSettlement, amount int64) error {
	if amount <= 0 {
		return ErrInvalidDeadSettlement
	}
	if amount > set.RemainMilli {
		return ErrInsufficientMilliInk
	}
	return nil
}

// checkResidualBasis binds the remainder to contrato/lei with
// notice and a lapsed appeal. Anything else blocks: the
// remainder waits instead of moving by inference.
func checkResidualBasis(req EffectRequest) error {
	if req.Basis != "contrato" && req.Basis != "lei" {
		return ErrDeadResidualBlocked
	}
	if req.NoticeAt.IsZero() || req.AppealDueAt.IsZero() || req.At.IsZero() {
		return ErrDeadResidualBlocked
	}
	if !req.AppealDueAt.UTC().After(req.NoticeAt.UTC()) {
		return ErrDeadResidualBlocked
	}
	if req.At.UTC().Before(req.AppealDueAt.UTC()) {
		return ErrDeadResidualBlocked
	}
	return nil
}

// checkThirdDestination binds third-party custody to its
// stranger owner: neither the account, nor the heir, nor the
// Treasury ever receives it here.
func checkThirdDestination(set DeadSettlement, dest string) error {
	if dest == set.Account || dest == set.Heir {
		return ErrInvalidDeadSettlement
	}
	if isTreasuryDest(dest) {
		return ErrInvalidDeadSettlement
	}
	return nil
}

// checkCommonDestination binds refunds and obligations away
// from the Treasury and away from the dead account itself.
// Fiat debt survives elsewhere by construction: no step
// clears it.
func checkCommonDestination(set DeadSettlement, dest string) error {
	if dest == set.Account {
		return ErrInvalidDeadSettlement
	}
	if isTreasuryDest(dest) {
		return ErrInvalidDeadSettlement
	}
	return nil
}

// checkPrincipalDestination binds the authorized principal to
// the applicable heir, else to the account. Bought never
// confiscates by default: the Treasury never receives it here.
func checkPrincipalDestination(set DeadSettlement, dest string) error {
	if isTreasuryDest(dest) {
		return ErrInvalidDeadSettlement
	}
	if set.Heir != "" {
		if dest != set.Heir {
			return ErrInvalidDeadSettlement
		}
		return nil
	}
	if dest != set.Account {
		return ErrInvalidDeadSettlement
	}
	return nil
}

// checkResidualDestination binds the remainder to an explicit
// recipient with contrato/lei basis, notice and lapsed appeal.
func checkResidualDestination(set DeadSettlement, req EffectRequest) error {
	if req.Destination == set.Account {
		return ErrInvalidDeadSettlement
	}
	return checkResidualBasis(req)
}

// checkEffectDestination binds each step to its rightful
// recipient: third to a stranger never the Treasury, refunds
// and obligations never to the Treasury, principal to the heir
// when one applies else to the account, residual only with its
// basis. Bought never confiscates by default.
func checkEffectDestination(set DeadSettlement, req EffectRequest, step DeadStep) error {
	if err := checkDeadToken(req.Destination); err != nil {
		return err
	}
	switch step {
	case DeadThird:
		return checkThirdDestination(set, req.Destination)
	case DeadRefunds, DeadObligations:
		return checkCommonDestination(set, req.Destination)
	case DeadPrincipal:
		return checkPrincipalDestination(set, req.Destination)
	case DeadResidual:
		return checkResidualDestination(set, req)
	default:
		return ErrInvalidDeadSettlement
	}
}

// markDeadStep records the completed step.
func markDeadStep(set *DeadSettlement, step DeadStep) {
	switch step {
	case DeadThird:
		set.ThirdDone = true
	case DeadRefunds:
		set.RefundsDone = true
	case DeadObligations:
		set.DebtsDone = true
	case DeadPrincipal:
		set.PrincipalOn = true
	}
}

// ApplyDeadEffect applies one ordered economic effect with
// crash-safe idempotency. Every refusal arrives before any
// movement: wrong book, out-of-order step, divergent replay,
// treasury by default, heir confusion, blocked residual and
// insufficient remainder all stop here with the remainder
// untouched.
func ApplyDeadEffect(set DeadSettlement, req EffectRequest) (DeadSettlement, error) {
	step, err := ParseDeadStep(req.StepRaw)
	if err != nil {
		return DeadSettlement{}, err
	}
	if step == DeadPreserve {
		return DeadSettlement{}, ErrDeadOutOfOrder
	}
	if err := checkDeadSeason(set, req.Season); err != nil {
		return DeadSettlement{}, err
	}
	if err := checkDeadToken(req.Key); err != nil {
		return DeadSettlement{}, err
	}
	if req.At.IsZero() {
		return DeadSettlement{}, ErrInvalidDeadSettlement
	}
	if replay, done, err := checkEffectReplay(set, req, step); err != nil || done {
		return replay, err
	}
	if err := checkEffectOrder(set, step); err != nil {
		return DeadSettlement{}, err
	}
	if err := checkEffectFunds(set, req.AmountMilli); err != nil {
		return DeadSettlement{}, err
	}
	if err := checkEffectDestination(set, req, step); err != nil {
		return DeadSettlement{}, err
	}
	set.RemainMilli -= req.AmountMilli
	markDeadStep(&set, step)
	set.Effects = append(set.Effects, DeadEffect{
		Key: req.Key, Step: step, AmountMilli: req.AmountMilli,
		Destination: req.Destination, Season: req.Season, At: req.At.UTC(),
	})
	return set, nil
}

// ConservedTotal answers the oracle invariant: recorded
// effects plus the remainder always equal the initial total.
func ConservedTotal(set DeadSettlement) int64 {
	var moved int64
	for _, effect := range set.Effects {
		moved += effect.AmountMilli
	}
	return moved + set.RemainMilli
}
