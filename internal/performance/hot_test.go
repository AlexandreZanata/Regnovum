// Package performance_test pins microbenchmarks for the backend hot
// functions named by P28-T02: password hashing, grapheme counting,
// export serialization, policy checks, domain allocation, i18n formatting
// and position aggregate derivation.
//
// Every benchmark uses fixed inputs, warms up before the timer starts and
// consumes its result through hotSink so the compiler cannot eliminate the
// measured work. Allocations are reported; budgets are enforced separately
// by TestHotFunctionBudgets in budget_test.go, which compares medians of
// several samples instead of trusting a single run.
package performance_test

import (
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpcache"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/exportjson"
	profilesapp "github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// hotSink consumes benchmark results so dead-code elimination cannot drop
// the measured call. It is read after the loop, never asserted.
var hotSink any

// hotPassword is the single fixed credential every hashing benchmark uses.
// Fixed inputs keep runs comparable; password variety is a correctness
// concern covered by the hasher unit tests, not by timing.
const hotPassword = "BenchmarkPassword2026!"

func newHotHasher() (*argon2id.Hasher, string, error) {
	hasher, err := argon2id.New(argon2id.DefaultParams(), clockseed.NewRandom())
	if err != nil {
		return nil, "", err
	}
	encoded, err := hasher.HashPassword(hotPassword)
	if err != nil {
		return nil, "", err
	}
	return hasher, encoded, nil
}

func mustHotHasher(b *testing.B) (*argon2id.Hasher, string) {
	b.Helper()
	hasher, encoded, err := newHotHasher()
	if err != nil {
		b.Fatalf("hot hasher: %v", err)
	}
	return hasher, encoded
}

func BenchmarkArgon2Verify(b *testing.B) {
	hasher, encoded := mustHotHasher(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		match, err := hasher.VerifyPassword(hotPassword, encoded)
		if err != nil || !match {
			b.Fatalf("VerifyPassword: match=%v err=%v", match, err)
		}
		hotSink = match
	}
}

// hotGraphemes mixes ASCII, latin accents, CJK, combining marks and a ZWJ
// emoji sequence: the shapes cluster segmentation must actually walk.
const hotGraphemes = "Olá, 世界! café naïve façade 👨‍👩‍👧‍👦 é covfefe"

func BenchmarkGraphemeCount(b *testing.B) {
	if text.GraphemeCount(hotGraphemes) <= 0 {
		b.Fatal("GraphemeCount of fixture is not positive")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hotSink = text.GraphemeCount(hotGraphemes)
	}
}

func hotExportDocument() profilesapp.PersonalExportDocument {
	at := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	return profilesapp.PersonalExportDocument{
		SchemaVersion:      1,
		GeneratedAt:        at,
		ExcludedCategories: []string{"moderation"},
		PersonalExportSections: profilesapp.PersonalExportSections{
			Account: profilesapp.PersonalExportAccount{
				ID:            "018f6b2a-0000-7000-8000-000000000001",
				Email:         "export-bench@arena.example.com",
				Status:        "active",
				EmailVerified: true,
				CreatedAt:     at,
			},
			Positions: []profilesapp.PersonalExportPosition{
				{ArenaID: "018f6b2a-0000-7000-8000-000000000010", ArenaSlug: "arena-sintetica", ArenaStatement: "A AGI existirá até 2040", InitialPosition: "agree", CurrentPosition: "agree", Version: 3, CreatedAt: at, UpdatedAt: at},
				{ArenaID: "018f6b2a-0000-7000-8000-000000000011", ArenaSlug: "outra-arena", ArenaStatement: "Outro enunciado", InitialPosition: "disagree", CurrentPosition: "disagree", Version: 1, CreatedAt: at, UpdatedAt: at},
			},
			Arguments: []profilesapp.PersonalExportArgument{
				{ID: "018f6b2a-0000-7000-8000-000000000020", ArenaID: "018f6b2a-0000-7000-8000-000000000010", Relation: "for", Content: "Um argumento sintético para medir serialização.", Status: "published", CreatedAt: at},
			},
		},
	}
}

func BenchmarkExportJSONEncode(b *testing.B) {
	encoder := exportjson.NewEncoder()
	document := hotExportDocument()
	if _, err := encoder.EncodePersonalExport(document); err != nil {
		b.Fatalf("EncodePersonalExport: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		encoded, err := encoder.EncodePersonalExport(document)
		if err != nil {
			b.Fatalf("EncodePersonalExport: %v", err)
		}
		hotSink = encoded
	}
}

func BenchmarkRateLimitPolicyFor(b *testing.B) {
	if _, ok := ratelimit.PolicyFor(ratelimit.ActionAuthLogin); !ok {
		b.Fatal("PolicyFor(auth.login) is not declared")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		policy, ok := ratelimit.PolicyFor(ratelimit.ActionAuthLogin)
		if !ok {
			b.Fatal("PolicyFor(auth.login) is not declared")
		}
		hotSink = policy
	}
}

func BenchmarkHTTPCacheValidator(b *testing.B) {
	body := []byte(`{"feed":["arena-1","arena-2"],"cursor":"018f6b2a-0000-7000-8000-000000000001"}`)
	if httpcache.Validator(body) == "" {
		b.Fatal("Validator returned an empty tag")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hotSink = httpcache.Validator(body)
	}
}

func mustHotInk(b *testing.B, amount int64) walletdomain.Ink {
	b.Helper()
	ink, err := walletdomain.NewInk(amount)
	if err != nil {
		b.Fatalf("NewInk(%d): %v", amount, err)
	}
	return ink
}

func newHotDebit() (amount, free, purchased walletdomain.Ink, err error) {
	if amount, err = walletdomain.NewInk(30); err != nil {
		return amount, free, purchased, err
	}
	if free, err = walletdomain.NewInk(100); err != nil {
		return amount, free, purchased, err
	}
	purchased, err = walletdomain.NewInk(1000)
	return amount, free, purchased, err
}

func BenchmarkWalletAllocateDebit(b *testing.B) {
	amount := mustHotInk(b, 30)
	free := mustHotInk(b, 100)
	purchased := mustHotInk(b, 1000)
	if _, err := walletdomain.AllocateDebit(amount, free, purchased); err != nil {
		b.Fatalf("AllocateDebit: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		allocation, err := walletdomain.AllocateDebit(amount, free, purchased)
		if err != nil {
			b.Fatalf("AllocateDebit: %v", err)
		}
		hotSink = allocation
	}
}

func BenchmarkI18nFormat(b *testing.B) {
	values := map[string]string{"subject": "A AGI existirá até 2040"}
	if _, err := i18n.Format("pt-BR", "arenas.document.page_title", values); err != nil {
		b.Fatalf("Format: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		formatted, err := i18n.Format("pt-BR", "arenas.document.page_title", values)
		if err != nil {
			b.Fatalf("Format: %v", err)
		}
		hotSink = formatted
	}
}

func newHotChanges() ([]positionsdomain.PositionChange, error) {
	arenaID, err := positionsdomain.ParseArenaID("018f6b2a-0000-7000-8000-000000000001")
	if err != nil {
		return nil, err
	}
	accountID, err := positionsdomain.ParseAccountID("018f6b2a-0000-7000-8000-000000000002")
	if err != nil {
		return nil, err
	}
	from, err := positionsdomain.ParsePosition(positionsdomain.PositionAgree)
	if err != nil {
		return nil, err
	}
	to, err := positionsdomain.ParsePosition(positionsdomain.PositionDisagree)
	if err != nil {
		return nil, err
	}
	at := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	changes := make([]positionsdomain.PositionChange, 0, 32)
	for version := int32(2); version <= 33; version++ {
		change, err := positionsdomain.NewPositionChange(arenaID, accountID, from, to, version, at.Add(time.Duration(version)*time.Second))
		if err != nil {
			return nil, err
		}
		changes = append(changes, change)
		from, to = to, from
	}
	return changes, nil
}

func hotPositionChanges(b *testing.B) []positionsdomain.PositionChange {
	b.Helper()
	changes, err := newHotChanges()
	if err != nil {
		b.Fatalf("hot changes: %v", err)
	}
	return changes
}

func BenchmarkDeriveCurrentPosition(b *testing.B) {
	changes := hotPositionChanges(b)
	initial, err := positionsdomain.ParsePosition(positionsdomain.PositionAgree)
	if err != nil {
		b.Fatalf("ParsePosition: %v", err)
	}
	if _, _, err := positionsdomain.DeriveCurrentPosition(initial, changes); err != nil {
		b.Fatalf("DeriveCurrentPosition: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		current, version, err := positionsdomain.DeriveCurrentPosition(initial, changes)
		if err != nil {
			b.Fatalf("DeriveCurrentPosition: %v", err)
		}
		hotSink = []any{current, version}
	}
}
