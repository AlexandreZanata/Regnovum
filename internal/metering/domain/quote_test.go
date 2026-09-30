package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

func acceptFixture(t *testing.T, raw, account string, accepted time.Time, ttl time.Duration) (MeasuredContent, PriceEntry, PublicationQuote) {
	t.Helper()
	content, err := ParseMeasuredContent(raw, text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("ParseMeasuredContent() error = %v", err)
	}
	service, err := ParseServiceID("argument-publish")
	if err != nil {
		t.Fatalf("ParseServiceID() error = %v", err)
	}
	price, err := NewPriceEntry(PriceRequest{
		Service:    service,
		Version:    2,
		ValidFrom:  accepted.Add(-time.Hour),
		ValidUntil: accepted.Add(time.Hour),
		PriceMilli: 250,
		Unit:       UnitGraphemeCluster,
		Authority:  "test-authority",
	})
	if err != nil {
		t.Fatalf("NewPriceEntry() error = %v", err)
	}
	quote, err := AcceptPublicationQuote(QuoteRequest{
		Account:    account,
		Content:    content,
		Price:      price,
		AcceptedAt: accepted,
		TTL:        ttl,
	})
	if err != nil {
		t.Fatalf("AcceptPublicationQuote() error = %v", err)
	}
	return content, price, quote
}

func TestAcceptPublicationQuoteSealsExactCost(t *testing.T) {
	accepted := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	content, price, quote := acceptFixture(t, "texto final", "acct-01", accepted, 5*time.Minute)
	wantTotal := int64(content.Units()) * price.PriceMilli
	if quote.TotalMilli != wantTotal {
		t.Fatalf("total = %d, want units(%d)×price(%d)=%d", quote.TotalMilli, content.Units(), price.PriceMilli, wantTotal)
	}
	if quote.Version != price.Version || quote.Service != price.Service {
		t.Fatalf("rule = %s/v%d, want %s/v%d", quote.Service, quote.Version, price.Service, price.Version)
	}
	if !quote.ContentHash.Equals(content.Hash()) {
		t.Fatal("quote does not carry the content hash")
	}
	if !quote.AcceptedAt.Equal(accepted) || !quote.ExpiresAt.Equal(accepted.Add(5*time.Minute)) {
		t.Fatalf("window = %v..%v, want accepted+ttl", quote.AcceptedAt, quote.ExpiresAt)
	}
	if err := quote.VerifyHash(); err != nil {
		t.Fatalf("VerifyHash() = %v, want nil", err)
	}
	if err := quote.VerifyAcceptance("acct-01", content, accepted.Add(time.Minute)); err != nil {
		t.Fatalf("VerifyAcceptance(same) = %v, want nil", err)
	}
}

func TestQuoteRefusesAlteredTextOrExpiredQuote(t *testing.T) {
	accepted := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	content, _, quote := acceptFixture(t, "texto final", "acct-01", accepted, 5*time.Minute)
	edited, err := ParseMeasuredContent("texto final editado", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("edited error = %v", err)
	}
	if err := quote.VerifyAcceptance("acct-01", edited, accepted.Add(time.Minute)); !errors.Is(err, ErrQuoteMismatch) {
		t.Fatalf("altered text = %v, want ErrQuoteMismatch", err)
	}
	if err := quote.VerifyAcceptance("acct-02", content, accepted.Add(time.Minute)); !errors.Is(err, ErrQuoteMismatch) {
		t.Fatalf("other account = %v, want ErrQuoteMismatch", err)
	}
	if err := quote.VerifyAcceptance("acct-01", content, accepted.Add(6*time.Minute)); !errors.Is(err, ErrQuoteExpired) {
		t.Fatalf("after expiry = %v, want ErrQuoteExpired", err)
	}
	if quote.Live(accepted.Add(6 * time.Minute)) {
		t.Fatal("Live after expiry must be false: expired terms need a new acceptance")
	}
}

func TestQuoteBoundaryTickAndReplay(t *testing.T) {
	accepted := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	ttl := 5 * time.Minute
	content, price, quote := acceptFixture(t, "texto final", "acct-01", accepted, ttl)
	if !quote.Live(quote.ExpiresAt.Add(-time.Nanosecond)) {
		t.Fatal("tick before expiry must price")
	}
	if quote.Live(quote.ExpiresAt) {
		t.Fatal("expiry tick itself must not price")
	}
	replayed, err := AcceptPublicationQuote(QuoteRequest{
		Account:    "acct-01",
		Content:    content,
		Price:      price,
		AcceptedAt: accepted,
		TTL:        ttl,
	})
	if err != nil {
		t.Fatalf("replay error = %v", err)
	}
	if replayed.Hash != quote.Hash || replayed.TotalMilli != quote.TotalMilli || !replayed.ExpiresAt.Equal(quote.ExpiresAt) {
		t.Fatal("replay changed the sealed value: identical inputs must seal identically")
	}
}

func TestQuoteIgnoresForgedClientClock(t *testing.T) {
	accepted := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	content, _, quote := acceptFixture(t, "texto final", "acct-01", accepted, 5*time.Minute)
	forged := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := quote.VerifyAcceptance("acct-01", content, forged); !errors.Is(err, ErrQuoteExpired) {
		t.Fatalf("forged future instant = %v, want ErrQuoteExpired for that call", err)
	}
	if quote.AcceptedAt.Equal(forged) || quote.ExpiresAt.Equal(forged) {
		t.Fatal("client instant rewrote the stored quote")
	}
	if err := quote.VerifyHash(); err != nil {
		t.Fatalf("stored quote mutated by the forged call: %v", err)
	}
	if err := quote.VerifyAcceptance("acct-01", content, accepted.Add(time.Minute)); err != nil {
		t.Fatalf("server instant after forged call = %v, want nil", err)
	}
}

func TestAcceptPublicationQuoteRefusals(t *testing.T) {
	accepted := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	content, price, _ := acceptFixture(t, "texto final", "acct-01", accepted, 5*time.Minute)
	base := QuoteRequest{Account: "acct-01", Content: content, Price: price, AcceptedAt: accepted, TTL: 5 * time.Minute}
	for _, tc := range []struct {
		name string
		mute func(*QuoteRequest)
		want error
	}{
		{name: "empty account", mute: func(r *QuoteRequest) { r.Account = "" }, want: ErrInvalidQuote},
		{name: "zero content", mute: func(r *QuoteRequest) { r.Content = MeasuredContent{} }, want: ErrInvalidQuote},
		{name: "zero accepted", mute: func(r *QuoteRequest) { r.AcceptedAt = time.Time{} }, want: ErrInvalidQuote},
		{name: "zero ttl", mute: func(r *QuoteRequest) { r.TTL = 0 }, want: ErrInvalidQuote},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mute(&req)
			if _, err := AcceptPublicationQuote(req); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	outside := base
	outside.AcceptedAt = accepted.Add(2 * time.Hour)
	if _, err := AcceptPublicationQuote(outside); !errors.Is(err, ErrPriceNotFound) {
		t.Fatalf("acceptance outside price window = %v, want ErrPriceNotFound", err)
	}
	huge, err := ParseMeasuredContent(strings.Repeat("a", 3000), text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("huge error = %v", err)
	}
	hugePrice := price
	hugePrice.PriceMilli = 1 << 62
	if _, err := AcceptPublicationQuote(QuoteRequest{Account: "acct-01", Content: huge, Price: hugePrice, AcceptedAt: accepted, TTL: time.Minute}); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("overflow total = %v, want ErrInvalidQuote", err)
	}
}
