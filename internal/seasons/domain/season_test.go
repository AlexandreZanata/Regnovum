package domain

import (
	"errors"
	"testing"
	"time"
)

func seasonReq() ManifestRequest {
	return ManifestRequest{
		ID: "temporada-1", Ordinal: 1,
		StartsAt:       time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC),
		CharterVersion: "v3", PolicyRef: "politica-inicial",
		InitialMonarch: "fundadora", Regent: "regente-tecnica",
	}
}

func mustManifest(t *testing.T, req ManifestRequest) Manifest {
	t.Helper()
	manifest, err := NewManifest(req)
	if err != nil {
		t.Fatalf("NewManifest(%+v): %v", req, err)
	}
	if err := manifest.VerifyManifestHash(); err != nil {
		t.Fatalf("VerifyManifestHash: %v", err)
	}
	return manifest
}

func mustSeason(t *testing.T, req ManifestRequest) Season {
	t.Helper()
	season, err := NewSeason(mustManifest(t, req))
	if err != nil {
		t.Fatalf("NewSeason: %v", err)
	}
	if season.State != StatePrepared {
		t.Fatalf("state = %q, want prepared", string(season.State))
	}
	return season
}

func TestWindowTicksBeforeAtAfterEnd(t *testing.T) {
	season := mustSeason(t, seasonReq())
	start := season.Manifest.StartsAt
	if want := start.Add(SeasonDurationSeconds * time.Second); !season.EndsAt.Equal(want) {
		t.Fatalf("ends_at = %v, want start plus 7776000s", season.EndsAt)
	}
	for at, want := range map[time.Time]bool{
		start.Add(-time.Nanosecond):         false,
		start:                               true,
		season.EndsAt.Add(-time.Nanosecond): true,
		season.EndsAt:                       false,
		season.EndsAt.Add(time.Hour):        false,
	} {
		if got := season.Contains(at); got != want {
			t.Fatalf("Contains(%v) = %v, want %v: the window is half-open", at, got, want)
		}
	}
}

func TestCalendarIgnoresCivilMonthsAndDST(t *testing.T) {
	// Autumn season crossing the US fall-back: UTC arithmetic holds
	// the exact ninety days whatever local clocks do.
	autumn := mustSeason(t, ManifestRequest{
		ID: "temporada-outono", Ordinal: 2,
		StartsAt:       time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC),
		CharterVersion: "v3", PolicyRef: "politica-inicial",
		InitialMonarch: "fundadora", Regent: "regente-tecnica",
	})
	if want := time.Date(2026, time.November, 30, 0, 0, 0, 0, time.UTC); !autumn.EndsAt.Equal(want) {
		t.Fatalf("autumn ends_at = %v, want %v", autumn.EndsAt, want)
	}
	if !autumn.Contains(time.Date(2026, time.November, 1, 5, 0, 0, 0, time.UTC)) {
		t.Fatal("fall-back weekend reads outside the window: the calendar is UTC, never local time")
	}
	// February, leap and common: ninety days land where they land.
	leap := mustSeason(t, ManifestRequest{
		ID: "temporada-bissexta", Ordinal: 3,
		StartsAt:       time.Date(2027, time.December, 3, 0, 0, 0, 0, time.UTC),
		CharterVersion: "v3", PolicyRef: "politica-inicial",
		InitialMonarch: "fundadora", Regent: "regente-tecnica",
	})
	if want := time.Date(2028, time.March, 2, 0, 0, 0, 0, time.UTC); !leap.EndsAt.Equal(want) {
		t.Fatalf("leap ends_at = %v, want %v across Feb 29", leap.EndsAt, want)
	}
	common := mustSeason(t, ManifestRequest{
		ID: "temporada-comum", Ordinal: 4,
		StartsAt:       time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC),
		CharterVersion: "v3", PolicyRef: "politica-inicial",
		InitialMonarch: "fundadora", Regent: "regente-tecnica",
	})
	if want := time.Date(2027, time.March, 1, 0, 0, 0, 0, time.UTC); !common.EndsAt.Equal(want) {
		t.Fatalf("common ends_at = %v, want %v across a 28-day February", common.EndsAt, want)
	}
	// A start expressed in a local zone normalizes to UTC.
	eastern := time.FixedZone("EDT", -4*60*60)
	zoned := mustSeason(t, ManifestRequest{
		ID: "temporada-fuso", Ordinal: 5,
		StartsAt:       time.Date(2026, time.October, 4, 8, 0, 0, 0, eastern),
		CharterVersion: "v3", PolicyRef: "politica-inicial",
		InitialMonarch: "fundadora", Regent: "regente-tecnica",
	})
	if want := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC); !zoned.Manifest.StartsAt.Equal(want) {
		t.Fatalf("zoned starts_at = %v, want %v", zoned.Manifest.StartsAt, want)
	}
}

