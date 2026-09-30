// Package context is a fixture of the static analysis gate (P23-T02): the
// context half of the pinned analyzer must refuse it. A nil context is the
// shape that panics far away from the line that caused it.
package context

import "context"

// Load receives a context from somewhere; the caller decides which one.
func Load(ctx context.Context) {
	_ = ctx
}

// Call passes a nil context, which is never the answer.
func Call() {
	Load(nil)
}
