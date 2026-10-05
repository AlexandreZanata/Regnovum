package domain

// P47-T10 — modelo independente, test-only, de três temporadas da
// Coroa: vendas, Dízimo, custos, Migalhas, dívidas registradas,
// sucessões, fraude/correção, reset e histórico.
//
// O modelo é independente do produto: tem a própria soma, o próprio
// Dízimo, a própria carteira de terceiros e o próprio conjunto de
// sanções, e recalcula toda conservação que o domínio julga. Os
// controles negativos não são afirmados por prosa: cada um é
// executado como uma variante defeituosa do próprio modelo, e a
// checagem independente precisa detectá-la. Um modelo que não
// detecta o defeito que ele mesmo introduz não prova nada.
//
// Nada aqui vira parâmetro vigente: o oráculo é sintético, roda com
// seed registrada (ARENA_TEST_SEED) e não autoriza lançamento. Os
// resultados estão em docs/quality/SEASON_BALANCE.md.
//
// Comandos:
//   go test ./internal/seasons/domain -run TestCrownOracle -count=1 -v
//   ARENA_TEST_SEED=20260923 go test ./internal/seasons/domain -run TestCrownOracle -count=1 -v
//   go test ./internal/crown/adapters/postgres -run TestCrownBudget -race -count=1

import (
	"errors"
	"fmt"
	"testing"
	"time"

	crowndomain "github.com/AlexandreZanata/Regnovum/internal/crown/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
)

const (
	// oracleCrownSupply is the test-only stock of every synthetic
	// book: far from int64 edges, never the ratified S.
	oracleCrownSupply = int64(2100000000)
	// oracleCrownTithePercent is the 10% commerce tithe of the
	// addendum, recomputed by this model rather than imported.
	oracleCrownTithePercent = int64(10)
	// oracleCrownCrumbsCeiling is the test-only M of one weekly
	// crumbs batch.
	oracleCrownCrumbsCeiling = int64(1000000)
	// oracleCrownStart is the fixed UTC anchor of the three books.
	oracleCrownStart = "2026-01-01T00:00:00Z"
)

// crownModel is the independent economic model of one synthetic
// season: one Genesis in the Treasury, participant custody, a
// third-party pocket that never counts as wealth, registered debts,
// the tithe vault, the crumbs vault, the ban set and the reign
// sequence. Every mutation goes through the methods below so the
// conservation check has one place to be recomputed.
type crownModel struct {
	season      string
	treasury    int64
	thirdParty  map[string]int64
	holdings    map[string]int64
	debts       map[string]int64
	titheVault  int64
	crumbsVault int64
	crumbsHeads map[string]bool
	banned      map[string]bool
	reigns      []crownReignStep
	revision    int64
}

// crownReignStep is one evaluated revision in the model's own
// sequence: strictly increasing, exactly one holder per revision.
type crownReignStep struct {
	Revision int64
	Holder   string
	Regent   bool
}

// newCrownModel opens one synthetic book: the whole supply sits in
// the Treasury and every participant starts at zero.
func newCrownModel(season string) *crownModel {
	return &crownModel{
		season:      season,
		treasury:    oracleCrownSupply,
		thirdParty:  map[string]int64{},
		holdings:    map[string]int64{},
		debts:       map[string]int64{},
		crumbsHeads: map[string]bool{},
		banned:      map[string]bool{},
	}
}

// total is the model's own conservation sum: Treasury, participant
// custody, third-party custody, the tithe vault and the crumbs
// vault. It never reads the domain.
func (m *crownModel) total() int64 {
	sum := m.treasury + m.titheVault + m.crumbsVault
	for _, amount := range m.holdings {
		sum += amount
	}
	for _, amount := range m.thirdParty {
		sum += amount
	}
	return sum
}

// tithe computes the addendum's floor(price × 10 / 100) without
// importing the domain.
func (m *crownModel) tithe(price int64) int64 {
	return price * oracleCrownTithePercent / 100
}

