package application_test

// Viewpoint-neutral policy handling (P13-T08, MODERATION §1/§2).
//
// The moderation pipeline never classifies truth or ideology: reports carry
// caller-supplied reasons, decisions carry moderator-chosen measures, and
// equivalent behavior receives the same handling whatever side of a
// controversy the content sits on. These tests pin that property with
// ideologically mirrored fixtures and prove no partisan or religious
// string is hardcoded as a prohibition in enforcement code.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

// mirroredPairs holds ideologically inverted statements with equal
// behavior: both sides merely express opinion (MODERATION §2), so both
// must receive identical handling. The suite never judges which side is
// true.
var mirroredPairs = [][2]string{
	{"Capitalismo é melhor que socialismo.", "Socialismo é melhor que capitalismo."},
	{"Deus existe.", "Deus não existe."},
	{"A AGI não existirá.", "A AGI certamente existirá."},
}

func neutralityTargets() *fakeTargets {
	return &fakeTargets{
		arenas: map[string]*application.TargetInfo{
			"018f6b2a-0000-7000-8000-000000000071": {Exists: true, Owner: reviewOwner},
		},
	}
}

func fileNeutralReport(t *testing.T, uc *application.FileReportUseCase, content string) *application.FileReportResult {
	t.Helper()
	result, err := uc.Execute(context.Background(), application.FileReportCommand{
		Reporter: reporterID,
		Target:   "arena",
		TargetID: "018f6b2a-0000-7000-8000-000000000071",
		Reason:   "spam",
		Context:  content,
	})
	if err != nil {
		t.Fatalf("file report %q: %v", content, err)
	}
	return result
}