func TestDateOverflowRefuses(t *testing.T) {
	req := seasonReq()
	req.ID = "temporada-fim-dos-tempos"
	req.StartsAt = time.Date(9999, time.December, 1, 0, 0, 0, 0, time.UTC)
	if _, err := NewManifest(req); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("overflow NewManifest = %v, want ErrInvalidSeason", err)
	}
	missingID := seasonReq()
	missingID.ID = ""
	if _, err := NewManifest(missingID); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("blank id NewManifest = %v, want ErrInvalidSeason", err)
	}
	zeroOrdinal := seasonReq()
	zeroOrdinal.Ordinal = 0
	if _, err := NewManifest(zeroOrdinal); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("zero ordinal NewManifest = %v, want ErrInvalidSeason", err)
	}
	badVersion := seasonReq()
	badVersion.CharterVersion = "3"
	if _, err := NewManifest(badVersion); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("bare version NewManifest = %v, want ErrInvalidSeason", err)
	}
}

func TestAlteredManifestBreaksSeal(t *testing.T) {
	manifest := mustManifest(t, seasonReq())
	tampered := manifest
	tampered.InitialMonarch = "usurpadora"
	if err := tampered.VerifyManifestHash(); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("tampered Verify = %v, want ErrInvalidSeason", err)
	}
	if _, err := NewSeason(tampered); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("tampered NewSeason = %v, want ErrInvalidSeason", err)
	}
	season := mustSeason(t, seasonReq())
	tamperedSeason := season
	tamperedSeason.Manifest.PolicyRef = "politica-nova"
	if _, err := tamperedSeason.Transition(StateActive, season.Manifest.StartsAt); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("tampered Transition = %v, want ErrInvalidSeason before the machine", err)
	}
}

func TestMachineAdvancesOneStepWithLiveClock(t *testing.T) {
	season := mustSeason(t, seasonReq())
	start := season.Manifest.StartsAt
	active, err := season.Transition(StateActive, start)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if _, err := season.Transition(StateClosing, start); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("jump prepared->closing = %v, want ErrInvalidTransition", err)
	}
	if _, err := active.Transition(StateActive, start); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("self transition = %v, want ErrInvalidTransition", err)
	}
	if _, err := active.Transition(StateClosing, start.Add(-time.Second)); !errors.Is(err, ErrStaleClock) {
		t.Fatalf("regressive clock = %v, want ErrStaleClock", err)
	}
	closing, err := active.Transition(StateClosing, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	sealed, err := closing.Transition(StateSealed, start.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	archived, err := sealed.Transition(StateArchived, start.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := archived.Transition(StateActive, start.Add(4*time.Hour)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("reopen archived = %v, want ErrInvalidTransition: the archive never reopens", err)
	}
	if _, err := sealed.Transition(StateActive, start.Add(4*time.Hour)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("sealed backwards = %v, want ErrInvalidTransition", err)
	}
}

func TestFreezeNeverExtendsCalendar(t *testing.T) {
	season := mustSeason(t, seasonReq())
	frozen, err := season.Transition(StateActive, season.Manifest.StartsAt)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	// A security freeze is a separate control with no field here:
	// the window stays a pure function of the published start, so
	// even a frozen-then-closed season ends exactly on time.
	closing, err := frozen.Transition(StateClosing, season.EndsAt)
	if err != nil {
		t.Fatalf("close at end: %v", err)
	}
	if !closing.EndsAt.Equal(season.EndsAt) {
		t.Fatalf("ends_at moved to %v: freeze never prolongs the calendar", closing.EndsAt)
	}
	if closing.Contains(season.EndsAt) {
		t.Fatal("end instant still admitted: the successor owns it")
	}
}

func TestSuccessorNeedsExactContinuity(t *testing.T) {
	previous := mustSeason(t, seasonReq())
	nextReq := seasonReq()
	nextReq.ID = "temporada-2"
	nextReq.Ordinal = 2
	nextReq.StartsAt = previous.EndsAt
	next, err := PlanNext(previous, nextReq)
	if err != nil {
		t.Fatalf("PlanNext: %v", err)
	}
	if next.Manifest.Ordinal != 2 || !next.Manifest.StartsAt.Equal(previous.EndsAt) || next.State != StatePrepared {
		t.Fatalf("next = %+v, want ordinal 2 starting at the predecessor end", next)
	}
	gapped := nextReq
	gapped.StartsAt = previous.EndsAt.Add(SeasonDurationSeconds * time.Second)
	if _, err := PlanNext(previous, gapped); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("gapped PlanNext = %v, want ErrInvalidSeason: downtime never auto-fills lapsed seasons", err)
	}
	renumbered := nextReq
	renumbered.Ordinal = 5
	if _, err := PlanNext(previous, renumbered); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("renumbered PlanNext = %v, want ErrInvalidSeason", err)
	}
	sameID := nextReq
	sameID.ID = previous.Manifest.ID
	if _, err := PlanNext(previous, sameID); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("reused id PlanNext = %v, want ErrInvalidSeason: ids are immutable", err)
	}
	for _, raw := range []string{"", "ACTIVE", " ativa", "encerrada"} {
		if _, err := ParseSeasonState(raw); !errors.Is(err, ErrInvalidSeason) {
			t.Fatalf("ParseSeasonState(%q) = %v, want ErrInvalidSeason", raw, err)
		}
	}
}
