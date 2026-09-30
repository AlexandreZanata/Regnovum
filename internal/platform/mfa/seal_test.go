package mfa_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/mfa"
)

// sealerKey is the raw key the sealing tests use. It is a literal: there is no
// secret in a test, and the property under test is that a wrong key cannot
// open what another key sealed.
var sealerKey = []byte("0123456789abcdef0123456789abcdef")

func TestSealAndOpenRoundTrip(t *testing.T) {
	t.Parallel()

	sealer, err := mfa.NewSealer(sealerKey, clockseed.NewRandom())
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}

	secret := []byte("12345678901234567890")
	account := []byte("account-1")

	sealed, err := sealer.Seal(secret, account)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(sealed, secret) {
		t.Fatal("the sealed value contains the plaintext secret")
	}

	opened, err := sealer.Open(sealed, account)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(opened, secret) {
		t.Errorf("opened = %x, want %x", opened, secret)
	}

	// Two sealings of the same value under the same key differ: a reusable
	// nonce is the failure AES-GCM cannot survive.
	again, err := sealer.Seal(secret, account)
	if err != nil {
		t.Fatalf("seal again: %v", err)
	}
	if bytes.Equal(sealed, again) {
		t.Error("sealing twice produced the same bytes, which means the nonce repeats")
	}
}

// TestSealedSecretsAreBoundToTheirAccount is the property the AAD exists for: a
// sealed secret moved to another account does not open. Without it, a database
// write that swapped two rows would silently give one operator another's
// factor.
func TestSealedSecretsAreBoundToTheirAccount(t *testing.T) {
	t.Parallel()

	sealer, err := mfa.NewSealer(sealerKey, clockseed.NewRandom())
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}

	sealed, err := sealer.Seal([]byte("12345678901234567890"), []byte("account-1"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	if _, err := sealer.Open(sealed, []byte("account-2")); !errors.Is(err, mfa.ErrSealedData) {
		t.Errorf("opening under another account: err = %v, want ErrSealedData", err)
	}
	if _, err := sealer.Open(sealed, nil); !errors.Is(err, mfa.ErrSealedData) {
		t.Errorf("opening without a context: err = %v, want ErrSealedData", err)
	}
}

func TestOpenRefusesWhatItCannotTrust(t *testing.T) {
	t.Parallel()

	sealer, err := mfa.NewSealer(sealerKey, clockseed.NewRandom())
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	other, err := mfa.NewSealer([]byte("fedcba9876543210fedcba9876543210"), clockseed.NewRandom())
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}

	account := []byte("account-1")
	sealed, err := sealer.Seal([]byte("12345678901234567890"), account)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	if _, err := other.Open(sealed, account); !errors.Is(err, mfa.ErrSealedData) {
		t.Errorf("another key opened the value: %v", err)
	}

	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := sealer.Open(tampered, account); !errors.Is(err, mfa.ErrSealedData) {
		t.Errorf("a tampered value opened: %v", err)
	}

	for _, truncated := range [][]byte{{}, sealed[:5], sealed[:12]} {
		if _, err := sealer.Open(truncated, account); !errors.Is(err, mfa.ErrSealedData) {
			t.Errorf("a truncated value of %d bytes opened: %v", len(truncated), err)
		}
	}
}

func TestNewSealerRefusesTheWrongKeySize(t *testing.T) {
	t.Parallel()

	for _, key := range [][]byte{nil, {}, []byte("short"), bytes.Repeat([]byte("k"), 31), bytes.Repeat([]byte("k"), 33)} {
		if _, err := mfa.NewSealer(key, clockseed.NewRandom()); !errors.Is(err, mfa.ErrInvalidConfig) {
			t.Errorf("key of %d bytes: err = %v, want ErrInvalidConfig", len(key), err)
		}
	}
	if _, err := mfa.NewSealer(sealerKey, clockseed.NewRandom()); err != nil {
		t.Errorf("a %d-byte key was refused: %v", len(sealerKey), err)
	}

	// A sealer without an entropy source cannot draw a nonce, and it refuses
	// at construction rather than at the first sealing: a nonce that is not
	// random is the failure mode AES-GCM's security argument excludes.
	if _, err := mfa.NewSealer(sealerKey, nil); !errors.Is(err, mfa.ErrInvalidConfig) {
		t.Errorf("a sealer without an entropy source: err = %v, want ErrInvalidConfig", err)
	}
}

