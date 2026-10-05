package domain

import (
	"errors"
	"math"
	"testing"
)

const (
	testSeason SeasonID = "temporada-1"
)

func testBeneficiaryAna() Beneficiary {
	return Beneficiary{
		Subject: "conta-ana",
		Kind:    BeneficiaryKindParticipant,
	}
}

func testBeneficiaryCrown() Beneficiary {
	return Beneficiary{
		Subject: InstitutionalCrownSubject,
		Kind:    BeneficiaryKindInstitutional,
	}
}

func TestWealthZero(t *testing.T) {
	ana := testBeneficiaryAna()

	// Empty holdings and obligations
	res, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Assets != 0 || res.Liabilities != 0 || res.NetWealth != 0 {
		t.Fatalf("expected all zeros, got %+v", res)
	}

	// Zero-valued holdings and obligations
	holdings := []AssetHolding{
		{
			CustodyID:   "cus-zero",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      0,
		},
	}
	obligations := []Obligation{
		{
			ID:     "obl-zero",
			Debtor: ana.Subject,
			Season: testSeason,
			Kind:   ObligationKindRegisteredLoan,
			Amount: 0,
		},
	}
	resZero, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, holdings, obligations)
	if err != nil {
		t.Fatalf("unexpected error on zero values: %v", err)
	}
	if resZero.Assets != 0 || resZero.Liabilities != 0 || resZero.NetWealth != 0 {
		t.Fatalf("expected all zeros, got %+v", resZero)
	}
}

func TestWealthNegativeClampedToZero(t *testing.T) {
	ana := testBeneficiaryAna()

	holdings := []AssetHolding{
		{
			CustodyID:   "cus-1",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      100,
		},
	}
	obligations := []Obligation{
		{
			ID:     "obl-1",
			Debtor: ana.Subject,
			Season: testSeason,
			Kind:   ObligationKindRegisteredLoan,
			Amount: 250,
		},
	}

	res, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, holdings, obligations)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Assets != 100 {
		t.Fatalf("assets = %d, want 100", res.Assets)
	}
	if res.Liabilities != 250 {
		t.Fatalf("liabilities = %d, want 250", res.Liabilities)
	}
	if res.NetWealth != 0 {
		t.Fatalf("net wealth = %d, want 0 (W negativo clamped to zero)", res.NetWealth)
	}
}

func TestWealthLimitsAndOverflow(t *testing.T) {
	ana := testBeneficiaryAna()

	// Negative asset amount
	badAsset := []AssetHolding{
		{
			CustodyID:   "cus-neg",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      -1,
		},
	}
	if _, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, badAsset, nil); !errors.Is(err, ErrInvalidWealthAmount) {
		t.Fatalf("negative asset = %v, want ErrInvalidWealthAmount", err)
	}

	// Negative obligation amount
	badObl := []Obligation{
		{
			ID:     "obl-neg",
			Debtor: ana.Subject,
			Season: testSeason,
			Kind:   ObligationKindRegisteredLoan,
			Amount: -10,
		},
	}
	if _, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, nil, badObl); !errors.Is(err, ErrInvalidWealthAmount) {
		t.Fatalf("negative obligation = %v, want ErrInvalidWealthAmount", err)
	}

	// Single amount exceeding max seasonal supply S
	overAsset := []AssetHolding{
		{
			CustodyID:   "cus-over",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      MaxSeasonalSupplyMillis + 1,
		},
	}
	if _, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, overAsset, nil); !errors.Is(err, ErrWealthOverflow) {
		t.Fatalf("asset over S = %v, want ErrWealthOverflow", err)
	}

	overObl := []Obligation{
		{
			ID:     "obl-over",
			Debtor: ana.Subject,
			Season: testSeason,
			Kind:   ObligationKindRegisteredLoan,
			Amount: MaxSeasonalSupplyMillis + 1,
		},
	}
	if _, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, nil, overObl); !errors.Is(err, ErrWealthOverflow) {
		t.Fatalf("obligation over S = %v, want ErrWealthOverflow", err)
	}

	// Sum of assets exceeding supply S
	twoAssetsOver := []AssetHolding{
		{
			CustodyID:   "cus-1",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      MaxSeasonalSupplyMillis - 100,
		},
		{
			CustodyID:   "cus-2",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      200,
		},
	}
	if _, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, twoAssetsOver, nil); !errors.Is(err, ErrWealthOverflow) {
		t.Fatalf("sum of assets over S = %v, want ErrWealthOverflow", err)
	}

	// Integer overflow checked near MaxInt64
	twoAssetsMaxInt := []AssetHolding{
		{
			CustodyID:   "cus-max1",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      math.MaxInt64 - 10,
		},
		{
			CustodyID:   "cus-max2",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      20,
		},
	}
	// Note: individual amount math.MaxInt64 - 10 already exceeds MaxSeasonalSupplyMillis, so it fails with ErrWealthOverflow
	if _, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, twoAssetsMaxInt, nil); !errors.Is(err, ErrWealthOverflow) {
		t.Fatalf("overflow near MaxInt64 = %v, want ErrWealthOverflow", err)
	}
}

