package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

type fixedMetricsClock struct{ now time.Time }

func (c fixedMetricsClock) Now() time.Time { return c.now }

type fakeMetricsProvider struct {
	reading *CrumbMetricsReading
	calls   int
	err     error
}

func (f *fakeMetricsProvider) ReadCrumbMetrics(_ context.Context) (*CrumbMetricsReading, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.reading, nil
}

func metricsWindow() (time.Time, time.Time) {
	start := time.Date(2026, 12, 28, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 0, 7)
}

func metricsReading() *CrumbMetricsReading {
	start, end := metricsWindow()
	return &CrumbMetricsReading{
		SupplyMillis: 2100000000000, TreasuryMillis: 1800000000000,
		CirculationMillis: 299999000000, R4Millis: 1400,
		CrumbsDistributedMillis: 1000, CrumbsHeadcount: 5,
		IRRPerMille: 2000, IRRHasRatio: true,
		WindowStart: start, WindowEnd: end, MethodologyVersion: 1,
	}
}

func metricsUseCase(reading *CrumbMetricsReading, now time.Time) (*CrumbMetricsUseCase, *fakeMetricsProvider) {
	provider := &fakeMetricsProvider{reading: reading}
	uc, err := NewCrumbMetricsUseCase(provider, fixedMetricsClock{now: now})
	if err != nil {
		panic(err)
	}
	return uc, provider
}

func TestCrumbMetricsCacheServesPerLocale(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	uc, provider := metricsUseCase(metricsReading(), now)
	ctx := context.Background()

	if _, err := uc.Metrics(ctx, "xx"); !errors.Is(err, domain.ErrInvalidTotals) {
		t.Fatalf("unknown locale = %v, want ErrInvalidTotals before any reading", err)
	}
	if provider.calls != 0 {
		t.Fatalf("calls = %d, want 0: locale refuses before reading", provider.calls)
	}
	pt, err := uc.Metrics(ctx, "pt")
	if err != nil {
		t.Fatalf("Metrics(pt): %v", err)
	}
	again, err := uc.Metrics(ctx, "pt")
	if err != nil || provider.calls != 1 {
		t.Fatalf("cached pt = %v with %d calls, want 1 derivation", err, provider.calls)
	}
	if again.GeneratedAt != pt.GeneratedAt {
		t.Fatal("cached document changed: want one derivation per window")
	}
	en, err := uc.Metrics(ctx, "en")
	if err != nil || provider.calls != 2 {
		t.Fatalf("en = %v with %d calls, want a fresh derivation per locale", err, provider.calls)
	}
	if en.Labels.Title == pt.Labels.Title {
		t.Fatal("titles match across locales: want translated titles")
	}
	if en.SupplyMillis != pt.SupplyMillis || en.R4Millis != pt.R4Millis ||
		en.CrumbsDistributedMillis != pt.CrumbsDistributedMillis || en.IRRLabel != pt.IRRLabel {
		t.Fatal("numbers differ across locales: want identical integers")
	}

	uc.clock = fixedMetricsClock{now: now.Add(3601 * time.Second)}
	if _, err := uc.Metrics(ctx, "pt"); err != nil || provider.calls != 3 {
		t.Fatalf("expired cache = %v with %d calls, want re-derivation", err, provider.calls)
	}
}

func TestCrumbMetricsSuppressesLowCountsOnly(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	small := metricsReading()
	small.CrumbsHeadcount = 3
	uc, _ := metricsUseCase(small, now)
	snapshot, err := uc.Metrics(context.Background(), "pt")
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if snapshot.CrumbsDistributedMillis != 0 || !snapshot.CrumbsSuppressed {
		t.Fatalf("small crumbs = %d/%v, want zeroed with suppression",
			snapshot.CrumbsDistributedMillis, snapshot.CrumbsSuppressed)
	}
	if snapshot.SupplyMillis != 2100000000000 || snapshot.TreasuryMillis != 1800000000000 ||
		snapshot.R4Millis != 1400 || snapshot.IRRLabel != "200.0%" {
		t.Fatalf("sovereign snapshot = %+v, want whole aggregates despite low counts", snapshot)
	}
	if snapshot.CrumbsHeadcount != 3 {
		t.Fatalf("headcount = %d, want the raw count beside the suppressed amount", snapshot.CrumbsHeadcount)
	}
}

