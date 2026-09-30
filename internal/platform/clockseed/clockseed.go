// Package clockseed provides the production implementations of the clock,
// randomness and identifier ports declared in internal/ports (P02-T02).
//
// It is the only internal package (besides future adapters and cmd/bootstrap)
// allowed to call time.Now and the crypto/rand reader; the architecture test
// enforces that boundary, so application and domain code stays deterministic
// under test.
package clockseed

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// System is the production Clock: it reads the real wall clock.
type System struct{}

// Compile-time proof that the concrete types satisfy the ports.
var (
	_ ports.Clock       = System{}
	_ ports.Random      = CryptoRandom{}
	_ ports.IDGenerator = (*RandomIDs)(nil)
)

// NewClock returns the production clock.
func NewClock() System { return System{} }

// Now returns time.Now, rounded to the microsecond for stable log ordering.
func (System) Now() time.Time {
	return time.Now().Round(time.Microsecond)
}

// SystemClockNow is the system clock as a plain function, for the packages that
// own an injectable clock and need a default for the caller who did not inject
// one. It lives here, and not where it is used, because this package is the one
// the architecture gate allows to read the wall clock (P02-T02, ADR-012): a
// default that reached for time.Now directly would be a wall-clock read in a
// package nobody audits, which is exactly what the gate exists to prevent.
//
// A test that wants a reproducible instant injects its own source instead; the
// sources of the test platform are internal/platform/testsource.
func SystemClockNow() time.Time { return System{}.Now() }

// CryptoRandom is the production Random: it reads the crypto/rand entropy
// source. The zero value is ready to use.
type CryptoRandom struct{}

// NewRandom returns the production cryptographic randomness source.
func NewRandom() CryptoRandom { return CryptoRandom{} }

// Read fills buffer with cryptographically secure random bytes.
func (CryptoRandom) Read(buffer []byte) (int, error) {
	return cryptorand.Read(buffer)
}

// RandomIDs generates opaque, URL-safe, collision-resistant identifiers
// from 128 bits of cryptographic randomness. It is safe for concurrent use.
type RandomIDs struct {
	random io.Reader
	now    func() time.Time
	mu     sync.Mutex
	prefix string
}

// NewIDGenerator builds the production identifier generator. The prefix
// (for example the module name) keeps identifiers greppable in logs without
// leaking entropy; an empty prefix is accepted.
func NewIDGenerator(prefix string, random io.Reader, clock ports.Clock) *RandomIDs {
	return &RandomIDs{
		random: random,
		now:    clock.Now,
		prefix: prefix,
	}
}

// NewID returns a fresh identifier with a second-precision timestamp and
// 128 bits of entropy, rendered URL-safe:
// <prefix>_<unix-seconds>_<22-char-base64url> (prefix optional).
// The entropy segment is always last and the format is sealed; callers must
// treat the whole string as opaque and must not split it.
func (generator *RandomIDs) NewID() string {
	generator.mu.Lock()
	defer generator.mu.Unlock()

	entropy := make([]byte, 16)
	if _, err := io.ReadFull(generator.random, entropy); err != nil {
		panic(fmt.Sprintf("clockseed: entropy source failed: %v", err))
	}

	encoded := base64.RawURLEncoding.EncodeToString(entropy)
	seconds := generator.now().Unix()

	if generator.prefix == "" {
		return fmt.Sprintf("%d_%s", seconds, encoded)
	}
	return fmt.Sprintf("%s_%d_%s", generator.prefix, seconds, encoded)
}