func TestWealthThirdPartyEscrowExcluded(t *testing.T) {
	ana := testBeneficiaryAna()

	holdings := []AssetHolding{
		{
			CustodyID:   "cus-personal",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      1000,
		},
		{
			CustodyID:   "cus-third-party",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindThirdPartyCustody,
			Amount:      5000,
		},
		{
			CustodyID:   "cus-escrow",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindConditionalEscrow,
			Amount:      2500,
		},
	}
	obligations := []Obligation{
		{
			ID:     "obl-return-escrow",
			Debtor: ana.Subject,
			Season: testSeason,
			Kind:   ObligationKindThirdPartyCustody,
			Amount: 5000,
		},
	}

	res, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, holdings, obligations)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Third party custody and conditional escrow are excluded from A:
	if res.Assets != 1000 {
		t.Fatalf("assets = %d, want 1000 (third-party custody and escrow excluded)", res.Assets)
	}
	// Third party custody obligation is excluded from L so personal assets are not penalized:
	if res.Liabilities != 0 {
		t.Fatalf("liabilities = %d, want 0 (third-party custody obligation excluded from L)", res.Liabilities)
	}
	if res.NetWealth != 1000 {
		t.Fatalf("net wealth = %d, want 1000", res.NetWealth)
	}
}

func TestWealthRegisteredLoanNeutralized(t *testing.T) {
	ana := testBeneficiaryAna()

	holdings := []AssetHolding{
		{
			CustodyID:   "cus-loan-receipt",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      1000,
		},
	}
	obligations := []Obligation{
		{
			ID:              "obl-loan-principal",
			Debtor:          ana.Subject,
			Season:          testSeason,
			Kind:            ObligationKindRegisteredLoan,
			Amount:          1000,
			AlreadyDeducted: false,
		},
	}

	res, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, holdings, obligations)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Assets != 1000 {
		t.Fatalf("assets = %d, want 1000", res.Assets)
	}
	if res.Liabilities != 1000 {
		t.Fatalf("liabilities = %d, want 1000", res.Liabilities)
	}
	if res.NetWealth != 0 {
		t.Fatalf("net wealth = %d, want 0 (registered loan neutralized liquid cash)", res.NetWealth)
	}
}

func TestWealthMultiplePersonalPocketsAggregated(t *testing.T) {
	ana := testBeneficiaryAna()

	holdings := []AssetHolding{
		{
			CustodyID:   "wallet-alpha",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      600,
		},
		{
			CustodyID:   "wallet-beta",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      400,
		},
	}

	res, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, holdings, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Assets != 1000 {
		t.Fatalf("assets = %d, want 1000 (aggregated personal wallets)", res.Assets)
	}
	if res.Liabilities != 0 {
		t.Fatalf("liabilities = %d, want 0", res.Liabilities)
	}
	if res.NetWealth != 1000 {
		t.Fatalf("net wealth = %d, want 1000", res.NetWealth)
	}
}

