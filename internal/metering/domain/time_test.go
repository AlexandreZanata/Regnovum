package domain

import (
	"testing"
	"time"
)

func dstCounter(value string) int {
	count := 0
	for range value {
		count++
	}
	return count
}

// TestQuoteWindowIgnoresDaylightSaving proves acceptance windows
// run on UTC instants, never wall readings: across the US spring
// forward, one TTL hour stays one 3600-second hour and the boundary
// tick still decides.
func TestQuoteWindowIgnoresDaylightSaving(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation(America/New_York): %v", err)
	}
	// 2026-03-08 01:30 EST is 06:30 UTC; 03:00 EDT is 07:00 UTC and
	// 03:30 EDT is 07:30 UTC. The 02:00 hour never exists on the
	// wall, but UTC never skips it.
	accepted := time.Date(2026, 3, 8, 1, 30, 0, 0, newYork)
	expiryWall := time.Date(2026, 3, 8, 3, 30, 0, 0, newYork)
	if expiryWall.Sub(accepted) != time.Hour {
		t.Fatalf("wall span = %v, want exactly one hour across the transition", expiryWall.Sub(accepted))
	}
	service, err := ParseServiceID("argument-publish")
	if err != nil {
		t.Fatalf("ParseServiceID: %v", err)
	}
	price, err := NewPriceEntry(PriceRequest{
		Service: service, Version: 1,
		ValidFrom: accepted.Add(-time.Hour), ValidUntil: accepted.Add(2 * time.Hour),
		PriceMilli: 250, Unit: UnitGraphemeCluster, Authority: "test-authority",
	})
	if err != nil {
		t.Fatalf("NewPriceEntry: %v", err)
	}
	content, err := ParseMeasuredContent("texto final", dstCounter, 3000)
	if err != nil {
		t.Fatalf("ParseMeasuredContent: %v", err)
	}
	quote, err := AcceptPublicationQuote(QuoteRequest{
		Account: "acct-01", Content: content, Price: price, AcceptedAt: accepted, TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("AcceptPublicationQuote: %v", err)
	}
	if !quote.AcceptedAt.Equal(accepted.UTC()) || !quote.ExpiresAt.Equal(expiryWall.UTC()) {
		t.Fatalf("window = %v..%v, want UTC-anchored %v..%v",
			quote.AcceptedAt, quote.ExpiresAt, accepted.UTC(), expiryWall.UTC())
	}
	afterJump := time.Date(2026, 3, 8, 3, 0, 0, 0, newYork)
	if !quote.Live(afterJump) {
		t.Fatal("instant past the wall jump must still price inside the UTC window")
	}
	if quote.Live(expiryWall) {
		t.Fatal("expiry tick itself must not price, transition or not")
	}
	if err := quote.VerifyAcceptance("acct-01", content, afterJump); err != nil {
		t.Fatalf("VerifyAcceptance after jump = %v, want nil", err)
	}
}
