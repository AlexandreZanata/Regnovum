package domain

import (
	"time"
)

// DeathMark is the digital-death record a blessing answers:
// whose account, which severe sanction stays visible, in which
// book, under which charter version, and when. It erases
// nobody: the person, the required data and third-party rights
// all survive elsewhere.
type DeathMark struct {
	Account  HolderSubject
	Sanction ActID
	Season   SeasonID
	Charter  CharterVersion
	DeadAt   time.Time
}

// Blessing is one royal blessing: the new blessing act reopening
// participation and authentication for the account, under the
// current book and reign, with the sanction still visible and
// the charter it was blessed under. It carries no balance,
// office, contract or patent: any restitution arrives only as an
// explicit transfer elsewhere.
type Blessing struct {
	ID              ActID
	Account         HolderSubject
	Season          SeasonID
	Reign           ReignVersion
	Sanction        ActID
	Charter         CharterVersion
	BlessedAt       time.Time
	CanAuthenticate bool
}

// BlessRequest carries one blessing: the new blessing act, the
// death it answers, the invested snapshot judging it, whether
// the (possibly new) charter was accepted, the wealth sealed in
// the old book, and how much wealth the caller claims in the
// live book. The claim is always zero: revival never mints.
type BlessRequest struct {
	Act             RoyalAct
	Death           DeathMark
	Current         CurrentReign
	CharterAccepted bool
	SealedWealth    int64
	ClaimedWealth   int64
}

// checkBlessAct seals the blessing act and binds its kind: only
// a blessing act blesses, and no blessing act carries origin or
// amount. An economic act offered as blessing mints, any other
// kind blesses nothing.
func checkBlessAct(act RoyalAct) (RoyalAct, error) {
	sealed, err := DefineAct(act)
	if err != nil {
		return RoyalAct{}, err
	}
	if sealed.Kind != ActBlessing {
		if sealed.Kind.Economic() {
			return RoyalAct{}, ErrMintedBlessing
		}
		return RoyalAct{}, ErrInvalidBlessing
	}
	if sealed.Origin != "" || sealed.Amount != 0 {
		return RoyalAct{}, ErrMintedBlessing
	}
	return sealed, nil
}

// checkBlessAuthority binds the act to the invested snapshot:
// the current holder blesses in the current book and reign, in
// an open window, never for itself and never for a third party.
func checkBlessAuthority(act RoyalAct, death DeathMark, current CurrentReign) error {
	if act.Author != current.Holder {
		return ErrNotHolder
	}
	if act.Season != current.Season {
		return ErrSeasonMismatch
	}
	if act.Reign != current.Reign {
		if act.Reign < current.Reign {
			return ErrStaleReign
		}
		return ErrFutureReign
	}
	if !current.Open || !current.contains(act.Effective.UTC()) {
		return ErrSeasonClosed
	}
	if act.Author == HolderSubject(death.Account) {
		return ErrInvalidBlessing
	}
	if act.Target != string(death.Account) {
		return ErrInvalidBlessing
	}
	return nil
}

// checkBlessDeath binds the blessing to one recorded death: whole
// account and sanction, a new act distinct from the sanction,
// death strictly before the decree, and the sanction kept
// visible. Reset alone removes nothing: without a new act this
// check never runs.
func checkBlessDeath(act RoyalAct, death DeathMark) error {
	if _, err := ParseHolderSubject(string(death.Account)); err != nil {
		return ErrInvalidBlessing
	}
	if _, err := ParseActID(string(death.Sanction)); err != nil {
		return ErrInvalidBlessing
	}
	if death.DeadAt.IsZero() {
		return ErrInvalidBlessing
	}
	if act.ID == ActID(death.Sanction) {
		return ErrInvalidBlessing
	}
	if !act.DecreedAt.UTC().After(death.DeadAt.UTC()) {
		return ErrInvalidBlessing
	}
	return nil
}

// checkBlessCharter binds the blessing to an accepted charter:
// when the blessing version differs from the death version, the
// new charter must be accepted first.
func checkBlessCharter(act RoyalAct, death DeathMark, accepted bool) error {
	if act.Charter == death.Charter {
		return nil
	}
	if !accepted {
		return ErrInvalidBlessing
	}
	return nil
}

// checkBlessWealth refuses any revived value: the live claim is
// always zero and sealed wealth stays sealed. A positive claim
// mints, whatever book it names.
func checkBlessWealth(req BlessRequest) error {
	if req.SealedWealth < 0 {
		return ErrInvalidBlessing
	}
	if req.ClaimedWealth != 0 {
		return ErrMintedBlessing
	}
	return nil
}

// GrantBlessing grants one royal blessing reopening participation
// and authentication by a new act. Every refusal arrives before
// any effect: a spent reign, a closed book, a third-party target,
// a self-blessing, an erased sanction, a missing charter
// acceptance and any revived value all stop here. The sanction
// stays visible and third-party rights stand untouched.
func GrantBlessing(req BlessRequest) (Blessing, error) {
	act, err := checkBlessAct(req.Act)
	if err != nil {
		return Blessing{}, err
	}
	if err := req.Current.validShape(); err != nil {
		return Blessing{}, err
	}
	if err := checkBlessDeath(act, req.Death); err != nil {
		return Blessing{}, err
	}
	if err := checkBlessAuthority(act, req.Death, req.Current); err != nil {
		return Blessing{}, err
	}
	if err := checkBlessCharter(act, req.Death, req.CharterAccepted); err != nil {
		return Blessing{}, err
	}
	if err := checkBlessWealth(req); err != nil {
		return Blessing{}, err
	}
	return Blessing{
		ID: act.ID, Account: req.Death.Account,
		Season: req.Current.Season, Reign: req.Current.Reign,
		Sanction: req.Death.Sanction, Charter: act.Charter,
		BlessedAt: act.Effective.UTC(), CanAuthenticate: true,
	}, nil
}
