// Package context is the clean half of the context rules: the context is passed
// on, derived with a deadline, or deliberately blank because the function answers
// a caller that has none — and `context.Background` at the root of a process is
// not the placeholder this gate refuses.
package context

import (
	"context"
	"time"
)

// Save passes the context of the caller to the write.
func Save(ctx context.Context, value string) error {
	return write(ctx, value)
}

// Bounded derives its own deadline from the one it was given.
func Bounded(ctx context.Context, value string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return write(ctx, value)
}

// Startup is the root of a background process: there is no caller to take a
// context from, and choosing `Background` here is naming the owner.
func Startup() error {
	ctx := context.Background()
	return write(ctx, "startup")
}

// Ignored declares the parameter blank on purpose, which is how a signature is
// satisfied by an implementation that needs nothing from the caller.
func Ignored(_ context.Context, value string) error {
	return finish(value)
}

// write takes a context and honours it, which is what makes it the neighbour the
// rule has to accept: a sink that needs nothing from the caller names its
// parameter blank instead, like Ignored above.
func write(ctx context.Context, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return finish(value)
}

func finish(value string) error { return nil }
