// Package error_handling is a fixture of the static analysis gate (P23-T02):
// the error-handling half of the pinned analyzer must refuse it. An error value
// that is not the last result is the shape that makes every caller write the
// check somewhere else, and the shape callers get wrong.
package error_handling

// Load answers the error first and the value second.
func Load() (error, int) {
	return nil, 0
}
