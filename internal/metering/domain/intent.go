package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxPublishKeyRunes bounds a publish intention key: long enough for
// a caller operation token, short enough to stay out of log and
// index abuse.
const maxPublishKeyRunes = 128

// PublishKey names one publication intention: the idempotency token
// scoped by account. The same key with the same payload replays the
// stored outcome; the same key with another payload conflicts and
// never merges.
type PublishKey string

// ParsePublishKey validates a publish intention key. Matching is
// exact: surrounding whitespace is not trimmed and control
// characters are refused.
func ParsePublishKey(raw string) (PublishKey, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidPublishKey
	}
	if utf8.RuneCountInString(raw) > maxPublishKeyRunes {
		return "", ErrInvalidPublishKey
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidPublishKey
		}
	}
	return PublishKey(raw), nil
}

// String returns the stored key value.
func (k PublishKey) String() string { return string(k) }

// PublishPayload carries the settled terms bound to one intention.
// Callers seal the same canonical shape on write and on replay, so
// any difference in endpoints, amounts or hashes produces another
// seal.
type PublishPayload struct {
	Account     string
	Service     string
	Version     int
	Units       int
	AmountMilli int64
	ContentHash string
	QuoteHash   string
	FromKind    string
	FromLabel   string
	ToKind      string
	ToLabel     string
}

// PublishPayloadHash binds the settled terms to the intention: any
// difference in endpoints, amounts or hashes produces another seal,
// so reuse of a key with changed terms can never pass as a replay.
func PublishPayloadHash(payload PublishPayload) string {
	canonical := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		payload.Account, payload.Service, payload.Version, payload.Units, payload.AmountMilli,
		payload.ContentHash, payload.QuoteHash, payload.FromKind, payload.FromLabel, payload.ToKind, payload.ToLabel)
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

// TotalFor computes units × price in milliINK, refusing the
// conversion that would cross the 64-bit ceiling instead of wrapping
// it. Adapters and use cases quote through this helper so every
// layer seals the same total.
func TotalFor(units int, priceMilli int64) (int64, error) {
	return multiplyUnits(units, priceMilli)
}
