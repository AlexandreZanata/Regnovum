package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/bits"
)

// Rounding selects how one integer division treats its remainder.
// The mode arrives inside the fee schedule: no approved rounding
// lives here, and the choice is recorded beside the terms it judged.
type Rounding int

const (
	// RoundDown discards the remainder: the buyer never receives more
	// than the exact integer share, protecting the Treasury.
	RoundDown Rounding = iota
	// RoundUp absorbs the remainder: the buyer never receives less
	// than the exact integer share, protecting the buyer.
	RoundUp
	// RoundNearest settles halves away from the Treasury: remainders
	// at or above half a unit round up, below round down.
	RoundNearest
)

// FeeSchedule prices one purchase: the INK/BTC parity, the spread and
// fee and tax loads, the minimum fiat ticket, the INK divisibility
// step and the rounding mode. Every field arrives per offer: no
// ratified spread, fee, tax or parity lives here, and later phases
// set the vigente values where purchases are accepted. Money stays
// integer throughout: fiat in centavos, INK in milliINK.
type FeeSchedule struct {
	Version      int
	SatsPerInk   int64
	SpreadBps    int64
	FeeMinor     int64
	TaxBps       int64
	MinFiatMinor int64
	StepMilliInk int64
	Rounding     Rounding
}

// Valid reports whether the schedule bounds make sense: a versioned,
// positive parity, non-negative loads, a positive minimum ticket, a
// positive divisibility step and a known rounding mode.
func (s FeeSchedule) Valid() bool {
	return s.Version > 0 && s.SatsPerInk > 0 && s.SpreadBps >= 0 &&
		s.FeeMinor >= 0 && s.TaxBps >= 0 && s.MinFiatMinor > 0 &&
		s.StepMilliInk >= 1 && (s.Rounding == RoundDown || s.Rounding == RoundUp || s.Rounding == RoundNearest)
}

// Terms is one quoted purchase: every fiat leg in centavos, the
// reference and asked prices, the net INK in milliINK with the dust
// below the divisibility step reported instead of silenced, the ISO
// currency and the schedule version that priced it, sealed by hash.
// Terms quote before acceptance: nothing here moves money.
type Terms struct {
	FiatGrossMinor int64
	FeeMinor       int64
	TaxMinor       int64
	FiatNetMinor   int64
	PriceMinor     int64
	PriceAskMinor  int64
	InkMilli       int64
	DustMilli      int64
	Currency       string
	Schedule       int
	Hash           string
}

// satoshisPerBitcoin anchors the derivation: one BTC is 1e8 sats.
const satoshisPerBitcoin = 100_000_000

// millisPerInk anchors the derivation: one INK is 1000 milliINK.
const millisPerInk = 1000

// mulDiv computes (a*b)/d with the schedule rounding, refusing
// overflow instead of wrapping: absurd tickets fail closed.
func mulDiv(a, b, d int64, mode Rounding) (int64, error) {
	if d <= 0 {
		return 0, ErrInvalidTerms
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi != 0 {
		return 0, ErrInvalidTerms
	}
	quotient := int64(lo / uint64(d))
	remainder := lo % uint64(d)
	switch mode {
	case RoundDown:
	case RoundUp:
		if remainder != 0 {
			if quotient == math.MaxInt64 {
				return 0, ErrInvalidTerms
			}
			quotient++
		}
	case RoundNearest:
		if remainder*2 >= uint64(d) {
			if quotient == math.MaxInt64 {
				return 0, ErrInvalidTerms
			}
			quotient++
		}
	default:
		return 0, ErrInvalidTerms
	}
	return quotient, nil
}

// QuoteTerms derives one purchase from a fiat ticket: spread over the
// reference, fixed fee, proportional tax, minimum ticket, INK
// derivation with divisibility floor and reported dust. Every
// division is checked; sub-milliINK fractions discard down by ledger
// construction, and the divisibility remainder reports in milliINK so
// no amount silences itself.
func QuoteTerms(fiatMinor int64, price PriceMinor, schedule FeeSchedule) (Terms, error) {
	if fiatMinor <= 0 {
		return Terms{}, ErrInvalidTerms
	}
	if price.Int64() <= 0 {
		return Terms{}, ErrInvalidPrice
	}
	if !schedule.Valid() {
		return Terms{}, ErrInvalidTerms
	}
	if schedule.SpreadBps > math.MaxInt64-10000 {
		return Terms{}, ErrInvalidTerms
	}
	ask, err := mulDiv(price.Int64(), 10000+schedule.SpreadBps, 10000, schedule.Rounding)
	if err != nil {
		return Terms{}, err
	}
	if ask <= 0 {
		return Terms{}, ErrInvalidTerms
	}
	net := fiatMinor - schedule.FeeMinor
	if net <= 0 {
		return Terms{}, ErrInvalidTerms
	}
	tax, err := mulDiv(net, schedule.TaxBps, 10000, schedule.Rounding)
	if err != nil {
		return Terms{}, err
	}
	liquid := net - tax
	if liquid < schedule.MinFiatMinor {
		return Terms{}, ErrInvalidTerms
	}
	if ask > math.MaxInt64/schedule.SatsPerInk {
		return Terms{}, ErrInvalidTerms
	}
	denominator := ask * schedule.SatsPerInk
	raw, err := mulDiv(liquid, satoshisPerBitcoin*millisPerInk, denominator, RoundDown)
	if err != nil {
		return Terms{}, err
	}
	ink := raw / schedule.StepMilliInk * schedule.StepMilliInk
	terms := Terms{
		FiatGrossMinor: fiatMinor,
		FeeMinor:       schedule.FeeMinor,
		TaxMinor:       tax,
		FiatNetMinor:   liquid,
		PriceMinor:     price.Int64(),
		PriceAskMinor:  ask,
		InkMilli:       ink,
		DustMilli:      raw - ink,
		Currency:       "BRL",
		Schedule:       schedule.Version,
	}
	terms.Hash = sealTerms(terms)
	return terms, nil
}

// sealTerms binds every canonical value: any display that does not
// recompute this seal is not the persisted terms.
func sealTerms(terms Terms) string {
	canonical := fmt.Sprintf("%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00%s\x00%d",
		terms.FiatGrossMinor, terms.FeeMinor, terms.TaxMinor, terms.FiatNetMinor,
		terms.PriceMinor, terms.PriceAskMinor, terms.InkMilli, terms.DustMilli,
		terms.Currency, terms.Schedule)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// VerifyTermsHash recomputes the seal and refuses terms that no
// longer agree: displayed terms that fail here are not the persisted
// ones.
func (t Terms) VerifyTermsHash() error {
	if t.Hash == "" || sealTerms(t) != t.Hash {
		return ErrInvalidTerms
	}
	return nil
}
