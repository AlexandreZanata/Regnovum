package domain

import (
	"math"
	"strings"
)

// WealthPolicyVersion identifies a ratified wealth evaluation policy.
type WealthPolicyVersion string

const (
	// WealthPolicyV1 is the ratified v1 wealth policy from TEMPORADAS_SUCESSAO §7.
	// A(b) = sum(liquid milliINK of unconditional economic ownership of beneficiary b).
	// L(b) = registered monetary obligations and encumbrances not yet deducted from A(b).
	// W(b) = max(0, A(b) - L(b)).
	WealthPolicyV1 WealthPolicyVersion = "wealth-v1"

	// MaxSeasonalSupplyMillis bounds any single holding, liability or aggregate:
	// fixed Genesis supply S = 2.100.000.000.000 milliINK.
	MaxSeasonalSupplyMillis int64 = 2100000000000

	// InstitutionalCrownSubject is the canonical subject identifier for the
	// Crown's institutional treasury and sovereign vaults.
	InstitutionalCrownSubject HolderSubject = "coroa-institucional"
)

// ParseWealthPolicyVersion validates that the policy version is ratified.
func ParseWealthPolicyVersion(raw string) (WealthPolicyVersion, error) {
	switch WealthPolicyVersion(raw) {
	case WealthPolicyV1:
		return WealthPolicyV1, nil
	default:
		return "", ErrUnknownWealthPolicy
	}
}

// BeneficiaryKind distinguishes citizen participants from institutional or technical accounts.
type BeneficiaryKind string

const (
	// BeneficiaryKindParticipant is an ordinary citizen participant account eligible for game succession.
	BeneficiaryKindParticipant BeneficiaryKind = "participante"

	// BeneficiaryKindInstitutional is the Crown institutional entity holding sovereign vaults.
	BeneficiaryKindInstitutional BeneficiaryKind = "institucional"

	// BeneficiaryKindTechnical is an operator or administrative technical account that never competes as a citizen.
	BeneficiaryKindTechnical BeneficiaryKind = "tecnica"
)

// ParseBeneficiaryKind validates the closed beneficiary kind vocabulary.
func ParseBeneficiaryKind(raw string) (BeneficiaryKind, error) {
	switch BeneficiaryKind(raw) {
	case BeneficiaryKindParticipant, BeneficiaryKindInstitutional, BeneficiaryKindTechnical:
		return BeneficiaryKind(raw), nil
	default:
		return "", ErrAmbiguousFixture
	}
}

// Beneficiary identifies a wealth beneficiary and their operational classification.
type Beneficiary struct {
	Subject HolderSubject
	Kind    BeneficiaryKind
}

// Valid validates the beneficiary shape and consistency.
func (b Beneficiary) Valid() error {
	subj, err := ParseHolderSubject(string(b.Subject))
	if err != nil {
		return ErrUnmappedBeneficiary
	}
	kind, err := ParseBeneficiaryKind(string(b.Kind))
	if err != nil {
		return err
	}
	// Crown institutional subject can only be institutional:
	if subj == InstitutionalCrownSubject && kind != BeneficiaryKindInstitutional {
		return ErrAmbiguousFixture
	}
	// Participant cannot claim to be the institutional Crown:
	if subj != InstitutionalCrownSubject && kind == BeneficiaryKindInstitutional {
		return ErrAmbiguousFixture
	}
	return nil
}

// AssetKind classifies holdings into eligible liquid assets and recognized excluded categories.
type AssetKind string

const (
	// AssetKindLiquidUnconditional is settled, unconditional milliINK owned by the beneficiary.
	AssetKindLiquidUnconditional AssetKind = "liquid-unconditional"

	// Excluded categories (validly recognized, but excluded from eligible A):
	AssetKindConditionalEscrow AssetKind = "conditional-escrow"
	AssetKindThirdPartyCustody AssetKind = "third-party-custody"
	AssetKindPendingReceivable AssetKind = "pending-receivable"
	AssetKindFiat              AssetKind = "fiat"
	AssetKindHistorical        AssetKind = "historical"
	AssetKindPatent            AssetKind = "patent"
	AssetKindReputation        AssetKind = "reputation"
)

// ParseAssetKind validates the closed asset kind vocabulary.
func ParseAssetKind(raw string) (AssetKind, error) {
	switch AssetKind(raw) {
	case AssetKindLiquidUnconditional,
		AssetKindConditionalEscrow,
		AssetKindThirdPartyCustody,
		AssetKindPendingReceivable,
		AssetKindFiat,
		AssetKindHistorical,
		AssetKindPatent,
		AssetKindReputation:
		return AssetKind(raw), nil
	default:
		return "", ErrUnknownAssetKind
	}
}

