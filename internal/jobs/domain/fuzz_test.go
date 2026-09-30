package domain_test

import (
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

// Seed rationale (P24-T02): period labels are locale-neutral canonical slots,
// so the corpus stresses shape boundaries instead — leap days, month edges,
// wrong-cadence shapes, case variants, padding, hostile Unicode (bidi
// override, NUL), overlong labels and raw invalid bytes. A panic, a
// non-canonical acceptance or a non-deterministic parse persists corpus by
// the native Go fuzz contract.

// FuzzParsePeriod proves period parsing never panics and that every accepted
// label is canonical: it re-parses from its own rendering, resolves to the
// same slot through an instant, and steps back deterministically.
func FuzzParsePeriod(f *testing.F) {
	f.Add("daily", "2026-09-18")
	f.Add("monthly", "2026-09")
	f.Add("daily", "2024-02-29")
	f.Add("daily", "2023-02-29")
	f.Add("monthly", "2024-02")
	f.Add("daily", "2026-13-01")
	f.Add("daily", "2026-00-10")
	f.Add("monthly", "2026-09-18")
	f.Add("daily", "2026-9-8")
	f.Add("weekly", "2026-09-18")
	f.Add("", "")
	f.Add("daily", "")
	f.Add("DAILY", "2026-09-18")
	f.Add("daily", " 2026-09-18 ")
	f.Add("daily", "\u202e2026-09-18")
	f.Add("daily", "2026-09-18\x00")
	f.Add("daily", strings.Repeat("2", 300))
	f.Add("daily", string([]byte{0xff, 0xfe}))

	f.Fuzz(func(t *testing.T, intervalRaw, label string) {
		interval := domain.Interval(intervalRaw)
		first, err := domain.ParsePeriod(interval, label)
		if err != nil {
			if !first.IsZero() {
				t.Fatalf("invalid period (%q, %q) parsed as %q", intervalRaw, label, first.String())
			}
			return
		}
		if first.IsZero() {
			t.Fatalf("accepted period (%q, %q) reports zero", intervalRaw, label)
		}

		// The rendering is canonical: re-parsing it agrees.
		reparsed, err := domain.ParsePeriod(interval, first.String())
		if err != nil {
			t.Fatalf("re-parsing canonical %q failed: %v", first.String(), err)
		}
		if reparsed.String() != first.String() {
			t.Fatalf("period is not canonical: %q vs %q", reparsed.String(), first.String())
		}

		// The slot survives an instant round-trip: the start of the period
		// falls back into the same slot.
		start, err := first.Start()
		if err != nil {
			t.Fatalf("Start() of accepted period %q failed: %v", first.String(), err)
		}
		throughInstant, err := domain.PeriodAt(interval, start)
		if err != nil {
			t.Fatalf("PeriodAt() of accepted start failed: %v", err)
		}
		if throughInstant.String() != first.String() {
			t.Fatalf("instant round-trip moved the slot: %q vs %q", throughInstant.String(), first.String())
		}

		// Catch-up arithmetic is deterministic: stepping back zero periods
		// is the identity, twice in a row.
		back, err := first.Back(0)
		if err != nil {
			t.Fatalf("Back(0) of accepted period %q failed: %v", first.String(), err)
		}
		if back.String() != first.String() {
			t.Fatalf("Back(0) moved the slot: %q vs %q", back.String(), first.String())
		}
		again, err := domain.ParsePeriod(interval, label)
		if err != nil || again.String() != first.String() {
			t.Fatalf("period parsing is not deterministic for (%q, %q)", intervalRaw, label)
		}
	})
}

// FuzzPeriodPayload proves scheduled-run payloads never panic on arbitrary
// bytes and that every accepted payload round-trips: parsing the bytes,
// re-encoding the period and parsing again lands on the same slot.
func FuzzPeriodPayload(f *testing.F) {
	f.Add("daily", "2026-09-18")
	f.Add("monthly", "2026-09")
	f.Add("daily", "not-a-label")
	f.Add("daily", "")
	f.Add("daily", `{"period":"2026-09-18"}`)
	f.Add("daily", `{"period":"}`)
	f.Add("daily", strings.Repeat("a", 300))
	f.Add("monthly", "2026-13")

	f.Fuzz(func(t *testing.T, intervalRaw, label string) {
		interval := domain.Interval(intervalRaw)

		// Arbitrary bytes must never panic: they are either refused or a
		// stable, re-encodable slot.
		raw, err := domain.ParsePeriodPayload(interval, []byte(label))
		if err != nil {
			return
		}
		reencoded, err := domain.PeriodPayload(raw)
		if err != nil {
			t.Fatalf("PeriodPayload() of accepted payload failed: %v", err)
		}
		stable, err := domain.ParsePeriodPayload(interval, reencoded)
		if err != nil {
			t.Fatalf("re-parsing the re-encoded payload failed: %v", err)
		}
		if stable.String() != raw.String() {
			t.Fatalf("payload is not stable: %q vs %q", stable.String(), raw.String())
		}

		// A period built through the validated path always encodes to a
		// payload that parses back to itself.
		period, err := domain.ParsePeriod(interval, label)
		if err != nil {
			return
		}
		encoded, err := domain.PeriodPayload(period)
		if err != nil {
			t.Fatalf("PeriodPayload(%q) failed: %v", period.String(), err)
		}
		decoded, err := domain.ParsePeriodPayload(interval, encoded)
		if err != nil {
			t.Fatalf("round-trip of %q failed: %v", period.String(), err)
		}
		if decoded.String() != period.String() {
			t.Fatalf("payload round-trip moved the slot: %q vs %q", decoded.String(), period.String())
		}
	})
}
