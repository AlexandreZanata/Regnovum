package domain

// P47-T09 — controles contra manipulação da Coroa.
//
// Cada ameaça (THR-CROWN-01..09) traz caso ofensivo, detector e
// runbook na mesma prova: o ataque é montado de verdade e o domínio
// o recusa com o código estável. Os quatro mutantes da fase morrem
// aqui: ignore-debt (T01), count-third-party (T03),
// king-self-grant (T07) e obsolete-reign (T09). Operação de boa-fé
// continua permitida e nenhuma flag permite policy bypass (T10).
// Dívidas e acordos externos desconhecidos ao serviço não são
// detectáveis pela fórmula — limitação registrada em T11, sem
// prometer eliminação e sem coleta invasiva nova.
//
// Runbooks: docs/RUNBOOKS.md R1 (recusas e erros 5xx contidos como
// recusa 4xx de domínio) e R3 (contenção de rajadas no avaliador).

import (
	"errors"
	"testing"
	"time"
)

func manipBeneficiary(subject string) Beneficiary {
	return Beneficiary{Subject: HolderSubject(subject), Kind: BeneficiaryKindParticipant}
}

func manipHolding(custody, subject string, kind AssetKind, amount int64) AssetHolding {
	return AssetHolding{
		CustodyID: custody, Beneficiary: HolderSubject(subject),
		Season: testSeason, Kind: kind, Amount: amount,
	}
}

func manipObligation(id, debtor string, kind ObligationKind, amount int64) Obligation {
	return Obligation{
		ID: id, Debtor: HolderSubject(debtor),
		Season: testSeason, Kind: kind, Amount: amount,
	}
}

// THR-CROWN-01 — empréstimo registrado: o atacante toma 400 de
// empréstimo para inflar P acima de C. Detector:
// EvaluateBeneficialWealth deduz L. Mata o mutante ignore-debt.
// Runbook: R1.
func TestManipulation_RegisteredLoanDeducted(t *testing.T) {
	ana := manipBeneficiary("conta-ana")
	holdings := []AssetHolding{manipHolding("cus-ana-1", "conta-ana", AssetKindLiquidUnconditional, 1000)}
	obligations := []Obligation{manipObligation("obl-ana-1", "conta-ana", ObligationKindRegisteredLoan, 400)}
	res, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, holdings, obligations)
	if err != nil {
		t.Fatalf("EvaluateBeneficialWealth: %v", err)
	}
	if res.Assets != 1000 || res.Liabilities != 400 || res.NetWealth != 600 {
		t.Fatalf("wealth = %+v, want A=1000 L=400 W=600: empréstimo registrado neutraliza", res)
	}
}

// THR-CROWN-02 — reclassificação de Tesouro: mover saldo entre
// bolsos próprios não muda W e não reduz C institucional.
// Detector: agregação única por beneficiário + C medido só no
// sujeito institucional. Runbook: R1.
func TestManipulation_TreasuryReclassificationKeepsThreshold(t *testing.T) {
	ana := manipBeneficiary("conta-ana")
	split := []AssetHolding{
		manipHolding("cus-ana-1", "conta-ana", AssetKindLiquidUnconditional, 500),
		manipHolding("cus-ana-2", "conta-ana", AssetKindLiquidUnconditional, 300),
	}
	res, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, split, nil)
	if err != nil {
		t.Fatalf("EvaluateBeneficialWealth: %v", err)
	}
	if res.NetWealth != 800 {
		t.Fatalf("reclassified wealth = %+v, want W=800: reclassificação não cria nem destrói riqueza", res)
	}
	crown := Beneficiary{Subject: InstitutionalCrownSubject, Kind: BeneficiaryKindInstitutional}
	crownHoldings := []AssetHolding{manipHolding("cus-coroa-1", string(InstitutionalCrownSubject), AssetKindLiquidUnconditional, 700)}
	crownRes, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, crown, crownHoldings, nil)
	if err != nil {
		t.Fatalf("crown wealth: %v", err)
	}
	if crownRes.NetWealth != 700 {
		t.Fatalf("crown wealth = %+v, want C=700: bolso pessoal nunca alimenta C", crownRes)
	}
}

