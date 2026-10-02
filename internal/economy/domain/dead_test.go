package domain

import (
	"errors"
	"testing"
	"time"
)

func deadAnchor() time.Time {
	return time.Date(2026, time.February, 8, 10, 0, 0, 0, time.UTC)
}

func openDead(total int64, heir string) DeadSettlement {
	set, err := OpenDeadSettlement(OpenDeadRequest{
		ID: "liquidacao-1", Account: "ana", Season: "temporada-1",
		TotalMilli: total, Heir: heir, FiatDebt: true, Defense: true,
	})
	if err != nil {
		panic(err)
	}
	return set
}

func preserveDead(set DeadSettlement, key string) DeadSettlement {
	next, err := PreserveDeadData(set, PreserveRequest{
		Key: key, ExportRef: "exportacao-1",
		Season: "temporada-1", At: deadAnchor().Add(time.Hour),
	})
	if err != nil {
		panic(err)
	}
	return next
}

func deadEffect(key, step string, amount int64, dest string) EffectRequest {
	return EffectRequest{
		Key: key, StepRaw: step, AmountMilli: amount,
		Destination: dest, Season: "temporada-1",
		At: deadAnchor().Add(2 * time.Hour),
	}
}

func deadResidual(key string, amount int64, dest string) EffectRequest {
	return EffectRequest{
		Key: key, StepRaw: "residual", AmountMilli: amount,
		Destination: dest, Season: "temporada-1", Basis: "contrato",
		NoticeAt:    deadAnchor().Add(3 * time.Hour),
		AppealDueAt: deadAnchor().Add(4 * time.Hour),
		At:          deadAnchor().Add(5 * time.Hour),
	}
}

func applyDead(set DeadSettlement, req EffectRequest) DeadSettlement {
	next, err := ApplyDeadEffect(set, req)
	if err != nil {
		panic(err)
	}
	return next
}

func TestOrderedDeadSettlementConserves(t *testing.T) {
	set := preserveDead(openDead(10000, "herdeiro-1"), "passo-dados")
	set = applyDead(set, deadEffect("passo-terceiro", "custodia-terceiros", 1000, "terceiro-1"))
	set = applyDead(set, deadEffect("passo-reembolso", "reembolsos", 2000, "provedor-1"))
	set = applyDead(set, deadEffect("passo-obrigacao", "obrigacoes", 1500, "credor-1"))
	set = applyDead(set, deadEffect("passo-principal", "principal-direito", 3000, "herdeiro-1"))
	set = applyDead(set, deadResidual("passo-residual", 2000, "beneficiario-1"))
	if ConservedTotal(set) != 10000 || set.RemainMilli != 500 {
		t.Fatalf("settlement = %+v, want moved 9500 with 500 held and total conserved", set)
	}
	if !set.FiatDebt || !set.DefenseOpen {
		t.Fatalf("settlement = %+v, want fiat debt and defense surviving closure", set)
	}
	if !set.Preserved || !set.ThirdDone || !set.RefundsDone || !set.DebtsDone || !set.PrincipalOn {
		t.Fatalf("settlement = %+v, want the ordered steps all recorded", set)
	}
	early := preserveDead(openDead(10000, ""), "dados-2")
	if _, err := ApplyDeadEffect(early, deadEffect("fura-fila", "reembolsos", 100, "provedor-1")); !errors.Is(err, ErrDeadOutOfOrder) {
		t.Fatalf("skipped third = %v, want ErrDeadOutOfOrder", err)
	}
}

