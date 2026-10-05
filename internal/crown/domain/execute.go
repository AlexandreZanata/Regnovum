package domain

import "time"

// freeTreasuryOrigin is the only origin that may fund a crown
// economic execution: the free treasury in the crown vocabulary.
// Escrow, contract, title, principal alheio and confiscation stay
// where they are; third-party compensation leaves this same vault,
// never by mint, burn or balance edit.
const freeTreasuryOrigin = "tesouro-livre"

// institutionalVaults is the closed vocabulary of institutional
// pockets a decree may reorganize without granting personal
// wealth: moving between them never credits a person. Personal
// grants carry a beneficiary and no vault; vault moves carry a
// vault and no beneficiary; carrying both or neither refuses.
func institutionalVaults() map[string]bool {
	return map[string]bool{
		"reserva-soberana":  true,
		"estoque-comercial": true,
		"caixa-operacional": true,
	}
}

// CustodyView is the conserved custody snapshot one execution
// judges against: which origin funds it, who benefits personally
// or which institutional pocket receives it, what the free
// treasury holds available, whether the book is frozen, and
// whether the benefit carries an unresolved conflict with its
// independent review reference. Balances arrive as data: this
// layer reads them, never the storage.
type CustodyView struct {
	Origin            string
	Beneficiary       HolderSubject
	Vault             string
	Available         int64
	Frozen            bool
	ConflictPending   bool
	IndependentReview string
}

// ExecutionOrder is one planned conserved movement: the act it
// executes, in which book and reign, over which payload digest,
// from the free treasury to one personal beneficiary or one
// institutional vault, for the exact decreed amount. Personal is
// true for a personal grant and false for an institutional pocket
// move: a pocket move never becomes personal wealth. The order
// moves nothing itself: the economic port appends it.
type ExecutionOrder struct {
	Act         ActID
	Season      SeasonID
	Reign       ReignVersion
	Digest      string
	Origin      string
	Beneficiary HolderSubject
	Vault       string
	Amount      int64
	Personal    bool
	ExpiresAt   time.Time
}

// ExecutionReceipt is the traceable proof of one appended
// movement: it cites the same act, book, reign and digest as the
// clearance, so the ledger and the Royal Book name one fact.
// Destination holds the beneficiary for a personal grant and
// "cofre/<vault>" for an institutional move.
type ExecutionReceipt struct {
	Act         ActID
	Season      SeasonID
	Reign       ReignVersion
	Digest      string
	Origin      string
	Destination string
	Amount      int64
	Personal    bool
}

// destinationOf names the receipt destination: the beneficiary
// for a personal grant, the vault pocket otherwise.
func destinationOf(order ExecutionOrder) string {
	if order.Personal {
		return string(order.Beneficiary)
	}
	return "cofre/" + order.Vault
}

// checkExecutionClearance binds one clearance to one sealed act:
// same act, book and reign, same digest, still live at the
// effect instant. A stale reign or a foreign payload stops here.
func checkExecutionClearance(sealed RoyalAct, digest string, clearance Clearance, now time.Time) error {
	if _, err := ParseActID(string(clearance.Act)); err != nil {
		return err
	}
	if _, err := ParseSeasonID(string(clearance.Season)); err != nil {
		return err
	}
	if _, err := ParseReignVersion(int(clearance.Reign)); err != nil {
		return err
	}
	if !isDigest(clearance.Digest) {
		return ErrInvalidAuthority
	}
	if clearance.ExpiresAt.IsZero() {
		return ErrInvalidAuthority
	}
	if clearance.Act != sealed.ID || clearance.Season != sealed.Season || clearance.Reign != sealed.Reign {
		return ErrTamperedAct
	}
	if clearance.Digest != digest {
		return ErrTamperedAct
	}
	if !now.UTC().Before(clearance.ExpiresAt.UTC()) {
		return ErrCheckExpired
	}
	return nil
}

// checkExecutionCustody judges custody, destination and conflict:
// frozen books move nothing, only the free treasury funds,
// beneficiary and vault never mix, the author never pays itself,
// conflicted benefits need an independent review, and the amount
// never exceeds availability. No mint, no burn, no balance edit.
func checkExecutionCustody(sealed RoyalAct, custody CustodyView) (bool, error) {
	if custody.Frozen {
		return false, ErrExecutionFrozen
	}
	if sealed.Origin != freeTreasuryOrigin || custody.Origin != freeTreasuryOrigin || custody.Origin != sealed.Origin {
		return false, ErrForbiddenOrigin
	}
	hasBeneficiary := custody.Beneficiary != ""
	hasVault := custody.Vault != ""
	if hasBeneficiary == hasVault {
		return false, ErrIncompleteAct
	}
	if hasVault {
		if !institutionalVaults()[custody.Vault] {
			return false, ErrUnknownOrigin
		}
		if custody.Vault == custody.Origin {
			return false, ErrInvalidAuthority
		}
	} else {
		if _, err := ParseHolderSubject(string(custody.Beneficiary)); err != nil {
			return false, err
		}
		if custody.Beneficiary == sealed.Author {
			return false, ErrSelfGrant
		}
	}
	if custody.ConflictPending {
		review, err := parseToken(custody.IndependentReview)
		if err != nil {
			return false, ErrConflictedBenefit
		}
		if review == string(sealed.ID) {
			return false, ErrConflictedBenefit
		}
	}
	if custody.Available < 0 {
		return false, ErrInvalidAuthority
	}
	if sealed.Amount > custody.Available {
		return false, ErrInsufficientTreasury
	}
	return hasBeneficiary, nil
}