func TestWealthInstitutionalReserveReclassified(t *testing.T) {
	crown := testBeneficiaryCrown()

	// Initial distribution of institutional vaults
	state1Holdings := []AssetHolding{
		{
			CustodyID:   "cus-reserva",
			Beneficiary: crown.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      1000000,
			Vault:       "reserva-soberana",
		},
		{
			CustodyID:   "cus-estoque",
			Beneficiary: crown.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      500000,
			Vault:       "estoque-comercial",
		},
		{
			CustodyID:   "cus-caixa",
			Beneficiary: crown.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      200000,
			Vault:       "caixa-operacional",
		},
		{
			CustodyID:   "cus-livre",
			Beneficiary: crown.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      300000,
			Vault:       "tesouro-livre",
		},
	}
	// Internal reserve without external obligation does not reduce institutional wealth
	obligations := []Obligation{
		{
			ID:     "earmark-reserva",
			Debtor: crown.Subject,
			Season: testSeason,
			Kind:   ObligationKindInternalReserve,
			Amount: 500000,
		},
	}

	res1, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, crown, state1Holdings, obligations)
	if err != nil {
		t.Fatalf("state1 error: %v", err)
	}
	if res1.Assets != 2000000 {
		t.Fatalf("state1 assets = %d, want 2000000", res1.Assets)
	}
	if res1.Liabilities != 0 {
		t.Fatalf("state1 liabilities = %d, want 0 (internal reserve is not an external debt)", res1.Liabilities)
	}
	if res1.NetWealth != 2000000 {
		t.Fatalf("state1 net wealth C = %d, want 2000000", res1.NetWealth)
	}

	// State 2: Reclassified (500k moved from reserva-soberana to caixa-operacional)
	state2Holdings := []AssetHolding{
		{
			CustodyID:   "cus-reserva",
			Beneficiary: crown.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      500000, // decreased
			Vault:       "reserva-soberana",
		},
		{
			CustodyID:   "cus-estoque",
			Beneficiary: crown.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      500000,
			Vault:       "estoque-comercial",
		},
		{
			CustodyID:   "cus-caixa",
			Beneficiary: crown.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      700000, // increased by 500k
			Vault:       "caixa-operacional",
		},
		{
			CustodyID:   "cus-livre",
			Beneficiary: crown.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      300000,
			Vault:       "tesouro-livre",
		},
	}

	res2, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, crown, state2Holdings, obligations)
	if err != nil {
		t.Fatalf("state2 error: %v", err)
	}
	if res2.NetWealth != res1.NetWealth {
		t.Fatalf("reclassification changed institutional wealth: %d != %d", res2.NetWealth, res1.NetWealth)
	}

	// Incumbent occupant / King test: Treasury does not belong to the King
	king := Beneficiary{
		Subject: "rainha-1",
		Kind:    BeneficiaryKindParticipant,
	}
	kingHoldings := []AssetHolding{
		{
			CustodyID:   "king-personal-wallet",
			Beneficiary: king.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      50000,
		},
	}
	// Combined pool containing both Crown vaults and King personal wallet:
	combinedHoldings := append(state1Holdings, kingHoldings...)
	resKing, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, king, combinedHoldings, nil)
	if err != nil {
		t.Fatalf("king evaluation error: %v", err)
	}
	if resKing.Assets != 50000 {
		t.Fatalf("king assets = %d, want 50000: sovereign treasury does not belong to the occupant", resKing.Assets)
	}
	if resKing.NetWealth != 50000 {
		t.Fatalf("king net wealth P = %d, want 50000 (not 2050000)", resKing.NetWealth)
	}
}

func TestWealthAlreadyDeductedObligationNotCountedTwice(t *testing.T) {
	ana := testBeneficiaryAna()

	holdings := []AssetHolding{
		{
			CustodyID:   "cus-1",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        AssetKindLiquidUnconditional,
			Amount:      1000,
		},
	}
	obligations := []Obligation{
		{
			ID:              "obl-already-netted",
			Debtor:          ana.Subject,
			Season:          testSeason,
			Kind:            ObligationKindRegisteredLoan,
			Amount:          300,
			AlreadyDeducted: true, // already deducted at source
		},
		{
			ID:              "obl-pending-deduction",
			Debtor:          ana.Subject,
			Season:          testSeason,
			Kind:            ObligationKindRegisteredLoan,
			Amount:          200,
			AlreadyDeducted: false,
		},
	}

	res, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, holdings, obligations)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Assets != 1000 {
		t.Fatalf("assets = %d, want 1000", res.Assets)
	}
	if res.Liabilities != 200 {
		t.Fatalf("liabilities = %d, want 200 (already deducted debt not counted twice)", res.Liabilities)
	}
	if res.NetWealth != 800 {
		t.Fatalf("net wealth = %d, want 800 (1000 - 200)", res.NetWealth)
	}

	// Duplicate obligation ID check
	dupObligations := []Obligation{
		{
			ID:     "obl-dup",
			Debtor: ana.Subject,
			Season: testSeason,
			Kind:   ObligationKindRegisteredLoan,
			Amount: 100,
		},
		{
			ID:     "obl-dup",
			Debtor: ana.Subject,
			Season: testSeason,
			Kind:   ObligationKindRegisteredLoan,
			Amount: 100,
		},
	}
	if _, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, holdings, dupObligations); !errors.Is(err, ErrDuplicateObligation) {
		t.Fatalf("duplicate obligation = %v, want ErrDuplicateObligation", err)
	}
}

