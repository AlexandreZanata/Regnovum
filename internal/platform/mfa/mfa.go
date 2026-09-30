// Package mfa is the second factor of the administrative surface (P16-T05):
// TOTP over the RFC 4226/6238 primitives, the secret that a challenge is
// checked against, the sealing of that secret at rest and the one-time backup
// codes that recover an operator whose authenticator is gone.
//
// The implementation is native on purpose, and the reasoning is registered in
// docs/adr/ADR-014-totp-native-rfc6238.md: the standard library covers every
// primitive (HMAC, AES-GCM, constant-time comparison, crypto/rand), the RFC
// publishes the vectors that prove correctness, and the parts a generic
// library does not decide — how much clock skew is tolerated, that a timestep
// is spent when it is accepted, that the secret is sealed under the account it
// belongs to — are precisely the parts this repository treats as explicit
// policy.
//
// The package holds no storage, no transport and no policy about who may
// enroll: it answers three questions (is this code valid *now*, is this secret
// intact, is this backup code the one it claims to be), and the identity
// module decides who asks and what happens afterwards.
package mfa

import (
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// Errors of the mechanism. They are sentinels because the caller (a use case)
// turns them into the product's stable problem codes; the mechanism itself has
// no vocabulary for HTTP.
var (
	// ErrInvalidCode means the presented code does not match any accepted step.
	ErrInvalidCode = errors.New("mfa: the code is not valid")
	// ErrReplayedStep means the code is valid for a step that was already
	// spent. It is kept apart from ErrInvalidCode because the two are
	// different facts: one is a wrong guess, the other is a replay of a
	// correct one, and an operator reading an incident needs to tell them
	// apart.
	ErrReplayedStep = errors.New("mfa: this time step was already used")
	// ErrInvalidSecret means the secret is not a decodable shared secret.
	ErrInvalidSecret = errors.New("mfa: the shared secret is not valid")
	// ErrInvalidConfig means the requested parameters are outside what the
	// RFC and this product accept.
	ErrInvalidConfig = errors.New("mfa: invalid configuration")
	// ErrInvalidBackupCode means a backup code does not belong to the account,
	// was already used, or does not exist.
	ErrInvalidBackupCode = errors.New("mfa: the backup code is not valid")
	// ErrSealedData means sealed bytes could not be opened with this key and
	// this context, which is what a wrong key, a truncated value or a secret
	// moved between accounts looks like.
	ErrSealedData = errors.New("mfa: the sealed value could not be opened")
)

// Algorithm is the hash of the challenge. SHA1 is the RFC 6238 default and
// what every authenticator application supports; the others are accepted
// because the RFC defines them and the vectors cover them.
type Algorithm string

const (
	// AlgorithmSHA1 is the interoperable default.
	AlgorithmSHA1 Algorithm = "SHA1"
	// AlgorithmSHA256 is the stronger alternative the RFC defines.
	AlgorithmSHA256 Algorithm = "SHA256"
	// AlgorithmSHA512 is the strongest the RFC defines.
	AlgorithmSHA512 Algorithm = "SHA512"
)

// The parameters of the mechanism. The defaults are the RFC's: six digits, a
// thirty-second step and SHA1, which is what an authenticator shows and what
// the user's other accounts use.
const (
	// DefaultDigits is the length of a code.
	DefaultDigits = 6
	// DefaultPeriod is the length of one time step.
	DefaultPeriod = 30 * time.Second
	// DefaultSkew is how many steps before and after "now" are accepted. One
	// step is what a clock that drifts by a few seconds needs; a wider window
	// is a wider guess surface, so widening it is a decision, not a default.
	DefaultSkew = 1
	// DefaultSecretBytes is the length of a generated secret, in bytes. 160
	// bits is the RFC 4226 recommendation for HMAC-SHA1 and remains the
	// interoperable choice; the longer algorithms use the same secret material
	// expanded by HMAC itself.
	DefaultSecretBytes = 20
	// DefaultBackupCodes is how many one-time codes an enrollment issues.
	DefaultBackupCodes = 10
	// MaxDigits and MinDigits bound the code length to what the RFC defines.
	MinDigits = 6
	MaxDigits = 8
)

// Config is the parameter set of one verification. A zero Config is the
// interoperable default, which is what a caller that has no opinion should
// get.
type Config struct {
	// Digits is the code length (6 or 8). Zero means DefaultDigits.
	Digits int
	// Period is the length of a step. Zero means DefaultPeriod.
	Period time.Duration
	// Algorithm is the HMAC hash. Empty means AlgorithmSHA1.
	Algorithm Algorithm
	// Skew is how many steps before and after "now" are accepted. Zero means
	// DefaultSkew (one step each way, which is what a clock drifting by a few
	// seconds needs); a negative value is rejected, because refusing a client
	// whose clock is a few seconds off is not a security feature, and widening
	// the window is a decision that gets written down rather than inferred.
	Skew int
	// SecretBytes is the length of a generated secret. Zero means
	// DefaultSecretBytes.
	SecretBytes int
	// BackupCodes is how many one-time codes an enrollment issues. Zero means
	// DefaultBackupCodes.
	BackupCodes int
}

// normalized fills the defaults and validates the parameters, so every method
// below can work with a complete configuration.
func (config Config) normalized() (Config, error) {
	if config.Digits == 0 {
		config.Digits = DefaultDigits
	}
	if config.Period == 0 {
		config.Period = DefaultPeriod
	}
	if config.Algorithm == "" {
		config.Algorithm = AlgorithmSHA1
	}
	if config.Skew == 0 {
		config.Skew = DefaultSkew
	}
	if config.SecretBytes == 0 {
		config.SecretBytes = DefaultSecretBytes
	}
	if config.BackupCodes == 0 {
		config.BackupCodes = DefaultBackupCodes
	}

	switch {
	case config.Digits < MinDigits || config.Digits > MaxDigits:
		return Config{}, fmt.Errorf("%w: digits must be between %d and %d", ErrInvalidConfig, MinDigits, MaxDigits)
	case config.Period < time.Second || config.Period > 5*time.Minute:
		return Config{}, fmt.Errorf("%w: period must be between one second and five minutes", ErrInvalidConfig)
	case config.Period%time.Second != 0:
		// A step that is not a whole number of seconds cannot be compared with
		// the step the authenticator computed, because the RFC's counter is a
		// whole number of periods.
		return Config{}, fmt.Errorf("%w: period must be a whole number of seconds", ErrInvalidConfig)
	case config.Skew < 0:
		return Config{}, fmt.Errorf("%w: skew cannot be negative (zero means the default tolerance of %d step(s))", ErrInvalidConfig, DefaultSkew)
	case config.SecretBytes < 10 || config.SecretBytes > 64:
		return Config{}, fmt.Errorf("%w: secret bytes must be between 10 and 64", ErrInvalidConfig)
	case config.BackupCodes < 1 || config.BackupCodes > 50:
		return Config{}, fmt.Errorf("%w: backup codes must be between 1 and 50", ErrInvalidConfig)
	}

	switch config.Algorithm {
	case AlgorithmSHA1, AlgorithmSHA256, AlgorithmSHA512:
	default:
		return Config{}, fmt.Errorf("%w: unknown algorithm %q", ErrInvalidConfig, config.Algorithm)
	}

	return config, nil
}

// secretEncoding is the RFC 3548 base32 alphabet, without padding, which is
// what `otpauth` URIs carry and what authenticator applications read.
var secretEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret creates a new shared secret from the injected entropy source.
// The length is the configured one.
//
// The source is a port and not crypto/rand because of the P02-T02 gate: the
// randomness effect stays at the edge, the deterministic reader of a test can
// produce the same secret twice, and the production wiring is
// clockseed.CryptoRandom — the same arrangement every other package here uses.
func (config Config) GenerateSecret(entropy ports.Random) ([]byte, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	if entropy == nil {
		return nil, fmt.Errorf("%w: an entropy source is required", ErrInvalidConfig)
	}
	secret := make([]byte, normalized.SecretBytes)
	if err := fill(entropy, secret); err != nil {
		return nil, err
	}
	return secret, nil
}

// fill reads exactly len(buffer) bytes from the entropy source. A short read is
// an error rather than a partially random buffer: the difference between a
// secret and a secret with predictable bytes is the whole point of drawing one.
func fill(entropy ports.Random, buffer []byte) error {
	read, err := entropy.Read(buffer)
	if err != nil {
		return fmt.Errorf("mfa: read entropy: %w", err)
	}
	if read != len(buffer) {
		return fmt.Errorf("%w: the entropy source returned %d of %d bytes", ErrInvalidConfig, read, len(buffer))
	}
	return nil
}

// EncodeSecret renders a secret the way an `otpauth` URI carries it: base32,
// upper case, no padding.
func EncodeSecret(secret []byte) string {
	return secretEncoding.EncodeToString(secret)
}

// DecodeSecret reads a secret produced by EncodeSecret, tolerating the lower
// case, the spaces and the padding that a person typing a secret by hand
// produces.
func DecodeSecret(encoded string) ([]byte, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(encoded), " ", ""))
	normalized = strings.TrimRight(normalized, "=")
	if normalized == "" {
		return nil, ErrInvalidSecret
	}
	secret, err := secretEncoding.DecodeString(normalized)
	if err != nil || len(secret) == 0 {
		return nil, ErrInvalidSecret
	}
	return secret, nil
}

