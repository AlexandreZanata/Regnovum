// Package discarded is the clean half of the discarded error rule: a function that
// answers with nothing cannot lose an error, and a result that is read is the
// handling the rule asks for.
package discarded

// Log answers with nothing, so calling it for the effect is the whole point.
func Log(value string) {}

// Record answers with an error and is read.
func Record(value string) error { return nil }

// Dispatch covers both shapes.
func Dispatch(value string) error {
	Log(value)
	if err := Record(value); err != nil {
		return err
	}
	return nil
}