// IsEligibleAssetKind reports whether an asset kind contributes to unconditional liquid assets A(b).
func IsEligibleAssetKind(kind AssetKind) bool {
	return kind == AssetKindLiquidUnconditional
}

// AssetHolding represents one custodial asset entry evaluated under a seasonal policy.
type AssetHolding struct {
	CustodyID   string
	Beneficiary HolderSubject
	Season      SeasonID
	Kind        AssetKind
	Amount      int64
	Vault       string
}

// Validate checks the shape, mapping, amounts, and boundaries of one asset holding.
func (h AssetHolding) Validate(evaluatedSeason SeasonID) error {
	if h.CustodyID == "" || strings.TrimSpace(h.CustodyID) != h.CustodyID {
		return ErrAmbiguousFixture
	}
	if h.Beneficiary == "" {
		return ErrUnmappedBeneficiary
	}
	if _, err := ParseHolderSubject(string(h.Beneficiary)); err != nil {
		return ErrUnmappedBeneficiary
	}
	if h.Season == "" || h.Season != evaluatedSeason {
		return ErrSeasonMismatch
	}
	if _, err := ParseSeasonID(string(h.Season)); err != nil {
		return ErrSeasonMismatch
	}
	if _, err := ParseAssetKind(string(h.Kind)); err != nil {
		return err
	}
	if h.Amount < 0 {
		return ErrInvalidWealthAmount
	}
	if h.Amount > MaxSeasonalSupplyMillis {
		return ErrWealthOverflow
	}
	if h.Vault != "" {
		vaults := institutionalVaults()
		if vaults[h.Vault] || h.Vault == freeTreasuryOrigin {
			// Institutional treasury vaults can only belong to InstitutionalCrownSubject:
			if h.Beneficiary != InstitutionalCrownSubject {
				return ErrAmbiguousFixture
			}
		}
	}
	return nil
}

// ObligationKind classifies liabilities.
type ObligationKind string

const (
	// ObligationKindRegisteredLoan is a registered monetary debt / loan against unconditional assets.
	ObligationKindRegisteredLoan ObligationKind = "registered-loan"

	// ObligationKindEncumbrance is a registered encumbrance or lien.
	ObligationKindEncumbrance ObligationKind = "encumbrance"

	// ObligationKindThirdPartyCustody is an obligation to return third-party custody/escrow.
	// As third-party custody is excluded from A, this is excluded from L to avoid double-penalizing personal wealth.
	ObligationKindThirdPartyCustody ObligationKind = "third-party-custody"

	// ObligationKindInternalReserve is an internal earmark of own funds without an external creditor.
	// Internal reserve without an external debt does not reduce institutional wealth.
	ObligationKindInternalReserve ObligationKind = "internal-reserve"
)

// ParseObligationKind validates the closed obligation kind vocabulary.
func ParseObligationKind(raw string) (ObligationKind, error) {
	switch ObligationKind(raw) {
	case ObligationKindRegisteredLoan,
		ObligationKindEncumbrance,
		ObligationKindThirdPartyCustody,
		ObligationKindInternalReserve:
		return ObligationKind(raw), nil
	default:
		return "", ErrUnknownObligationKind
	}
}

// IsDeductibleObligation reports whether an obligation is deducted in L(b).
func IsDeductibleObligation(kind ObligationKind, alreadyDeducted bool) bool {
	if alreadyDeducted {
		return false
	}
	switch kind {
	case ObligationKindRegisteredLoan, ObligationKindEncumbrance:
		return true
	default:
		return false
	}
}

// Obligation represents one liability evaluated against a beneficiary's assets.
type Obligation struct {
	ID              string
	Debtor          HolderSubject
	Season          SeasonID
	Kind            ObligationKind
	Amount          int64
	AlreadyDeducted bool
}

// Validate checks the shape, mapping, amounts, and boundaries of one obligation.
func (o Obligation) Validate(evaluatedSeason SeasonID) error {
	if o.ID == "" || strings.TrimSpace(o.ID) != o.ID {
		return ErrAmbiguousFixture
	}
	if o.Debtor == "" {
		return ErrUnmappedObligation
	}
	if _, err := ParseHolderSubject(string(o.Debtor)); err != nil {
		return ErrUnmappedObligation
	}
	if o.Season == "" || o.Season != evaluatedSeason {
		return ErrSeasonMismatch
	}
	if _, err := ParseSeasonID(string(o.Season)); err != nil {
		return ErrSeasonMismatch
	}
	if _, err := ParseObligationKind(string(o.Kind)); err != nil {
		return err
	}
	if o.Amount < 0 {
		return ErrInvalidWealthAmount
	}
	if o.Amount > MaxSeasonalSupplyMillis {
		return ErrWealthOverflow
	}
	return nil
}

