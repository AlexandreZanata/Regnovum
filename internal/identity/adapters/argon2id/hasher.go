package argon2id

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
	"golang.org/x/crypto/argon2"
)

// Hasher implements application.PasswordHasher using the Argon2id algorithm.
type Hasher struct {
	params    Params
	random    ports.Random
	dummyHash string
}

// Compile-time assertion that Hasher satisfies application.PasswordHasher.
var _ application.PasswordHasher = (*Hasher)(nil)

// New initializes an Argon2id Hasher with the specified parameters and randomness source.
// It pre-generates a valid dummy hash for uniform execution timing when verifying non-existent accounts.
func New(params Params, random ports.Random) (*Hasher, error) {
	if err := params.Validate(); err != nil {
		return nil, err
	}
	if random == nil {
		return nil, fmt.Errorf("%w: random entropy source is required", ErrInvalidParams)
	}

	h := &Hasher{
		params: params,
		random: random,
	}

	dummySecret := make([]byte, 32)
	if _, err := random.Read(dummySecret); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEntropyFailed, err)
	}

	dummySalt := make([]byte, params.SaltLength)
	if _, err := random.Read(dummySalt); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEntropyFailed, err)
	}

	dummyKey := argon2.IDKey(dummySecret, dummySalt, params.Iterations, params.Memory, params.Parallelism, params.KeyLength)
	h.dummyHash = formatHash(CurrentVersion, params, dummySalt, dummyKey)

	return h, nil
}

// NewDefault initializes an Argon2id Hasher with OWASP-recommended production parameters.
func NewDefault(random ports.Random) (*Hasher, error) {
	return New(DefaultParams(), random)
}

// Params returns the active cost and sizing configuration of this hasher.
func (h *Hasher) Params() Params {
	return h.params
}

// DummyHash returns the pre-computed valid Argon2id hash for non-existent accounts.
func (h *Hasher) DummyHash() string {
	return h.dummyHash
}

// HashPassword derives a secure Argon2id hash for the given plain-text password.
func (h *Hasher) HashPassword(password string) (string, error) {
	if password == "" {
		return "", ErrEmptyPassword
	}

	salt := make([]byte, h.params.SaltLength)
	if _, err := h.random.Read(salt); err != nil {
		return "", fmt.Errorf("%w: %v", ErrEntropyFailed, err)
	}

	derivedKey := argon2.IDKey(
		[]byte(password),
		salt,
		h.params.Iterations,
		h.params.Memory,
		h.params.Parallelism,
		h.params.KeyLength,
	)

	return formatHash(CurrentVersion, h.params, salt, derivedKey), nil
}

// VerifyPassword verifies a plain-text password against a PHC-encoded Argon2id hash.
// Comparison of derived and expected keys is performed in constant-time.
func (h *Hasher) VerifyPassword(password, encodedHash string) (bool, error) {
	if password == "" {
		return false, ErrEmptyPassword
	}

	parsed, err := decodeHash(encodedHash)
	if err != nil {
		return false, err
	}

	derivedKey := argon2.IDKey(
		[]byte(password),
		parsed.salt,
		parsed.iterations,
		parsed.memory,
		parsed.parallelism,
		uint32(len(parsed.key)),
	)

	if subtle.ConstantTimeCompare(parsed.key, derivedKey) == 1 {
		return true, nil
	}
	return false, nil
}

// NeedsRehash determines whether an encoded hash was computed with deprecated
// parameters or an older algorithm version compared to the current hasher configuration.
func (h *Hasher) NeedsRehash(encodedHash string) bool {
	parsed, err := decodeHash(encodedHash)
	if err != nil {
		return true
	}
	if parsed.version != CurrentVersion {
		return true
	}
	if parsed.memory != h.params.Memory ||
		parsed.iterations != h.params.Iterations ||
		parsed.parallelism != h.params.Parallelism ||
		uint32(len(parsed.salt)) != h.params.SaltLength ||
		uint32(len(parsed.key)) != h.params.KeyLength {
		return true
	}
	return false
}

type parsedHash struct {
	version     int
	memory      uint32
	iterations  uint32
	parallelism uint8
	salt        []byte
	key         []byte
}

func formatHash(version int, p Params, salt, hash []byte) string {
	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)
	return fmt.Sprintf("$%s$v=%d$m=%d,t=%d,p=%d$%s$%s",
		AlgorithmName, version, p.Memory, p.Iterations, p.Parallelism, b64Salt, b64Hash)
}

func decodeHash(encoded string) (*parsedHash, error) {
	parts := strings.Split(encoded, "$")
	// Format: $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
	if len(parts) != 6 {
		return nil, ErrInvalidHash
	}
	if parts[0] != "" || parts[1] != AlgorithmName {
		return nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return nil, ErrInvalidHash
	}
	if version != CurrentVersion {
		return nil, ErrIncompatibleVersion
	}

	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return nil, ErrInvalidHash
	}
	if memory == 0 || iterations == 0 || parallelism == 0 {
		return nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return nil, ErrInvalidHash
	}

	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return nil, ErrInvalidHash
	}

	return &parsedHash{
		version:     version,
		memory:      memory,
		iterations:  iterations,
		parallelism: parallelism,
		salt:        salt,
		key:         key,
	}, nil
}