func TestBoughtNeverConfiscatedAndThirdNeverTreasury(t *testing.T) {
	set := preserveDead(openDead(10000, "herdeiro-1"), "dados-3")
	if _, err := ApplyDeadEffect(set, deadEffect("terceiro-tesouro", "custodia-terceiros", 100, "tesouro")); !errors.Is(err, ErrInvalidDeadSettlement) {
		t.Fatalf("third to treasury = %v, want ErrInvalidDeadSettlement", err)
	}
	set = applyDead(set, deadEffect("terceiro-ok", "custodia-terceiros", 1000, "terceiro-1"))
	if _, err := ApplyDeadEffect(set, deadEffect("reembolso-tesouro", "reembolsos", 100, "treasury")); !errors.Is(err, ErrInvalidDeadSettlement) {
		t.Fatalf("refund to treasury = %v, want ErrInvalidDeadSettlement", err)
	}
	set = applyDead(set, deadEffect("reembolso-ok", "reembolsos", 2000, "provedor-1"))
	if _, err := ApplyDeadEffect(set, deadEffect("obrigacao-tesouro", "obrigacoes", 100, "tesouro-livre")); !errors.Is(err, ErrInvalidDeadSettlement) {
		t.Fatalf("obligation to treasury = %v, want ErrInvalidDeadSettlement", err)
	}
	set = applyDead(set, deadEffect("obrigacao-ok", "obrigacoes", 1500, "credor-1"))
	if _, err := ApplyDeadEffect(set, deadEffect("principal-tesouro", "principal-direito", 100, "tesouro")); !errors.Is(err, ErrInvalidDeadSettlement) {
		t.Fatalf("principal to treasury = %v, want bought never confiscated", err)
	}
	if _, err := ApplyDeadEffect(set, deadEffect("principal-estranho", "principal-direito", 3000, "estranho-1")); !errors.Is(err, ErrInvalidDeadSettlement) {
		t.Fatalf("principal past heir = %v, want the applicable heir", err)
	}
	bare := preserveDead(openDead(5000, ""), "dados-4")
	bare = applyDead(bare, deadEffect("terceiro-4", "custodia-terceiros", 500, "terceiro-1"))
	bare = applyDead(bare, deadEffect("reembolso-4", "reembolsos", 500, "provedor-1"))
	bare = applyDead(bare, deadEffect("obrigacao-4", "obrigacoes", 500, "credor-1"))
	bare = applyDead(bare, deadEffect("principal-4", "principal-direito", 1000, "ana"))
	if bare.RemainMilli != 2500 || ConservedTotal(bare) != 5000 {
		t.Fatalf("heirless = %+v, want principal to the account with total conserved", bare)
	}
}

func TestChargebackDebtAndHeirSurvive(t *testing.T) {
	set := preserveDead(openDead(6000, "herdeiro-1"), "dados-5")
	set = applyDead(set, deadEffect("terceiro-5", "custodia-terceiros", 1000, "terceiro-1"))
	set = applyDead(set, deadEffect("reembolso-5", "reembolsos", 1000, "provedor-1"))
	set = applyDead(set, deadEffect("obrigacao-5", "obrigacoes", 1000, "credor-1"))
	set = applyDead(set, deadEffect("principal-5", "principal-direito", 1000, "herdeiro-1"))
	set = applyDead(set, deadResidual("residual-5", 1000, "beneficiario-1"))
	if !set.FiatDebt || !set.DefenseOpen {
		t.Fatalf("settlement = %+v, want chargeback debt and defense never erased", set)
	}
	if ConservedTotal(set) != 6000 {
		t.Fatalf("settlement = %+v, want no minted repair", set)
	}
	if _, err := OpenDeadSettlement(OpenDeadRequest{
		ID: "liquidacao-6", Account: "ana", Season: "temporada-1",
		TotalMilli: 1000, Heir: "ana",
	}); !errors.Is(err, ErrInvalidDeadSettlement) {
		t.Fatalf("self heir = %v, want ErrInvalidDeadSettlement", err)
	}
	if _, err := OpenDeadSettlement(OpenDeadRequest{
		ID: "liquidacao-7", Account: "ana", Season: "temporada-1",
		TotalMilli: 1000, Heir: "tesouro",
	}); !errors.Is(err, ErrInvalidDeadSettlement) {
		t.Fatalf("treasury heir = %v, want ErrInvalidDeadSettlement", err)
	}
}

