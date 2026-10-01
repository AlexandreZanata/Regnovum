package domain

import (
	"errors"
	"testing"
	"time"
)

const (
	seasonPersonA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	seasonPersonB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	seasonEpochA  = "2026-W10"
)

func seasonalReq(account, person string, season SeasonKey, epoch string) SeasonalAdmitRequest {
	return SeasonalAdmitRequest{
		Account: account, PersonProofHash: person, Season: season, EpochKey: epoch,
		AccountActive: true, AntifraudClear: true, ResetAcknowledged: true,
	}
}

func mustSeasonalAdmit(t *testing.T, req SeasonalAdmitRequest, admitted []SeasonalGrant) SeasonalGrant {
	t.Helper()
	grant, err := AdmitSeasonalNewcomer(req, admitted)
	if err != nil {
		t.Fatalf("AdmitSeasonalNewcomer(%+v): %v", req, err)
	}
	if grant.Status != NewcomerAdmitted {
		t.Fatalf("status = %q, want admitted", grant.Status)
	}
	if err := grant.VerifyHash(); err != nil {
		t.Fatalf("VerifyHash: %v", err)
	}
	return grant
}

func TestSeasonalOldAccountIngressOncePerBook(t *testing.T) {
	seasonA, _ := ParseSeasonKey("temporada-migalha-a")
	seasonB, _ := ParseSeasonKey("temporada-migalha-b")
	first := mustSeasonalAdmit(t, seasonalReq("campones-1", seasonPersonA, seasonA, seasonEpochA), nil)
	admitted := []SeasonalGrant{first}
	// Same old account and person in the next book admits again.
	second := mustSeasonalAdmit(t, seasonalReq("campones-1", seasonPersonA, seasonB, seasonEpochA), admitted)
	if second.Season != seasonB || second.Status != NewcomerAdmitted {
		t.Fatalf("next book = %+v, want admitted in %q", second, seasonB)
	}
	// Exact replay in the same book and epoch is idempotent.
	replay, err := AdmitSeasonalNewcomer(seasonalReq("campones-1", seasonPersonA, seasonA, seasonEpochA), admitted)
	if err != nil || replay.Hash != first.Hash || replay.Status != NewcomerAdmitted {
		t.Fatalf("replay = %+v/%v, want idempotent %q", replay, err, first.Hash)
	}
	// Same key in another book never redirects the original receipt.
	if err := CheckSeasonMatch(first.Season, seasonB); err == nil {
		t.Fatal("cross-book match passed: receipts stay pinned to the original book")
	}
}

func TestSeasonalDuplicatePersonBlockedInSameBook(t *testing.T) {
	seasonA, _ := ParseSeasonKey("temporada-migalha-a")
	first := mustSeasonalAdmit(t, seasonalReq("campones-1", seasonPersonA, seasonA, seasonEpochA), nil)
	blocked, err := AdmitSeasonalNewcomer(seasonalReq("campones-2", seasonPersonA, seasonA, seasonEpochA), []SeasonalGrant{first})
	if !errors.Is(err, ErrDuplicateNewcomer) || blocked.Status != NewcomerBlocked {
		t.Fatalf("duplicate = %+v/%v, want blocked duplicate", blocked, err)
	}
	if err := blocked.VerifyHash(); err != nil {
		t.Fatalf("blocked VerifyHash: %v", err)
	}
	// Same person in the same book but another week still duplicates:
	// one concession per person per book.
	if _, err := AdmitSeasonalNewcomer(seasonalReq("campones-3", seasonPersonA, seasonA, "2026-W11"), []SeasonalGrant{first}); !errors.Is(err, ErrDuplicateNewcomer) {
		t.Fatalf("second week duplicate = %v, want ErrDuplicateNewcomer", err)
	}
}

func TestSeasonalFirstEligibilityIsNeverFake(t *testing.T) {
	seasonB, _ := ParseSeasonKey("temporada-migalha-b")
	for _, req := range []SeasonalAdmitRequest{
		{Account: "campones-9", PersonProofHash: seasonPersonB, Season: seasonB, EpochKey: seasonEpochA, AntifraudClear: true, ResetAcknowledged: true},
		{Account: "campones-9", PersonProofHash: seasonPersonB, Season: seasonB, EpochKey: seasonEpochA, AccountActive: true, ResetAcknowledged: true},
		{Account: "campones-9", PersonProofHash: seasonPersonB, Season: seasonB, EpochKey: seasonEpochA, AccountActive: true, AntifraudClear: true},
	} {
		blocked, err := AdmitSeasonalNewcomer(req, nil)
		if err == nil {
			t.Fatalf("fake claim %+v passed: new books never clear standing, antifraud or reset", req)
		}
		if req.ResetAcknowledged && (!req.AccountActive || !req.AntifraudClear) {
			if !errors.Is(err, ErrAccountBlocked) || blocked.Status != NewcomerBlocked {
				t.Fatalf("sanctioned = %+v/%v, want blocked", blocked, err)
			}
		}
	}
	// Global sanctions survive the reset: a digest blocked before
	// still blocks in the next book when the verdicts arrive false.
	seasonA, _ := ParseSeasonKey("temporada-migalha-a")
	first := mustSeasonalAdmit(t, seasonalReq("campones-1", seasonPersonA, seasonA, seasonEpochA), nil)
	sanctioned := SeasonalAdmitRequest{
		Account: "campones-1", PersonProofHash: seasonPersonA, Season: seasonB, EpochKey: seasonEpochA,
		AccountActive: false, AntifraudClear: false, ResetAcknowledged: true,
	}
	if _, err := AdmitSeasonalNewcomer(sanctioned, []SeasonalGrant{first}); !errors.Is(err, ErrAccountBlocked) {
		t.Fatalf("sanctioned next book = %v, want ErrAccountBlocked", err)
	}
}

