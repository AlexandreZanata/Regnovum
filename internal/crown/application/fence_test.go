package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

func fenceAnchor() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func fenceReign(holder string, reign int) domain.CurrentReign {
	anchor := fenceAnchor()
	return domain.CurrentReign{
		Season: "temporada-1", Holder: domain.HolderSubject(holder), Reign: domain.ReignVersion(reign),
		StartsAt: anchor, EndsAt: anchor.Add(7776000 * time.Second), Open: true,
	}
}

func fenceAct(id, author string, reign int, target, effect, reason string) domain.RoyalAct {
	anchor := fenceAnchor()
	return domain.RoyalAct{
		ID: domain.ActID(id), Author: domain.HolderSubject(author), Season: "temporada-1",
		Reign: domain.ReignVersion(reign), Competence: "patrimonial", Kind: domain.ActEconomic,
		Reason: reason, Target: target, Effect: effect,
		DecreedAt: anchor, Effective: anchor.Add(time.Hour),
		EndsAt:  anchor.Add(30 * 24 * time.Hour),
		Charter: "v3", Origin: "tesouro-livre", Amount: 500,
	}
}

func fenceClearance(t *testing.T, act domain.RoyalAct, reign domain.CurrentReign, now time.Time) domain.Clearance {
	t.Helper()
	digest, err := domain.ActDigest(act)
	if err != nil {
		t.Fatalf("ActDigest: %v", err)
	}
	checked := now.Add(-5 * time.Minute)
	first := domain.Approval{
		ID: "appr-1", Act: act.ID, Checker: "checker-1",
		Season: act.Season, Reign: act.Reign, Digest: digest,
		CheckedAt: checked, ExpiresAt: now.Add(time.Hour),
	}
	second := domain.Approval{
		ID: "appr-2", Act: act.ID, Checker: "checker-2",
		Season: act.Season, Reign: act.Reign, Digest: digest,
		CheckedAt: checked, ExpiresAt: now.Add(time.Hour),
	}
	clearance, err := domain.RequireIndependentCheck(act, first, second, reign, now)
	if err != nil {
		t.Fatalf("RequireIndependentCheck: %v", err)
	}
	return clearance
}

// 1. Old authenticated session of predecessor monarch is rejected.
func TestFenceNegativeMatrix_OldAuthenticatedSession(t *testing.T) {
	now := fenceAnchor().Add(2 * time.Hour)
	// Alice was Queen in Reign 1, but Bob is now invested in Reign 2:
	current := fenceReign("bob", 2)
	fake := &fakeReigns{current: current}
	uc := NewAuthorizeUseCase(fake)

	// Alice presents an old session from Reign 1:
	auth := now.Add(-10 * time.Minute)
	cmdOldReign := AuthorizeCommand{
		Act: "ato-alice-1", Season: "temporada-1", Holder: "alice",
		Competence: "cerimonial", Reign: 1,
		Session: domain.SovereignSession{
			Season: "temporada-1", Subject: "alice", Reign: 1,
			Competence: "cerimonial", AuthenticatedAt: auth,
			MFAAt: auth.Add(time.Minute), ExpiresAt: auth.Add(20 * time.Minute),
		},
		Now: now, MaxSessionAge: 30 * time.Minute,
	}
	if _, err := uc.Execute(context.Background(), cmdOldReign); !errors.Is(err, domain.ErrStaleReign) {
		t.Fatalf("old reign claim err = %v, want ErrStaleReign", err)
	}

	// Alice tries claiming Reign 2 without investiture:
	cmdFabricatedReign := AuthorizeCommand{
		Act: "ato-alice-2", Season: "temporada-1", Holder: "alice",
		Competence: "cerimonial", Reign: 2,
		Session: domain.SovereignSession{
			Season: "temporada-1", Subject: "alice", Reign: 2,
			Competence: "cerimonial", AuthenticatedAt: auth,
			MFAAt: auth.Add(time.Minute), ExpiresAt: auth.Add(20 * time.Minute),
		},
		Now: now, MaxSessionAge: 30 * time.Minute,
	}
	if _, err := uc.Execute(context.Background(), cmdFabricatedReign); !errors.Is(err, domain.ErrNotHolder) {
		t.Fatalf("uninvested holder claim err = %v, want ErrNotHolder", err)
	}
}