func TestCrashAtEachStepReplaysSingleState(t *testing.T) {
	set := preserveDead(openDead(10000, "herdeiro-1"), "dados-8")
	replay, err := PreserveDeadData(set, PreserveRequest{
		Key: "dados-8", ExportRef: "exportacao-1",
		Season: "temporada-1", At: deadAnchor().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("preserve replay: %v", err)
	}
	if len(replay.Effects) != len(set.Effects) {
		t.Fatalf("replay = %+v, want the same single preserved state", replay)
	}
	if _, err := PreserveDeadData(set, PreserveRequest{
		Key: "dados-8", ExportRef: "outra-exportacao",
		Season: "temporada-1", At: deadAnchor().Add(time.Hour),
	}); !errors.Is(err, ErrDeadConflict) {
		t.Fatalf("divergent preserve = %v, want ErrDeadConflict", err)
	}
	requests := []EffectRequest{
		deadEffect("terceiro-8", "custodia-terceiros", 1000, "terceiro-1"),
		deadEffect("reembolso-8", "reembolsos", 2000, "provedor-1"),
		deadEffect("obrigacao-8", "obrigacoes", 1500, "credor-1"),
		deadEffect("principal-8", "principal-direito", 3000, "herdeiro-1"),
		deadResidual("residual-8", 2000, "beneficiario-1"),
	}
	for _, req := range requests {
		before := len(set.Effects)
		kept := set.RemainMilli
		set = applyDead(set, req)
		again, err := ApplyDeadEffect(set, req)
		if err != nil {
			t.Fatalf("replay %s: %v", req.Key, err)
		}
		if len(again.Effects) != before+1 || again.RemainMilli != kept-req.AmountMilli {
			t.Fatalf("replay %s = %+v, want one effect only", req.Key, again)
		}
		divergent := req
		divergent.AmountMilli++
		if _, err := ApplyDeadEffect(set, divergent); !errors.Is(err, ErrDeadConflict) {
			t.Fatalf("divergent %s = %v, want ErrDeadConflict", req.Key, err)
		}
	}
	if ConservedTotal(set) != 10000 {
		t.Fatalf("settlement = %+v, want crash recovery conserving the total", set)
	}
}

func TestResidualNeedsBasisNoticeAppealAndSeason(t *testing.T) {
	set := preserveDead(openDead(8000, ""), "dados-9")
	set = applyDead(set, deadEffect("terceiro-9", "custodia-terceiros", 1000, "terceiro-1"))
	set = applyDead(set, deadEffect("reembolso-9", "reembolsos", 1000, "provedor-1"))
	set = applyDead(set, deadEffect("obrigacao-9", "obrigacoes", 1000, "credor-1"))
	set = applyDead(set, deadEffect("principal-9", "principal-direito", 1000, "ana"))
	bare := deadEffect("residual-9", "residual", 500, "beneficiario-1")
	bare.StepRaw = "residual"
	if _, err := ApplyDeadEffect(set, bare); !errors.Is(err, ErrDeadResidualBlocked) {
		t.Fatalf("baseless residual = %v, want ErrDeadResidualBlocked", err)
	}
	early := deadResidual("residual-10", 500, "beneficiario-1")
	early.At = deadAnchor().Add(3*time.Hour + 30*time.Minute)
	if _, err := ApplyDeadEffect(set, early); !errors.Is(err, ErrDeadResidualBlocked) {
		t.Fatalf("residual before appeal = %v, want ErrDeadResidualBlocked", err)
	}
	crossed := deadResidual("residual-11", 500, "beneficiario-1")
	crossed.Season = "temporada-2"
	if _, err := ApplyDeadEffect(set, crossed); !errors.Is(err, ErrCrossSeason) {
		t.Fatalf("cross-season residual = %v, want ErrCrossSeason", err)
	}
	if _, err := ApplyDeadEffect(set, deadEffect("", "residual", 500, "beneficiario-1")); !errors.Is(err, ErrInvalidDeadSettlement) {
		t.Fatalf("blank key residual = %v, want the shape refused before funds", err)
	}
	if _, err := ApplyDeadEffect(set, deadResidual("residual-12", 5000, "beneficiario-1")); !errors.Is(err, ErrInsufficientMilliInk) {
		t.Fatalf("residual beyond remainder = %v, want ErrInsufficientMilliInk", err)
	}
	if _, err := OpenDeadSettlement(OpenDeadRequest{
		ID: "liquidacao-9", Account: "ana", Season: "temporada-1",
		TotalMilli: 1000, Sealed: true,
	}); !errors.Is(err, ErrBookSealed) {
		t.Fatalf("sealed book = %v, want ErrBookSealed", err)
	}
}
