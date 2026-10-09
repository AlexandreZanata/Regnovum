package domain_test

import (
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

func TestGenerateAccountKey(t *testing.T) {
	t.Parallel()

	key, err := domain.GenerateAccountKey()
	if err != nil {
		t.Fatalf("unexpected error generating account key: %v", err)
	}

	if len(key) != domain.AccountKeyLength {
		t.Fatalf("expected key length %d, got %d", domain.AccountKeyLength, len(key))
	}

	for _, ch := range key {
		if !strings.ContainsRune(domain.AccountKeyAlphabet, ch) {
			t.Errorf("character %q not in alphabet %s", ch, domain.AccountKeyAlphabet)
		}
	}

	// Verify entropy / uniqueness
	anotherKey, err := domain.GenerateAccountKey()
	if err != nil {
		t.Fatalf("unexpected error generating second key: %v", err)
	}
	if key == anotherKey {
		t.Fatal("two consecutive random account keys should not collide")
	}
}

func TestCanonicalizeAccountKey(t *testing.T) {
	t.Parallel()

	validRaw := "23456789abcdefghjkmnpqrstuvwxyz2" // 32 valid chars
	canonical, err := domain.CanonicalizeAccountKey(validRaw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if canonical != validRaw {
		t.Fatalf("expected %s, got %s", validRaw, canonical)
	}

	// Formatting with hyphens and spaces
	withDashes := "2345-6789-abcd-efgh-jkmn-pqrs-tuvw-xyz2"
	canonical2, err := domain.CanonicalizeAccountKey(withDashes)
	if err != nil {
		t.Fatalf("unexpected error with dashes: %v", err)
	}
	if canonical2 != validRaw {
		t.Fatalf("expected %s, got %s", validRaw, canonical2)
	}

	// Uppercase conversion
	upper := strings.ToUpper(validRaw)
	canonicalUpper, err := domain.CanonicalizeAccountKey(upper)
	if err != nil {
		t.Fatalf("unexpected error with uppercase: %v", err)
	}
	if canonicalUpper != validRaw {
		t.Fatalf("expected uppercase to normalize to lowercase %s, got %s", validRaw, canonicalUpper)
	}

	// Errors
	if _, err := domain.CanonicalizeAccountKey(""); err != domain.ErrEmptyAccountKey {
		t.Errorf("expected ErrEmptyAccountKey for empty input, got %v", err)
	}

	if _, err := domain.CanonicalizeAccountKey("short-key"); err != domain.ErrInvalidAccountKeyLength {
		t.Errorf("expected ErrInvalidAccountKeyLength for short key, got %v", err)
	}

	// Invalid characters (e.g. '0', 'o', '1', 'i', 'l', '!')
	invalidChar := "23456789abcdefghjkmnpqrstuvwxyz!"
	if _, err := domain.CanonicalizeAccountKey(invalidChar); err != domain.ErrInvalidAccountKeyCharacter {
		t.Errorf("expected ErrInvalidAccountKeyCharacter for invalid char, got %v", err)
	}

	confusingChar := "23456789abcdefghjkmnpqrstuvwxyzo" // 'o' is excluded
	if _, err := domain.CanonicalizeAccountKey(confusingChar); err != domain.ErrInvalidAccountKeyCharacter {
		t.Errorf("expected ErrInvalidAccountKeyCharacter for 'o', got %v", err)
	}
}

func TestFormatAccountKey(t *testing.T) {
	t.Parallel()

	key := "23456789abcdefghjkmnpqrstuvwxyz2"
	formatted := domain.FormatAccountKey(key)
	expected := "2345-6789-abcd-efgh-jkmn-pqrs-tuvw-xyz2"
	if formatted != expected {
		t.Fatalf("expected %s, got %s", expected, formatted)
	}
}

func TestKeyHashesAndConstantTimeVerification(t *testing.T) {
	t.Parallel()

	key, err := domain.GenerateAccountKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	salt, err := domain.GenerateKeySalt()
	if err != nil {
		t.Fatalf("generate salt: %v", err)
	}
	if len(salt) != 32 {
		t.Fatalf("expected 16 bytes hex-encoded (32 hex chars), got len %d (%s)", len(salt), salt)
	}

	lookup := domain.ComputeKeyLookup(key)
	if lookup == [32]byte{} {
		t.Fatal("lookup hash cannot be all zeroes")
	}

	keyHash := domain.ComputeKeyHash(salt, key)
	if keyHash == [32]byte{} {
		t.Fatal("key hash cannot be all zeroes")
	}

	// Positive verification
	if !domain.VerifyKeyHash(salt, key, keyHash) {
		t.Fatal("expected VerifyKeyHash to return true for matching key and salt")
	}

	// Negative verification: wrong key
	wrongKey, _ := domain.GenerateAccountKey()
	if domain.VerifyKeyHash(salt, wrongKey, keyHash) {
		t.Fatal("expected VerifyKeyHash to return false for wrong key")
	}

	// Negative verification: wrong salt
	wrongSalt, _ := domain.GenerateKeySalt()
	if domain.VerifyKeyHash(wrongSalt, key, keyHash) {
		t.Fatal("expected VerifyKeyHash to return false for wrong salt")
	}

	// Negative verification: modified hash
	corruptHash := keyHash
	corruptHash[0] ^= 0xFF
	if domain.VerifyKeyHash(salt, key, corruptHash) {
		t.Fatal("expected VerifyKeyHash to return false for corrupt hash")
	}
}

func TestComputeUsernameHash(t *testing.T) {
	t.Parallel()

	h1 := domain.ComputeUsernameHash("Alexandre")
	h2 := domain.ComputeUsernameHash("alexandre")
	h3 := domain.ComputeUsernameHash(" alexandre ")
	if h1 != h2 || h2 != h3 {
		t.Fatal("username hash must be case-insensitive and trim whitespace")
	}

	hOther := domain.ComputeUsernameHash("outro_usuario")
	if h1 == hOther {
		t.Fatal("different usernames should produce different hashes")
	}
}