// PlanDelegatedExecution plans one conserved movement of an economic
// decree against its clearance, an optional delegation chain, the invested
// reign and the custody snapshot at the effect instant.
func PlanDelegatedExecution(act RoyalAct, clearance Clearance, delegation *Delegation, current CurrentReign, custody CustodyView, now time.Time) (ExecutionOrder, error) {
	if now.IsZero() {
		return ExecutionOrder{}, ErrInvalidAuthority
	}
	sealed, err := DefineAct(act)
	if err != nil {
		return ExecutionOrder{}, err
	}
	if !sealed.Kind.Economic() {
		return ExecutionOrder{}, ErrNonMonetaryAct
	}
	digest := actDigestOf(sealed)
	moment := now.UTC()
	if err := checkExecutionClearance(sealed, digest, clearance, moment); err != nil {
		return ExecutionOrder{}, err
	}
	fence := EffectFence{
		Season:               sealed.Season,
		Reign:                sealed.Reign,
		Competence:           sealed.Competence,
		Author:               sealed.Author,
		Delegation:           delegation,
		CurrentReign:         current,
		EconomicBacklogClean: true,
		Now:                  moment,
	}
	if err := ValidateEffectFence(fence); err != nil {
		return ExecutionOrder{}, err
	}
	if moment.Before(sealed.Effective.UTC()) {
		return ExecutionOrder{}, ErrSeasonClosed
	}
	if !sealed.EndsAt.IsZero() && !moment.Before(sealed.EndsAt.UTC()) {
		return ExecutionOrder{}, ErrSeasonClosed
	}
	personal, err := checkExecutionCustody(sealed, custody)
	if err != nil {
		return ExecutionOrder{}, err
	}
	order := ExecutionOrder{
		Act: sealed.ID, Season: sealed.Season, Reign: sealed.Reign,
		Digest: digest, Origin: sealed.Origin,
		Beneficiary: custody.Beneficiary, Vault: custody.Vault,
		Amount: sealed.Amount, Personal: personal,
		ExpiresAt: clearance.ExpiresAt.UTC(),
	}
	return order, nil
}

// PlanExecution plans one conserved movement of an economic
// decree against its clearance, the invested reign and the
// custody snapshot at the effect instant. Every refusal arrives
// before any effect: non-monetary kinds, foreign payloads,
// stale reigns, ex-holders, closed or frozen books, forbidden
// origins, self-grants, conflicted benefits and insufficiency
// all stop here. Institutional pocket moves return Personal
// false: reclassifying a pocket never grants personal wealth.
func PlanExecution(act RoyalAct, clearance Clearance, current CurrentReign, custody CustodyView, now time.Time) (ExecutionOrder, error) {
	return PlanDelegatedExecution(act, clearance, nil, current, custody, now)
}

// SealReceipt cites the traceable proof of one appended order:
// the same act, book, reign and digest the clearance allowed,
// so the ledger and the Royal Book name one fact. It moves
// nothing: the economic port appends first, this function only
// names the proof.
func SealReceipt(order ExecutionOrder) (ExecutionReceipt, error) {
	if _, err := ParseActID(string(order.Act)); err != nil {
		return ExecutionReceipt{}, err
	}
	if _, err := ParseSeasonID(string(order.Season)); err != nil {
		return ExecutionReceipt{}, err
	}
	if _, err := ParseReignVersion(int(order.Reign)); err != nil {
		return ExecutionReceipt{}, err
	}
	if !isDigest(order.Digest) {
		return ExecutionReceipt{}, ErrInvalidAuthority
	}
	if order.Origin != freeTreasuryOrigin {
		return ExecutionReceipt{}, ErrForbiddenOrigin
	}
	if order.Amount <= 0 {
		return ExecutionReceipt{}, ErrIncompleteAct
	}
	if order.Personal {
		if order.Vault != "" {
			return ExecutionReceipt{}, ErrIncompleteAct
		}
		if _, err := ParseHolderSubject(string(order.Beneficiary)); err != nil {
			return ExecutionReceipt{}, err
		}
	} else {
		if order.Beneficiary != "" || !institutionalVaults()[order.Vault] {
			return ExecutionReceipt{}, ErrUnknownOrigin
		}
	}
	return ExecutionReceipt{
		Act: order.Act, Season: order.Season, Reign: order.Reign,
		Digest: order.Digest, Origin: order.Origin,
		Destination: destinationOf(order),
		Amount:      order.Amount, Personal: order.Personal,
	}, nil
}