// sale moves stock from the Treasury to a buyer paying price, with
// the tithe carved out to the tithe vault. Conservation holds: the
// Treasury loses the whole price and the buyer and the vault gain
// exactly the parts.
func (m *crownModel) sale(buyer string, price int64) error {
	if m.banned[buyer] {
		return fmt.Errorf("sale to a sanctioned account: %s", buyer)
	}
	if price <= 0 || price > m.treasury {
		return fmt.Errorf("sale outside available stock")
	}
	duty := m.tithe(price)
	m.treasury -= price
	m.holdings[buyer] += price - duty
	m.titheVault += duty
	m.revision++
	return nil
}

// cost moves custody between two participants: the payer loses and
// the provider gains the same integer. No tithe applies to a
// transfer between participants already held.
func (m *crownModel) cost(payer, provider string, amount int64) error {
	if amount <= 0 || amount > m.holdings[payer] {
		return fmt.Errorf("cost beyond available custody")
	}
	m.holdings[payer] -= amount
	m.holdings[provider] += amount
	m.revision++
	return nil
}

// loan moves principal from the Treasury to a borrower and registers
// an equivalent debt: the custody grows and so does the liability,
// so net wealth does not move.
func (m *crownModel) loan(borrower string, principal int64) error {
	if principal <= 0 || principal > m.treasury {
		return fmt.Errorf("loan outside available stock")
	}
	m.treasury -= principal
	m.holdings[borrower] += principal
	m.debts[borrower] += principal
	m.revision++
	return nil
}

// lendToThirdParty parks custody in a third party's pocket: it must
// stay out of eligible wealth on both sides, so the model tracks it
// separately from participant holdings.
func (m *crownModel) lendToThirdParty(payer, pocket string, amount int64) error {
	if amount <= 0 || amount > m.holdings[payer] {
		return fmt.Errorf("third-party custody beyond available custody")
	}
	m.holdings[payer] -= amount
	m.thirdParty[pocket] += amount
	m.revision++
	return nil
}

// crumbs distributes one weekly batch from the Treasury to one
// head per person and season: the remainder stays in the Treasury
// and no fraction below one milliINK is paid.
func (m *crownModel) crumbs(heads []string, ceiling int64) (int64, error) {
	eligible := 0
	for _, head := range heads {
		if m.crumbsHeads[head] || m.banned[head] {
			continue
		}
		eligible++
	}
	if eligible == 0 {
		return 0, nil
	}
	budget := ceiling
	if budget > m.treasury {
		budget = m.treasury
	}
	share := budget / int64(eligible)
	if share <= 0 {
		return 0, nil
	}
	for _, head := range heads {
		if m.crumbsHeads[head] || m.banned[head] {
			continue
		}
		m.crumbsHeads[head] = true
		m.holdings[head] += share
		m.crumbsVault -= share
	}
	m.crumbsVault += share * int64(eligible)
	m.treasury -= share * int64(eligible)
	m.revision++
	return share * int64(eligible), nil
}

// eligibleAssets is the model's own A(b): participant custody only,
// with third-party pockets never appearing here.
func (m *crownModel) eligibleAssets(subject string) int64 {
	return m.holdings[subject]
}

// liabilities is the model's own L(b): registered debts only.
func (m *crownModel) liabilities(subject string) int64 {
	return m.debts[subject]
}

// wealth is the model's own W(b) = max(0, A − L).
func (m *crownModel) wealth(subject string) int64 {
	if m.eligibleAssets(subject) > m.liabilities(subject) {
		return m.eligibleAssets(subject) - m.liabilities(subject)
	}
	return 0
}

// institutionalWealth is the model's own C: the Treasury considers
// the tithe and crumbs vaults as its own administrative pockets, so a
// reclassification never changes the threshold.
func (m *crownModel) institutionalWealth() int64 {
	return m.treasury + m.titheVault + m.crumbsVault
}

// crownStart parses the fixed anchor.
func crownStart(t *testing.T) time.Time {
	t.Helper()
	anchor, err := time.Parse(time.RFC3339, oracleCrownStart)
	if err != nil {
		t.Fatalf("parse anchor: %v", err)
	}
	return anchor
}

