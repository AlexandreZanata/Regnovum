package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/metering/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

type publishFake struct {
	calls  int
	result *PublishResult
	err    error
	last   PublishRequest
}

func (f *publishFake) Publish(ctx context.Context, request PublishRequest) (*PublishResult, error) {
	f.calls++
	f.last = request
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func publishFixture(t *testing.T) (PublishCommand, time.Time) {
	t.Helper()
	accepted := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	content, err := domain.ParseMeasuredContent("texto final", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("ParseMeasuredContent() error = %v", err)
	}
	service, _ := domain.ParseServiceID("argument-publish")
	price, err := domain.NewPriceEntry(domain.PriceRequest{
		Service: service, Version: 2,
		ValidFrom: accepted.Add(-time.Hour), ValidUntil: accepted.Add(time.Hour),
		PriceMilli: 250, Unit: domain.UnitGraphemeCluster, Authority: "test-authority",
	})
	if err != nil {
		t.Fatalf("NewPriceEntry() error = %v", err)
	}
	quote, err := domain.AcceptPublicationQuote(domain.QuoteRequest{
		Account: "acct-01", Content: content, Price: price, AcceptedAt: accepted, TTL: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("AcceptPublicationQuote() error = %v", err)
	}
	now := accepted.Add(time.Minute)
	return PublishCommand{
		Key: "publish-01", Account: "acct-01", Content: content, Price: price, Quote: quote,
		FromKind: "user", FromLabel: "acct-01", ToKind: "treasury", ToLabel: "main", Now: now,
	}, now
}

func TestPublishUseCaseSettlesValidatedTerms(t *testing.T) {
	cmd, _ := publishFixture(t)
	fake := &publishFake{result: &PublishResult{PublicationID: "pub-1", TransferID: "tx-1", TotalMilli: cmd.Quote.TotalMilli, Replayed: false}}
	uc, err := NewPublishUseCase(fake)
	if err != nil {
		t.Fatalf("NewPublishUseCase() error = %v", err)
	}
	result, err := uc.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result != fake.result {
		t.Fatal("use case must return the repository outcome untouched")
	}
	if fake.calls != 1 {
		t.Fatalf("calls = %d, want 1", fake.calls)
	}
	if fake.last.PayloadHash == "" || fake.last.At.IsZero() {
		t.Fatal("validated request must carry the payload seal and the judgment instant")
	}
	if _, err := NewPublishUseCase(nil); !errors.Is(err, ErrInvalidPublishConfig) {
		t.Fatalf("nil repository = %v, want ErrInvalidPublishConfig", err)
	}
}

func TestPublishUseCaseKeepsAcceptedPricingAcrossPriceChange(t *testing.T) {
	cmd, _ := publishFixture(t)
	// The v1 window ends before settlement, with the quote still
	// live: a later v2 must not move the accepted terms.
	cmd.Price.ValidUntil = cmd.Quote.AcceptedAt.Add(time.Minute)
	cmd.Now = cmd.Quote.AcceptedAt.Add(10 * time.Minute)
	if cmd.Price.Covers(cmd.Now.UTC()) {
		t.Fatal("fixture must place settlement outside the v1 window")
	}
	if !cmd.Quote.Live(cmd.Now) {
		t.Fatal("fixture must keep the quote live at settlement")
	}
	fake := &publishFake{result: &PublishResult{}}
	uc, _ := NewPublishUseCase(fake)
	if _, err := uc.Execute(context.Background(), cmd); err != nil {
		t.Fatalf("Execute() = %v, want settlement at accepted v1 terms", err)
	}
	if fake.calls != 1 {
		t.Fatal("accepted pricing must reach the store despite the price change")
	}
}

func TestPublishUseCaseRefusesBeforeAnyWrite(t *testing.T) {
	cmd, now := publishFixture(t)
	edited, err := domain.ParseMeasuredContent("texto editado", text.GraphemeCount, 3000)
	if err != nil {
		t.Fatalf("edited error = %v", err)
	}
	otherPrice := cmd.Price
	otherPrice.Version = 9
	cases := []struct {
		name string
		mute func(*PublishCommand)
		want error
	}{
		{name: "empty key", mute: func(c *PublishCommand) { c.Key = "" }, want: domain.ErrInvalidPublishKey},
		{name: "other account", mute: func(c *PublishCommand) { c.Account = "acct-02" }, want: domain.ErrQuoteMismatch},
		{name: "edited content", mute: func(c *PublishCommand) { c.Content = edited }, want: domain.ErrQuoteMismatch},
		{name: "expired quote", mute: func(c *PublishCommand) { c.Now = now.Add(time.Hour) }, want: domain.ErrQuoteExpired},
		{name: "price version drift", mute: func(c *PublishCommand) { c.Price = otherPrice }, want: domain.ErrInvalidQuote},
		{name: "empty endpoint", mute: func(c *PublishCommand) { c.ToLabel = " " }, want: domain.ErrInvalidQuote},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			muted := cmd
			tc.mute(&muted)
			fake := &publishFake{result: &PublishResult{}}
			uc, _ := NewPublishUseCase(fake)
			if _, err := uc.Execute(context.Background(), muted); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if fake.calls != 0 {
				t.Fatal("refused envelope must never reach the store")
			}
		})
	}
}
