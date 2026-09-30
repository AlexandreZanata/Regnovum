package mfa

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// Sealer protects the shared secret at rest.
//
// A TOTP secret cannot be hashed: verifying a code requires the secret itself.
// It is therefore sealed with AES-256-GCM, which gives confidentiality and
// integrity, and the additional authenticated data is the account the secret
// belongs to — so a sealed secret lifted from one row and pasted into another
// does not open. That property is what makes the embedding of the context in
// the ciphertext structural rather than a rule someone has to remember.
type Sealer struct {
	aead    cipher.AEAD
	entropy ports.Random
}

// KeySize is the length of the encryption key, in bytes (AES-256).
const KeySize = 32

// NewSealer builds a sealer from a raw key and the entropy source its nonces
// come from. The source is injected for the same reason the key is: the
// randomness effect belongs to the composition, and a test that seals twice
// with a deterministic reader can assert what a repeated nonce would mean.
func NewSealer(key []byte, entropy ports.Random) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w: the encryption key must be %d bytes (got %d)", ErrInvalidConfig, KeySize, len(key))
	}
	if entropy == nil {
		return nil, fmt.Errorf("%w: an entropy source is required for the nonces", ErrInvalidConfig)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return &Sealer{aead: aead, entropy: entropy}, nil
}

// DecodeKey reads a key from its configured representation: standard base64
// with optional padding, which is what an environment variable can carry
// without becoming unreadable in a diff.
//
// Reading the environment is the configuration package's job (P02-T01 gate);
// this function only accepts the value it is handed.
func DecodeKey(encoded string) ([]byte, error) {
	trimmed := trimSpace(encoded)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: the encryption key is empty", ErrInvalidConfig)
	}
	key, err := base64.StdEncoding.WithPadding(base64.NoPadding).DecodeString(trimmed)
	if err != nil {
		// A padded value is accepted too: a key pasted from a tool commonly
		// carries the padding.
		padded, paddedErr := base64.StdEncoding.DecodeString(trimmed)
		if paddedErr != nil {
			return nil, fmt.Errorf("%w: the encryption key is not base64", ErrInvalidConfig)
		}
		key = padded
	}
	return key, nil
}

// Seal encrypts the secret of one account. The returned value is the nonce
// followed by the ciphertext, which is what the storage layer writes to a
// single column.
//
// A random nonce per sealing is required by AES-GCM's security argument: reusing
// a nonce under the same key would break the authentication. The nonce is
// therefore drawn from the entropy source the sealer was built with — production
// wires clockseed.CryptoRandom, a test wires a deterministic reader — and never
// derived from the plaintext or the account, nor read from a source this package
// reaches for on its own.
func (sealer *Sealer) Seal(plaintext []byte, context []byte) ([]byte, error) {
	if sealer == nil || sealer.aead == nil {
		return nil, fmt.Errorf("%w: sealer is not configured", ErrInvalidConfig)
	}
	if len(plaintext) == 0 {
		return nil, ErrInvalidSecret
	}

	nonce := make([]byte, sealer.aead.NonceSize())
	if err := fill(sealer.entropy, nonce); err != nil {
		return nil, err
	}
	// The context is passed as additional authenticated data: it is bound to
	// the ciphertext but not stored in it, so a reviewer can see which account
	// a secret belongs to without decrypting anything.
	return sealer.aead.Seal(nonce, nonce, plaintext, context), nil
}

// Open reverses Seal. A wrong key, a truncated value, a tampered byte and a
// context that does not match all produce ErrSealedData, because the caller's
// only correct reaction to every one of them is the same: refuse and ask a
// human to look.
func (sealer *Sealer) Open(sealed []byte, context []byte) ([]byte, error) {
	if sealer == nil || sealer.aead == nil {
		return nil, fmt.Errorf("%w: sealer is not configured", ErrInvalidConfig)
	}
	nonceSize := sealer.aead.NonceSize()
	if len(sealed) <= nonceSize {
		return nil, ErrSealedData
	}

	plaintext, err := sealer.aead.Open(nil, sealed[:nonceSize], sealed[nonceSize:], context)
	if err != nil {
		// The underlying error distinguishes "message authentication failed"
		// from a malformed input, and that distinction is exactly what an
		// attacker probing the endpoint would like to have.
		return nil, ErrSealedData
	}
	return plaintext, nil
}

// trimSpace removes the surrounding whitespace of a configured value without
// importing strings for one call site.
func trimSpace(value string) string {
	start, end := 0, len(value)
	for start < end && isSpace(value[start]) {
		start++
	}
	for end > start && isSpace(value[end-1]) {
		end--
	}
	return value[start:end]
}

// isSpace reports whether a byte is whitespace.
func isSpace(character byte) bool {
	switch character {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}
