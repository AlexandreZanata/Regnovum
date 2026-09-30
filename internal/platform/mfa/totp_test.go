package mfa_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/mfa"
)

// TestHOTPMatchesRFC4226AppendixD runs the published HOTP vectors. They are the
// reason this implementation can be called the algorithm rather than an
// approximation of it: the counters and the codes are fixed by the RFC, and a
// single changed parameter breaks one of the ten rows.
func TestHOTPMatchesRFC4226AppendixD(t *testing.T) {
	t.Parallel()

	// The RFC's secret is the ASCII string "12345678901234567890".
	secret := []byte("12345678901234567890")
	expected := []string{
		"755224", "287082", "359152", "969429", "338314",
		"254676", "287922", "162583", "399871", "520489",
	}

	for counter, want := range expected {
		got, err := mfa.HOTP(secret, uint64(counter), 6, mfa.AlgorithmSHA1)
		if err != nil {
			t.Fatalf("counter %d: %v", counter, err)
		}
		if got != want {
			t.Errorf("counter %d: code = %q, want %q", counter, got, want)
		}
	}
}

// TestTOTPMatchesRFC6238AppendixB runs the published TOTP vectors for the three
// algorithms the RFC defines, at every instant the appendix tabulates.
func TestTOTPMatchesRFC6238AppendixB(t *testing.T) {
	t.Parallel()

	// Each row is one instant of the RFC's table, with the code each algorithm
	// must produce there (eight digits, thirty-second steps).
	vectors := []struct {
		instant int64
		sha1    string
		sha256  string
		sha512  string
	}{
		{59, "94287082", "46119246", "90693936"},
		{1111111109, "07081804", "68084774", "25091201"},
		{1111111111, "14050471", "67062674", "99943326"},
		{1234567890, "89005924", "91819424", "93441116"},
		{2000000000, "69279037", "90698825", "38618901"},
		{20000000000, "65353130", "77737706", "47863826"},
	}

	secrets := map[mfa.Algorithm][]byte{
		mfa.AlgorithmSHA1:   []byte("12345678901234567890"),
		mfa.AlgorithmSHA256: []byte("12345678901234567890123456789012"),
		mfa.AlgorithmSHA512: []byte("1234567890123456789012345678901234567890123456789012345678901234"),
	}

	for _, vector := range vectors {
		instant := time.Unix(vector.instant, 0).UTC()
		expected := map[mfa.Algorithm]string{
			mfa.AlgorithmSHA1:   vector.sha1,
			mfa.AlgorithmSHA256: vector.sha256,
			mfa.AlgorithmSHA512: vector.sha512,
		}
		for algorithm, want := range expected {
			config := mfa.Config{Digits: 8, Period: 30 * time.Second, Algorithm: algorithm}
			got, err := config.Code(secrets[algorithm], instant)
			if err != nil {
				t.Fatalf("%s at %d: %v", algorithm, vector.instant, err)
			}
			if got != want {
				t.Errorf("%s at %d: code = %q, want %q", algorithm, vector.instant, got, want)
			}
		}
	}
}

// TestStepIsUTCOrderedAndIndependentOfTheLocalZone asserts the RFC's counter is
// computed in UTC: the same instant in two representations is the same step,
// which is what keeps a code valid across a DST transition.
func TestStepIsUTCOrderedAndIndependentOfTheLocalZone(t *testing.T) {
	t.Parallel()

	config := mfa.Config{}
	instant := time.Unix(1_700_000_000, 0)

	utc, err := config.Step(instant.UTC())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	// The same instant expressed in another zone is the same instant.
	elsewhere, err := config.Step(instant.In(time.FixedZone("UTC+13", 13*60*60)))
	if err != nil {
		t.Fatalf("step in another zone: %v", err)
	}
	if utc != elsewhere {
		t.Errorf("step depends on the representation: %d != %d", utc, elsewhere)
	}
	if want := instant.Unix() / 30; utc != want {
		t.Errorf("step = %d, want %d", utc, want)
	}
}

