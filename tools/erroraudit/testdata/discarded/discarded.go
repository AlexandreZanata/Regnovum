// Package discarded is the fixture of the error that is thrown away: the call is
// a function this module declares and it answers with an error, so the statement
// that ignores it is the failure the program decided not to know about.
package discarded

// Record saves and answers with the error its caller has to see.
func Record(value string) error { return nil }

// Dispatch discards it twice, in the two shapes the rule reads.
func Dispatch(value string) {
	Record(value)
	_ = Record(value + " again")
}