// 2. Act approved before takeover is rejected at effect time and cannot execute under the new reign.
func TestFenceNegativeMatrix_ActApprovedBeforeTakeover(t *testing.T) {
	now := fenceAnchor().Add(2 * time.Hour)
	reign1 := fenceReign("alice", 1)
	act1 := fenceAct("decreto-alice", "alice", 1, "conta/charlie", "reparacao autorizada", "justo motivo")
	clearance1 := fenceClearance(t, act1, reign1, now)

	// Succession occurs: Bob is now sovereign in Reign 2:
	reign2 := fenceReign("bob", 2)
	fakeR := &fakeReigns{current: reign2}
	fakeC := &fakeCustody{snapshot: CustodySnapshot{Available: 10000, Frozen: false}}
	fakeL := &fakeLedger{}
	uc := NewExecuteUseCase(fakeR, fakeC, fakeL)

	// Attempting to execute Alice's approved decree under Reign 2:
	cmd := ExecuteCommand{
		Act:         act1,
		Clearance:   clearance1,
		Beneficiary: "charlie",
		Now:         now.Add(10 * time.Minute),
	}
	if _, err := uc.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrStaleReign) && !errors.Is(err, domain.ErrNotHolder) {
		t.Fatalf("execute act approved before takeover err = %v, want ErrStaleReign or ErrNotHolder", err)
	}

	// Attempting to execute decree under Bob if modified to Reign 2 with clearance 1 fails tampered check:
	actModified := fenceAct("decreto-alice", "bob", 2, "conta/charlie", "reparacao autorizada", "justo motivo")
	cmdTampered := ExecuteCommand{
		Act:         actModified,
		Clearance:   clearance1,
		Beneficiary: "charlie",
		Now:         now.Add(10 * time.Minute),
	}
	if _, err := uc.Execute(context.Background(), cmdTampered); !errors.Is(err, domain.ErrTamperedAct) {
		t.Fatalf("execute with mismatched clearance err = %v, want ErrTamperedAct", err)
	}
}

// 3. Technical administrator credential confused with game sovereignty is rejected.
func TestFenceNegativeMatrix_TechnicalCredentialConfusedWithGame(t *testing.T) {
	now := fenceAnchor().Add(time.Hour)
	current := fenceReign("operator-root", 1)
	fake := &fakeReigns{current: current}
	uc := NewAuthorizeUseCase(fake)

	auth := now.Add(-5 * time.Minute)
	cmd := AuthorizeCommand{
		Act: "ato-admin", Season: "temporada-1", Holder: "operator-root",
		Competence: "cerimonial", Reign: 1,
		Operator: true, // technical credential arrives
		Session: domain.SovereignSession{
			Season: "temporada-1", Subject: "operator-root", Reign: 1,
			Competence: "cerimonial", AuthenticatedAt: auth,
			MFAAt: auth.Add(time.Minute), ExpiresAt: auth.Add(20 * time.Minute),
		},
		Now: now, MaxSessionAge: 30 * time.Minute,
	}
	if _, err := uc.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrOperatorNotSovereign) {
		t.Fatalf("operator sovereign claim err = %v, want ErrOperatorNotSovereign", err)
	}
}

// 4. Freeze bypass is rejected at execution time.
func TestFenceNegativeMatrix_FreezeBypass(t *testing.T) {
	now := fenceAnchor().Add(2 * time.Hour)
	current := fenceReign("bob", 2)
	act := fenceAct("decreto-bob", "bob", 2, "conta/charlie", "reparacao autorizada", "justo motivo")
	clearance := fenceClearance(t, act, current, now)

	fakeR := &fakeReigns{current: current}
	fakeC := &fakeCustody{snapshot: CustodySnapshot{Available: 10000, Frozen: true}} // Book is frozen!
	fakeL := &fakeLedger{}
	uc := NewExecuteUseCase(fakeR, fakeC, fakeL)

	cmd := ExecuteCommand{
		Act:         act,
		Clearance:   clearance,
		Beneficiary: "charlie",
		Now:         now.Add(10 * time.Minute),
	}
	if _, err := uc.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrExecutionFrozen) {
		t.Fatalf("execute during freeze err = %v, want ErrExecutionFrozen", err)
	}
}

