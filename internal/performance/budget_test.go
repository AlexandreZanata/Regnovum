package performance_test

import (
	"sort"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/exportjson"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// Hot-function budgets, P28-T02.
//
// Measured 2026-09-28 on Linux x86_64, i7-13620H (16 CPUs), Go 1.27.1, with
// `go test -bench . -benchtime 100x -count 3` (argon2 ~47ms, graphemes
// ~0.8µs, export ~2.7µs, ratelimit ~22ns, cache validator ~210ns, allocate
// ~18ns, i18n ~110ns, derive ~110ns). Budgets sit 3–8× above those samples
// so CI variance never fails the gate; a genuine regression clears them by
// an order of magnitude. Budgets change only with a new recorded
// measurement, never to accommodate slower code.
type hotBudget struct {
	name        string
	iters       int
	samples     int
	maxMedianNs int64
	maxAllocs   int
	run         func()
}

// measureHot runs warmup once untimed, then collects per-op nanoseconds for
// every sample. Timing in a plain loop (not testing.B) keeps slow and fast
// operations on the same harness with explicit iteration counts.
func measureHot(samples, iters int, run func()) []float64 {
	run()
	observations := make([]float64, 0, samples)
	for s := 0; s < samples; s++ {
		start := time.Now()
		for i := 0; i < iters; i++ {
			run()
		}
		observations = append(observations, float64(time.Since(start).Nanoseconds())/float64(iters))
	}
	return observations
}

func medianNs(observations []float64) float64 {
	ordered := append([]float64(nil), observations...)
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	if len(ordered)%2 == 0 {
		return (ordered[middle-1] + ordered[middle]) / 2
	}
	return ordered[middle]
}

func TestHotFunctionBudgets(t *testing.T) {
	hasher, encoded, err := newHotHasher()
	if err != nil {
		t.Fatalf("hot hasher: %v", err)
	}
	changes, err := newHotChanges()
	if err != nil {
		t.Fatalf("hot changes: %v", err)
	}
	debitAmount, debitFree, debitPurchased, err := newHotDebit()
	if err != nil {
		t.Fatalf("hot debit: %v", err)
	}
	initial, err := positionsdomain.ParsePosition(positionsdomain.PositionAgree)
	if err != nil {
		t.Fatalf("ParsePosition: %v", err)
	}
	encoder := exportjson.NewEncoder()
	document := hotExportDocument()
	validatorBody := []byte(`{"feed":["arena-1","arena-2"],"cursor":"018f6b2a-0000-7000-8000-000000000001"}`)
	formatValues := map[string]string{"subject": "A AGI existirá até 2040"}

	budgets := []hotBudget{
		{
			name: "argon2-verify", iters: 3, samples: 5,
			maxMedianNs: int64(150 * time.Millisecond), maxAllocs: 70,
			run: func() {
				match, err := hasher.VerifyPassword(hotPassword, encoded)
				if err != nil || !match {
					t.Errorf("VerifyPassword: match=%v err=%v", match, err)
				}
			},
		},
		{
			name: "grapheme-count", iters: 2000, samples: 5,
			maxMedianNs: 5000, maxAllocs: 0,
			run: func() { hotSink = text.GraphemeCount(hotGraphemes) },
		},
		{
			name: "export-json-encode", iters: 2000, samples: 5,
			maxMedianNs: 15000, maxAllocs: 8,
			run: func() {
				encoded, err := encoder.EncodePersonalExport(document)
				if err != nil {
					t.Errorf("EncodePersonalExport: %v", err)
				}
				hotSink = encoded
			},
		},
		{
			name: "ratelimit-policy-for", iters: 20000, samples: 5,
			maxMedianNs: 200, maxAllocs: 2,
			run: func() {
				policy, ok := ratelimit.PolicyFor(ratelimit.ActionAuthLogin)
				if !ok {
					t.Error("PolicyFor(auth.login) is not declared")
				}
				hotSink = policy
			},
		},
		{
			name: "httpcache-validator", iters: 5000, samples: 5,
			maxMedianNs: 1500, maxAllocs: 6,
			run: func() { hotSink = httpcache.Validator(validatorBody) },
		},
		{
			name: "wallet-allocate-debit", iters: 20000, samples: 5,
			maxMedianNs: 150, maxAllocs: 2,
			run: func() {
				allocation, err := walletdomain.AllocateDebit(debitAmount, debitFree, debitPurchased)
				if err != nil {
					t.Errorf("AllocateDebit: %v", err)
				}
				hotSink = allocation
			},
		},
		{
			name: "i18n-format", iters: 5000, samples: 5,
			maxMedianNs: 800, maxAllocs: 4,
			run: func() {
				formatted, err := i18n.Format("pt-BR", "arenas.document.page_title", formatValues)
				if err != nil {
					t.Errorf("Format: %v", err)
				}
				hotSink = formatted
			},
		},
		{
			name: "derive-current-position", iters: 5000, samples: 5,
			maxMedianNs: 800, maxAllocs: 5,
			run: func() {
				current, version, err := positionsdomain.DeriveCurrentPosition(initial, changes)
				if err != nil {
					t.Errorf("DeriveCurrentPosition: %v", err)
				}
				hotSink = []any{current, version}
			},
		},
	}

	for _, budget := range budgets {
		t.Run(budget.name, func(t *testing.T) {
			observations := measureHot(budget.samples, budget.iters, budget.run)
			median := medianNs(observations)
			allocs := testing.AllocsPerRun(budget.iters, budget.run)
			t.Logf("%s: samples=%v median=%.1fns/op allocs=%.1f/op budget=(%.0fns, %d)",
				budget.name, observations, median, allocs, float64(budget.maxMedianNs), budget.maxAllocs)
			if median > float64(budget.maxMedianNs) {
				t.Fatalf("%s median %.1fns/op exceeds budget %dns/op (samples %v)",
					budget.name, median, budget.maxMedianNs, observations)
			}
			if int(allocs+0.5) > budget.maxAllocs {
				t.Fatalf("%s allocs %.1f/op exceeds budget %d/op", budget.name, allocs, budget.maxAllocs)
			}
		})
	}
}