func TestWealthAmbiguousFixtureFailsClosed(t *testing.T) {
	ana := testBeneficiaryAna()

	cases := map[string]struct {
		holdings    []AssetHolding
		obligations []Obligation
		wantErr     error
	}{
		"unmapped asset beneficiary": {
			holdings: []AssetHolding{
				{
					CustodyID:   "cus-unmapped",
					Beneficiary: "",
					Season:      testSeason,
					Kind:        AssetKindLiquidUnconditional,
					Amount:      100,
				},
			},
			wantErr: ErrUnmappedBeneficiary,
		},
		"unmapped obligation debtor": {
			obligations: []Obligation{
				{
					ID:     "obl-unmapped",
					Debtor: "",
					Season: testSeason,
					Kind:   ObligationKindRegisteredLoan,
					Amount: 100,
				},
			},
			wantErr: ErrUnmappedObligation,
		},
		"blank custody ID": {
			holdings: []AssetHolding{
				{
					CustodyID:   "",
					Beneficiary: ana.Subject,
					Season:      testSeason,
					Kind:        AssetKindLiquidUnconditional,
					Amount:      100,
				},
			},
			wantErr: ErrAmbiguousFixture,
		},
		"blank obligation ID": {
			obligations: []Obligation{
				{
					ID:     "",
					Debtor: ana.Subject,
					Season: testSeason,
					Kind:   ObligationKindRegisteredLoan,
					Amount: 100,
				},
			},
			wantErr: ErrAmbiguousFixture,
		},
		"unknown asset kind": {
			holdings: []AssetHolding{
				{
					CustodyID:   "cus-unknown",
					Beneficiary: ana.Subject,
					Season:      testSeason,
					Kind:        "speculative-coin",
					Amount:      100,
				},
			},
			wantErr: ErrUnknownAssetKind,
		},
		"unknown obligation kind": {
			obligations: []Obligation{
				{
					ID:     "obl-unknown",
					Debtor: ana.Subject,
					Season: testSeason,
					Kind:   "social-pledge",
					Amount: 100,
				},
			},
			wantErr: ErrUnknownObligationKind,
		},
		"season mismatch on asset": {
			holdings: []AssetHolding{
				{
					CustodyID:   "cus-other-season",
					Beneficiary: ana.Subject,
					Season:      "temporada-99",
					Kind:        AssetKindLiquidUnconditional,
					Amount:      100,
				},
			},
			wantErr: ErrSeasonMismatch,
		},
		"season mismatch on obligation": {
			obligations: []Obligation{
				{
					ID:     "obl-other-season",
					Debtor: ana.Subject,
					Season: "temporada-99",
					Kind:   ObligationKindRegisteredLoan,
					Amount: 100,
				},
			},
			wantErr: ErrSeasonMismatch,
		},
		"duplicate custody for same beneficiary": {
			holdings: []AssetHolding{
				{
					CustodyID:   "cus-duplicate",
					Beneficiary: ana.Subject,
					Season:      testSeason,
					Kind:        AssetKindLiquidUnconditional,
					Amount:      100,
				},
				{
					CustodyID:   "cus-duplicate",
					Beneficiary: ana.Subject,
					Season:      testSeason,
					Kind:        AssetKindLiquidUnconditional,
					Amount:      200,
				},
			},
			wantErr: ErrDuplicateCustody,
		},
		"participant claiming institutional treasury vault": {
			holdings: []AssetHolding{
				{
					CustodyID:   "cus-vault-theft",
					Beneficiary: ana.Subject,
					Season:      testSeason,
					Kind:        AssetKindLiquidUnconditional,
					Amount:      5000,
					Vault:       "reserva-soberana",
				},
			},
			wantErr: ErrAmbiguousFixture,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, tc.holdings, tc.obligations)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("%s: err = %v, want %v", name, err, tc.wantErr)
			}
		})
	}

	// Policy version check: unknown policy fails
	if _, err := EvaluateBeneficialWealth("wealth-v99", testSeason, ana, nil, nil); !errors.Is(err, ErrUnknownWealthPolicy) {
		t.Fatalf("unknown policy = %v, want ErrUnknownWealthPolicy", err)
	}

	// Crown competing as participant fails
	badCrownCitizen := Beneficiary{
		Subject: InstitutionalCrownSubject,
		Kind:    BeneficiaryKindParticipant,
	}
	if _, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, badCrownCitizen, nil, nil); !errors.Is(err, ErrAmbiguousFixture) {
		t.Fatalf("crown as participant = %v, want ErrAmbiguousFixture", err)
	}

	// Participant claiming institutional entity fails
	badParticipantCrown := Beneficiary{
		Subject: "conta-ana",
		Kind:    BeneficiaryKindInstitutional,
	}
	if _, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, badParticipantCrown, nil, nil); !errors.Is(err, ErrAmbiguousFixture) {
		t.Fatalf("participant claiming institutional = %v, want ErrAmbiguousFixture", err)
	}
}

