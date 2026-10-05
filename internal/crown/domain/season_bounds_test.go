package domain

import (
	"errors"
	"testing"
	"time"
)

func boundsWindow() (start, end time.Time) {
	start = time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	end = start.Add(SeasonalWindowSeconds * time.Second)
	return start, end
}

func boundsInput() InitialInvestitureInput {
	start, end := boundsWindow()
	return InitialInvestitureInput{
		Policy:            WealthPolicyV1,
		Season:            "temporada-2",
		StartsAt:          start,
		EndsAt:            end,
		PredecessorSealed: true,
		InitialMonarch:    "fundadora",
		Regent:            "regente-tecnica",
		MonarchPresent:    true,
		MonarchEligible:   true,
	}
}

func TestDecideInitialReign_FounderEligible(t *testing.T) {
	out, err := DecideInitialReign(boundsInput())
	if err != nil {
		t.Fatalf("DecideInitialReign: %v", err)
	}
	if out.Holder != "fundadora" || out.IsRegent || out.Reason == SuccessionReasonConquest {
		t.Fatalf("founder outcome = %+v, want fundadora without conquest", out)
	}
	if out.Reason != SuccessionReasonInitialInvestiture {
		t.Fatalf("reason = %q, want initial-investiture (never fictitious victory)", out.Reason)
	}
	if out.Reign != 1 || out.WinningWealth != 0 || out.AttainedRevision != 0 {
		t.Fatalf("initial reign must be v1 with zero wealth, got %+v", out)
	}
	if out.Policy != WealthPolicyV1 || out.Predecessor != "" {
		t.Fatalf("policy/predecessor = %+v, want registered policy and empty predecessor", out)
	}
}

func TestDecideInitialReign_FounderIneligibleUsesRegency(t *testing.T) {
	in := boundsInput()
	in.MonarchEligible = false
	out, err := DecideInitialReign(in)
	if err != nil {
		t.Fatalf("DecideInitialReign: %v", err)
	}
	if out.Holder != "regente-tecnica" || !out.IsRegent || out.Reason != SuccessionReasonRegency {
		t.Fatalf("regency outcome = %+v, want limited regent", out)
	}
	if out.Predecessor != "fundadora" {
		t.Fatalf("regency predecessor = %q, want founder link", out.Predecessor)
	}
}

func TestDecideInitialReign_SealMissingBlocksEconomy(t *testing.T) {
	in := boundsInput()
	in.PredecessorSealed = false
	if _, err := DecideInitialReign(in); !errors.Is(err, ErrSeasonClosed) {
		t.Fatalf("unsealed predecessor err = %v, want ErrSeasonClosed (sem selo não há sucessora)", err)
	}
}

func TestDecideInitialReign_AlteredWindowRefuses(t *testing.T) {
	in := boundsInput()
	in.EndsAt = in.EndsAt.Add(24 * time.Hour)
	if _, err := DecideInitialReign(in); !errors.Is(err, ErrProhibitedAlteration) {
		t.Fatalf("stretched window err = %v, want ErrProhibitedAlteration", err)
	}
	shortened := boundsInput()
	shortened.EndsAt = shortened.EndsAt.Add(-time.Second)
	if _, err := DecideInitialReign(shortened); !errors.Is(err, ErrProhibitedAlteration) {
		t.Fatalf("shortened window err = %v, want ErrProhibitedAlteration", err)
	}
}

func TestSeasonalAuthorityLive_FinalTickBelongsOut(t *testing.T) {
	start, end := boundsWindow()
	current := CurrentReign{
		Season: "temporada-1", Holder: "alice", Reign: 2,
		StartsAt: start, EndsAt: end, Open: true,
	}
	if err := IsSeasonalAuthorityLive(current, end.Add(-time.Nanosecond)); err != nil {
		t.Fatalf("tick before end err = %v, want live", err)
	}
	if err := IsSeasonalAuthorityLive(current, end); !errors.Is(err, ErrSeasonClosed) {
		t.Fatalf("exact end err = %v, want ErrSeasonClosed (tick final não decide)", err)
	}
}

func TestGuardSuccessionAgainstClose_ConcurrentCloseWins(t *testing.T) {
	_, end := boundsWindow()
	now := end.Add(-time.Hour)
	if err := GuardSuccessionAgainstClose(true, end, now, true); err != nil {
		t.Fatalf("live guard err = %v, want nil", err)
	}
	if err := GuardSuccessionAgainstClose(false, end, now, true); !errors.Is(err, ErrSeasonClosed) {
		t.Fatalf("closed guard err = %v, want ErrSeasonClosed (sucessão × fecho: fecho vence)", err)
	}
	if err := GuardSuccessionAgainstClose(true, end, now, false); !errors.Is(err, ErrBacklogUnevaluated) {
		t.Fatalf("backlog guard err = %v, want ErrBacklogUnevaluated", err)
	}
	if err := GuardSuccessionAgainstClose(true, end, end, true); !errors.Is(err, ErrSeasonClosed) {
		t.Fatalf("cutoff guard err = %v, want ErrSeasonClosed", err)
	}
}

func TestTerminateReignAtCutoff_TechnicalOnlyAfterEnd(t *testing.T) {
	start, end := boundsWindow()
	current := CurrentReign{
		Season: "temporada-1", Holder: "alice", Reign: 1,
		StartsAt: start, EndsAt: end, Open: true,
	}
	live, technical := TerminateReignAtCutoff(current, end.Add(-time.Hour))
	if technical || !live.Open {
		t.Fatalf("live cutoff = %+v technical=%v, want open reign", live, technical)
	}
	closed, technical := TerminateReignAtCutoff(current, end)
	if !technical || closed.Open {
		t.Fatalf("past cutoff = %+v technical=%v, want closed with technical-only liquidation", closed, technical)
	}
}

func TestAssertSingleAuthority_NeverTwoKings(t *testing.T) {
	if err := AssertSingleAuthority(nil); err != nil {
		t.Fatalf("zero kings err = %v, want nil", err)
	}
	if err := AssertSingleAuthority([]HolderSubject{"alice", "alice"}); err != nil {
		t.Fatalf("duplicated holder err = %v, want nil", err)
	}
	if err := AssertSingleAuthority([]HolderSubject{"alice", "bob"}); !errors.Is(err, ErrActiveReignConflict) {
		t.Fatalf("two kings err = %v, want ErrActiveReignConflict", err)
	}
}
