// Package inside is the negative half of the direct-read rule: this gate must
// leave a read of a registered key alone — the variable arrives, the loader
// validates it — and a variable of the platform, which is not this repository's
// configuration at all.
package inside

import "os"

// Addr is the address of a component that reads the same variable the
// configuration accepts.
func Addr() string {
	return os.Getenv("ARENA_ENV")
}

// Home is the platform's own variable: the ARENA_* prefix is what makes a key
// configuration, and this one does not carry it.
func Home() string {
	return os.Getenv("HOME")
}
