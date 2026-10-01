package domain

import (
	"errors"
	"testing"
	"time"
)

func checkAnchor() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func checkAct() RoyalAct {
	anchor := checkAnchor()
	return RoyalAct{
		ID: "decreto-9", Author: "rainha-1", Season: "temporada-1",
		Reign: 2, Competence: "patrimonial", Kind: ActEconomic,
		Reason: "reparacao devida com origem identificada",
		Target: "conta/ana", Effect: "transferir 1000 de tesouro-livre sem mint",
		DecreedAt: anchor, Effective: anchor.Add(time.Hour),
		EndsAt:  anchor.Add(30 * 24 * time.Hour),
		Charter: "v3", Origin: "tesouro-livre", Amount: 1000,
	}
}

func checkCurrent() CurrentReign {
	anchor := checkAnchor()
	return CurrentReign{
		Season: "temporada-1", Holder: "rainha-1", Reign: 2,
		StartsAt: anchor, EndsAt: anchor.Add(7776000 * time.Second), Open: true,
	}
}

func checkApproval(id, checker string, act RoyalAct, checked, expires time.Time) Approval {
	digest, err := ActDigest(act)
	if err != nil {
		panic(err)
	}
	return Approval{
		ID: ApprovalID(id), Act: act.ID, Checker: HolderSubject(checker),
		Season: act.Season, Reign: act.Reign, Digest: digest,
		CheckedAt: checked, ExpiresAt: expires,
	}
}

func TestIndependentCheckAuthorizesFinalExecution(t *testing.T) {
	act := checkAct()
	now := checkAnchor().Add(2 * time.Hour)
	checked := now.Add(-10 * time.Minute)
	first := checkApproval("conf-1", "auditor-1", act, checked, now.Add(time.Hour))
	second := checkApproval("conf-2", "auditor-2", act, checked, now.Add(30*time.Minute))
	clearance, err := RequireIndependentCheck(act, first, second, checkCurrent(), now)
	if err != nil {
		t.Fatalf("RequireIndependentCheck: %v", err)
	}
	if clearance.Act != act.ID || clearance.Reign != act.Reign || clearance.Digest != first.Digest {
		t.Fatalf("clearance = %+v, want the sealed act", clearance)
	}
	if !clearance.ExpiresAt.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("expiry = %v, want the earlier check", clearance.ExpiresAt)
	}
}

func TestIndependentCheckRefusesSelfApproval(t *testing.T) {
	act := checkAct()
	now := checkAnchor().Add(2 * time.Hour)
	checked := now.Add(-10 * time.Minute)
	self := checkApproval("conf-1", "rainha-1", act, checked, now.Add(time.Hour))
	other := checkApproval("conf-2", "auditor-2", act, checked, now.Add(time.Hour))
	if _, err := RequireIndependentCheck(act, self, other, checkCurrent(), now); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("self = %v, want ErrSelfApproval", err)
	}
}

func TestIndependentCheckRefusesIdenticalAndReplay(t *testing.T) {
	act := checkAct()
	now := checkAnchor().Add(2 * time.Hour)
	checked := now.Add(-10 * time.Minute)
	first := checkApproval("conf-1", "auditor-1", act, checked, now.Add(time.Hour))
	sameID := checkApproval("conf-1", "auditor-2", act, checked, now.Add(time.Hour))
	if _, err := RequireIndependentCheck(act, first, sameID, checkCurrent(), now); !errors.Is(err, ErrDuplicateApproval) {
		t.Fatalf("same id = %v, want ErrDuplicateApproval", err)
	}
	sameChecker := checkApproval("conf-2", "auditor-1", act, checked, now.Add(time.Hour))
	if _, err := RequireIndependentCheck(act, first, sameChecker, checkCurrent(), now); !errors.Is(err, ErrDuplicateApproval) {
		t.Fatalf("same checker = %v, want ErrDuplicateApproval", err)
	}
}

func TestIndependentCheckRefusesAlteredPayload(t *testing.T) {
	act := checkAct()
	now := checkAnchor().Add(2 * time.Hour)
	checked := now.Add(-10 * time.Minute)
	first := checkApproval("conf-1", "auditor-1", act, checked, now.Add(time.Hour))
	second := checkApproval("conf-2", "auditor-2", act, checked, now.Add(time.Hour))
	mutated := act
	mutated.Effect = "transferir 9000 de tesouro-livre sem mint"
	if _, err := RequireIndependentCheck(mutated, first, second, checkCurrent(), now); !errors.Is(err, ErrTamperedAct) {
		t.Fatalf("mutated = %v, want ErrTamperedAct", err)
	}
}

func TestIndependentCheckRefusesExpiryAndStaleReign(t *testing.T) {
	act := checkAct()
	now := checkAnchor().Add(2 * time.Hour)
	checked := now.Add(-time.Hour)
	spent := checkApproval("conf-1", "auditor-1", act, checked, now.Add(-time.Nanosecond))
	live := checkApproval("conf-2", "auditor-2", act, checked, now.Add(time.Hour))
	if _, err := RequireIndependentCheck(act, spent, live, checkCurrent(), now); !errors.Is(err, ErrCheckExpired) {
		t.Fatalf("spent = %v, want ErrCheckExpired", err)
	}
	freshChecked := now.Add(-10 * time.Minute)
	first := checkApproval("conf-1", "auditor-1", act, freshChecked, now.Add(time.Hour))
	second := checkApproval("conf-2", "auditor-2", act, freshChecked, now.Add(time.Hour))
	moved := checkCurrent()
	moved.Holder = "rainha-2"
	moved.Reign = 3
	if _, err := RequireIndependentCheck(act, first, second, moved, now); !errors.Is(err, ErrStaleReign) {
		t.Fatalf("stale reign = %v, want ErrStaleReign: pending acts need fresh validation", err)
	}
}

func TestIndependentCheckRefusesEmergencyWithoutReview(t *testing.T) {
	act := checkAct()
	now := checkAnchor().Add(2 * time.Hour)
	checked := now.Add(-10 * time.Minute)
	first := checkApproval("conf-1", "auditor-1", act, checked, now.Add(time.Hour))
	second := checkApproval("conf-2", "auditor-2", act, checked, now.Add(time.Hour))
	first.Emergency = true
	if _, err := RequireIndependentCheck(act, first, second, checkCurrent(), now); !errors.Is(err, ErrEmergencyWithoutReview) {
		t.Fatalf("urgent without review = %v, want ErrEmergencyWithoutReview", err)
	}
	first.ReviewDue = now.Add(24 * time.Hour)
	second.Emergency = true
	second.ReviewDue = now.Add(24 * time.Hour)
	if _, err := RequireIndependentCheck(act, first, second, checkCurrent(), now); err != nil {
		t.Fatalf("urgent containment with review: %v", err)
	}
}