// URI renders the `otpauth` URI of an enrollment: the string an authenticator
// application encodes in a QR code, or accepts typed by hand.
//
// The label is issuer-qualified because that is what the application shows, and
// the parameters are explicit so that a client cannot silently assume different
// ones than the server verifies with.
func (config Config) URI(issuer, account string, secret []byte) (string, error) {
	normalized, err := config.normalized()
	if err != nil {
		return "", err
	}
	issuer = strings.TrimSpace(issuer)
	account = strings.TrimSpace(account)
	if issuer == "" || account == "" {
		return "", fmt.Errorf("%w: issuer and account are required", ErrInvalidConfig)
	}

	// The label is `issuer:account`, both percent-encoded, which is what
	// RFC 6901-style consumers of the de-facto `otpauth` scheme expect.
	label := escapeURIComponent(issuer) + ":" + escapeURIComponent(account)
	parameters := fmt.Sprintf(
		"secret=%s&issuer=%s&algorithm=%s&digits=%d&period=%d",
		EncodeSecret(secret), escapeURIComponent(issuer), normalized.Algorithm, normalized.Digits,
		int(normalized.Period.Seconds()),
	)
	return "otpauth://totp/" + label + "?" + parameters, nil
}

// escapeURIComponent percent-encodes one URI component.
func escapeURIComponent(value string) string {
	var builder strings.Builder
	for _, character := range []byte(value) {
		if isUnreserved(character) {
			builder.WriteByte(character)
			continue
		}
		fmt.Fprintf(&builder, "%%%02X", character)
	}
	return builder.String()
}