func TestWealthExcludedCategories(t *testing.T) {
	ana := testBeneficiaryAna()

	excludedKinds := []AssetKind{
		AssetKindConditionalEscrow,
		AssetKindThirdPartyCustody,
		AssetKindPendingReceivable,
		AssetKindFiat,
		AssetKindHistorical,
		AssetKindPatent,
		AssetKindReputation,
	}

	var holdings []AssetHolding
	for i, k := range excludedKinds {
		holdings = append(holdings, AssetHolding{
			CustodyID:   string(k) + "-id",
			Beneficiary: ana.Subject,
			Season:      testSeason,
			Kind:        k,
			Amount:      int64((i + 1) * 100),
		})
	}

	res, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, holdings, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Assets != 0 {
		t.Fatalf("assets = %d, want 0: all excluded categories must yield 0 eligible wealth", res.Assets)
	}
	if res.NetWealth != 0 {
		t.Fatalf("net wealth = %d, want 0", res.NetWealth)
	}
}

func TestEvaluateAllBeneficialWealth(t *testing.T) {
	ana := testBeneficiaryAna()
	bob := Beneficiary{Subject: "conta-bob", Kind: BeneficiaryKindParticipant}
	crown := testBeneficiaryCrown()

	holdings := []AssetHolding{
		{CustodyID: "cus-ana", Beneficiary: ana.Subject, Season: testSeason, Kind: AssetKindLiquidUnconditional, Amount: 500},
		{CustodyID: "cus-bob", Beneficiary: bob.Subject, Season: testSeason, Kind: AssetKindLiquidUnconditional, Amount: 800},
		{CustodyID: "cus-crown", Beneficiary: crown.Subject, Season: testSeason, Kind: AssetKindLiquidUnconditional, Amount: 5000, Vault: "tesouro-livre"},
	}
	obligations := []Obligation{
		{ID: "obl-bob", Debtor: bob.Subject, Season: testSeason, Kind: ObligationKindRegisteredLoan, Amount: 300},
	}

	all, err := EvaluateAllBeneficialWealth(WealthPolicyV1, testSeason, []Beneficiary{ana, bob, crown}, holdings, obligations)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if all[ana.Subject].NetWealth != 500 {
		t.Fatalf("ana net wealth = %d, want 500", all[ana.Subject].NetWealth)
	}
	if all[bob.Subject].NetWealth != 500 { // 800 - 300
		t.Fatalf("bob net wealth = %d, want 500", all[bob.Subject].NetWealth)
	}
	if all[crown.Subject].NetWealth != 5000 {
		t.Fatalf("crown net wealth = %d, want 5000", all[crown.Subject].NetWealth)
	}
}
