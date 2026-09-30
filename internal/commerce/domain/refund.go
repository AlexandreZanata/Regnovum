package domain

// SplitServiceRefund divides one service refund into the Treasury
// tithe reversal and the provider share, both in milliINK.
//
// The rule mirrors the settlement tithe (see SplitTithe): the
// reversal is `floor(refund × 10 / 100)` and the provider returns
// the rest. For integer milliINK this equals `refund / 10` with
// truncation, computed by division only so amounts near MaxInt64
// never overflow through the intermediate `×10`. The two outputs
// always sum to the refunded value: `tithe + provider == refund`.
//
// A refund never reopens its cause: the released contract stays
// terminal and each compensation links to the untouched original.
// Q24 stays PENDENTE, so this function prices nothing on its own
// and activates nothing.
func SplitServiceRefund(refundMill int64) (tithe, provider int64, err error) {
	if refundMill <= 0 {
		return 0, 0, ErrInvalidContract
	}
	tithe = refundMill / 10
	provider = refundMill - tithe
	return tithe, provider, nil
}

// ValidateRefundAccumulation guards one partial refund against the
// original payment: the request must be positive and the accumulated
// total must never exceed the original. Amounts already refunded
// outside [0, original] fail closed as corrupted state.
func ValidateRefundAccumulation(originalMill, refundedMill, requestedMill int64) error {
	if originalMill <= 0 || refundedMill < 0 || refundedMill > originalMill {
		return ErrInvalidContract
	}
	if requestedMill <= 0 {
		return ErrInvalidContract
	}
	if refundedMill+requestedMill > originalMill {
		return ErrRefundExceedsOriginal
	}
	return nil
}
