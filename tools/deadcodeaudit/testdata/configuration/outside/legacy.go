// Package legacy is the fixture of the impossible path: the process reads a
// literal ARENA_* key straight from the environment, and the loader of the
// fixture refuses that variable before the read can happen. With the variable
// set the process does not start; without it the read is empty.
package legacy

import "os"

// Mode answers the mode of the legacy surface, which can never be set.
func Mode() string {
	return os.Getenv("ARENA_LEGACY_FLAG")
}