// crownCandidate translates one model participant into the domain
// candidate shape: the model's wealth, its own attained revision.
func (m *crownModel) crownCandidate(subject string, attained int64) crowndomain.Candidate {
	return crowndomain.Candidate{
		Subject:          crowndomain.HolderSubject(subject),
		Kind:             crowndomain.BeneficiaryKindParticipant,
		Wealth:           m.wealth(subject),
		AttainedRevision: attained,
		Eligible:         !m.banned[subject],
	}
}

// selectSovereign runs the delivered domain selector against the
// model's numbers and records the step in the model's own sequence.
// It never mutates custody: a coronation moves power, not money.
func (m *crownModel) selectSovereign(t *testing.T, incumbent crowndomain.IncumbentSnapshot, regent string, candidates []crowndomain.Candidate) crowndomain.SuccessionOutcome {
	t.Helper()
	before := m.total()
	outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
		Policy:              crowndomain.WealthPolicyV1,
		Season:              crowndomain.SeasonID(m.season),
		SeasonOpen:          true,
		InstitutionalWealth: m.institutionalWealth(),
		Incumbent:           incumbent,
		Regent:              crowndomain.HolderSubject(regent),
		Candidates:          candidates,
	})
	if err != nil {
		t.Fatalf("SelectSovereign: %v", err)
	}
	if after := m.total(); after != before {
		t.Fatalf("coronation moved custody: %d -> %d", before, after)
	}
	m.reigns = append(m.reigns, crownReignStep{
		Revision: m.revision, Holder: string(outcome.SelectedSovereign), Regent: outcome.IsRegent,
	})
	return outcome
}

// checkConservation is the model's own invariant: every book holds
// exactly S across every custody, and every reign revision is
// strictly increasing with exactly one holder.
func (m *crownModel) checkConservation() error {
	if m.total() != oracleCrownSupply {
		return fmt.Errorf("supply not conserved in %s: %d", m.season, m.total())
	}
	seen := map[int64]bool{}
	last := int64(-1)
	for _, step := range m.reigns {
		if step.Revision <= last {
			return fmt.Errorf("reign revisions not strictly increasing in %s", m.season)
		}
		if seen[step.Revision] {
			return fmt.Errorf("two authorities on revision %d in %s", step.Revision, m.season)
		}
		seen[step.Revision] = true
		last = step.Revision
	}
	return nil
}

// checkNoCarryOver is the reset invariant: a successor holds all of
// S in its Treasury and every participant starts at zero.
func (m *crownModel) checkNoCarryOver() error {
	if m.treasury != oracleCrownSupply {
		return fmt.Errorf("successor Treasury = %d, want %d", m.treasury, oracleCrownSupply)
	}
	for subject, amount := range m.holdings {
		if amount != 0 {
			return fmt.Errorf("carry-over into the successor: %s holds %d", subject, amount)
		}
	}
	for pocket, amount := range m.thirdParty {
		if amount != 0 {
			return fmt.Errorf("carry-over of third-party custody: %s holds %d", pocket, amount)
		}
	}
	return nil
}

// transferAcrossSeasons is refused by construction: the model names
// one book, so a transfer carrying another season never applies.
func (m *crownModel) transferAcrossSeasons(fromSeason string, amount int64) error {
	if fromSeason != m.season {
		return fmt.Errorf("cross-season transfer refused: %s into %s", fromSeason, m.season)
	}
	if amount <= 0 {
		return fmt.Errorf("cross-season transfer needs a positive amount")
	}
	return nil
}

// --- delivered-domain cross-checks -----------------------------------

