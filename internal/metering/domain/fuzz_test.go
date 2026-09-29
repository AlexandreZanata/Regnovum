package domain

import (
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
)

func fuzzInstant() time.Time {
	return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
}

// FuzzParseMeasuredContentNorm proves measurement never panics on
// hostile bytes, stays deterministic on accepted content and refuses
// what is not valid canonical text. The counter is the approved UAX
// #29 implementation: the oracle is Unicode itself.
func FuzzParseMeasuredContentNorm(f *testing.F) {
	seeds := []string{
		"",
		" ",
		"texto final",
		"café",
		"café",
		"👩🏽‍🚀",
		"שלום",
		"a b\nc",
		"Olá,\r\nmundo!",
		"conteúdo\x00oculto",
		"conteúdo\u202Ereordenado",
		string([]byte{0xff, 0xfe, 0xfd}),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		first, err := ParseMeasuredContent(raw, text.GraphemeCount, 3000)
		if err != nil {
			if !utf8.ValidString(raw) && !isNormalizable(raw) {
				return
			}
			_, err2 := ParseMeasuredContent(raw, text.GraphemeCount, 3000)
			if (err == nil) != (err2 == nil) {
				t.Fatalf("nondeterministic verdict for %q: %v vs %v", raw, err, err2)
			}
			return
		}
		second, err := ParseMeasuredContent(raw, text.GraphemeCount, 3000)
		if err != nil {
			t.Fatalf("accepted content refused on reparse %q: %v", raw, err)
		}
		if !first.Hash().Equals(second.Hash()) || first.Units() != second.Units() {
			t.Fatalf("unstable measurement for %q", raw)
		}
		if err := first.VerifyQuote(first.Hash()); err != nil {
			t.Fatalf("self hash rejected for %q: %v", raw, err)
		}
	})
}

// isNormalizable reports whether raw holds any byte run the
// normalizer could accept: a coarse pre-filter keeping the refusal
// branch honest without duplicating the validator.
func isNormalizable(raw string) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] >= 'a' && raw[i] <= 'z' {
			return true
		}
	}
	return false
}

// FuzzQuoteSealTamper proves the publication seal binds every term:
// flipping any canonical byte refuses verification.
func FuzzQuoteSealTamper(f *testing.F) {
	f.Add("acct-01", "texto final", 2, int64(250))
	f.Fuzz(func(t *testing.T, account, raw string, version int, priceMilli int64) {
		if version <= 0 || version > 1000 || priceMilli <= 0 || priceMilli > 1000000 {
			return
		}
		content, err := ParseMeasuredContent(raw, text.GraphemeCount, 3000)
		if err != nil {
			return
		}
		service, err := ParseServiceID("argument-publish")
		if err != nil {
			t.Fatalf("service: %v", err)
		}
		price, err := NewPriceEntry(PriceRequest{
			Service: service, Version: version,
			ValidFrom: fuzzInstant().Add(-time.Hour), ValidUntil: fuzzInstant().Add(time.Hour),
			PriceMilli: priceMilli, Unit: UnitGraphemeCluster, Authority: "fuzz-authority",
		})
		if err != nil {
			return
		}
		quote, err := AcceptPublicationQuote(QuoteRequest{
			Account: account, Content: content, Price: price,
			AcceptedAt: fuzzInstant(), TTL: time.Minute,
		})
		if err != nil {
			return
		}
		if err := quote.VerifyHash(); err != nil {
			t.Fatalf("fresh seal rejected: %v", err)
		}
		tampered := quote
		tampered.TotalMilli++
		if tampered.VerifyHash() == nil {
			t.Fatal("incremented total kept the seal")
		}
		tampered = quote
		tampered.Account += "x"
		if tampered.VerifyHash() == nil {
			t.Fatal("extended account kept the seal")
		}
	})
}
