package argon2id_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/logging"
)

type staticRandom struct {
	byteVal byte
}

func (s staticRandom) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = s.byteVal
	}
	return len(p), nil
}

type failingRandom struct{}

func (f failingRandom) Read(p []byte) (int, error) {
	return 0, errors.New("simulated entropy failure")
}

func newTestHasher(t *testing.T, params argon2id.Params) *argon2id.Hasher {
	t.Helper()
	h, err := argon2id.New(params, clockseed.NewRandom())
	if err != nil {
		t.Fatalf("failed to create test hasher: %v", err)
	}
	return h
}

func TestHashAndVerifySuccess(t *testing.T) {
	hasher := newTestHasher(t, argon2id.FastParams())
	password := "Correct-Horse-Battery-Staple-2026!"

	encodedHash, err := hasher.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	if !strings.HasPrefix(encodedHash, "$argon2id$v=19$") {
		t.Errorf("expected hash to start with '$argon2id$v=19$', got %q", encodedHash)
	}

	match, err := hasher.VerifyPassword(password, encodedHash)
	if err != nil {
		t.Fatalf("VerifyPassword returned unexpected error: %v", err)
	}
	if !match {
		t.Error("VerifyPassword returned false for the correct password")
	}
}

func TestVerifyWrongPassword(t *testing.T) {
	hasher := newTestHasher(t, argon2id.FastParams())
	encodedHash, err := hasher.HashPassword("MySecretPassword42")
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	match, err := hasher.VerifyPassword("WrongPassword!", encodedHash)
	if err != nil {
		t.Fatalf("VerifyPassword failed unexpectedly: %v", err)
	}
	if match {
		t.Error("VerifyPassword returned true for wrong password")
	}
}

func TestEmptyPassword(t *testing.T) {
	hasher := newTestHasher(t, argon2id.FastParams())

	_, err := hasher.HashPassword("")
	if !errors.Is(err, argon2id.ErrEmptyPassword) {
		t.Errorf("expected ErrEmptyPassword on HashPassword, got: %v", err)
	}

	validHash, err := hasher.HashPassword("non-empty")
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	match, err := hasher.VerifyPassword("", validHash)
	if match || !errors.Is(err, argon2id.ErrEmptyPassword) {
		t.Errorf("expected (false, ErrEmptyPassword) on empty password verify, got (%v, %v)", match, err)
	}
}

func TestCorruptedHashes(t *testing.T) {
	hasher := newTestHasher(t, argon2id.FastParams())
	validHash, err := hasher.HashPassword("valid-password")
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	tests := []struct {
		name        string
		corrupted   string
		expectedErr error
	}{
		{name: "empty string", corrupted: "", expectedErr: argon2id.ErrInvalidHash},
		{name: "missing leading dollar", corrupted: "argon2id$v=19$m=8192,t=1,p=1$c2FsdA$aGFzaA", expectedErr: argon2id.ErrInvalidHash},
		{name: "wrong algorithm argon2i", corrupted: "$argon2i$v=19$m=8192,t=1,p=1$c2FsdA$aGFzaA", expectedErr: argon2id.ErrInvalidHash},
		{name: "wrong algorithm bcrypt", corrupted: "$2y$12$e8Y/1O8.K84O3K9hZ2xRCO.Q12345678901234567890123456789", expectedErr: argon2id.ErrInvalidHash},
		{name: "unsupported version v18", corrupted: "$argon2id$v=18$m=8192,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2g", expectedErr: argon2id.ErrIncompatibleVersion},
		{name: "unsupported version v20", corrupted: "$argon2id$v=20$m=8192,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2g", expectedErr: argon2id.ErrIncompatibleVersion},
		{name: "non-numeric version", corrupted: "$argon2id$v=abc$m=8192,t=1,p=1$c2FsdA$aGFzaA", expectedErr: argon2id.ErrInvalidHash},
		{name: "non-numeric memory", corrupted: "$argon2id$v=19$m=abc,t=1,p=1$c2FsdA$aGFzaA", expectedErr: argon2id.ErrInvalidHash},
		{name: "zero memory", corrupted: "$argon2id$v=19$m=0,t=1,p=1$c2FsdA$aGFzaA", expectedErr: argon2id.ErrInvalidHash},
		{name: "zero iterations", corrupted: "$argon2id$v=19$m=8192,t=0,p=1$c2FsdA$aGFzaA", expectedErr: argon2id.ErrInvalidHash},
		{name: "zero parallelism", corrupted: "$argon2id$v=19$m=8192,t=1,p=0$c2FsdA$aGFzaA", expectedErr: argon2id.ErrInvalidHash},
		{name: "corrupted base64 in salt", corrupted: "$argon2id$v=19$m=8192,t=1,p=1$!invalid!$aGFzaA", expectedErr: argon2id.ErrInvalidHash},
		{name: "corrupted base64 in key", corrupted: "$argon2id$v=19$m=8192,t=1,p=1$c2FsdA$!invalid!", expectedErr: argon2id.ErrInvalidHash},
		{name: "empty salt", corrupted: "$argon2id$v=19$m=8192,t=1,p=1$$aGFzaA", expectedErr: argon2id.ErrInvalidHash},
		{name: "empty key", corrupted: "$argon2id$v=19$m=8192,t=1,p=1$c2FsdA$", expectedErr: argon2id.ErrInvalidHash},
		{name: "truncated parts", corrupted: "$argon2id$v=19$m=8192,t=1,p=1", expectedErr: argon2id.ErrInvalidHash},
		{name: "tampered key payload", corrupted: validHash[:len(validHash)-4] + "AAAA", expectedErr: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			match, err := hasher.VerifyPassword("valid-password", tc.corrupted)
			if tc.expectedErr != nil {
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("expected error %v, got: %v", tc.expectedErr, err)
				}
				if match {
					t.Error("match must be false on error")
				}
			} else {
				// Tampered payload should cleanly fail authentication without error
				if match {
					t.Error("tampered key should not match")
				}
				if err != nil {
					t.Errorf("tampered valid-base64 key should not return error, got: %v", err)
				}
			}
		})
	}
}