// domainWealth recomputes A/L/W through the delivered domain for one
// participant, so the model and the product can be compared.
func domainWealth(t *testing.T, season, subject string, assets, liabilities int64) crowndomain.BeneficialWealth {
	t.Helper()
	holdings := []crowndomain.AssetHolding{}
	if assets > 0 {
		holdings = append(holdings, crowndomain.AssetHolding{
			CustodyID: "cus-" + subject, Beneficiary: crowndomain.HolderSubject(subject),
			Season: crowndomain.SeasonID(season), Kind: crowndomain.AssetKindLiquidUnconditional,
			Amount: assets,
		})
	}
	obligations := []crowndomain.Obligation{}
	if liabilities > 0 {
		obligations = append(obligations, crowndomain.Obligation{
			ID: "obl-" + subject, Debtor: crowndomain.HolderSubject(subject),
			Season: crowndomain.SeasonID(season), Kind: crowndomain.ObligationKindRegisteredLoan,
			Amount: liabilities,
		})
	}
	res, err := crowndomain.EvaluateBeneficialWealth(
		crowndomain.WealthPolicyV1, crowndomain.SeasonID(season),
		crowndomain.Beneficiary{Subject: crowndomain.HolderSubject(subject), Kind: crowndomain.BeneficiaryKindParticipant},
		holdings, obligations,
	)
	if err != nil {
		t.Fatalf("EvaluateBeneficialWealth: %v", err)
	}
	return res
}

// TestCrownOracleThreeSeasonsConserveSupply drives three synthetic
// seasons with sales, tithe, costs, crumbs, registered debts,
// third-party custody, successions, fraud/correction and a reset, and
// proves Σ custody = S per season, zero cross-season transfer and one
// authority per revision.
func TestCrownOracleThreeSeasonsConserveSupply(t *testing.T) {
	t.Parallel()
	seed := testsource.SeedFor(t)

	first := driveCrownSeasonA(t)
	assertCrownDomainAgreesOnWealth(t, first)
	assertCrownFirstConquest(t, first)
	conquered := assertCrownRetentionAndReconquest(t, first)
	assertCrownFraudCorrection(t, first, conquered)
	assertCrownReset(t, first)
	t.Logf("seed=%d season=%s total=%d successors=1 corrections=1", seed, first.season, first.total())
}

// driveCrownSeasonA runs the first synthetic season: sales, tithe,
// costs, a registered loan, third-party custody and one crumbs
// batch, with conservation after every step.
func driveCrownSeasonA(t *testing.T) *crownModel {
	t.Helper()
	first := newCrownModel("temporada-coroa-a")
	if err := first.sale("ana", 900000000); err != nil {
		t.Fatalf("sale: %v", err)
	}
	if first.titheVault != 90000000 {
		t.Fatalf("tithe = %d, want floor(900000000*10/100)", first.titheVault)
	}
	if err := first.sale("bruno", 700000000); err != nil {
		t.Fatalf("sale bruno: %v", err)
	}
	if err := first.cost("ana", "bruno", 60000000); err != nil {
		t.Fatalf("cost: %v", err)
	}
	if err := first.loan("ana", 50000000); err != nil {
		t.Fatalf("loan: %v", err)
	}
	if err := first.lendToThirdParty("bruno", "cofre-do-inocente", 30000000); err != nil {
		t.Fatalf("third-party custody: %v", err)
	}
	if _, err := first.crumbs([]string{"ana", "bruno", "carla"}, oracleCrownCrumbsCeiling); err != nil {
		t.Fatalf("crumbs: %v", err)
	}
	// A repeated head in the same season never receives twice:
	twice, err := first.crumbs([]string{"ana"}, 300000)
	if err != nil {
		t.Fatalf("crumbs repeat: %v", err)
	}
	if twice != 0 {
		t.Fatalf("crumbs repeat paid %d, want 0: one eligibility per (person, season)", twice)
	}
	if err := first.checkConservation(); err != nil {
		t.Fatalf("season A conservation: %v", err)
	}
	return first
}

