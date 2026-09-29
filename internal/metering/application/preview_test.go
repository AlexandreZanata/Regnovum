package application

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/metering/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

func previewCatalog(t *testing.T, at time.Time) domain.Catalog {
	t.Helper()
	service, _ := domain.ParseServiceID("argument-publish")
	price, err := domain.NewPriceEntry(domain.PriceRequest{
		Service: service, Version: 3,
		ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour),
		PriceMilli: 250, Unit: domain.UnitGraphemeCluster, Authority: "test-authority",
	})
	if err != nil {
		t.Fatalf("NewPriceEntry: %v", err)
	}
	var catalog domain.Catalog
	if err := catalog.Add(price); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return catalog
}

func TestPreviewUseCasePricesWithoutStoring(t *testing.T) {
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	uc, err := NewPreviewUseCase(previewCatalog(t, at), text.GraphemeCount)
	if err != nil {
		t.Fatalf("NewPreviewUseCase: %v", err)
	}
	result, err := uc.Execute(PreviewCommand{
		Account: "acct-01", Content: "texto final", Service: "argument-publish",
		Now: at, MaxUnits: 3000, TTL: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// "texto final" counts 11 clusters with the approved counter.
	if result.Units != 11 || result.TotalMilli != 11*250 || result.Version != 3 {
		t.Fatalf("preview = %+v, want 11 units at 250 v3", result)
	}
	if result.ContentHash == "" || result.QuoteHash == "" {
		t.Fatal("preview must bind the confirmation hash")
	}
	if !result.ExpiresAt.Equal(at.Add(5 * time.Minute)) {
		t.Fatalf("expires = %v, want accepted+ttl", result.ExpiresAt)
	}
	if _, err := NewPreviewUseCase(previewCatalog(t, at), nil); !errors.Is(err, ErrInvalidPublishConfig) {
		t.Fatalf("nil counter = %v, want ErrInvalidPublishConfig", err)
	}
}

func TestPreviewUseCaseRefusesBeforePricing(t *testing.T) {
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	uc, _ := NewPreviewUseCase(previewCatalog(t, at), text.GraphemeCount)
	cases := []struct {
		name string
		cmd  PreviewCommand
		want error
	}{
		{name: "blank account", cmd: PreviewCommand{Account: " ", Content: "texto", Service: "argument-publish", Now: at, MaxUnits: 3000, TTL: time.Minute}, want: domain.ErrInvalidQuote},
		{name: "bad service", cmd: PreviewCommand{Account: "a", Content: "texto", Service: "Nope", Now: at, MaxUnits: 3000, TTL: time.Minute}, want: domain.ErrInvalidService},
		{name: "zero instant", cmd: PreviewCommand{Account: "a", Content: "texto", Service: "argument-publish", MaxUnits: 3000, TTL: time.Minute}, want: domain.ErrInvalidQuote},
		{name: "empty content", cmd: PreviewCommand{Account: "a", Content: "  ", Service: "argument-publish", Now: at, MaxUnits: 3000, TTL: time.Minute}, want: domain.ErrEmptyMeasuredContent},
		{name: "unapproved service", cmd: PreviewCommand{Account: "a", Content: "texto", Service: "casino-bet", Now: at, MaxUnits: 3000, TTL: time.Minute}, want: domain.ErrPriceNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := uc.Execute(tc.cmd); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}
