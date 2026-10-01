package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxContractRunes bounds contract keys, objects and party labels:
// long enough for a service description, short enough to stay out
// of log and index abuse.
const maxContractRunes = 280

// ContractStatus names the escrow lifecycle of one formal trade.
// Funds lock at funding and move exactly once, by acceptance, by
// cancellation or by a competent resolution after expiry. Expiry
// marks without moving: lapsed funds wait for a decision, they never
// deliver themselves.
type ContractStatus string

const (
	// ContractFunded locks the buyer amount in exclusive escrow:
	// nothing has been delivered yet.
	ContractFunded ContractStatus = "funded"
	// ContractAccepted records the buyer delivery acceptance with
	// funds still locked: release may now pay the provider.
	ContractAccepted ContractStatus = "accepted"
	// ContractReleased paid the provider in full: terminal.
	ContractReleased ContractStatus = "released"
	// ContractRefunded returned the amount to the buyer in full:
	// terminal.
	ContractRefunded ContractStatus = "refunded"
	// ContractExpired marked a lapsed contract without moving any
	// leg: only a competent resolution settles it from here.
	ContractExpired ContractStatus = "expired"
	// ContractResolved settled an expired contract by decision:
	// terminal.
	ContractResolved ContractStatus = "resolved"
)

// ParseContractStatus validates a status against the closed machine.
func ParseContractStatus(raw string) (ContractStatus, error) {
	status := ContractStatus(raw)
	if !status.IsValid() {
		return "", ErrInvalidContract
	}
	return status, nil
}

// IsValid reports whether the status belongs to the closed machine.
func (s ContractStatus) IsValid() bool {
	switch s {
	case ContractFunded, ContractAccepted, ContractReleased,
		ContractRefunded, ContractExpired, ContractResolved:
		return true
	default:
		return false
	}
}

// Terminal reports whether the status settles nothing further:
// released, refunded and resolved contracts never reopen.
func (s ContractStatus) Terminal() bool {
	return s == ContractReleased || s == ContractRefunded || s == ContractResolved
}

// String returns the stored status value.
func (s ContractStatus) String() string { return string(s) }

// ResolveDecision names the competent outcome for an expired
// contract: pay the provider or return the buyer.
type ResolveDecision string

const (
	// ResolveRelease pays the expired escrow to the provider.
	ResolveRelease ResolveDecision = "release"
	// ResolveRefund returns the expired escrow to the buyer.
	ResolveRefund ResolveDecision = "refund"
)

// ParseResolveDecision validates a resolution outcome.
func ParseResolveDecision(raw string) (ResolveDecision, error) {
	decision := ResolveDecision(raw)
	switch decision {
	case ResolveRelease, ResolveRefund:
		return decision, nil
	default:
		return "", ErrInvalidContract
	}
}

// String returns the stored decision value.
func (d ResolveDecision) String() string { return string(d) }

// TradeContract is one formal service payment: the object, the
// parties, the exact amount, the expiry and the sealed terms. Only
// trade kind enters escrow: gifts never lock, refunds flow through
// their own linked entries and treasury movements are not payments.
// Season names the commerce book and the terminal policy with both
// accepts travel beside the seal: the seal covers object, parties,
// amount and expiry, the book and clause checks match seasons and
// terms before any intention opens.
type TradeContract struct {
	Key            string
	Object         string
	Buyer          string
	Provider       string
	AmountMill     int64
	ExpiresAt      time.Time
	Status         ContractStatus
	Hash           string
	Season         SeasonKey
	PolicyRef      string
	PolicyHash     string
	BuyerAccept    string
	ProviderAccept string
}

// ContractRequest carries the fields of one funding. Every value
// arrives from the caller: no ratified object, amount or deadline
// lives here.
type ContractRequest struct {
	Key        string
	Object     string
	Buyer      string
	Provider   string
	AmountMill int64
	ExpiresAt  time.Time
	Now        time.Time
}

// FundContract seals one trade contract in funded state. Blank
// objects, unknown parties, non-positive amounts and past expiries
// refuse before anything locks. The legacy path binds the
// explicitly inactive compat-legacy book with no terminal clause.
func FundContract(req ContractRequest) (TradeContract, error) {
	key, err := parseContractToken(req.Key)
	if err != nil {
		return TradeContract{}, err
	}
	object, err := parseContractToken(req.Object)
	if err != nil {
		return TradeContract{}, err
	}
	buyer, err := parseContractToken(req.Buyer)
	if err != nil {
		return TradeContract{}, err
	}
	provider, err := parseContractToken(req.Provider)
	if err != nil {
		return TradeContract{}, err
	}
	if buyer == provider {
		return TradeContract{}, ErrInvalidContract
	}
	if req.AmountMill <= 0 {
		return TradeContract{}, ErrInvalidContract
	}
	if req.ExpiresAt.IsZero() || req.Now.IsZero() || !req.ExpiresAt.After(req.Now) {
		return TradeContract{}, ErrInvalidContract
	}
	contract := TradeContract{
		Key: key, Object: object, Buyer: buyer, Provider: provider,
		AmountMill: req.AmountMill, ExpiresAt: req.ExpiresAt.UTC(),
		Status: ContractFunded, Season: SeasonKey(CompatSeasonKey),
	}
	contract.Hash = sealContract(contract)
	return contract, nil
}