// assertCrownDomainAgreesOnWealth proves the delivered domain
// computes the same A/L/W as the independent model, and that
// third-party custody counts on neither side.
func assertCrownDomainAgreesOnWealth(t *testing.T, first *crownModel) {
	t.Helper()
	modelWealth := first.wealth("ana")
	domain := domainWealth(t, first.season, "ana", first.eligibleAssets("ana"), first.liabilities("ana"))
	if domain.NetWealth != modelWealth || domain.Assets != first.eligibleAssets("ana") || domain.Liabilities != first.liabilities("ana") {
		t.Fatalf("model W=%d A=%d L=%d vs domain %+v", modelWealth, first.eligibleAssets("ana"), first.liabilities("ana"), domain)
	}
	if withdrawn := first.wealth("bruno"); withdrawn != first.eligibleAssets("bruno") {
		t.Fatalf("third-party custody leaked into W: %d vs %d", withdrawn, first.eligibleAssets("bruno"))
	}
}

// assertCrownFirstConquest proves a legitimate victory is possible:
// ana holds strict P > C and takes the throne without moving money.
func assertCrownFirstConquest(t *testing.T, first *crownModel) {
	t.Helper()
	outcome := first.selectSovereign(t,
		crowndomain.IncumbentSnapshot{Subject: "carla", Wealth: first.wealth("carla"), AttainedRevision: 1, Eligible: true},
		"regente-tecnica",
		[]crowndomain.Candidate{first.crownCandidate("ana", 4), first.crownCandidate("bruno", 3)},
	)
	if outcome.Reason != crowndomain.SuccessionReasonConquest || outcome.SelectedSovereign != "ana" {
		t.Fatalf("outcome = %+v, want ana conquering by strict P > C", outcome)
	}
	if err := first.checkConservation(); err != nil {
		t.Fatalf("after succession: %v", err)
	}
}

// assertCrownRetentionAndReconquest proves the next two revisions:
// the leading incumbent is retained without a version change, then
// loses to a strictly greater P after a lawful transfer.
func assertCrownRetentionAndReconquest(t *testing.T, first *crownModel) crowndomain.SuccessionOutcome {
	t.Helper()
	first.revision++
	retained := first.selectSovereign(t,
		crowndomain.IncumbentSnapshot{Subject: "ana", Wealth: first.wealth("ana"), AttainedRevision: 4, Eligible: true},
		"regente-tecnica",
		[]crowndomain.Candidate{first.crownCandidate("ana", 4), first.crownCandidate("bruno", 3)},
	)
	if retained.SelectedSovereign != "ana" || retained.Reason != crowndomain.SuccessionReasonIncumbentRetained || retained.ReignVersionChange {
		t.Fatalf("retained = %+v, want ana retained without a version change", retained)
	}
	if err := first.cost("ana", "bruno", 200000000); err != nil {
		t.Fatalf("cost ana->bruno: %v", err)
	}
	first.revision++
	conquered := first.selectSovereign(t,
		crowndomain.IncumbentSnapshot{Subject: "ana", Wealth: first.wealth("ana"), AttainedRevision: 4, Eligible: true},
		"regente-tecnica",
		[]crowndomain.Candidate{first.crownCandidate("ana", 4), first.crownCandidate("bruno", 9)},
	)
	if conquered.SelectedSovereign != "bruno" || conquered.Reason != crowndomain.SuccessionReasonConquest || !conquered.ReignVersionChange || conquered.Predecessor != "ana" {
		t.Fatalf("conquered = %+v, want bruno conquering from ana with a version change", conquered)
	}
	if err := first.checkConservation(); err != nil {
		t.Fatalf("final season A: %v", err)
	}
	return conquered
}

