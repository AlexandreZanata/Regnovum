package domain_test

import (
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/metering/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

func TestSeasonalQuoteCapsByBookEnd(t *testing.T) {
	content, err := domain.ParseMeasuredContent("texto final", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("ParseMeasuredContent: %v", err)
	}
	service, err := domain.ParseServiceID("argument-publish")
	if err != nil {
		t.Fatalf("ParseServiceID: %v", err)
	}
	accepted := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	price, err := domain.NewPriceEntry(domain.PriceRequest{
		Service: service, Version: 2,
		ValidFrom: accepted.Add(-time.Hour), ValidUntil: accepted.Add(2 * time.Hour),
		PriceMilli: 250, Unit: domain.UnitGraphemeCluster, Authority: "test-authority",
	})
	if err != nil {
		t.Fatalf("NewPriceEntry: %v", err)
	}
	ends := accepted.Add(10 * time.Minute)
	quote, err := domain.AcceptSeasonalPublicationQuote(domain.SeasonalQuoteRequest{
		Account: "conta-1", Content: content, Price: price,
		AcceptedAt: accepted, TTL: time.Hour,
		Season: domain.SeasonKey("temporada-1"), SeasonEndsAt: ends, ResetAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("AcceptSeasonalPublicationQuote: %v", err)
	}
	if !quote.ExpiresAt.Equal(ends) {
		t.Fatalf("expiry = %v, want book end %v", quote.ExpiresAt, ends)
	}
	if string(quote.Season) != "temporada-1" {
		t.Fatalf("season = %q, want temporada-1", quote.Season)
	}
	if err := quote.VerifySeasonalAcceptance("conta-1", content, accepted, domain.SeasonKey("temporada-1")); err != nil {
		t.Fatalf("VerifySeasonalAcceptance: %v", err)
	}
	if err := quote.VerifySeasonalAcceptance("conta-1", content, accepted, domain.SeasonKey("outra-temporada")); err == nil {
		t.Fatal("preview cruzado aceitou: livro distinto deve recusar")
	}
	if _, err := domain.AcceptSeasonalPublicationQuote(domain.SeasonalQuoteRequest{
		Account: "conta-1", Content: content, Price: price,
		AcceptedAt: accepted, TTL: time.Hour,
		Season: domain.SeasonKey("temporada-1"), SeasonEndsAt: ends,
	}); err == nil {
		t.Fatal("sem reset aceitou: reset omitido nunca autoriza")
	}
	starts := accepted.Add(-time.Hour)
	if err := domain.CheckSeasonWindow(domain.SeasonKey("temporada-1"), ends, starts, ends); err == nil {
		t.Fatal("tick do fim admitiu: fim exclusivo pertence a sucessora")
	}
	if err := domain.CheckSeasonWindow(domain.SeasonKey("temporada-1"), ends.Add(-time.Nanosecond), starts, ends); err != nil {
		t.Fatalf("tick antes do fim recusou: %v", err)
	}
}
