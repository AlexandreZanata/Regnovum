// Package contextdropped is the fixture of the context that arrives and is never
// used: the caller's cancellation, deadline and values live in that argument, and
// a body that never mentions it has dropped all three.
package contextdropped

import "context"

// Save takes the request context and writes without it: the cancel of the caller
// is heard by nobody, and the database keeps working after the client left.
func Save(ctx context.Context, value string) error {
	return write(value)
}

// Publish names the context differently, which is the same defect: the rule reads
// the type of the parameter and not its name.
func Publish(scope context.Context, topic string) error {
	return write(topic)
}

func write(value string) error { return nil }
