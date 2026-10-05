package domain_test

import (
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// restoreFingerprint is one book at one instant: S conserved, two
// custodies, sealed archive and Bob reigning after Alice.
func restoreFingerprint() domain.BookFingerprint {
	return domain.BookFingerprint{
		Season:             domain.SeasonKey("S-2077-PITR"),
		SupplyMillis:       domain.GenesisSupplyMillis,
		Custodies:          map[string]int64{"treasury/main": domain.GenesisSupplyMillis - 1000, "user/ana": 1000},
		Legs:               3,
		Intentions:         1,
		ArchiveDigest:      "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ArchiveSealed:      true,
		ActiveReignHolder:  "bob",
		ActiveReignVersion: 2,
		HasActiveReign:     true,
		FormerHolders:      []string{"alice"},
	}
}

// TestRestoreIdenticalSnapshotsResume proves byte-equal snapshots
// resume: the restore returned exactly what the disaster took.
func TestRestoreIdenticalSnapshotsResume(t *testing.T) {
	t.Parallel()
	verdict, err := domain.CompareBookRestore(restoreFingerprint(), restoreFingerprint())
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !verdict.ResumeAllowed() || len(verdict.Divergences) != 0 {
		t.Fatalf("identical snapshots: %+v", verdict)
	}
}

// TestRestoreOneMilliDriftBlocks proves conservation is strict: one
// milliINK more or less in supply or in any custody blocks resumption.
func TestRestoreOneMilliDriftBlocks(t *testing.T) {
	t.Parallel()
	supplyDrift := restoreFingerprint()
	drifted := restoreFingerprint()
	drifted.SupplyMillis++
	verdict, err := domain.CompareBookRestore(supplyDrift, drifted)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if verdict.ResumeAllowed() || len(verdict.Divergences) != 1 || verdict.Divergences[0].Code != "supply-diverged" {
		t.Fatalf("supply drift: %+v", verdict)
	}

	custodyDrift := restoreFingerprint()
	drifted = restoreFingerprint()
	drifted.Custodies["user/ana"]--
	verdict, err = domain.CompareBookRestore(custodyDrift, drifted)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if verdict.ResumeAllowed() || len(verdict.Divergences) != 1 || verdict.Divergences[0].Code != "custody-diverged" {
		t.Fatalf("custody drift: %+v", verdict)
	}

	missing := restoreFingerprint()
	drifted = restoreFingerprint()
	delete(drifted.Custodies, "user/ana")
	verdict, err = domain.CompareBookRestore(missing, drifted)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if verdict.ResumeAllowed() {
		t.Fatalf("missing custody resumes: %+v", verdict)
	}

	invented := restoreFingerprint()
	drifted = restoreFingerprint()
	drifted.Custodies["user/mallory"] = 0
	verdict, err = domain.CompareBookRestore(invented, drifted)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if verdict.ResumeAllowed() {
		t.Fatalf("invented custody resumes: %+v", verdict)
	}
}

// TestRestoreRewrittenHistoryBlocks proves replayed or dropped legs
// and intentions block resumption: nothing is reapplied in silence.
func TestRestoreRewrittenHistoryBlocks(t *testing.T) {
	t.Parallel()
	duplicated := restoreFingerprint()
	drifted := restoreFingerprint()
	drifted.Legs++
	verdict, err := domain.CompareBookRestore(duplicated, drifted)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if verdict.ResumeAllowed() || verdict.Divergences[0].Code != "history-rewritten" {
		t.Fatalf("duplicated legs: %+v", verdict)
	}

	dropped := restoreFingerprint()
	drifted = restoreFingerprint()
	drifted.Intentions--
	verdict, err = domain.CompareBookRestore(dropped, drifted)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if verdict.ResumeAllowed() {
		t.Fatalf("dropped intentions resume: %+v", verdict)
	}
}

// TestRestoreArchiveViolationBlocks proves a broken seal or a swapped
// digest blocks resumption: the archive never becomes spendable by
// restore.
func TestRestoreArchiveViolationBlocks(t *testing.T) {
	t.Parallel()
	unsealed := restoreFingerprint()
	drifted := restoreFingerprint()
	drifted.ArchiveSealed = false
	verdict, err := domain.CompareBookRestore(unsealed, drifted)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if verdict.ResumeAllowed() || verdict.Divergences[0].Code != "archive-violated" {
		t.Fatalf("broken seal: %+v", verdict)
	}

	swapped := restoreFingerprint()
	drifted = restoreFingerprint()
	drifted.ArchiveDigest = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	verdict, err = domain.CompareBookRestore(swapped, drifted)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if verdict.ResumeAllowed() {
		t.Fatalf("swapped digest resumes: %+v", verdict)
	}
}

// TestRestoreExKingBlocks proves a restored cluster that crowns the
// predecessor blocks resumption with its own code, while any other
// succession rewrite blocks as succession-rewritten.
func TestRestoreExKingBlocks(t *testing.T) {
	t.Parallel()
	crowned := restoreFingerprint()
	drifted := restoreFingerprint()
	drifted.ActiveReignHolder = "alice"
	drifted.ActiveReignVersion = 1
	verdict, err := domain.CompareBookRestore(crowned, drifted)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if verdict.ResumeAllowed() || len(verdict.Divergences) != 1 || verdict.Divergences[0].Code != "ex-king-restored" {
		t.Fatalf("ex-king crowned: %+v", verdict)
	}

	rewritten := restoreFingerprint()
	drifted = restoreFingerprint()
	drifted.ActiveReignHolder = "carol"
	verdict, err = domain.CompareBookRestore(rewritten, drifted)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if verdict.ResumeAllowed() || verdict.Divergences[0].Code != "succession-rewritten" {
		t.Fatalf("rewritten succession: %+v", verdict)
	}
}

// TestRestoreRegistryIsComplete proves every emitted code carries
// severity, owner, runbook and action, and the redaction carries no
// PII.
func TestRestoreRegistryIsComplete(t *testing.T) {
	t.Parallel()
	if len(domain.RestoreDivergenceCodes()) != 6 {
		t.Fatalf("registry codes = %v", domain.RestoreDivergenceCodes())
	}
	cases := []func(base domain.BookFingerprint) domain.BookFingerprint{
		func(base domain.BookFingerprint) domain.BookFingerprint { base.SupplyMillis++; return base },
		func(base domain.BookFingerprint) domain.BookFingerprint { base.Custodies["user/ana"]++; return base },
		func(base domain.BookFingerprint) domain.BookFingerprint { base.Legs++; return base },
		func(base domain.BookFingerprint) domain.BookFingerprint { base.ArchiveSealed = false; return base },
		func(base domain.BookFingerprint) domain.BookFingerprint {
			base.ActiveReignHolder = "carol"
			return base
		},
		func(base domain.BookFingerprint) domain.BookFingerprint {
			base.ActiveReignHolder = "alice"
			base.ActiveReignVersion = 1
			return base
		},
	}
	seen := map[string]bool{}
	for i, mutate := range cases {
		verdict, err := domain.CompareBookRestore(restoreFingerprint(), mutate(restoreFingerprint()))
		if err != nil {
			t.Fatalf("case %d: Compare: %v", i, err)
		}
		if verdict.ResumeAllowed() {
			t.Fatalf("case %d resumes", i)
		}
		for _, divergence := range verdict.Divergences {
			seen[divergence.Code] = true
			if divergence.Severity == "" || divergence.Owner == "" || divergence.Runbook == "" || divergence.Action == "" {
				t.Fatalf("%s: incomplete row %+v", divergence.Code, divergence)
			}
			line := divergence.Redacted(restoreFingerprint().Season)
			for _, marker := range []string{"@", "sk_", "BEGIN", "email"} {
				if strings.Contains(strings.ToLower(line), marker) {
					t.Fatalf("redaction leaks %q: %s", marker, line)
				}
			}
		}
	}
	for _, code := range domain.RestoreDivergenceCodes() {
		if !seen[code] {
			t.Fatalf("registry code %q never produced", code)
		}
	}
}

// TestRestoreRejectsUnjudgeableSnapshots proves fail-closed shapes:
// unnamed books and negative counts never compare.
func TestRestoreRejectsUnjudgeableSnapshots(t *testing.T) {
	t.Parallel()
	unnamed := restoreFingerprint()
	unnamed.Season = ""
	if _, err := domain.CompareBookRestore(unnamed, restoreFingerprint()); err == nil {
		t.Fatal("unnamed book compared")
	}
	negative := restoreFingerprint()
	negative.SupplyMillis = -1
	if _, err := domain.CompareBookRestore(restoreFingerprint(), negative); err == nil {
		t.Fatal("negative supply compared")
	}
	negativeCustody := restoreFingerprint()
	negativeCustody.Custodies["user/ana"] = -1
	if _, err := domain.CompareBookRestore(restoreFingerprint(), negativeCustody); err == nil {
		t.Fatal("negative custody compared")
	}
}