// TestVerifyAcceptsTheSkewWindowAndRefusesBeyondIt is the clock skew item: one
// step each way by default, and a step outside the window is refused even
// though it is mathematically correct.
func TestVerifyAcceptsTheSkewWindowAndRefusesBeyondIt(t *testing.T) {
	t.Parallel()

	secret := []byte("12345678901234567890")
	config := mfa.Config{}
	now := time.Unix(1_700_000_000, 0).UTC()

	codeAt := func(offsetSteps int64) string {
		code, err := config.Code(secret, now.Add(time.Duration(offsetSteps)*30*time.Second))
		if err != nil {
			t.Fatalf("generate code: %v", err)
		}
		return code
	}

	for _, offset := range []int64{-1, 0, 1} {
		step, err := config.Verify(secret, codeAt(offset), now, -1)
		if err != nil {
			t.Errorf("offset %d: the code was refused: %v", offset, err)
			continue
		}
		if wantStep := uint64(now.Unix()/30 + offset); step != int64(wantStep) {
			t.Errorf("offset %d: accepted step = %d, want %d", offset, step, wantStep)
		}
	}

	for _, offset := range []int64{-2, 2, -10, 10} {
		if _, err := config.Verify(secret, codeAt(offset), now, -1); !errors.Is(err, mfa.ErrInvalidCode) {
			t.Errorf("offset %d: err = %v, want the code to be refused as invalid", offset, err)
		}
	}
}

// TestVerifyRefusesAReplayedStep is the timestep replay item: a code that was
// already accepted cannot be accepted again, even inside the window, and even
// when it is presented as a different request.
func TestVerifyRefusesAReplayedStep(t *testing.T) {
	t.Parallel()

	secret := []byte("12345678901234567890")
	config := mfa.Config{}
	now := time.Unix(1_700_000_000, 0).UTC()

	code, err := config.Code(secret, now)
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}

	step, err := config.Verify(secret, code, now, -1)
	if err != nil {
		t.Fatalf("the first use was refused: %v", err)
	}

	// The same code, at the same instant, with the step persisted: a replay.
	if _, err := config.Verify(secret, code, now, step); !errors.Is(err, mfa.ErrReplayedStep) {
		t.Errorf("the second use: err = %v, want ErrReplayedStep", err)
	}

	// A code from an earlier step is a replay too once a later step was spent:
	// replay is about the step, not about the string.
	earlier, err := config.Code(secret, now.Add(-30*time.Second))
	if err != nil {
		t.Fatalf("generate earlier code: %v", err)
	}
	if _, err := config.Verify(secret, earlier, now, step); !errors.Is(err, mfa.ErrReplayedStep) {
		t.Errorf("an earlier step after a later one: err = %v, want ErrReplayedStep", err)
	}

	// The next step is a new code and is accepted.
	next, err := config.Code(secret, now.Add(30*time.Second))
	if err != nil {
		t.Fatalf("generate next code: %v", err)
	}
	if _, err := config.Verify(secret, next, now, step); err != nil {
		t.Errorf("the next step was refused: %v", err)
	}
}

// TestVerifyRefusesMalformedCodes keeps the fail-closed path honest: a code
// that is not a code is refused before any comparison, with the same error a
// wrong code gets, so the endpoint cannot be probed for the code's shape.
func TestVerifyRefusesMalformedCodes(t *testing.T) {
	t.Parallel()

	secret := []byte("12345678901234567890")
	config := mfa.Config{}
	now := time.Unix(1_700_000_000, 0).UTC()

	for _, code := range []string{"", "12345", "1234567", "12345a", "abcdef", "  ", "123456789"} {
		if _, err := config.Verify(secret, code, now, -1); !errors.Is(err, mfa.ErrInvalidCode) {
			t.Errorf("code %q: err = %v, want ErrInvalidCode", code, err)
		}
	}

	// Formatting is tolerated: a person typing a code from an authenticator
	// may copy it with a space or a dash in it.
	valid, err := config.Code(secret, now)
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	spaced := valid[:3] + " " + valid[3:]
	if _, err := config.Verify(secret, spaced, now, -1); err != nil {
		t.Errorf("a spaced code was refused: %v", err)
	}

	if _, err := config.Verify(nil, valid, now, -1); !errors.Is(err, mfa.ErrInvalidSecret) {
		t.Error("an empty secret was accepted")
	}
}

