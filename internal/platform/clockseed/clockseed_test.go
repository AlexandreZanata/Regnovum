package clockseed

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// stubClock is a deterministic Clock: it returns a fixed instant and counts
// how many times it was read. Defined next to the test, as the plan requires.
type stubClock struct {
	current time.Time
	reads   int
}

func (stub *stubClock) Now() time.Time {
	stub.reads++
	return stub.current
}

func newStubClock(instant time.Time) *stubClock {
	return &stubClock{current: instant}
}

// Compile-time proof that the stub satisfies the port.
var _ ports.Clock = (*stubClock)(nil)

// counterRandom fills each read with a pattern derived from a counter, so
// successive reads differ deterministically — reproducible, never equal.
type counterRandom struct {
	reads int
}

func (stub *counterRandom) Read(buffer []byte) (int, error) {
	stub.reads++
	for index := range buffer {
		buffer[index] = byte(stub.reads*31 + index)
	}
	return len(buffer), nil
}

var _ ports.Random = (*counterRandom)(nil)

// fixedRandom always returns the same pattern: single-read determinism.
type fixedRandom struct {
	pattern []byte
}

func (stub fixedRandom) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = stub.pattern[index%len(stub.pattern)]
	}
	return len(buffer), nil
}

var _ ports.Random = fixedRandom{}

func TestSystemClockReturnsCurrentTime(t *testing.T) {
	before := time.Now().Round(time.Microsecond)
	got := (System{}).Now()
	after := time.Now().Round(time.Microsecond)

	// A ±1 microsecond tolerance absorbs the rounding boundary of Now();
	// the assertion is that the clock reads the real wall clock, neither a
	// frozen nor a far-future value.
	lower := before.Add(-time.Microsecond)
	upper := after.Add(time.Microsecond)
	if got.Before(lower) || got.After(upper) {
		t.Fatalf("System.Now() = %v, want between %v and %v", got, lower, upper)
	}
}

func TestCryptoRandomProducesDistinctBuffers(t *testing.T) {
	first := make([]byte, 32)
	second := make([]byte, 32)

	if _, err := (CryptoRandom{}).Read(first); err != nil {
		t.Fatalf("crypto read: %v", err)
	}
	if _, err := (CryptoRandom{}).Read(second); err != nil {
		t.Fatalf("crypto read: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two crypto reads produced identical buffers")
	}
}

func TestCryptoRandomFailsWhenSourceFails(t *testing.T) {
	// A reader that always fails proves the port propagates entropy errors.
	failing := failingReader{err: errors.New("entropy exhausted")}
	var random ports.Random = failing

	if _, err := random.Read(make([]byte, 8)); err == nil {
		t.Fatal("expected error from failing entropy source")
	}
}

type failingReader struct{ err error }

func (stub failingReader) Read([]byte) (int, error) { return 0, stub.err }

func TestNewIDIsOpaqueURLSafeAndUnique(t *testing.T) {
	clock := newStubClock(time.Unix(1760000000, 0).UTC())
	generator := NewIDGenerator("arena", &counterRandom{}, clock)

	first := generator.NewID()
	second := generator.NewID()

	if first == second {
		t.Fatalf("two deterministic reads produced the same id: %q", first)
	}
}

func TestNewIDFormatIsStable(t *testing.T) {
	clock := newStubClock(time.Unix(1760000000, 0).UTC())
	generator := NewIDGenerator("arena", fixedRandom{pattern: make([]byte, 16)}, clock)

	id := generator.NewID()
	parts := strings.Split(id, "_")
	if len(parts) != 3 {
		t.Fatalf("id %q must have 3 underscore-separated parts", id)
	}
	if parts[0] != "arena" || parts[1] != "1760000000" {
		t.Fatalf("prefix/timestamp wrong in %q", id)
	}
	if len(parts[2]) != 22 { // 16 bytes in base64 RawURL = 22 chars.
		t.Fatalf("entropy part %q must have 22 chars", parts[2])
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[2]); err != nil {
		t.Fatalf("entropy part %q is not base64url: %v", parts[2], err)
	}
	if len(parts) != 3 {
		t.Fatalf("id %q must have exactly 3 segments (no checksum)", id)
	}
}

func TestNewIDWithoutPrefixAndWithRealEntropy(t *testing.T) {
	clock := newStubClock(time.Unix(1760000000, 0).UTC())
	generator := NewIDGenerator("", CryptoRandom{}, clock)

	id := generator.NewID()
	if strings.HasPrefix(id, "_") {
		t.Fatalf("id %q must not start with an underscore when the prefix is empty", id)
	}
	// Parse from the left: timestamp up to the first underscore, entropy as
	// the remainder. The entropy alphabet includes underscores, so callers
	// must never split blindly.
	timestamp, entropy, found := strings.Cut(id, "_")
	if !found {
		t.Fatalf("id %q must be timestamp_entropy", id)
	}
	if timestamp != "1760000000" {
		t.Fatalf("timestamp part wrong in %q", id)
	}
	if len(entropy) != 22 {
		t.Fatalf("entropy part %q must have 22 chars", entropy)
	}
	if _, err := base64.RawURLEncoding.DecodeString(entropy); err != nil {
		t.Fatalf("entropy part %q is not base64url: %v", entropy, err)
	}
}

func TestNewIDConcurrentGenerationIsSafe(t *testing.T) {
	clock := newStubClock(time.Unix(1760000000, 0).UTC())
	generator := NewIDGenerator("arena", CryptoRandom{}, clock)

	const workers = 8
	const perWorker = 50
	results := make(chan string, workers*perWorker)

	for worker := 0; worker < workers; worker++ {
		go func() {
			defer func() { results <- "done" }()
			for item := 0; item < perWorker; item++ {
				results <- generator.NewID()
			}
		}()
	}

	seen := make(map[string]bool, workers*perWorker+workers)
	for received := 0; received < workers*perWorker+workers; received++ {
		value := <-results
		if value == "done" {
			continue
		}
		if seen[value] {
			t.Fatalf("duplicate id %q under concurrency", value)
		}
		seen[value] = true
	}
}