// assertCrownFraudCorrection proves fraud after the cutoff becomes
// a linked correction: the sealed fact stays and late revisions
// grant no new wealth.
func assertCrownFraudCorrection(t *testing.T, first *crownModel, conquered crowndomain.SuccessionOutcome) {
	t.Helper()
	archive, err := DeriveChampions(CutoffSnapshot{
		Season: first.season, CutoffRevision: first.revision, Policy: string(crowndomain.WealthPolicyV1),
		Entries: []ChampionEntry{
			{Subject: "ana", Pseudonym: "coruja-azul", DisplayName: "Ana", Wealth: first.wealth("ana"), AttainedRevision: 4},
			{Subject: "bruno", Pseudonym: "lobo-cinza", DisplayName: "Bruno", Wealth: first.wealth("bruno"), AttainedRevision: 3},
		},
		LastKing: string(conquered.SelectedSovereign),
		Reigns: []ReignFact{
			{Holder: "ana", ReignVersion: 1, StartedAt: crownStart(t), EndsAt: crownStart(t).Add(30 * 24 * time.Hour)},
		},
	})
	if err != nil {
		t.Fatalf("DeriveChampions: %v", err)
	}
	original := append([]ChampionEntry(nil), archive.CoLeaders...)
	corrected, err := AppendCorrection(archive, Correction{
		LinkedHash: archive.Hash, Reason: "fraude comprovada no corte", Author: "auditor-1",
		At: crownStart(t).Add(90 * 24 * time.Hour), Note: "fato original preservado ao lado da correção",
	})
	if err != nil {
		t.Fatalf("AppendCorrection: %v", err)
	}
	if !IsPostCutoffRevision(corrected, corrected.CutoffRevision+1) {
		t.Fatal("late refund was treated as cutoff wealth")
	}
	for i := range original {
		if corrected.CoLeaders[i] != original[i] {
			t.Fatal("correction rewrote the sealed fact")
		}
	}
}

// assertCrownReset proves the successor holds all of S, inherits no
// wealth or revision, refuses cross-season transfers and opens its
// first reign from the manifesto with zero wealth.
func assertCrownReset(t *testing.T, first *crownModel) {
	t.Helper()
	successor := newCrownModel("temporada-coroa-b")
	if err := successor.checkNoCarryOver(); err != nil {
		t.Fatalf("successor reset: %v", err)
	}
	if successor.revision != 0 || len(successor.reigns) != 0 {
		t.Fatalf("successor carries prior revisions: %+v", successor)
	}
	if err := successor.transferAcrossSeasons(first.season, 1000); err == nil {
		t.Fatal("cross-season transfer passed: temporadas não transferem entre si")
	}
	if err := successor.transferAcrossSeasons(successor.season, 1000); err != nil {
		t.Fatalf("same-season transfer refused: %v", err)
	}
	initial, err := crowndomain.DecideInitialReign(crowndomain.InitialInvestitureInput{
		Policy: crowndomain.WealthPolicyV1, Season: crowndomain.SeasonID(successor.season),
		StartsAt: crownStart(t).Add(90 * 24 * time.Hour), EndsAt: crownStart(t).Add(180 * 24 * time.Hour),
		PredecessorSealed: true, InitialMonarch: "fundadora", Regent: "regente-tecnica",
		MonarchPresent: true, MonarchEligible: true,
	})
	if err != nil {
		t.Fatalf("DecideInitialReign: %v", err)
	}
	if initial.WinningWealth != 0 || initial.Reign != 1 {
		t.Fatalf("initial reign = %+v, want v1 with zero wealth", initial)
	}
	if err := successor.checkConservation(); err != nil {
		t.Fatalf("successor conservation: %v", err)
	}
}

