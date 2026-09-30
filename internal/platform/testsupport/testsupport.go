// Package testsupport provides the scenario builders of the test platform
// (P22-T03): one typed builder per noun a journey is made of — account,
// session, Arena, position, argument, wallet, entitlement, billing, moderation
// and job — each valid by default and each deterministic from a registered
// seed.
//
// Three rules shape the surface, and each one is proved by a test here rather
// than promised in this comment:
//
//   - **Valid by default.** A builder returns what the product accepts. The
//     values come out of the product's own constructors and state transitions
//     (identity.NewAccount, Arena.Publish, jobs.Job.Validate, ...), so a
//     scenario that names no override cannot begin in a state the product
//     would refuse — and a builder that cannot satisfy an invariant returns the
//     domain's error instead of a value nobody would accept.
//   - **No shared state.** Every call builds from its own identifier and its
//     own slices. Two calls never alias the same pointer, slice or map, so a
//     scenario that mutates what it was handed cannot change what another
//     scenario sees, in this test or the next.
//   - **Invalid inputs have names.** The ordinary surface cannot express an
//     invalid value: a test that needs one calls a helper of invalid.go, whose
//     name says so and which answers with the domain's refusal instead of the
//     invalid value. A deliberate contravention is therefore findable by
//     reading ("Invalid..."), and it can never be mistaken for a fixture.
//
// Determinism comes from internal/platform/testsource. The builder holds one
// explicit clock and one explicit entropy stream, and every identifier, instant
// and key is a pure function of the seed that SeedFor registers: the same seed
// builds equal values, and two calls in one run build distinct ones because the
// identifier generator advances. Nothing here reads the wall clock, so a
// scenario cannot change its result with the hour it runs at.
//
// Nothing the product ships may import this package: the builders exist to
// compose tests, and the architecture test refuses an import from a non-test
// file exactly as it does for testsource.
package testsupport

