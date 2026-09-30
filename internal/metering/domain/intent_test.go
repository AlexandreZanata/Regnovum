package domain

import (
	"errors"
	"testing"
)

func TestParsePublishKeyRefusals(t *testing.T) {
	for _, raw := range []string{"", " key", "key ", "key\x00", "a\nb", "x\ty"} {
		if _, err := ParsePublishKey(raw); !errors.Is(err, ErrInvalidPublishKey) {
			t.Errorf("ParsePublishKey(%q) = %v, want ErrInvalidPublishKey", raw, err)
		}
	}
	long := ""
	for range maxPublishKeyRunes + 1 {
		long += "k"
	}
	if _, err := ParsePublishKey(long); !errors.Is(err, ErrInvalidPublishKey) {
		t.Errorf("oversized key = %v, want ErrInvalidPublishKey", err)
	}
	key, err := ParsePublishKey("publish-01")
	if err != nil {
		t.Fatalf("ParsePublishKey() error = %v", err)
	}
	if key.String() != "publish-01" {
		t.Fatalf("key = %q, want publish-01", key.String())
	}
}

func payloadFixture(account, service string, version, units int, amount int64, content, quote, fromLabel string) PublishPayload {
	return PublishPayload{
		Account: account, Service: service, Version: version, Units: units,
		AmountMilli: amount, ContentHash: content, QuoteHash: quote,
		FromKind: "user", FromLabel: fromLabel, ToKind: "treasury", ToLabel: "main",
	}
}

func TestPublishPayloadHashBindsEveryTerm(t *testing.T) {
	base := PublishPayloadHash(payloadFixture("acct", "argument-publish", 2, 11, 2750, "v1:aa", "hash-q", "acct"))
	for _, tc := range []struct {
		name string
		hash string
	}{
		{name: "account", hash: PublishPayloadHash(payloadFixture("other", "argument-publish", 2, 11, 2750, "v1:aa", "hash-q", "acct"))},
		{name: "service", hash: PublishPayloadHash(payloadFixture("acct", "arena-publish", 2, 11, 2750, "v1:aa", "hash-q", "acct"))},
		{name: "version", hash: PublishPayloadHash(payloadFixture("acct", "argument-publish", 3, 11, 2750, "v1:aa", "hash-q", "acct"))},
		{name: "amount", hash: PublishPayloadHash(payloadFixture("acct", "argument-publish", 2, 11, 2751, "v1:aa", "hash-q", "acct"))},
		{name: "content", hash: PublishPayloadHash(payloadFixture("acct", "argument-publish", 2, 11, 2750, "v1:bb", "hash-q", "acct"))},
		{name: "endpoint", hash: PublishPayloadHash(payloadFixture("acct", "argument-publish", 2, 11, 2750, "v1:aa", "hash-q", "other"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.hash == base {
				t.Fatal("changed term reused the payload hash")
			}
		})
	}
	if again := PublishPayloadHash(payloadFixture("acct", "argument-publish", 2, 11, 2750, "v1:aa", "hash-q", "acct")); again != base {
		t.Fatal("identical terms must seal identically")
	}
}

func TestTotalForRefusesOverflow(t *testing.T) {
	total, err := TotalFor(11, 250)
	if err != nil || total != 2750 {
		t.Fatalf("TotalFor(11,250) = %d,%v, want 2750,nil", total, err)
	}
	if _, err := TotalFor(0, 250); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("zero units = %v, want ErrInvalidQuote", err)
	}
	if _, err := TotalFor(3000, 1<<62); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("overflow = %v, want ErrInvalidQuote", err)
	}
}