// 5. Self-grant is rejected.
func TestFenceNegativeMatrix_SelfGrant(t *testing.T) {
	now := fenceAnchor().Add(2 * time.Hour)
	current := fenceReign("bob", 2)
	act := fenceAct("decreto-bob", "bob", 2, "conta/bob", "reparacao propria", "autobeneficio")
	clearance := fenceClearance(t, act, current, now)

	fakeR := &fakeReigns{current: current}
	fakeC := &fakeCustody{snapshot: CustodySnapshot{Available: 10000, Frozen: false}}
	fakeL := &fakeLedger{}
	uc := NewExecuteUseCase(fakeR, fakeC, fakeL)

	cmd := ExecuteCommand{
		Act:         act,
		Clearance:   clearance,
		Beneficiary: "bob", // author granting funds to self
		Now:         now.Add(10 * time.Minute),
	}
	if _, err := uc.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrSelfGrant) {
		t.Fatalf("self-grant err = %v, want ErrSelfGrant", err)
	}
}

// 6. Prohibited alteration of season duration, genesis supply, wealth criteria or succession rules.
func TestFenceNegativeMatrix_ProhibitedAlterations(t *testing.T) {
	forbiddenScenarios := []struct {
		name   string
		target string
		effect string
		reason string
	}{
		{"alterar-prazo", "sistema/calendario", "alterar-prazo para 120 dias", "estender prazo da season"},
		{"prorrogar-temporada", "sistema/temporada", "prorrogar-temporada por mais 1 mes", "emergencia de calendario"},
		{"alterar-genesis", "sistema/oferta", "alterar-genesis supply para 3 bilhoes", "aumentar caixa do tesouro"},
		{"alterar-riqueza", "sistema/regras", "alterar-riqueza para excluir ana", "mudar-limiar de elegibilidade"},
		{"vetar-sucessao", "sistema/trono", "vetar-sucessao automatica", "impedir-sucessor de assumir"},
	}

	for _, sc := range forbiddenScenarios {
		t.Run(sc.name, func(t *testing.T) {
			act := fenceAct("decreto-bad", "bob", 2, sc.target, sc.effect, sc.reason)
			if _, err := domain.DefineAct(act); !errors.Is(err, domain.ErrProhibitedAlteration) {
				t.Fatalf("DefineAct(%s) err = %v, want ErrProhibitedAlteration", sc.name, err)
			}
		})
	}
}

// 7. Stale delegation from predecessor monarch is rejected under new reign.
func TestFenceNegativeMatrix_StaleDelegation(t *testing.T) {
	now := fenceAnchor().Add(2 * time.Hour)
	// Alice delegated to Charlie during Reign 1:
	delegation := &domain.Delegation{
		Delegator:  "alice",
		Delegate:   "charlie",
		Season:     "temporada-1",
		Competence: "patrimonial",
		Reign:      1,
		ExpiresAt:  now.Add(24 * time.Hour),
	}

	// Now Bob is sovereign in Reign 2:
	current := fenceReign("bob", 2)
	fakeR := &fakeReigns{current: current}
	uc := NewAuthorizeUseCase(fakeR)

	auth := now.Add(-5 * time.Minute)
	cmd := AuthorizeCommand{
		Act: "ato-delegado", Season: "temporada-1", Holder: "charlie",
		Competence: "patrimonial", Reign: 2,
		Delegation: delegation, // Stale delegation from Alice (Reign 1)
		Session: domain.SovereignSession{
			Season: "temporada-1", Subject: "charlie", Reign: 2,
			Competence: "patrimonial", AuthenticatedAt: auth,
			MFAAt: auth.Add(time.Minute), ExpiresAt: auth.Add(20 * time.Minute),
		},
		Now: now, MaxSessionAge: 30 * time.Minute,
	}
	if _, err := uc.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidDelegation) {
		t.Fatalf("stale delegation authorize err = %v, want ErrInvalidDelegation", err)
	}

	// ValidateEffectFence also refuses the stale delegation directly:
	fence := domain.EffectFence{
		Season:               "temporada-1",
		Reign:                2,
		Competence:           "patrimonial",
		Author:               "charlie",
		Delegation:           delegation,
		CurrentReign:         current,
		EconomicBacklogClean: true,
		Now:                  now,
	}
	if err := domain.ValidateEffectFence(fence); !errors.Is(err, domain.ErrInvalidDelegation) {
		t.Fatalf("ValidateEffectFence with stale delegation err = %v, want ErrInvalidDelegation", err)
	}
}

