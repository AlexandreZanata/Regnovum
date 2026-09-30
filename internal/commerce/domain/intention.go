package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxIntentionRunes bounds intention keys and party labels: long
// enough for operation tokens and custody labels, short enough to
// stay out of log and index abuse.
const maxIntentionRunes = 128

// parseToken validates one opaque intention token: exact match, no
// control characters, bounded length.
func parseToken(raw string, err DomainError) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", err
	}
	if utf8.RuneCountInString(raw) > maxIntentionRunes {
		return "", err
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", err
		}
	}
	return raw, nil
}

// TransferIntention is one accepted voluntary movement: the
// idempotency key, the immutable business kind, the payer, the
// payee and the exact integer amount in milliINK, sealed by hash.
// The kind is part of the seal: a settled transfer never changes
// kind, and reuse of a key with different terms conflicts instead
// of merging.
type TransferIntention struct {
	Key         string
	Kind        TransferKind
	Payer       string
	Payee       string
	AmountMilli int64
	Hash        string
}

// IntentionRequest carries the fields of one acceptance. Every
// value arrives from the caller: no ratified party, amount or kind
// lives here.
type IntentionRequest struct {
	Key         string
	Kind        TransferKind
	Payer       string
	Payee       string
	AmountMilli int64
}

// AcceptIntention seals one transfer intention. Empty or ambiguous
// kinds, blank parties, unknown kinds and non-positive amounts all
// refuse before anything is stored.
func AcceptIntention(req IntentionRequest) (TransferIntention, error) {
	key, err := parseToken(req.Key, ErrInvalidIntention)
	if err != nil {
		return TransferIntention{}, err
	}
	if !req.Kind.IsValid() {
		return TransferIntention{}, ErrInvalidTransferKind
	}
	payer, err := parseToken(req.Payer, ErrInvalidIntention)
	if err != nil {
		return TransferIntention{}, err
	}
	payee, err := parseToken(req.Payee, ErrInvalidIntention)
	if err != nil {
		return TransferIntention{}, err
	}
	if req.AmountMilli <= 0 {
		return TransferIntention{}, ErrInvalidIntention
	}
	intention := TransferIntention{
		Key: key, Kind: req.Kind, Payer: payer, Payee: payee,
		AmountMilli: req.AmountMilli,
	}
	intention.Hash = sealIntention(intention)
	return intention, nil
}

// sealIntention binds key, kind, parties and amount: the canonical
// bytes any holder recomputes to detect tampering or reclassification.
func sealIntention(intention TransferIntention) string {
	canonical := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d",
		intention.Key, intention.Kind.String(), intention.Payer,
		intention.Payee, intention.AmountMilli)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// VerifyHash recomputes the seal and refuses an intention whose
// terms no longer agree: a checkpoint that fails here was edited,
// mixed up or reclassified.
func (in TransferIntention) VerifyHash() error {
	if in.Hash == "" || sealIntention(in) != in.Hash {
		return ErrInvalidTerms
	}
	return nil
}

// Matches reports whether a stored intention settles the same terms:
// same key, kind, parties and amount with a valid seal. A different
// kind under one key is a conflict, never a reclassification.
func (in TransferIntention) Matches(other TransferIntention) error {
	if err := in.VerifyHash(); err != nil {
		return err
	}
	if err := other.VerifyHash(); err != nil {
		return err
	}
	if in.Key != other.Key || in.Kind != other.Kind || in.Payer != other.Payer ||
		in.Payee != other.Payee || in.AmountMilli != other.AmountMilli {
		return ErrIntentionConflict
	}
	return nil
}

// VisibleTerms are the settlement terms both parties read: kind,
// parties and exact amount. Payer and payee read the same document;
// per-participant redaction arrives with private receipts in a
// later task, never by hiding terms at acceptance.
type VisibleTerms struct {
	Kind        TransferKind
	Payer       string
	Payee       string
	AmountMilli int64
}

// DescribeFor renders the terms visible to one party. Both parties
// read identical terms in this phase: the payer sees exactly what
// the payee sees, so neither side settles blind.
func (in TransferIntention) DescribeFor(party string) (VisibleTerms, error) {
	if err := in.VerifyHash(); err != nil {
		return VisibleTerms{}, err
	}
	if party != in.Payer && party != in.Payee {
		return VisibleTerms{}, ErrInvalidTerms
	}
	return VisibleTerms{
		Kind: in.Kind, Payer: in.Payer, Payee: in.Payee,
		AmountMilli: in.AmountMilli,
	}, nil
}