// SeasonalContractRequest carries one seasonal funding beside the
// formal terms: the book with its exclusive end, the terminal
// policy with its hash, the accept evidence of both parties and
// the reset acknowledgement shown before funding.
type SeasonalContractRequest struct {
	ContractRequest
	Season            SeasonKey
	PolicyRef         string
	PolicyHash        string
	BuyerAccept       string
	ProviderAccept    string
	SeasonEndsAt      time.Time
	ResetAcknowledged bool
}

// FundSeasonalContract seals one trade contract in one commerce
// book: the terminal clause of section 5.1 is required outside
// compat-legacy, and the deadline never passes the book end. The
// seal still covers object, parties, amount and expiry; the book
// and clause travel beside it and are matched before any escrow
// opens. Absence or divergence refuses new seasonal funding.
func FundSeasonalContract(req SeasonalContractRequest) (TradeContract, error) {
	contract, err := FundContract(req.ContractRequest)
	if err != nil {
		return TradeContract{}, err
	}
	terms := SeasonalTradeTerms{
		Season: req.Season, PolicyRef: req.PolicyRef, PolicyHash: req.PolicyHash,
		BuyerAccept: req.BuyerAccept, ProviderAccept: req.ProviderAccept,
		SeasonEndsAt: req.SeasonEndsAt, ResetAcknowledged: req.ResetAcknowledged,
	}
	if err := ValidateSeasonalTradeTerms(terms, contract.ExpiresAt); err != nil {
		return TradeContract{}, err
	}
	contract.Season = req.Season
	contract.PolicyRef = req.PolicyRef
	contract.PolicyHash = req.PolicyHash
	contract.BuyerAccept = req.BuyerAccept
	contract.ProviderAccept = req.ProviderAccept
	return contract, nil
}

func parseContractToken(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidContract
	}
	if utf8.RuneCountInString(raw) > maxContractRunes {
		return "", ErrInvalidContract
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidContract
		}
	}
	return raw, nil
}

// sealContract binds key, object, parties, amount and expiry: the
// canonical bytes any holder recomputes to detect tampering.
func sealContract(contract TradeContract) string {
	canonical := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%d",
		contract.Key, contract.Object, contract.Buyer,
		contract.Provider, contract.AmountMill, contract.ExpiresAt.UnixNano())
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// VerifyHash recomputes the seal and refuses a contract whose terms
// no longer agree.
func (c TradeContract) VerifyHash() error {
	if c.Hash == "" || sealContract(c) != c.Hash {
		return ErrInvalidContract
	}
	return nil
}

// Accept marks the buyer delivery acceptance: funded contracts
// only. The provider accepting its own delivery is refused by the
// caller check, not by the state.
func (c TradeContract) Accept(by string) (TradeContract, error) {
	if err := c.VerifyHash(); err != nil {
		return TradeContract{}, err
	}
	if c.Status != ContractFunded {
		return TradeContract{}, ErrContractState
	}
	if by != c.Buyer {
		return TradeContract{}, ErrUnauthorizedRelease
	}
	next := c
	next.Status = ContractAccepted
	next.Hash = sealContract(next)
	return next, nil
}

// Release pays the accepted escrow to the provider: accepted
// contracts only, never by the buyer alone without acceptance and
// never twice.
func (c TradeContract) Release() (TradeContract, error) {
	if err := c.VerifyHash(); err != nil {
		return TradeContract{}, err
	}
	if c.Status != ContractAccepted {
		return TradeContract{}, ErrContractState
	}
	next := c
	next.Status = ContractReleased
	next.Hash = sealContract(next)
	return next, nil
}

// Cancel refunds a funded contract to the buyer: funded contracts
// only, before any acceptance.
func (c TradeContract) Cancel() (TradeContract, error) {
	if err := c.VerifyHash(); err != nil {
		return TradeContract{}, err
	}
	if c.Status != ContractFunded {
		return TradeContract{}, ErrContractState
	}
	next := c
	next.Status = ContractRefunded
	next.Hash = sealContract(next)
	return next, nil
}

// Expire marks a lapsed contract without moving any leg: funded and
// accepted contracts only. Expiry never delivers by itself.
func (c TradeContract) Expire(at time.Time) (TradeContract, error) {
	if err := c.VerifyHash(); err != nil {
		return TradeContract{}, err
	}
	if c.Status != ContractFunded && c.Status != ContractAccepted {
		return TradeContract{}, ErrContractState
	}
	if at.IsZero() || !at.After(c.ExpiresAt) {
		return TradeContract{}, ErrInvalidContract
	}
	next := c
	next.Status = ContractExpired
	next.Hash = sealContract(next)
	return next, nil
}

// Resolve settles an expired contract by competent decision:
// expired contracts only, exactly once.
func (c TradeContract) Resolve(decision ResolveDecision) (TradeContract, error) {
	if err := c.VerifyHash(); err != nil {
		return TradeContract{}, err
	}
	if c.Status != ContractExpired {
		return TradeContract{}, ErrContractState
	}
	if decision != ResolveRelease && decision != ResolveRefund {
		return TradeContract{}, ErrInvalidContract
	}
	next := c
	next.Status = ContractResolved
	next.Hash = sealContract(next)
	return next, nil
}