// TestConfigValidationRefusesParametersTheRFCWouldNotDefine covers the
// parameters that would silently change the meaning of a code.
func TestConfigValidationRefusesParametersTheRFCWouldNotDefine(t *testing.T) {
	t.Parallel()

	cases := map[string]mfa.Config{
		"digits below the minimum":  {Digits: 5},
		"digits above the maximum":  {Digits: 9},
		"period below a second":     {Period: 500 * time.Millisecond},
		"period above five minutes": {Period: 10 * time.Minute},
		"period not whole seconds":  {Period: 1500 * time.Millisecond},
		"negative skew":             {Skew: -1},
		"unknown algorithm":         {Algorithm: mfa.Algorithm("MD5")},
		"secret too small":          {SecretBytes: 4},
		"secret too large":          {SecretBytes: 128},
		"no backup codes":           {BackupCodes: -1},
		"too many backup codes":     {BackupCodes: 100},
	}

	for name, config := range cases {
		if _, err := config.GenerateSecret(clockseed.NewRandom()); !errors.Is(err, mfa.ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
		}
		if _, err := config.Code([]byte("12345678901234567890"), time.Unix(0, 0)); !errors.Is(err, mfa.ErrInvalidConfig) {
			t.Errorf("%s: Code err = %v, want ErrInvalidConfig", name, err)
		}
	}

	// The zero configuration is the interoperable default and must work.
	if _, err := (mfa.Config{}).GenerateSecret(clockseed.NewRandom()); err != nil {
		t.Errorf("the default configuration was refused: %v", err)
	}

	// Without an entropy source there is no secret to draw, and the refusal is
	// explicit rather than a zero-filled secret: the source is a port because
	// the effect belongs to the composition (P02-T02).
	if _, err := (mfa.Config{}).GenerateSecret(nil); !errors.Is(err, mfa.ErrInvalidConfig) {
		t.Errorf("a missing entropy source: err = %v, want ErrInvalidConfig", err)
	}
	if _, err := (mfa.Config{BackupCodes: 3}).GenerateBackupCodes(nil); !errors.Is(err, mfa.ErrInvalidConfig) {
		t.Errorf("backup codes without an entropy source: err = %v, want ErrInvalidConfig", err)
	}
}

// TestHOTPRejectsImpossibleParameters states the two inputs HOTP cannot answer
// for, so a caller never gets a code computed from nothing.
func TestHOTPRejectsImpossibleParameters(t *testing.T) {
	t.Parallel()

	if _, err := mfa.HOTP(nil, 0, 6, mfa.AlgorithmSHA1); !errors.Is(err, mfa.ErrInvalidSecret) {
		t.Error("an empty secret produced a code")
	}
	if _, err := mfa.HOTP([]byte("secret"), 0, 4, mfa.AlgorithmSHA1); !errors.Is(err, mfa.ErrInvalidConfig) {
		t.Error("four digits produced a code")
	}
	if _, err := mfa.HOTP([]byte("secret"), 0, 6, mfa.Algorithm("none")); !errors.Is(err, mfa.ErrInvalidConfig) {
		t.Error("an unknown algorithm produced a code")
	}
}

