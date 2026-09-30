package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxQuoteAccountRunes bounds the quote account identifier: long
// enough for an opaque account id, short enough to stay out of log
// and index abuse.
const maxQuoteAccountRunes = 128

// parseQuoteAccount validates the account bound to one quote.
// Matching is exact: surrounding whitespace is not trimmed and
// control characters are refused. The account travels opaque: the
// domain never reads identity, it only binds the quote to one
// holder.
func parseQuoteAccount(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidQuote
	}
	if utf8.RuneCountInString(raw) > maxQuoteAccountRunes {
		return "", ErrInvalidQuote
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidQuote
		}
	}
	return raw, nil
}

// PublicationQuote is one accepted publication quotation: the exact
// integer cost, the rule version, the content hash, the bound
// account and the half-open acceptance window
// [AcceptedAt, ExpiresAt), sealed by hash.
//
// The quote binds one account to one content hash: another account
// or edited bytes never reuse it. The client clock never enters the
// seal: acceptance and expiry are server instants, and a forged
// client instant changes nothing stored.
type PublicationQuote struct {
	Account     string
	Service     ServiceID
	Version     int
	Units       int
	PriceMilli  int64
	TotalMilli  int64
	ContentHash MeasuredHash
	AcceptedAt  time.Time
	ExpiresAt   time.Time
	Hash        string
}

// QuoteRequest carries the fields of one publication acceptance.
// Every value arrives from the caller: no ratified price, limit or
// lifetime lives here.
type QuoteRequest struct {
	Account    string
	Content    MeasuredContent
	Price      PriceEntry
	AcceptedAt time.Time
	TTL        time.Duration
}

// AcceptPublicationQuote seals one publication quotation at
// acceptance: the exact units × price cost, the price rule version,
// the content hash and the acceptance window. The price must cover
// the acceptance instant; the lifetime counts from acceptance.
func AcceptPublicationQuote(req QuoteRequest) (PublicationQuote, error) {
	account, err := parseQuoteAccount(req.Account)
	if err != nil {
		return PublicationQuote{}, err
	}
	if req.Content.IsZero() || req.Content.Hash().IsZero() {
		return PublicationQuote{}, ErrInvalidQuote
	}
	if req.AcceptedAt.IsZero() {
		return PublicationQuote{}, ErrInvalidQuote
	}
	if req.TTL <= 0 {
		return PublicationQuote{}, ErrInvalidQuote
	}
	if req.Price.Service.String() == "" || req.Price.Version <= 0 || req.Price.PriceMilli <= 0 {
		return PublicationQuote{}, ErrInvalidQuote
	}
	accepted := req.AcceptedAt.UTC()
	if !req.Price.Covers(accepted) {
		return PublicationQuote{}, ErrPriceNotFound
	}
	expires := accepted.Add(req.TTL)
	if !expires.After(accepted) {
		return PublicationQuote{}, ErrInvalidQuote
	}
	total, err := multiplyUnits(req.Content.Units(), req.Price.PriceMilli)
	if err != nil {
		return PublicationQuote{}, err
	}
	quote := PublicationQuote{
		Account:     account,
		Service:     req.Price.Service,
		Version:     req.Price.Version,
		Units:       req.Content.Units(),
		PriceMilli:  req.Price.PriceMilli,
		TotalMilli:  total,
		ContentHash: req.Content.Hash(),
		AcceptedAt:  accepted,
		ExpiresAt:   expires,
	}
	quote.Hash = sealPublicationQuote(quote)
	return quote, nil
}

// multiplyUnits computes units × price in milliINK, refusing the
// conversion that would cross the 64-bit ceiling instead of wrapping
// it.
func multiplyUnits(units int, priceMilli int64) (int64, error) {
	if units <= 0 || priceMilli <= 0 {
		return 0, ErrInvalidQuote
	}
	count := int64(units)
	if count > math.MaxInt64/priceMilli {
		return 0, ErrInvalidQuote
	}
	return count * priceMilli, nil
}

// sealPublicationQuote binds account, service, version, units,
// prices, content hash and instants: the canonical bytes any holder
// recomputes to detect tampering. The client clock never enters.
func sealPublicationQuote(quote PublicationQuote) string {
	canonical := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%s\x00%d\x00%d",
		quote.Account, quote.Service.String(), quote.Version,
		quote.Units, quote.PriceMilli, quote.TotalMilli,
		quote.ContentHash.String(),
		quote.AcceptedAt.UnixNano(), quote.ExpiresAt.UnixNano())
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// VerifyHash recomputes the seal and refuses a quotation whose terms
// no longer agree: a checkpoint that fails here was edited or mixed
// up.
func (q PublicationQuote) VerifyHash() error {
	if q.Hash == "" || sealPublicationQuote(q) != q.Hash {
		return ErrInvalidQuote
	}
	return nil
}

// Live reports whether the quotation prices at the given instant:
// the half-open window [AcceptedAt, ExpiresAt). A tick before expiry
// prices, the expiry tick itself does not.
func (q PublicationQuote) Live(at time.Time) bool {
	instant := at.UTC()
	return !instant.Before(q.AcceptedAt) && instant.Before(q.ExpiresAt)
}

// VerifyAcceptance refuses any use outside the accepted terms: a
// tampered seal, another account, edited content or a lapsed window
// all need a new acceptance. The judgment instant arrives from the
// server: a forged client clock changes the verdict for that call
// but never the stored quote.
func (q PublicationQuote) VerifyAcceptance(account string, content MeasuredContent, at time.Time) error {
	if err := q.VerifyHash(); err != nil {
		return err
	}
	if account != q.Account || content.Hash().IsZero() || !content.Hash().Equals(q.ContentHash) {
		return ErrQuoteMismatch
	}
	if at.IsZero() {
		return ErrInvalidQuote
	}
	if !q.Live(at) {
		return ErrQuoteExpired
	}
	return nil
}