func TestSeasonalContainedWeeksOnly(t *testing.T) {
	// Book window: Monday 2026-03-02 to Monday 2026-03-16 (two full
	// weeks W10 and W11). Head week W09 starts before the book and
	// tail week W12 ends after it: both excluded even on overlap.
	seasonStart := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	seasonEnd := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	w09 := Epoch{Year: 2026, Week: 9}
	w10 := Epoch{Year: 2026, Week: 10}
	w11 := Epoch{Year: 2026, Week: 11}
	w12 := Epoch{Year: 2026, Week: 12}
	if IsEpochContained(w09, seasonStart, seasonEnd) {
		t.Fatal("head partial W09 contained: weeks starting before the book never count")
	}
	if !IsEpochContained(w10, seasonStart, seasonEnd) || !IsEpochContained(w11, seasonStart, seasonEnd) {
		t.Fatal("inner weeks W10/W11 excluded: fully contained weeks must count")
	}
	if IsEpochContained(w12, seasonStart, seasonEnd) {
		t.Fatal("tail partial W12 contained: weeks ending after the book never count")
	}
	weeks := []SeasonWeek{{Epoch: w09, Net: 9000}, {Epoch: w10, Net: 100}, {Epoch: w11, Net: 300}, {Epoch: w12, Net: 9000}}
	got, err := SeasonalMedianR4(weeks, seasonStart, seasonEnd)
	if err != nil {
		t.Fatalf("SeasonalMedianR4: %v", err)
	}
	// Contained nets 100,300: central sum 400, floor 200.
	if got != 200 {
		t.Fatalf("seasonal R4 = %d, want 200 from inner weeks only", got)
	}
	// No applicable history is defined zero, without failing.
	empty, err := SeasonalMedianR4(nil, seasonStart, seasonEnd)
	if err != nil || empty != 0 {
		t.Fatalf("empty seasonal R4 = %d/%v, want defined zero", empty, err)
	}
	previous, err := SeasonalMedianR4([]SeasonWeek{{Epoch: w09, Net: 5000}}, seasonStart, seasonEnd)
	if err != nil || previous != 0 {
		t.Fatalf("previous-book R4 = %d/%v, want zero: earlier weeks never feed the new book", previous, err)
	}
}

func TestSeasonalW53AndEmptyDistribution(t *testing.T) {
	// 2026 holds W53: a book exactly covering it contains it, a
	// 52-week year never seals it.
	w53 := Epoch{Year: 2026, Week: 53}
	start53, err := w53.Start()
	if err != nil {
		t.Fatalf("W53 start: %v", err)
	}
	end53, err := w53.End()
	if err != nil {
		t.Fatalf("W53 end: %v", err)
	}
	if !IsEpochContained(w53, start53, end53) {
		t.Fatal("exact W53 book excluded: a fully covered W53 must count")
	}
	if IsEpochContained(Epoch{Year: 2021, Week: 53}, start53, end53) {
		t.Fatal("W53 of a 52-week year contained: nonexistent weeks never count")
	}
	// N=0 moves zero with the whole budget left over, per book.
	plan, err := PlanDistribution(1000, 5000, seasonEpochA, nil)
	if err != nil {
		t.Fatalf("PlanDistribution: %v", err)
	}
	if plan.Quota != 0 || plan.Distributed != 0 || len(plan.Shares) != 0 || plan.Remainder != 1000 {
		t.Fatalf("empty plan = %+v, want zero shares with whole M left over", plan)
	}
	// Per-book oracle stays exact in integers: gross returns over
	// gross outflows with the same floored permille.
	report, err := RealRefluxIndex(
		[]RefluxLeg{{Origin: RefluxPublication, Direction: "credit", Amount: 10000}},
		[]OutflowLeg{{Kind: OutflowCrumbDistribution, Amount: 5000}},
	)
	if err != nil || !report.HasRatio || report.PerMille != 2000 {
		t.Fatalf("per-book index = %+v/%v, want 2000 permille", report, err)
	}
}

func TestSeasonalWindowAndSeal(t *testing.T) {
	seasonA, _ := ParseSeasonKey("temporada-migalha-a")
	starts := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	ends := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	if err := CheckSeasonWindow(seasonA, ends, starts, ends); err == nil {
		t.Fatal("closing tick admitted: the exclusive end already belongs to the successor")
	}
	if err := CheckSeasonWindow(seasonA, ends.Add(-time.Nanosecond), starts, ends); err != nil {
		t.Fatalf("tick before end refused: %v", err)
	}
	if _, err := ParseSeasonKey(""); err == nil {
		t.Fatal("empty book parsed: missing books fail before storage")
	}
	if err := RequireSeasonalReset(seasonA, false); err == nil {
		t.Fatal("omitted reset passed: reset never authorizes by omission")
	}
	grant := mustSeasonalAdmit(t, seasonalReq("campones-1", seasonPersonA, seasonA, seasonEpochA), nil)
	tampered := grant
	tampered.Account = "campones-2"
	if err := tampered.VerifyHash(); err == nil {
		t.Fatal("tampered seal verified: reclassification must break the seal first")
	}
	if _, err := AdmitSeasonalNewcomer(SeasonalAdmitRequest{
		Account: "campones-1", PersonProofHash: seasonPersonB, Season: seasonA, EpochKey: seasonEpochA,
		AccountActive: true, AntifraudClear: true, ResetAcknowledged: true,
	}, []SeasonalGrant{grant}); err == nil {
		t.Fatal("account-epoch conflict passed: same slot claiming another person refuses")
	}
}