// BeneficialWealth contains the audited seasonal wealth components for one beneficiary:
// A(b): liquid unconditional assets
// L(b): registered obligations not yet deducted
// W(b): max(0, A(b) - L(b))
type BeneficialWealth struct {
	Policy      WealthPolicyVersion
	Season      SeasonID
	Beneficiary HolderSubject
	Kind        BeneficiaryKind
	Assets      int64
	Liabilities int64
	NetWealth   int64
}

// EvaluateBeneficialWealth evaluates wealth for a specific beneficiary under a ratified policy.
// It enforces single deduction, checked integer arithmetic, and strict validation of all entries.
func EvaluateBeneficialWealth(
	policy WealthPolicyVersion,
	season SeasonID,
	beneficiary Beneficiary,
	holdings []AssetHolding,
	obligations []Obligation,
) (BeneficialWealth, error) {
	if _, err := ParseWealthPolicyVersion(string(policy)); err != nil {
		return BeneficialWealth{}, err
	}
	if _, err := ParseSeasonID(string(season)); err != nil {
		return BeneficialWealth{}, err
	}
	if err := beneficiary.Valid(); err != nil {
		return BeneficialWealth{}, err
	}

	seenCustodies := make(map[string]bool)
	var totalAssets int64 = 0

	for _, h := range holdings {
		if err := h.Validate(season); err != nil {
			return BeneficialWealth{}, err
		}
		if h.Beneficiary != beneficiary.Subject {
			continue
		}
		if seenCustodies[h.CustodyID] {
			return BeneficialWealth{}, ErrDuplicateCustody
		}
		seenCustodies[h.CustodyID] = true

		if IsEligibleAssetKind(h.Kind) {
			if h.Amount > math.MaxInt64-totalAssets || totalAssets+h.Amount > MaxSeasonalSupplyMillis {
				return BeneficialWealth{}, ErrWealthOverflow
			}
			totalAssets += h.Amount
		}
	}

	seenObligations := make(map[string]bool)
	var totalLiabilities int64 = 0

	for _, o := range obligations {
		if err := o.Validate(season); err != nil {
			return BeneficialWealth{}, err
		}
		if o.Debtor != beneficiary.Subject {
			continue
		}
		if seenObligations[o.ID] {
			return BeneficialWealth{}, ErrDuplicateObligation
		}
		seenObligations[o.ID] = true

		if IsDeductibleObligation(o.Kind, o.AlreadyDeducted) {
			if o.Amount > math.MaxInt64-totalLiabilities || totalLiabilities+o.Amount > MaxSeasonalSupplyMillis {
				return BeneficialWealth{}, ErrWealthOverflow
			}
			totalLiabilities += o.Amount
		}
	}

	var netWealth int64 = 0
	if totalAssets > totalLiabilities {
		netWealth = totalAssets - totalLiabilities
	}

	return BeneficialWealth{
		Policy:      policy,
		Season:      season,
		Beneficiary: beneficiary.Subject,
		Kind:        beneficiary.Kind,
		Assets:      totalAssets,
		Liabilities: totalLiabilities,
		NetWealth:   netWealth,
	}, nil
}

// EvaluateAllBeneficialWealth evaluates wealth across all specified beneficiaries,
// ensuring every beneficiary receives an audited BeneficialWealth record.
func EvaluateAllBeneficialWealth(
	policy WealthPolicyVersion,
	season SeasonID,
	beneficiaries []Beneficiary,
	holdings []AssetHolding,
	obligations []Obligation,
) (map[HolderSubject]BeneficialWealth, error) {
	if _, err := ParseWealthPolicyVersion(string(policy)); err != nil {
		return nil, err
	}
	if _, err := ParseSeasonID(string(season)); err != nil {
		return nil, err
	}

	// Validate all holdings and obligations up front to ensure fail-closed on any malformed entry:
	for _, h := range holdings {
		if err := h.Validate(season); err != nil {
			return nil, err
		}
	}
	for _, o := range obligations {
		if err := o.Validate(season); err != nil {
			return nil, err
		}
	}

	results := make(map[HolderSubject]BeneficialWealth, len(beneficiaries))
	for _, b := range beneficiaries {
		res, err := EvaluateBeneficialWealth(policy, season, b, holdings, obligations)
		if err != nil {
			return nil, err
		}
		results[b.Subject] = res
	}
	return results, nil
}