// TestSealingTwiceDoesNotRepeatTheNonce is the property the injected entropy
// exists for: two seals of the same plaintext under the same key and the same
// account differ, because each one draws a fresh nonce. The reader here is a
// counter, so the difference is caused by the number of draws and not by
// chance — a sealer that ignored its source would repeat the nonce and the two
// ciphertexts would be equal.
func TestSealingTwiceDoesNotRepeatTheNonce(t *testing.T) {
	t.Parallel()

	source := &countingRandom{next: 1}
	sealer, err := mfa.NewSealer(sealerKey, source)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}

	secret := []byte("12345678901234567890")
	account := []byte("account-1")
	first, err := sealer.Seal(secret, account)
	if err != nil {
		t.Fatalf("first seal: %v", err)
	}
	second, err := sealer.Seal(secret, account)
	if err != nil {
		t.Fatalf("second seal: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two seals of the same plaintext produced the same ciphertext: the nonce was reused")
	}

	// Both still open, and the second one opens to the same secret: freshness
	// of the nonce does not cost correctness.
	opened, err := sealer.Open(second, account)
	if err != nil {
		t.Fatalf("open second seal: %v", err)
	}
	if !bytes.Equal(opened, secret) {
		t.Fatalf("the second seal opened to %q, want %q", opened, secret)
	}
}

// countingRandom is a deterministic reader that answers a different byte on
// every read, so a test can tell a draw apart from a repeated value.
type countingRandom struct {
	mu   sync.Mutex
	next byte
}

func (source *countingRandom) Read(buffer []byte) (int, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	for index := range buffer {
		buffer[index] = source.next
		source.next++
	}
	return len(buffer), nil
}

func TestDecodeKeyAcceptsTheConfiguredRepresentations(t *testing.T) {
	t.Parallel()

	encoded := base64.StdEncoding.EncodeToString(sealerKey)

	decoded, err := mfa.DecodeKey(encoded)
	if err != nil {
		t.Fatalf("decode padded key: %v", err)
	}
	if !bytes.Equal(decoded, sealerKey) {
		t.Error("the padded key did not decode to the original bytes")
	}

	decoded, err = mfa.DecodeKey("  " + base64.StdEncoding.WithPadding(base64.NoPadding).EncodeToString(sealerKey) + "\n")
	if err != nil {
		t.Fatalf("decode unpadded key: %v", err)
	}
	if !bytes.Equal(decoded, sealerKey) {
		t.Error("the unpadded key did not decode to the original bytes")
	}

	for _, invalid := range []string{"", "   ", "not base64!!"} {
		if _, err := mfa.DecodeKey(invalid); !errors.Is(err, mfa.ErrInvalidConfig) {
			t.Errorf("DecodeKey(%q): err = %v, want ErrInvalidConfig", invalid, err)
		}
	}
}

// TestASealedSecretSurvivesTheEnrollmentRoundTrip is the end of the story the
// enrollment walks: generate, seal, open, verify a code. It is the only test
// here that connects the sealer to the code, and it is what proves a stored
// secret is usable without ever being stored in the clear.
func TestASealedSecretSurvivesTheEnrollmentRoundTrip(t *testing.T) {
	t.Parallel()

	sealer, err := mfa.NewSealer(sealerKey, clockseed.NewRandom())
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	config := mfa.Config{}

	secret, err := config.GenerateSecret(clockseed.NewRandom())
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}
	sealed, err := sealer.Seal(secret, []byte("account-1"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	opened, err := sealer.Open(sealed, []byte("account-1"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	now := time.Unix(1_700_000_000, 0).UTC()
	code, err := config.Code(opened, now)
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	if _, err := config.Verify(opened, code, now, -1); err != nil {
		t.Errorf("the code of the opened secret was refused: %v", err)
	}
}