func TestViewpointMirroredReportsReceiveIdenticalHandling(t *testing.T) {
	t.Parallel()

	runMatrix := func() []application.FileReportResult {
		targets := neutralityTargets()
		reports := &fakeReports{duplicates: map[string]*application.ReportRecord{}}
		uc, err := application.NewFileReportUseCase(application.FileReportDependencies{
			Targets: targets,
			Reports: reports,
			Clock:   &fakeClock{},
		})
		if err != nil {
			t.Fatalf("NewFileReportUseCase: %v", err)
		}
		var outcomes []application.FileReportResult
		for _, pair := range mirroredPairs {
			left := fileNeutralReport(t, uc, pair[0])
			right := fileNeutralReport(t, uc, pair[1])
			// Identical handling: same acceptance, same replay state, same
			// rate signal. Only the stored identity may differ.
			if left.Replayed != right.Replayed || left.RateLimited != right.RateLimited {
				t.Fatalf("mirrored handling diverged: %+v vs %+v", left, right)
			}
			outcomes = append(outcomes, *left, *right)
		}
		return outcomes
	}

	first := runMatrix()
	second := runMatrix()

	// Deterministic suite: two runs over fresh fixtures produce the same
	// handling sequence.
	if len(first) != len(second) {
		t.Fatalf("runs diverged in length: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Replayed != second[i].Replayed ||
			first[i].RateLimited != second[i].RateLimited ||
			first[i].ReportsInWindow != second[i].ReportsInWindow {
			t.Fatalf("run %d diverged: %+v vs %+v", i, first[i], second[i])
		}
	}
}

func TestReportReasonIsCallerSuppliedNeverInferred(t *testing.T) {
	t.Parallel()

	targets := neutralityTargets()
	reports := &fakeReports{duplicates: map[string]*application.ReportRecord{}}
	uc, err := application.NewFileReportUseCase(application.FileReportDependencies{
		Targets: targets,
		Reports: reports,
		Clock:   &fakeClock{},
	})
	if err != nil {
		t.Fatalf("NewFileReportUseCase: %v", err)
	}

	// The same content filed under different valid reasons is accepted
	// each time: the pipeline never maps content to a reason.
	for _, reason := range []string{"spam", "harassment", "other"} {
		result, err := uc.Execute(context.Background(), application.FileReportCommand{
			Reporter: reporterID,
			Target:   "arena",
			TargetID: "018f6b2a-0000-7000-8000-000000000071",
			Reason:   reason,
			Context:  mirroredPairs[0][0],
		})
		if err != nil {
			t.Fatalf("reason %q rejected for fixed content: %v", reason, err)
		}
		if result.Replayed {
			t.Fatalf("reason %q unexpectedly replayed", reason)
		}
	}
}

func TestDecisionsApplyEquallyToMirroredFixtures(t *testing.T) {
	t.Parallel()

	// The moderator's explicit measure applies untouched whatever the
	// viewpoint of the underlying content: decisions carry rule and
	// justification, never a content verdict.
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	lease := now.Add(time.Minute)
	for _, pair := range mirroredPairs {
		cases := newFakeCases()
		caseID := "case-neutral-1"
		cases.records[caseID] = &application.CaseRecord{
			ID:             caseID,
			Target:         domain.TargetArena,
			TargetID:       "018f6b2a-0000-7000-8000-000000000071",
			TargetOwner:    reviewOwner,
			Status:         application.CaseUnderReview,
			ClaimedBy:      reviewModerator,
			LeaseExpiresAt: &lease,
		}
		uc, err := application.NewDecideCaseUseCase(application.ReviewDependencies{
			Cases:      cases,
			Authorizer: reviewAuthorizer(),
			Clock:      &fakeClock{now: now},
		})
		if err != nil {
			t.Fatalf("NewDecideCaseUseCase: %v", err)
		}
		result, err := uc.Execute(context.Background(), application.DecideCaseCommand{
			CaseID: caseID, Actor: string(reviewModerator),
			Action: "warning", Rule: "MOD-2:warning", Justification: "Least restrictive measure for " + pair[0],
		})
		if err != nil {
			t.Fatalf("mirrored decision %q: %v", pair[0], err)
		}
		if result.Action != domain.ActionWarning {
			t.Fatalf("mirrored decision action = %q, want the moderator's warning untouched", result.Action)
		}
	}
}

// partisanReligiousMarkers lists viewpoint tokens that must never appear as
// hardcoded prohibitions in enforcement code. The list itself lives only in
// this test: enforcement files (non-test sources, queries, migrations) are
// walked below and must not contain any of them.
var partisanReligiousMarkers = []string{
	"bolsonaro", "lula", "lulista", "petista", "bolsominion",
	"esquerda", "direita", "comunista", "fascista", "esquerdista", "direitista",
	"jesus", "cristo", "allah", "maomé", "maome",
	"evangélico", "evangelico", "católico", "catolico",
	"ateu", "ateia", "bíblia", "biblia", "corão", "corao",
}

func TestNoPartisanOrReligiousHardcodedProhibition(t *testing.T) {
	t.Parallel()

	// The closed reason vocabulary carries behavior categories only.
	for _, reason := range domain.AllReasons() {
		lowered := strings.ToLower(reason.String())
		for _, marker := range partisanReligiousMarkers {
			if strings.Contains(lowered, marker) {
				t.Fatalf("reason vocabulary carries viewpoint token %q in %q", marker, reason)
			}
		}
	}

	// Enforcement sources carry no viewpoint token as prohibition logic.
	// Test files are excluded: this file legitimately names the markers.
	root := repositoryRoot(t)
	enforcement := []string{
		"internal/moderation/domain",
		"internal/moderation/application",
		"internal/moderation/adapters",
		"db/queries/moderation.sql",
		"internal/platform/dbmigrate/migrations/00022_moderation_schema.sql",
		"internal/platform/dbmigrate/migrations/00023_admin_security_role.sql",
		"internal/platform/dbmigrate/migrations/00024_moderation_claim_lease.sql",
	}
	for _, entry := range enforcement {
		path := filepath.Join(root, entry)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", entry, err)
		}
		if !info.IsDir() {
			assertFileHasNoMarkers(t, path)
			continue
		}
		walkErr := filepath.WalkDir(path, func(child string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(child, ".go") || strings.HasSuffix(child, "_test.go") {
				return nil
			}
			assertFileHasNoMarkers(t, child)
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", entry, walkErr)
		}
	}
}

func assertFileHasNoMarkers(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lowered := strings.ToLower(string(data))
	for _, marker := range partisanReligiousMarkers {
		if strings.Contains(lowered, marker) {
			t.Fatalf("%s hardcodes viewpoint token %q as prohibition logic", path, marker)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// This test lives in internal/moderation/application; the repository
	// root is three levels above.
	root := filepath.Clean(filepath.Join(wd, "..", "..", ".."))
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.Contains(string(data), "module github.com/AlexandreZanata/Regnovum") {
		t.Fatalf("go.mod does not declare the arena module")
	}
	return root
}
