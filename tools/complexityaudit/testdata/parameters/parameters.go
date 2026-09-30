// Package parameters is a fixture of the complexity gate: a call with more
// parameters than the budget allows. The body does nothing, so nothing but the
// parameter rule can refuse it.
package parameters

// tooManyParameters has one parameter per position to be refused.
func tooManyParameters(first, second, third, fourth, fifth, sixth, seventh, eighth int) int {
	return first + second + third + fourth + fifth + sixth + seventh + eighth
}