import (
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// scenarioEpoch is the date every scenario starts from, before the seed moves
// it. A fixed, committed date keeps a scenario's instants readable in a failure
// message, and the seed's own offset is what makes two seeds two scenarios.
var scenarioEpoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// Builder builds the values of one scenario. Create it with New, which
// registers the seed in the test log, and then ask it for the nouns the
// scenario needs.
type Builder struct {
	t      *testing.T
	seed   int64
	clock  *testsource.Clock
	random *testsource.Random
	ids    ports.IDGenerator
}

// New starts a builder for one test. The seed is the one SeedFor registers:
// `ARENA_TEST_SEED=<seed> go test ./...` rebuilds the same scenario, and a seed
// that does not parse fails the test instead of quietly running the default.
//
// The identifier prefix is derived from the test's name, so a value that leaks
// into a failure message names the scenario that built it.
func New(t *testing.T) *Builder {
	t.Helper()
	return NewWithSeed(t, testsource.SeedFor(t))
}

// NewWithSeed starts a builder for an explicit seed. It is what a test that
// compares two scenarios uses — "another seed is another dataset" is a claim
// about two builders, and it cannot be stated with one registered seed. The
// seed is literal in the test source, so the run is replayable by reading it.
//
// The prefix still comes from the test's name, so a value that leaks into a
// failure message names the scenario that built it, seed or no seed.
func NewWithSeed(t *testing.T, seed int64) *Builder {
	t.Helper()
	prefix := "ts-" + slugPrefix(t.Name())

	// The seed decides the start of the scenario, not just its stream: two
	// seeds are two hours of the same day, which is what makes "another seed is
	// another run" observable in a value and not only in a slice of bytes.
	instant := scenarioEpoch.Add(time.Duration(seed%24) * time.Hour)

	random := testsource.NewRandom(seed)
	clock := testsource.NewClock(instant)
	return &Builder{
		t:      t,
		seed:   seed,
		clock:  clock,
		random: random,
		ids:    testsource.NewIDs(prefix, random, clock),
	}
}

// Seed answers the registered seed of this scenario.
func (b *Builder) Seed() int64 { return b.seed }

// Now answers the instant the scenario is stopped at. It is the only source of
// time a builder uses, and it never moves on its own.
func (b *Builder) Now() time.Time { return b.clock.Now() }

// Advance moves the scenario's clock forward, which is how a test crosses a
// window, an expiry or a lease deliberately instead of waiting for one. It
// answers the new instant.
func (b *Builder) Advance(duration time.Duration) time.Time { return b.clock.Advance(duration) }

// Source answers the deterministic entropy stream, for the rare scenario that
// needs a token of its own (a session hash, a webhook secret).
func (b *Builder) Source() *testsource.Random { return b.random }

// id answers one fresh opaque identifier from the product's own generator
// (clockseed.RandomIDs). It is what a scenario uses where the product uses it:
// an idempotency key, a reference, a token that no table keys on.
func (b *Builder) id() string { return b.ids.NewID() }

// shortToken answers a short lowercase hexadecimal token drawn from the
// scenario's stream. It is what the builders append to a default the schema
// makes unique — an account address and an Arena slug — so that building the
// same noun twice produces two rows instead of one collision. The value is a
// pure function of the seed, like everything else here.
func (b *Builder) shortToken() string {
	raw := make([]byte, 4)
	if written, err := b.Source().Read(raw); err != nil || written != len(raw) {
		b.t.Fatalf("testsupport: the deterministic stream answered %d bytes: %v", written, err)
		return ""
	}
	const hex = "0123456789abcdef"
	token := make([]byte, 0, 8)
	for _, octet := range raw {
		token = append(token, hex[octet>>4], hex[octet&0x0F])
	}
	return string(token)
}

// Identifier answers the identifier the storage layer assigns to a new row: a
// canonical UUID whose first 48 bits are the scenario's instant in milliseconds
// and whose remaining bits come from the scenario's stream.
//
// The shape is not a preference. Every table of the schema keys on `uuid`
// generated by the database (PostgreSQL 18's `uuidv7()`, which is sortable by
// creation), and the adapters parse identifiers with `pgtype.UUID.Scan`; a
// builder that invented an opaque string would answer a value the domain
// accepts and the storage layer refuses, which is the kind of fixture this
// package exists to make impossible. Deriving it from the instant and the
// stream keeps the scenario reproducible and the identifier ordered.
func (b *Builder) Identifier() string {
	millis := uint64(b.Now().UnixMilli()) & 0xFFFFFFFFFFFF

	entropy := make([]byte, 10)
	if written, err := b.Source().Read(entropy); err != nil || written != len(entropy) {
		b.t.Fatalf("testsupport: the deterministic stream answered %d bytes: %v", written, err)
		return ""
	}

	var value [16]byte
	value[0] = byte(millis >> 40)
	value[1] = byte(millis >> 32)
	value[2] = byte(millis >> 24)
	value[3] = byte(millis >> 16)
	value[4] = byte(millis >> 8)
	value[5] = byte(millis)
	copy(value[6:], entropy)
	// Version 7 (time-ordered, like the database's own generator) and the
	// RFC 4122 variant, so every parser accepts the value.
	value[6] = (value[6] & 0x0F) | 0x70
	value[8] = (value[8] & 0x3F) | 0x80

	const hex = "0123456789abcdef"
	rendered := make([]byte, 0, 36)
	for index, octet := range value {
		switch index {
		case 4, 6, 8, 10:
			rendered = append(rendered, '-')
		}
		rendered = append(rendered, hex[octet>>4], hex[octet&0x0F])
	}
	return string(rendered)
}

// slugPrefix turns a test name into the lowercase token the identifiers carry.
// It is deliberately total: any name yields a usable prefix, because a builder
// that refused to run over an exotic test name would be a builder nobody could
// use in the one test that needed a table.
func slugPrefix(name string) string {
	var builder strings.Builder
	previousDash := false
	for _, symbol := range strings.ToLower(name) {
		switch {
		case symbol >= 'a' && symbol <= 'z', symbol >= '0' && symbol <= '9':
			builder.WriteRune(symbol)
			previousDash = false
		case !previousDash:
			builder.WriteByte('-')
			previousDash = true
		}
		if builder.Len() >= 32 {
			break
		}
	}
	trimmed := strings.Trim(builder.String(), "-")
	if trimmed == "" {
		return "scenario"
	}
	return trimmed
}