func TestRehashDetection(t *testing.T) {
	fastParams := argon2id.FastParams()
	defaultParams := argon2id.DefaultParams()

	fastHasher := newTestHasher(t, fastParams)
	defaultHasher := newTestHasher(t, defaultParams)

	password := "SecurityUpgrade2026!"

	fastHash, err := fastHasher.HashPassword(password)
	if err != nil {
		t.Fatalf("fastHasher.HashPassword failed: %v", err)
	}

	// 1. fastHash verified with defaultHasher must succeed
	match, err := defaultHasher.VerifyPassword(password, fastHash)
	if err != nil || !match {
		t.Fatalf("defaultHasher should verify older fastHash, got match=%v err=%v", match, err)
	}

	// 2. defaultHasher should report that fastHash needs rehash
	if !defaultHasher.NeedsRehash(fastHash) {
		t.Error("defaultHasher.NeedsRehash(fastHash) should be true")
	}

	// 3. fastHasher with matching params should report false
	if fastHasher.NeedsRehash(fastHash) {
		t.Error("fastHasher.NeedsRehash(fastHash) should be false")
	}

	// 4. defaultHash with defaultHasher should report false
	defaultHash, err := defaultHasher.HashPassword(password)
	if err != nil {
		t.Fatalf("defaultHasher.HashPassword failed: %v", err)
	}
	if defaultHasher.NeedsRehash(defaultHash) {
		t.Error("defaultHasher.NeedsRehash(defaultHash) should be false")
	}

	// 5. Corrupted hash should report true for NeedsRehash
	if !defaultHasher.NeedsRehash("corrupted_hash") {
		t.Error("NeedsRehash on corrupted hash should be true")
	}
}

func TestDummyHashUniformityAndTiming(t *testing.T) {
	hasher := newTestHasher(t, argon2id.FastParams())
	dummy := hasher.DummyHash()

	if !strings.HasPrefix(dummy, "$argon2id$v=19$") {
		t.Fatalf("dummy hash format invalid: %q", dummy)
	}

	// Candidate password against dummy hash must always return false, nil
	candidates := []string{
		"admin",
		"password",
		"123456",
		"correct-horse-battery-staple",
		"RandomGuess999!#$",
	}

	for _, cand := range candidates {
		match, err := hasher.VerifyPassword(cand, dummy)
		if err != nil {
			t.Errorf("VerifyPassword against dummy hash produced error for %q: %v", cand, err)
		}
		if match {
			t.Errorf("candidate password %q unexpectedly matched dummy hash", cand)
		}
	}

	// Dummy hash should match current params
	if hasher.NeedsRehash(dummy) {
		t.Error("DummyHash should match active params and not need rehash")
	}
}

