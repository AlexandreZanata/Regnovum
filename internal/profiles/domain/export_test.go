package domain_test

import (
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

func TestExportSchemaVersionIsPinned(t *testing.T) {
	t.Parallel()

	if domain.ExportSchemaVersion != 1 {
		t.Fatalf("ExportSchemaVersion = %d, want 1", domain.ExportSchemaVersion)
	}
}

func TestExportStepUpWindow(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		age  time.Duration
		want bool
	}{
		{name: "fresh", age: time.Minute, want: true},
		{name: "boundary", age: domain.ExportStepUpWindow, want: true},
		{name: "stale", age: domain.ExportStepUpWindow + time.Nanosecond, want: false},
		{name: "negative", age: -time.Second, want: false},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := domain.StepUpSatisfied(testCase.age); got != testCase.want {
				t.Fatalf("StepUpSatisfied(%s) = %v, want %v", testCase.age, got, testCase.want)
			}
		})
	}
}

func TestExportAvailability(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name          string
		status        domain.ExportStatus
		expiresAt     time.Time
		downloadCount int32
		maxDownloads  int32
		want          bool
	}{
		{name: "ready and fresh", status: domain.ExportStatusReady, expiresAt: now.Add(time.Hour), maxDownloads: 1, want: true},
		{name: "boundary instant", status: domain.ExportStatusReady, expiresAt: now, maxDownloads: 1, want: false},
		{name: "expired", status: domain.ExportStatusReady, expiresAt: now.Add(-time.Second), maxDownloads: 1, want: false},
		{name: "exhausted", status: domain.ExportStatusReady, expiresAt: now.Add(time.Hour), downloadCount: 1, maxDownloads: 1, want: false},
		{name: "remaining budget", status: domain.ExportStatusReady, expiresAt: now.Add(time.Hour), downloadCount: 1, maxDownloads: 2, want: true},
		{name: "requested", status: domain.ExportStatusRequested, expiresAt: now.Add(time.Hour), maxDownloads: 1, want: false},
		{name: "expired status", status: domain.ExportStatusExpired, expiresAt: now.Add(time.Hour), maxDownloads: 1, want: false},
		{name: "incoherent budget", status: domain.ExportStatusReady, expiresAt: now.Add(time.Hour), maxDownloads: 0, want: false},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := domain.ExportAvailable(testCase.status, testCase.expiresAt, testCase.downloadCount, testCase.maxDownloads, now)
			if got != testCase.want {
				t.Fatalf("ExportAvailable(%s) = %v, want %v", testCase.name, got, testCase.want)
			}
		})
	}
}
