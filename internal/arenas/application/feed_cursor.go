package application

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
)

// feedCursorVersion prefixes every cursor payload so a future layout can be
// introduced without silently misreading old cursors.
const feedCursorVersion = "v1"

// minFeedCursorSecretLength is the minimum HMAC key size accepted for feed
// cursor signing (256 bits).
const minFeedCursorSecretLength = 32

// FeedCursorCodec encodes and verifies opaque, server-signed feed cursors:
// clients may pass them back verbatim, but a forged or corrupted cursor is
// rejected instead of being interpreted.
type FeedCursorCodec struct {
	secret []byte
}

// NewFeedCursorCodec builds the codec from the configured signing secret.
// Secrets shorter than 256 bits are refused.
func NewFeedCursorCodec(secret []byte) (*FeedCursorCodec, error) {
	if len(secret) < minFeedCursorSecretLength {
		return nil, ErrWeakFeedCursorSecret
	}
	copied := make([]byte, len(secret))
	copy(copied, secret)
	return &FeedCursorCodec{secret: copied}, nil
}

// Encode renders the signed cursor of the last delivered Arena.
func (c *FeedCursorCodec) Encode(arena domain.Arena) string {
	publishedAt := time.Time{}
	if instant := arena.PublishedAt(); instant != nil {
		publishedAt = instant.UTC()
	}
	payload := strings.Join([]string{
		feedCursorVersion,
		publishedAt.Format(time.RFC3339Nano),
		arena.ID().String(),
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
func (c *FeedCursorCodec) Decode(raw string) (*FeedPosition, error) {
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
	if len(fields) != 3 || fields[0] != feedCursorVersion {
		return nil, ErrInvalidCursor
	}

	publishedAt, err := time.Parse(time.RFC3339Nano, fields[1])
	if err != nil {
		return nil, ErrInvalidCursor
	}

	arenaID := fields[2]
	if arenaID == "" {
		return nil, ErrInvalidCursor
	}
	for i := 0; i < len(arenaID); i++ {
		if arenaID[i] < 0x21 || arenaID[i] > 0x7e {
			return nil, ErrInvalidCursor
		}
	}

	return &FeedPosition{PublishedAt: publishedAt.UTC(), ArenaID: arenaID}, nil
}