func TestSaltUniqueness(t *testing.T) {
	hasher := newTestHasher(t, argon2id.FastParams())
	password := "IdenticalPassword"

	h1, err := hasher.HashPassword(password)
	if err != nil {
		t.Fatalf("first hash failed: %v", err)
	}
	h2, err := hasher.HashPassword(password)
	if err != nil {
		t.Fatalf("second hash failed: %v", err)
	}

	if h1 == h2 {
		t.Error("two hashes of identical password must not produce identical output (salts must differ)")
	}

	match1, _ := hasher.VerifyPassword(password, h1)
	match2, _ := hasher.VerifyPassword(password, h2)
	if !match1 || !match2 {
		t.Errorf("both hashes must verify successfully: match1=%v, match2=%v", match1, match2)
	}
}

func TestEntropyFailure(t *testing.T) {
	failRng := failingRandom{}

	// 1. New fails when entropy reader fails to generate dummy salt/secret
	_, err := argon2id.New(argon2id.FastParams(), failRng)
	if !errors.Is(err, argon2id.ErrEntropyFailed) {
		t.Errorf("expected ErrEntropyFailed on New, got: %v", err)
	}

	// 2. HashPassword fails when entropy reader fails
	// Initialize hasher with static rng first, then swap
	hasher, err := argon2id.New(argon2id.FastParams(), staticRandom{byteVal: 0x42})
	if err != nil {
		t.Fatalf("init hasher failed: %v", err)
	}

	// Create hasher with failing random by calling New with failing reader
	_, err = argon2id.New(argon2id.FastParams(), failRng)
	if !errors.Is(err, argon2id.ErrEntropyFailed) {
		t.Errorf("expected ErrEntropyFailed on New, got %v", err)
	}

	_ = hasher
}

func TestParamsValidation(t *testing.T) {
	tests := []struct {
		name    string
		params  argon2id.Params
		wantErr bool
	}{
		{
			name:    "valid default params",
			params:  argon2id.DefaultParams(),
			wantErr: false,
		},
		{
			name:    "valid fast params",
			params:  argon2id.FastParams(),
			wantErr: false,
		},
		{
			name: "parallelism zero",
			params: argon2id.Params{
				Memory: 8192, Iterations: 1, Parallelism: 0, SaltLength: 16, KeyLength: 32,
			},
			wantErr: true,
		},
		{
			name: "iterations zero",
			params: argon2id.Params{
				Memory: 8192, Iterations: 0, Parallelism: 1, SaltLength: 16, KeyLength: 32,
			},
			wantErr: true,
		},
		{
			name: "memory too low for parallelism",
			params: argon2id.Params{
				Memory: 7, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
			},
			wantErr: true,
		},
		{
			name: "salt length too short",
			params: argon2id.Params{
				Memory: 8192, Iterations: 1, Parallelism: 1, SaltLength: 15, KeyLength: 32,
			},
			wantErr: true,
		},
		{
			name: "key length too short",
			params: argon2id.Params{
				Memory: 8192, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 15,
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.params.Validate()
			if tc.wantErr && err == nil {
				t.Error("expected error from Validate(), got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error from Validate(): %v", err)
			}
		})
	}

	// nil random source
	_, err := argon2id.New(argon2id.FastParams(), nil)
	if !errors.Is(err, argon2id.ErrInvalidParams) {
		t.Errorf("expected ErrInvalidParams when random source is nil, got: %v", err)
	}
}

func TestNoSecretsOrHashesInLogger(t *testing.T) {
	hasher := newTestHasher(t, argon2id.FastParams())
	password := "UltraSecretPassword99"
	encodedHash, err := hasher.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	// Verify RedactValue redaction
	redactedPass := logging.RedactValue(fmt.Sprintf("password=%s", password))
	if redactedPass != "[REDACTED]" {
		t.Errorf("password value was not redacted: got %q", redactedPass)
	}

	redactedHash := logging.RedactValue(encodedHash)
	if redactedHash != "[REDACTED]" {
		t.Errorf("Argon2id hash was not redacted: got %q", redactedHash)
	}

	// Verify structured logging records
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))

	logger.Info("user authentication attempted",
		"email", logging.RedactEmail("user@example.com"),
		"password", logging.RedactValue(fmt.Sprintf("password=%s", password)),
		"hash", logging.RedactValue(encodedHash),
	)

	loggedOutput := buf.String()
	if strings.Contains(loggedOutput, password) {
		t.Errorf("log output contains raw password: %s", loggedOutput)
	}
	if strings.Contains(loggedOutput, encodedHash) {
		t.Errorf("log output contains raw hash: %s", loggedOutput)
	}
	if !strings.Contains(loggedOutput, "[REDACTED]") {
		t.Errorf("log output expected to contain [REDACTED]: %s", loggedOutput)
	}
}
