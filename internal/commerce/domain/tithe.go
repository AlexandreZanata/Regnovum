package domain

// SplitTithe divides one liquidated formal payment into the
// Treasury tithe and the provider net, both in milliINK.
//
// The rule follows RESPOSTAS §D item 24 (Q24 proposta): the tithe
// is `floor(valor × 10 / 100)` and the provider keeps the rest.
// For integer milliINK this equals `valor / 10` with truncation,
// computed by division only so amounts near MaxInt64 never
// overflow through the intermediate `×10`. The two outputs always
// sum to the paid value: `tithe + net == amount`.
//
// Only formal trade routes here (see TransferKind.BearsTithe):
// gifts never bear tithe, unreleased escrow never splits and
// refunds compensate through their own linked entries. The rate
// itself is mechanics for the commerce settlement; Q24 stays
// PENDENTE, so this function prices nothing on its own and
// activates nothing.
func SplitTithe(amountMill int64) (tithe, net int64, err error) {
	if amountMill <= 0 {
		return 0, 0, ErrInvalidContract
	}
	tithe = amountMill / 10
	net = amountMill - tithe
	return tithe, net, nil
}