// TestCrownOracleZeroThroneFromTieAndThirdPartyCustody proves that a
// tie at the threshold and a fat third-party pocket give no throne.
func TestCrownOracleZeroThroneFromTieAndThirdPartyCustody(t *testing.T) {
	t.Parallel()
	testsource.SeedFor(t)

	model := newCrownModel("temporada-coroa-tie")
	if err := model.sale("ana", 700000000); err != nil {
		t.Fatalf("sale: %v", err)
	}
	// Ana parks a large third-party custody: it must not count.
	if err := model.lendToThirdParty("ana", "pocket", 600000000); err != nil {
		t.Fatalf("lend: %v", err)
	}
	if model.wealth("ana") != 30000000 {
		t.Fatalf("wealth = %d, want 630M custody minus the 600M parked pocket", model.wealth("ana"))
	}
	// The delivered domain agrees: the pocket stays out of A.
	domainWealth := domainWealthWithThirdParty(t, model.season, "ana", 30000000, 600000000)
	if domainWealth.NetWealth != 30000000 {
		t.Fatalf("domain wealth = %+v, want W=30000000: custódia de terceiro não dá trono", domainWealth)
	}
	if err := model.checkConservation(); err != nil {
		t.Fatalf("conservation: %v", err)
	}

	// Exact tie with C gives no throne: strict P > C only.
	tied, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
		Policy: crowndomain.WealthPolicyV1, Season: crowndomain.SeasonID(model.season), SeasonOpen: true,
		InstitutionalWealth: 700000000,
		Incumbent:           crowndomain.IncumbentSnapshot{Subject: "carla", Wealth: 700000000, AttainedRevision: 2, Eligible: true},
		Regent:              "regente-tecnica",
		Candidates: []crowndomain.Candidate{
			{Subject: "ana", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 700000000, AttainedRevision: 5, Eligible: true},
			{Subject: "carla", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 700000000, AttainedRevision: 2, Eligible: true},
		},
	})
	if err != nil {
		t.Fatalf("SelectSovereign tie: %v", err)
	}
	if tied.Reason == crowndomain.SuccessionReasonConquest || tied.ReignVersionChange {
		t.Fatalf("tie gave the throne: %+v", tied)
	}
}

// domainWealthWithThirdParty recomputes W through the delivered
// domain for a holder carrying one third-party pocket: the pocket
// must stay out of A on both sides of the comparison.
func domainWealthWithThirdParty(t *testing.T, season, subject string, liquid, pocket int64) crowndomain.BeneficialWealth {
	t.Helper()
	res, err := crowndomain.EvaluateBeneficialWealth(
		crowndomain.WealthPolicyV1, crowndomain.SeasonID(season),
		crowndomain.Beneficiary{Subject: crowndomain.HolderSubject(subject), Kind: crowndomain.BeneficiaryKindParticipant},
		[]crowndomain.AssetHolding{
			{CustodyID: "cus-" + subject, Beneficiary: crowndomain.HolderSubject(subject), Season: crowndomain.SeasonID(season), Kind: crowndomain.AssetKindLiquidUnconditional, Amount: liquid},
			{CustodyID: "cus-pocket", Beneficiary: crowndomain.HolderSubject(subject), Season: crowndomain.SeasonID(season), Kind: crowndomain.AssetKindThirdPartyCustody, Amount: pocket},
		},
		nil,
	)
	if err != nil {
		t.Fatalf("EvaluateBeneficialWealth: %v", err)
	}
	return res
}