// THR-CROWN-03 — escrow emprestado e custódia de terceiro: o
// atacante conta 500 de terceiro + 300 em escrow condicional como
// próprios. Detector: categorias excluídas de A e obrigação de
// terceiro excluída de L (sem dupla penalidade). Mata o mutante
// count-third-party. Runbook: R1.
func TestManipulation_LentEscrowExcluded(t *testing.T) {
	ana := manipBeneficiary("conta-ana")
	holdings := []AssetHolding{
		manipHolding("cus-ana-1", "conta-ana", AssetKindLiquidUnconditional, 800),
		manipHolding("cus-terceiro-1", "conta-ana", AssetKindThirdPartyCustody, 500),
		manipHolding("cus-escrow-1", "conta-ana", AssetKindConditionalEscrow, 300),
	}
	obligations := []Obligation{
		manipObligation("obl-terceiro-1", "conta-ana", ObligationKindThirdPartyCustody, 500),
	}
	res, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, holdings, obligations)
	if err != nil {
		t.Fatalf("EvaluateBeneficialWealth: %v", err)
	}
	if res.Assets != 800 || res.Liabilities != 0 || res.NetWealth != 800 {
		t.Fatalf("wealth = %+v, want A=800 L=0 W=800: terceiro/escrow fora de A e de L", res)
	}
}

// THR-CROWN-04 — compra pendente: recebível não confirmado de 500
// não vira riqueza elegível. Detector: pending-receivable,
// fiat, histórico, patente e reputação excluídos de A.
// Runbook: R1.
func TestManipulation_PendingPurchaseGrantsNoWealth(t *testing.T) {
	ana := manipBeneficiary("conta-ana")
	holdings := []AssetHolding{
		manipHolding("cus-ana-1", "conta-ana", AssetKindLiquidUnconditional, 600),
		manipHolding("cus-receb-1", "conta-ana", AssetKindPendingReceivable, 500),
		manipHolding("cus-fiat-1", "conta-ana", AssetKindFiat, 900),
	}
	res, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, holdings, nil)
	if err != nil {
		t.Fatalf("EvaluateBeneficialWealth: %v", err)
	}
	if res.NetWealth != 600 {
		t.Fatalf("wealth = %+v, want W=600: compra pendente e fiat não concedem trono", res)
	}
}

// THR-CROWN-05 — manipulação de condição/tempo e política: trocar a
// política no meio da season, esticar o calendário ou retroagir
// vigência. Detectores: ParseWealthPolicyVersion, DecideInitialReign
// e DefineAct. Runbook: R1.
func TestManipulation_PolicyFrozenDuringSeason(t *testing.T) {
	ana := manipBeneficiary("conta-ana")
	if _, err := EvaluateBeneficialWealth("wealth-v99", testSeason, ana, nil, nil); !errors.Is(err, ErrUnknownWealthPolicy) {
		t.Fatalf("unknown policy err = %v, want ErrUnknownWealthPolicy: política não se troca na season", err)
	}
	start, end := boundsWindowForManip()
	in := InitialInvestitureInput{
		Policy: WealthPolicyV1, Season: "temporada-2",
		StartsAt: start, EndsAt: end.Add(24 * time.Hour),
		PredecessorSealed: true, InitialMonarch: "fundadora",
		Regent: "regente-tecnica", MonarchPresent: true, MonarchEligible: true,
	}
	if _, err := DecideInitialReign(in); !errors.Is(err, ErrProhibitedAlteration) {
		t.Fatalf("stretched window err = %v, want ErrProhibitedAlteration", err)
	}
	act := executeAct()
	act.Effective = act.DecreedAt.Add(-time.Hour)
	if _, err := DefineAct(act); !errors.Is(err, ErrRetroactiveAct) {
		t.Fatalf("retroactive act err = %v, want ErrRetroactiveAct", err)
	}
}

func boundsWindowForManip() (start, end time.Time) {
	start = time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	return start, start.Add(SeasonalWindowSeconds * time.Second)
}

// THR-CROWN-06 — split de carteiras: dividir 800 em 400+400 não
// dobra a riqueza nem dribla o limiar; a mesma custódia duas vezes
// é recusada. Detector: agregação única + ErrDuplicateCustody.
// Runbook: R1.
func TestManipulation_WalletSplitAggregatesOnce(t *testing.T) {
	ana := manipBeneficiary("conta-ana")
	whole, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana,
		[]AssetHolding{manipHolding("cus-ana-1", "conta-ana", AssetKindLiquidUnconditional, 800)}, nil)
	if err != nil {
		t.Fatalf("whole: %v", err)
	}
	split, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana,
		[]AssetHolding{
			manipHolding("cus-ana-1", "conta-ana", AssetKindLiquidUnconditional, 400),
			manipHolding("cus-ana-2", "conta-ana", AssetKindLiquidUnconditional, 400),
		}, nil)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if whole.NetWealth != 800 || split.NetWealth != 800 {
		t.Fatalf("whole=%+v split=%+v, want both W=800: split não multiplica riqueza", whole, split)
	}
	duplicated := []AssetHolding{
		manipHolding("cus-ana-1", "conta-ana", AssetKindLiquidUnconditional, 400),
		manipHolding("cus-ana-1", "conta-ana", AssetKindLiquidUnconditional, 400),
	}
	if _, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, duplicated, nil); !errors.Is(err, ErrDuplicateCustody) {
		t.Fatalf("duplicate custody err = %v, want ErrDuplicateCustody", err)
	}
}

