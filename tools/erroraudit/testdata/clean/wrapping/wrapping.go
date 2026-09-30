// Package wrapping is the clean half of the wrapping rule: `%w` keeps the chain,
// and a format with a value that is not an error is not a wrap at all — refusing
// it would be refusing every message that names what it was doing.
package wrapping

import (
	"errors"
	"fmt"
)

var errStorage = errors.New("storage is unavailable")

// Load wraps the failure it was given, so the caller's `errors.Is` still matches.
func Load(key string) error {
	err := read(key)
	if err != nil {
		return fmt.Errorf("load %q: %w", key, err)
	}
	return nil
}

// Unknown names the event it did not find: the argument is an identifier, not an
// error, and there is no chain to keep.
func Unknown(eventID string) error {
	return fmt.Errorf("event %s not found", eventID)
}

// Counted formats numbers, which is the other shape that must stay green.
func Counted(key string, rows int) error {
	return fmt.Errorf("key %s stored %d row(s)", key, rows)
}

func read(key string) error { return errStorage }