func TestCrumbMetricsCarriesNoIndividualBalances(t *testing.T) {
	banned := []string{"account", "holder", "email", "transfer", "governor", "custody"}
	shape := reflect.TypeOf(CrumbMetricsSnapshot{})
	for i := 0; i < shape.NumField(); i++ {
		lower := strings.ToLower(shape.Field(i).Name)
		for _, bad := range banned {
			if strings.Contains(lower, bad) {
				t.Fatalf("field %q carries %q: aggregates only, never individual balances",
					shape.Field(i).Name, bad)
			}
		}
	}
}

func TestCrumbMetricsWindowSurvivesDisplayZones(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	uc, _ := metricsUseCase(metricsReading(), now)
	snapshot, err := uc.Metrics(context.Background(), "en")
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if snapshot.WindowStart.Location() != time.UTC || snapshot.WindowEnd.Location() != time.UTC {
		t.Fatal("window left UTC: display timezones must never move it")
	}
	saoPaulo, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	utcWall := snapshot.WindowStart.Format("2006-01-02 15:04")
	spWall := snapshot.WindowStart.In(saoPaulo).Format("2006-01-02 15:04")
	if utcWall == spWall {
		t.Fatal("walls match: the fixture needs zones that render this instant differently")
	}
	if !snapshot.WindowStart.In(saoPaulo).Equal(snapshot.WindowStart) {
		t.Fatal("instant moved across zones: the window instant must survive display")
	}
	if snapshot.WindowEnd.Sub(snapshot.WindowStart) != 7*24*time.Hour {
		t.Fatalf("window = %s, want exactly one sealed week", snapshot.WindowEnd.Sub(snapshot.WindowStart))
	}
}

func TestCrumbMetricsRefusesMalformedReadings(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	uc, _ := metricsUseCase(nil, now)
	if _, err := uc.Metrics(ctx, "pt"); !errors.Is(err, domain.ErrInvalidTotals) {
		t.Fatalf("nil reading = %v, want ErrInvalidTotals", err)
	}
	start, end := metricsWindow()
	cases := map[string]func(*CrumbMetricsReading){
		"negative supply":      func(r *CrumbMetricsReading) { r.SupplyMillis = -1 },
		"negative treasury":    func(r *CrumbMetricsReading) { r.TreasuryMillis = -1 },
		"negative circulation": func(r *CrumbMetricsReading) { r.CirculationMillis = -1 },
		"negative crumbs":      func(r *CrumbMetricsReading) { r.CrumbsDistributedMillis = -1 },
		"inverted window":      func(r *CrumbMetricsReading) { r.WindowStart, r.WindowEnd = end, start },
		"negative ratio":       func(r *CrumbMetricsReading) { r.IRRPerMille = -1 },
	}
	for name, mutate := range cases {
		reading := metricsReading()
		mutate(reading)
		uc, _ := metricsUseCase(reading, now)
		if _, err := uc.Metrics(ctx, "pt"); !errors.Is(err, domain.ErrInvalidTotals) {
			t.Fatalf("%s = %v, want ErrInvalidTotals", name, err)
		}
	}
	negative := metricsReading()
	negative.R4Millis = -5000
	uc, _ = metricsUseCase(negative, now)
	visible, err := uc.Metrics(ctx, "pt")
	if err != nil || visible.R4Millis != -5000 {
		t.Fatalf("negative R4 = %+v/%v, want the deficit visible, never hidden", visible, err)
	}
	empty, err := func() (CrumbMetricsSnapshot, error) {
		reading := metricsReading()
		reading.CrumbsHeadcount = 0
		reading.CrumbsDistributedMillis = 0
		reading.IRRHasRatio = false
		uc, _ := metricsUseCase(reading, now)
		return uc.Metrics(ctx, "pt")
	}()
	if err != nil || empty.IRRLabel != "N/A" {
		t.Fatalf("empty period = %+v/%v, want N/A without error", empty, err)
	}
}