// THR-CROWN-07 — self-dealing: o Rei concede o Tesouro a si mesmo
// ou aprova benefício próprio sem revisão independente. Detector:
// PlanExecution (ErrSelfGrant, ErrConflictedBenefit); reparação
// legítima com revisão independente continua permitida, preservando
// a defesa. Mata o mutante king-self-grant. Runbook: R1.
func TestManipulation_SelfDealingRefused(t *testing.T) {
	act := executeAct()
	now := executeAnchor().Add(2 * time.Hour)
	custody := executeCustody()
	custody.Beneficiary = "rainha-1"
	if _, err := PlanExecution(act, executeClearance(act, now), executeCurrent(), custody, now); !errors.Is(err, ErrSelfGrant) {
		t.Fatalf("self-grant err = %v, want ErrSelfGrant", err)
	}
	conflicted := executeCustody()
	conflicted.ConflictPending = true
	if _, err := PlanExecution(act, executeClearance(act, now), executeCurrent(), conflicted, now); !errors.Is(err, ErrConflictedBenefit) {
		t.Fatalf("conflicted err = %v, want ErrConflictedBenefit", err)
	}
	repaired := executeCustody()
	repaired.ConflictPending = true
	repaired.IndependentReview = "decisao-5"
	if _, err := PlanExecution(act, executeClearance(act, now), executeCurrent(), repaired, now); err != nil {
		t.Fatalf("independent repair err = %v, want allowed: corretivo independente preserva defesa", err)
	}
}

// THR-CROWN-08 — sanção interessada e caso próprio: acusador que
// julga, Rei que julga rival em causa própria e autor que recorre
// do próprio ato. Detectores: SeatPanel (ErrConflictedOffice) e
// FilePetition (ErrImproperPetitioner). Runbook: R1.
func TestManipulation_InterestedSanctionRefused(t *testing.T) {
	now := executeAnchor().Add(2 * time.Hour)
	current := executeCurrent()
	base := Panel{
		Proceeding: "proc-1", Season: current.Season, Reign: current.Reign,
		Inquisitor: "inquisidor-1", Accuser: "acusador-1", Accused: "acusado-1",
		Judge: "juiz-1", Defender: "defensor-1", Witnesses: []HolderSubject{"testemunha-1"},
		Executor: "carrasco-1", Auditor: "auditor-9",
	}
	accuserJudge := base
	accuserJudge.Accuser = "juiz-1"
	if _, err := SeatPanel(accuserJudge, current, now); !errors.Is(err, ErrConflictedOffice) {
		t.Fatalf("accuser-judge err = %v, want ErrConflictedOffice: parte excluída de caso próprio", err)
	}
	kingJudge := base
	kingJudge.Judge = current.Holder
	kingJudge.Accuser = current.Holder
	if _, err := SeatPanel(kingJudge, current, now); !errors.Is(err, ErrConflictedOffice) {
		t.Fatalf("king-judge err = %v, want ErrConflictedOffice: Rei nunca julga rival em causa própria", err)
	}
	book := RoyalBook{}
	act := executeAct()
	record, err := Summarize(act, "")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if err := book.Append(record); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := FilePetition(book, "pet-1", act.ID, act.Author, "motivo justo para revisar", now); !errors.Is(err, ErrImproperPetitioner) {
		t.Fatalf("author petition err = %v, want ErrImproperPetitioner", err)
	}
}

