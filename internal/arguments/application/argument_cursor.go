package application

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
)

// argumentCursorVersion prefixes every cursor payload so a future layout can
// be introduced without silently misreading old cursors.
const argumentCursorVersion = "v1"

// minArgumentCursorSecretLength is the minimum HMAC key size accepted for
// cursor signing (256 bits).
const minArgumentCursorSecretLength = 32

// ArgumentPosition is the decoded keyset position: the last argument already
// delivered to the caller.
type ArgumentPosition struct {
	CreatedAt  time.Time
	ArgumentID string
}

// ArgumentCursorCodec encodes and verifies opaque, server-signed keyset
// cursors: clients may pass them back verbatim, but a forged or corrupted
// cursor is rejected instead of being interpreted.
type ArgumentCursorCodec struct {
	secret []byte
}

// NewArgumentCursorCodec builds the codec from the configured signing
// secret. Secrets shorter than 256 bits are refused.
func NewArgumentCursorCodec(secret []byte) (*ArgumentCursorCodec, error) {
	if len(secret) < minArgumentCursorSecretLength {
		return nil, ErrWeakCursorSecret
	}
	copied := make([]byte, len(secret))
	copy(copied, secret)
	return &ArgumentCursorCodec{secret: copied}, nil
}

// Encode renders the signed cursor of the last delivered argument.
func (c *ArgumentCursorCodec) Encode(argument PublicArgument) string {
	payload := strings.Join([]string{
		argumentCursorVersion,
		argument.CreatedAt.UTC().Format(time.RFC3339Nano),
		argument.ID.String(),
	}, "|")

	signature := hmac.New(sha256.New, c.secret)
	signature.Write([]byte(payload))

	return base64.RawURLEncoding.EncodeToString([]byte(payload)) +
		"." + base64.RawURLEncoding.EncodeToString(signature.Sum(nil))
}

// Decode verifies the signature and decodes the keyset position. An empty
// cursor yields a nil position (first page); malformed, forged or
// version-mismatched cursors fail with ErrInvalidCursor instead of being
// reflected back.
func (c *ArgumentCursorCodec) Decode(raw string) (*ArgumentPosition, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}

	parts := strings.Split(trimmed, ".")
	if len(parts) != 2 {
		return nil, ErrInvalidCursor
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidCursor
	}
	signatureBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidCursor
	}

	expected := hmac.New(sha256.New, c.secret)
	expected.Write(payloadBytes)
	if !hmac.Equal(signatureBytes, expected.Sum(nil)) {
		return nil, ErrInvalidCursor
	}

	fields := strings.Split(string(payloadBytes), "|")
	if len(fields) != 3 || fields[0] != argumentCursorVersion {
		return nil, ErrInvalidCursor
	}

	createdAt, err := time.Parse(time.RFC3339Nano, fields[1])
	if err != nil {
		return nil, ErrInvalidCursor
	}

	argumentID := fields[2]
	if _, err := domain.ParseArgumentID(argumentID); err != nil {
		return nil, ErrInvalidCursor
	}

	return &ArgumentPosition{CreatedAt: createdAt.UTC(), ArgumentID: argumentID}, nil
}
