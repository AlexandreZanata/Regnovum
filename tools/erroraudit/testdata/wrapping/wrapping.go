// Package wrapping is the fixture of the error built over another error without
// `%w`: the text survives and the chain does not, so the caller's `errors.Is` and
// `errors.As` stop matching the failure this function was given.
package wrapping

import (
	"errors"
	"fmt"
)

var errStorage = errors.New("storage is unavailable")

// Load loses the chain with the `%s` verb over an error.
func Load(key string) error {
	err := read(key)
	if err != nil {
		return fmt.Errorf("load %q: %s", key, err)
	}
	return nil
}

// Nested loses it through the call of the error itself.
func Nested(key string) error {
	err := read(key)
	if err != nil {
		return fmt.Errorf("nested %q: %v", key, err.Error())
	}
	return nil
}

func read(key string) error { return errStorage }