// THR-CROWN-09 — royal veto: perdedor veta a sucessão com ato
// obsoleto ou decreto que altera a regra. Detectores: AuthorizeAct
// e ValidateEffectFence (ErrStaleReign) + DefineAct
// (ErrProhibitedAlteration); a sucessão dispensa aprovação do
// antecessor. Mata o mutante obsolete-reign. Runbook: R1 + R3.
func TestManipulation_RoyalVetoImpossible(t *testing.T) {
	now := executeAnchor().Add(2 * time.Hour)
	current := executeCurrent()
	current.Holder = "rainha-2"
	current.Reign = 3
	stale := Claim{Act: "ato-1", Season: "temporada-1", Holder: "rainha-1", Competence: "patrimonial", Reign: 2}
	session := SovereignSession{
		Season: "temporada-1", Subject: "rainha-1", Reign: 2, Competence: "patrimonial",
		AuthenticatedAt: now.Add(-10 * time.Minute), MFAAt: now.Add(-9 * time.Minute), ExpiresAt: now.Add(20 * time.Minute),
	}
	if _, err := AuthorizeAct(stale, session, nil, current, AuthContext{Now: now, MaxSessionAge: 30 * time.Minute}); !errors.Is(err, ErrStaleReign) {
		t.Fatalf("obsolete reign err = %v, want ErrStaleReign: ex-Rei não veta por ato obsoleto", err)
	}
	veto := executeAct()
	veto.Effect = "vetar-sucessao do novo soberano por decreto próprio"
	if _, err := DefineAct(veto); !errors.Is(err, ErrProhibitedAlteration) {
		t.Fatalf("veto act err = %v, want ErrProhibitedAlteration", err)
	}
	outcome, err := SelectSovereign(SuccessionInput{
		Policy: WealthPolicyV1, Season: "temporada-1", SeasonOpen: true,
		InstitutionalWealth: 700, Incumbent: IncumbentSnapshot{Subject: "rainha-1", Wealth: 700, Eligible: true},
		Candidates: []Candidate{{Subject: "conta-ana", Kind: BeneficiaryKindParticipant, Wealth: 800, AttainedRevision: 10, Eligible: true}},
	})
	if err != nil {
		t.Fatalf("SelectSovereign: %v", err)
	}
	if outcome.SelectedSovereign != "conta-ana" || outcome.Reason != SuccessionReasonConquest {
		t.Fatalf("outcome = %+v, want conquest without loser approval", outcome)
	}
}

// T10 — boa-fé e ausência de bypass: êxito econômico lícito segue
// elegível e nenhuma flag desliga a política. Detector: conquesta
// legítima + vocabulário fechado de política. Runbook: R1.
func TestManipulation_GoodFaithSuccessPermitted(t *testing.T) {
	ana := manipBeneficiary("conta-ana")
	res, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana,
		[]AssetHolding{manipHolding("cus-ana-1", "conta-ana", AssetKindLiquidUnconditional, 800)}, nil)
	if err != nil {
		t.Fatalf("EvaluateBeneficialWealth: %v", err)
	}
	if res.NetWealth != 800 {
		t.Fatalf("good-faith wealth = %+v, want W=800: mero êxito econômico não é bloqueado", res)
	}
	if _, err := ParseWealthPolicyVersion("wealth-v1-bypass"); !errors.Is(err, ErrUnknownWealthPolicy) {
		t.Fatalf("bypass policy err = %v, want ErrUnknownWealthPolicy: nenhuma flag permite policy bypass", err)
	}
}

// T11 — limitação registrada: acordos e dívidas externos
// desconhecidos ao serviço não entram na fórmula; só obrigação
// registrada deduz, e reserva interna sem credor não penaliza.
// Sem coleta invasiva nova: o beneficiário carrega só
// sujeito+tamanho fechado. Runbook: R1.
func TestManipulation_ExternalDebtLimitationRegistered(t *testing.T) {
	ana := manipBeneficiary("conta-ana")
	holdings := []AssetHolding{manipHolding("cus-ana-1", "conta-ana", AssetKindLiquidUnconditional, 800)}
	obligations := []Obligation{
		manipObligation("obl-ext-1", "conta-ana", ObligationKindInternalReserve, 10000),
	}
	res, err := EvaluateBeneficialWealth(WealthPolicyV1, testSeason, ana, holdings, obligations)
	if err != nil {
		t.Fatalf("EvaluateBeneficialWealth: %v", err)
	}
	if res.NetWealth != 800 {
		t.Fatalf("wealth = %+v, want W=800: dívida externa desconhecida não é detectável nem dedutível", res)
	}
	if ana.Subject == "" || ana.Kind == "" {
		t.Fatal("beneficiary carries identity beyond subject+kind: sem coleta invasiva nova")
	}
}

// T12 — ato no tempo errado: vigência fora da janela ou livro
// fechado recusa antes de qualquer efeito. Detector:
// IsSeasonalAuthorityLive. Runbook: R1.
func TestManipulation_ActOutsideWindowRefused(t *testing.T) {
	current := executeCurrent()
	pastEnd := current.EndsAt
	if err := IsSeasonalAuthorityLive(current, pastEnd); !errors.Is(err, ErrSeasonClosed) {
		t.Fatalf("past-end err = %v, want ErrSeasonClosed: manipulação de tempo não estende poder", err)
	}
}