// 8. Conflicted proceeding without independent review is rejected.
func TestFenceNegativeMatrix_ConflictedProceeding(t *testing.T) {
	now := fenceAnchor().Add(2 * time.Hour)
	current := fenceReign("bob", 2)
	act := fenceAct("decreto-bob", "bob", 2, "conta/david", "reparacao objeto de litígio", "motivo regular")
	clearance := fenceClearance(t, act, current, now)

	fakeR := &fakeReigns{current: current}
	fakeC := &fakeCustody{snapshot: CustodySnapshot{Available: 10000, Frozen: false}}
	fakeL := &fakeLedger{}
	uc := NewExecuteUseCase(fakeR, fakeC, fakeL)

	// Conflict pending but review points to self:
	cmdSelfReview := ExecuteCommand{
		Act:               act,
		Clearance:         clearance,
		Beneficiary:       "david",
		ConflictPending:   true,
		IndependentReview: string(act.ID), // points to self
		Now:               now.Add(10 * time.Minute),
	}
	if _, err := uc.Execute(context.Background(), cmdSelfReview); !errors.Is(err, domain.ErrConflictedBenefit) {
		t.Fatalf("conflicted benefit with self-review err = %v, want ErrConflictedBenefit", err)
	}

	// Conflict pending with blank review:
	cmdBlankReview := ExecuteCommand{
		Act:               act,
		Clearance:         clearance,
		Beneficiary:       "david",
		ConflictPending:   true,
		IndependentReview: "",
		Now:               now.Add(10 * time.Minute),
	}
	if _, err := uc.Execute(context.Background(), cmdBlankReview); !errors.Is(err, domain.ErrConflictedBenefit) {
		t.Fatalf("conflicted benefit with blank review err = %v, want ErrConflictedBenefit", err)
	}
}

// 9. Unevaluated economic backlog blocks royal effects.
func TestFenceNegativeMatrix_UnevaluatedBacklog(t *testing.T) {
	now := fenceAnchor().Add(2 * time.Hour)
	current := fenceReign("bob", 2)

	// EffectFence with dirty backlog halts:
	fence := domain.EffectFence{
		Season:               "temporada-1",
		Reign:                2,
		Competence:           "patrimonial",
		Author:               "bob",
		Delegation:           nil,
		CurrentReign:         current,
		EconomicBacklogClean: false, // backlog is dirty / unevaluated!
		Now:                  now,
	}
	if err := domain.ValidateEffectFence(fence); !errors.Is(err, domain.ErrBacklogUnevaluated) {
		t.Fatalf("ValidateEffectFence with backlog err = %v, want ErrBacklogUnevaluated", err)
	}

	// When ReignResolver reports unevaluated backlog, ExecuteUseCase halts:
	fakeR := &fakeReigns{err: domain.ErrBacklogUnevaluated}
	fakeC := &fakeCustody{snapshot: CustodySnapshot{Available: 10000, Frozen: false}}
	fakeL := &fakeLedger{}
	uc := NewExecuteUseCase(fakeR, fakeC, fakeL)

	act := fenceAct("decreto-bob", "bob", 2, "conta/david", "reparacao objeto de litígio", "motivo regular")
	cmd := ExecuteCommand{
		Act:         act,
		Clearance:   domain.Clearance{Act: act.ID, Season: act.Season, Reign: 2},
		Beneficiary: "david",
		Now:         now.Add(10 * time.Minute),
	}
	if _, err := uc.Execute(context.Background(), cmd); !errors.Is(err, domain.ErrBacklogUnevaluated) {
		t.Fatalf("ExecuteUseCase with backlog err = %v, want ErrBacklogUnevaluated", err)
	}
}
