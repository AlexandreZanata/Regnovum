package application_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/transparency/application"
	"github.com/AlexandreZanata/Regnovum/internal/transparency/domain"
)

type fakeSource struct {
	raw *application.RawCounts
	err error
}

func (f *fakeSource) RawCounts(_ context.Context, _, _ time.Time) (*application.RawCounts, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.raw, nil
}

func TestDeriveAppliesSuppressionUniformly(t *testing.T) {
	t.Parallel()

	period, err := domain.NewPeriod(
		time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("NewPeriod: %v", err)
	}
	uc, err := application.NewDeriveMetricsUseCase(&fakeSource{raw: &application.RawCounts{
		EligibleAccounts: 6,
		ArenasPublished:  4,
		InkFreeGranted:   30000,
		ReportsFiled:     2,
	}})
	if err != nil {
		t.Fatalf("NewDeriveMetricsUseCase: %v", err)
	}

	snapshot, err := uc.Execute(context.Background(), period)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if snapshot.MethodologyVersion != domain.MethodologyVersion {
		t.Fatalf("version = %d, want %d", snapshot.MethodologyVersion, domain.MethodologyVersion)
	}
	if snapshot.EligibleAccounts != 6 || snapshot.InkFreeGranted != 30000 {
		t.Fatalf("above-threshold values changed: %+v", snapshot)
	}
	if snapshot.ArenasPublished != 0 || snapshot.ReportsFiled != 0 {
		t.Fatalf("below-threshold values must suppress to zero: %+v", snapshot)
	}
	if !snapshot.PeriodStart.Equal(period.Start()) || !snapshot.PeriodEnd.Equal(period.End()) {
		t.Fatalf("period not carried: %+v", snapshot)
	}
}

func TestSnapshotCarriesNoPrivateProjections(t *testing.T) {
	t.Parallel()

	// The snapshot must be reconstructible into numbers only: reflection
	// over every field refuses strings (emails, identifiers, reasons),
	// slices, maps and anything but instants and integers.
	snapshotType := reflect.TypeOf(application.Snapshot{})
	for i := 0; i < snapshotType.NumField(); i++ {
		field := snapshotType.Field(i)
		switch field.Type.Kind() {
		case reflect.Int, reflect.Int64:
		case reflect.Struct:
			if field.Type != reflect.TypeOf(time.Time{}) {
				t.Fatalf("Snapshot.%s has non-instant struct type %s", field.Name, field.Type)
			}
		default:
			t.Fatalf("Snapshot.%s has kind %s; only integers and instants may serialize", field.Name, field.Type.Kind())
		}
		lowered := strings.ToLower(field.Name)
		// Counts aggregate; per-account positions and attributor
		// identities never enter the shape. Field names carry family
		// nouns (positions, authors) but no identifier, secret or
		// per-account marker.
		for _, marker := range []string{"email", "stripe", "attributor", "username", "token", "password", "session", "identifier", "address"} {
			if strings.Contains(lowered, marker) {
				t.Fatalf("Snapshot.%s names %q", field.Name, marker)
			}
		}
	}
}

func TestDeriveRejectsIncoherentInput(t *testing.T) {
	t.Parallel()

	if _, err := application.NewDeriveMetricsUseCase(nil); err == nil {
		t.Fatal("nil source must refuse composition")
	}
	uc, err := application.NewDeriveMetricsUseCase(&fakeSource{raw: &application.RawCounts{}})
	if err != nil {
		t.Fatalf("NewDeriveMetricsUseCase: %v", err)
	}
	if _, err := uc.Execute(context.Background(), domain.Period{}); err == nil {
		t.Fatal("zero period must fail")
	}
}