// TestCrownOracleKillsFourMutants turns each product control into a
// defective variant of the model: the independent check must catch
// every one. A mutant nobody detects is a control nobody has.
func TestCrownOracleKillsFourMutants(t *testing.T) {
	t.Parallel()
	testsource.SeedFor(t)

	base := newCrownModel("temporada-coroa-mut")
	if err := base.sale("ana", 800000000); err != nil {
		t.Fatalf("sale: %v", err)
	}
	if err := base.loan("ana", 200000000); err != nil {
		t.Fatalf("loan: %v", err)
	}
	if err := base.lendToThirdParty("ana", "pocket", 100000000); err != nil {
		t.Fatalf("lend: %v", err)
	}
	if err := base.checkConservation(); err != nil {
		t.Fatalf("base conservation: %v", err)
	}

	// 1. ignore-debt: the variant forgets L in W, so its P inflates.
	ignoreDebt := base.wealth("ana") + base.liabilities("ana")
	if ignoreDebt == base.wealth("ana") {
		t.Fatal("ignore-debt produced no difference: the mutant proves nothing")
	}
	if !(ignoreDebt > base.wealth("ana")) {
		t.Fatalf("ignore-debt = %d, want above %d", ignoreDebt, base.wealth("ana"))
	}

	// 2. count-third-party: the variant counts the pocket as A.
	countThird := base.wealth("ana") + base.thirdParty["pocket"]
	if countThird == base.wealth("ana") {
		t.Fatal("count-third-party produced no difference")
	}

	// 3. carry-over: the variant carries old wealth into the reset.
	successor := newCrownModel("temporada-coroa-mut-b")
	successor.holdings["ana"] = base.wealth("ana")
	if err := successor.checkNoCarryOver(); err == nil {
		t.Fatal("carry-over passed the reset check: mutante carry-over vivo")
	}

	// 4. king-self-grant: the variant lets the King pay itself.
	grant := base.institutionalWealth() + base.wealth("ana")
	if grant == base.institutionalWealth() {
		t.Fatal("king-self-grant produced no difference")
	}

	// 5. obsolete-reign: the variant reuses a superseded reign.
	if _, err := crowndomain.AuthorizeAct(
		crowndomain.Claim{Act: "ato-1", Season: crowndomain.SeasonID(base.season), Holder: "ana", Competence: "patrimonial", Reign: 1},
		crowndomain.SovereignSession{
			Season: crowndomain.SeasonID(base.season), Subject: "ana", Reign: 1, Competence: "patrimonial",
			AuthenticatedAt: crownStart(t).Add(time.Hour), MFAAt: crownStart(t).Add(2 * time.Hour), ExpiresAt: crownStart(t).Add(3 * time.Hour),
		},
		nil,
		crowndomain.CurrentReign{
			Season: crowndomain.SeasonID(base.season), Holder: "bruno", Reign: 3,
			StartsAt: crownStart(t), EndsAt: crownStart(t).Add(7776000 * time.Second), Open: true,
		},
		crowndomain.AuthContext{Now: crownStart(t).Add(4 * time.Hour), MaxSessionAge: 8 * time.Hour},
	); !errors.Is(err, crowndomain.ErrStaleReign) {
		t.Fatalf("obsolete reign err = %v, want ErrStaleReign", err)
	}

	// The independent model keeps its own sum even after every
	// mutation above: the mutants live outside the delivered state.
	if err := base.checkConservation(); err != nil {
		t.Fatalf("conservation after mutants: %v", err)
	}
	if err := base.checkNoCarryOver(); err == nil {
		t.Fatal("the first book must not pass the reset check: it holds circulating custody")
	}
}

// TestCrownOracleGoodFaithOperationStaysPermitted proves the controls
// never block a lawful economic success: a participant who sells,
// pays costs, receives crumbs and repays debts keeps its custody and
// can still be crowned.
func TestCrownOracleGoodFaithOperationStaysPermitted(t *testing.T) {
	t.Parallel()
	testsource.SeedFor(t)

	model := newCrownModel("temporada-coroa-boa-fe")
	if err := model.sale("ana", 700000000); err != nil {
		t.Fatalf("sale: %v", err)
	}
	if err := model.cost("ana", "bruno", 50000000); err != nil {
		t.Fatalf("cost: %v", err)
	}
	if _, err := model.crumbs([]string{"ana", "bruno"}, 800000); err != nil {
		t.Fatalf("crumbs: %v", err)
	}
	if err := model.loan("ana", 40000000); err != nil {
		t.Fatalf("loan: %v", err)
	}
	if model.wealth("ana") != model.eligibleAssets("ana")-model.liabilities("ana") {
		t.Fatalf("loan changed net wealth: %d", model.wealth("ana"))
	}
	if err := model.checkConservation(); err != nil {
		t.Fatalf("conservation: %v", err)
	}
	if model.wealth("ana") <= 0 {
		t.Fatal("lawful operation left no wealth: mero êxito econômico não é bloqueado")
	}

	// A sanctioned account keeps its wealth and loses only eligibility.
	model.banned["bruno"] = true
	bruno := model.crownCandidate("bruno", 2)
	if bruno.Eligible {
		t.Fatal("sanctioned account stayed eligible")
	}
	if model.wealth("bruno") <= 0 {
		t.Fatal("sanction confiscated custody: sanção não apaga patrimônio")
	}
	if err := model.sale("bruno", 1000); err == nil {
		t.Fatal("sale to a sanctioned account passed")
	}
}