// isUnreserved reports whether a byte may appear literally in a URI component.
func isUnreserved(character byte) bool {
	switch {
	case character >= 'a' && character <= 'z':
		return true
	case character >= 'A' && character <= 'Z':
		return true
	case character >= '0' && character <= '9':
		return true
	case character == '-' || character == '.' || character == '_' || character == '~':
		return true
	}
	return false
}

// backupCodeAlphabet is the set a one-time code is drawn from: Crockford-style
// base32 without the characters that a person reading from paper confuses
// (I, L, O, U with the digits and other letters). Thirty-two characters means
// five bits each, so a twelve-character code carries sixty bits of entropy.
const backupCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// backupCodeLength is the length of one code, in characters, excluding the
// separators that make it readable.
const backupCodeLength = 12

// GenerateBackupCodes creates one-time recovery codes. They are rendered in
// groups of four separated by dashes, and NormalizeBackupCode accepts them
// back with or without the separators, in either case.
func (config Config) GenerateBackupCodes(entropy ports.Random) ([]string, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	if entropy == nil {
		return nil, fmt.Errorf("%w: an entropy source is required", ErrInvalidConfig)
	}

	codes := make([]string, 0, normalized.BackupCodes)
	for index := 0; index < normalized.BackupCodes; index++ {
		code, err := randomBackupCode(entropy)
		if err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, nil
}

// randomBackupCode draws one code from the entropy source, rejecting the
// values that would bias the alphabet (a modulo over a byte's range would make
// the first characters more likely, which is how a "random" code becomes
// guessable).
func randomBackupCode(entropy ports.Random) (string, error) {
	limit := 256 - 256%len(backupCodeAlphabet)
	if limit <= 0 {
		return "", fmt.Errorf("%w: backup code alphabet is unusable", ErrInvalidConfig)
	}
	code := make([]byte, 0, backupCodeLength)

	for len(code) < backupCodeLength {
		buffer := make([]byte, backupCodeLength)
		if err := fill(entropy, buffer); err != nil {
			return "", err
		}
		for _, value := range buffer {
			if int(value) >= limit {
				continue
			}
			code = append(code, backupCodeAlphabet[int(value)%len(backupCodeAlphabet)])
			if len(code) == backupCodeLength {
				break
			}
		}
	}

	grouped := make([]string, 0, 3)
	for start := 0; start < backupCodeLength; start += 4 {
		grouped = append(grouped, string(code[start:start+4]))
	}
	return strings.Join(grouped, "-"), nil
}

// NormalizeBackupCode canonicalises a code for comparison: upper case, no
// separators. A code is shown with dashes for legibility; the stored form and
// the verified form never carry them.
func NormalizeBackupCode(code string) string {
	var builder strings.Builder
	for _, character := range strings.ToUpper(strings.TrimSpace(code)) {
		switch {
		case character == '-' || character == ' ':
			continue
		case strings.ContainsRune(backupCodeAlphabet, character):
			builder.WriteRune(character)
		default:
			// A character outside the alphabet cannot be part of a code, and
			// keeping it would only make the comparison fail later.
		}
	}
	return builder.String()
}

// ValidBackupCodeShape reports whether a normalized code has the shape of a
// generated one. It says nothing about whether it belongs to anybody.
func ValidBackupCodeShape(code string) bool {
	if len(code) != backupCodeLength {
		return false
	}
	for _, character := range code {
		if !strings.ContainsRune(backupCodeAlphabet, character) {
			return false
		}
	}
	return true
}

// equalInConstantTime compares two strings without leaking where they differ.
func equalInConstantTime(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
