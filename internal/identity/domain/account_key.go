package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"math/big"
	"strings"
)

// AccountKeyAlphabet defines the 31 allowed symbols for unique account keys.
// Excludes ambiguous characters (0, o, 1, i, l) to avoid visual confusion.
const AccountKeyAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// AccountKeyLength is the fixed character length of an account key (32 chars ≈ 158 bits of entropy).
const AccountKeyLength = 32

// GenerateAccountKey produces a cryptographically secure random account key
// of 32 characters uniformly sampled from AccountKeyAlphabet using crypto/rand.
func GenerateAccountKey() (string, error) {
	b := make([]byte, AccountKeyLength)
	max := big.NewInt(int64(len(AccountKeyAlphabet)))
	for i := 0; i < AccountKeyLength; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = AccountKeyAlphabet[n.Int64()]
	}
	return string(b), nil
}

// CanonicalizeAccountKey normalizes an entered key by removing whitespace and hyphens,
// converting letters to lowercase, and validating length and character set.
func CanonicalizeAccountKey(raw string) (string, error) {
	cleaned := strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, raw)
	cleaned = strings.ToLower(cleaned)

	if len(cleaned) == 0 {
		return "", ErrEmptyAccountKey
	}
	if len(cleaned) != AccountKeyLength {
		return "", ErrInvalidAccountKeyLength
	}
	for _, ch := range cleaned {
		if !strings.ContainsRune(AccountKeyAlphabet, ch) {
			return "", ErrInvalidAccountKeyCharacter
		}
	}
	return cleaned, nil
}

// FormatAccountKey formats a 32-character canonical key into 8 groups of 4 separated by hyphens.
func FormatAccountKey(canonical string) string {
	if len(canonical) != AccountKeyLength {
		return canonical
	}
	var b strings.Builder
	for i := 0; i < AccountKeyLength; i += 4 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(canonical[i : i+4])
	}
	return b.String()
}

// ComputeKeyLookup calculates the SHA-256 hash of the canonical key for blind database lookup.
func ComputeKeyLookup(canonicalKey string) [32]byte {
	return sha256.Sum256([]byte(canonicalKey))
}

// GenerateKeySalt produces 16 cryptographically random bytes encoded as a 32-character hex string.
func GenerateKeySalt() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ComputeKeyHash calculates SHA-256(hex_salt + 0x00 + canonical_key).
func ComputeKeyHash(hexSalt, canonicalKey string) [32]byte {
	h := sha256.New()
	h.Write([]byte(hexSalt))
	h.Write([]byte{0x00})
	h.Write([]byte(canonicalKey))
	var res [32]byte
	copy(res[:], h.Sum(nil))
	return res
}

// VerifyKeyHash compares a key's computed hash against the stored hash in constant time.
func VerifyKeyHash(hexSalt, canonicalKey string, storedKeyHash [32]byte) bool {
	computed := ComputeKeyHash(hexSalt, canonicalKey)
	return subtle.ConstantTimeCompare(computed[:], storedKeyHash[:]) == 1
}

// ComputeUsernameHash calculates the SHA-256 hash of a normalized username.
func ComputeUsernameHash(username string) [32]byte {
	canonical := strings.ToLower(strings.TrimSpace(username))
	return sha256.Sum256([]byte(canonical))
}

// ValidateUsername checks that a username is 3 to 30 characters, ASCII alphanumeric,
// with optional internal underscores or hyphens.
func ValidateUsername(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 3 || len(trimmed) > 30 {
		return ErrInvalidUsernameLength
	}
	for i, ch := range trimmed {
		isAlphanumeric := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
		isSeparator := ch == '-' || ch == '_'
		if !isAlphanumeric && !isSeparator {
			return ErrInvalidUsernameCharacter
		}
		if (i == 0 || i == len(trimmed)-1) && isSeparator {
			return ErrInvalidUsernameCharacter
		}
	}
	return nil
}