// TestSecretRoundTripAndURI covers the enrollment material: a generated secret
// is the requested length, survives the encoding an authenticator reads, and
// the URI carries the parameters the server verifies with.
func TestSecretRoundTripAndURI(t *testing.T) {
	t.Parallel()

	config := mfa.Config{}
	secret, err := config.GenerateSecret(clockseed.NewRandom())
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}
	if len(secret) != mfa.DefaultSecretBytes {
		t.Errorf("secret length = %d, want %d", len(secret), mfa.DefaultSecretBytes)
	}

	encoded := mfa.EncodeSecret(secret)
	if strings.ContainsAny(encoded, "=") {
		t.Errorf("encoded secret carries padding: %q", encoded)
	}
	decoded, err := mfa.DecodeSecret(encoded)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	if string(decoded) != string(secret) {
		t.Error("the decoded secret is not the generated one")
	}

	// A pasted secret arrives lower case, spaced or padded.
	decoded, err = mfa.DecodeSecret(strings.ToLower(" " + encoded[:4] + " " + encoded[4:] + "== "))
	if err != nil {
		t.Fatalf("decode a pasted secret: %v", err)
	}
	if string(decoded) != string(secret) {
		t.Error("a pasted secret was not decoded to the generated one")
	}

	for _, invalid := range []string{"", "   ", "0189!!", "0"} {
		if _, err := mfa.DecodeSecret(invalid); !errors.Is(err, mfa.ErrInvalidSecret) {
			t.Errorf("DecodeSecret(%q): err = %v, want ErrInvalidSecret", invalid, err)
		}
	}

	uri, err := config.URI("Regnovum", "admin@arena.example", secret)
	if err != nil {
		t.Fatalf("uri: %v", err)
	}
	for _, fragment := range []string{
		"otpauth://totp/Regnovum:admin%40arena.example",
		"secret=" + encoded,
		"issuer=Regnovum",
		"algorithm=SHA1",
		"digits=6",
		"period=30",
	} {
		if !strings.Contains(uri, fragment) {
			t.Errorf("uri %q is missing %q", uri, fragment)
		}
	}

	if _, err := config.URI("", "admin@arena.example", secret); !errors.Is(err, mfa.ErrInvalidConfig) {
		t.Error("a uri without an issuer was produced")
	}
}

// TestBackupCodesAreSingleUseMaterial covers the one-time codes: the requested
// count, the shape the server accepts back, distinct values, and the
// normalisation that makes a code written on paper usable.
func TestBackupCodesAreSingleUseMaterial(t *testing.T) {
	t.Parallel()

	config := mfa.Config{BackupCodes: 10}
	codes, err := config.GenerateBackupCodes(clockseed.NewRandom())
	if err != nil {
		t.Fatalf("generate backup codes: %v", err)
	}
	if len(codes) != 10 {
		t.Fatalf("generated %d codes, want 10", len(codes))
	}

	seen := map[string]bool{}
	for _, code := range codes {
		normalized := mfa.NormalizeBackupCode(code)
		if !mfa.ValidBackupCodeShape(normalized) {
			t.Errorf("code %q normalises to %q, which is not a valid shape", code, normalized)
		}
		if seen[normalized] {
			t.Errorf("code %q was generated twice", code)
		}
		seen[normalized] = true

		// The dashed, lower-case form a user retypes is the same code.
		if again := mfa.NormalizeBackupCode(strings.ToLower(" " + code + " ")); again != normalized {
			t.Errorf("normalising %q twice gave %q and %q", code, normalized, again)
		}
	}

	for _, invalid := range []string{"", "AAAA", "!!!!!!!!!!!!", "01234567890"} {
		if mfa.ValidBackupCodeShape(mfa.NormalizeBackupCode(invalid)) {
			t.Errorf("%q was accepted as a backup code shape", invalid)
		}
	}

	// Two enrollments must not produce the same set: a "random" generator that
	// always answers the same is the failure this catches.
	other, err := config.GenerateBackupCodes(clockseed.NewRandom())
	if err != nil {
		t.Fatalf("generate backup codes again: %v", err)
	}
	same := 0
	for index := range codes {
		if mfa.NormalizeBackupCode(codes[index]) == mfa.NormalizeBackupCode(other[index]) {
			same++
		}
	}
	if same == len(codes) {
		t.Error("two enrollments produced identical codes")
	}
}
