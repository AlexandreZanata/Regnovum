// Package contexttodo is the fixture of the context nobody chose: `context.TODO`
// is the placeholder of the standard library, and a delivered path that answers
// with it is telling the reader that the author knew a context was missing.
package contexttodo

import "context"

// Sweep runs with the placeholder context instead of the one its caller has.
func Sweep() error {
	ctx := context.TODO()
	return run(ctx)
}

func run(ctx context.Context) error { return nil }
