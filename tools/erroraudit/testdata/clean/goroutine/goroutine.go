// Package goroutine is the clean half of the ownership rule and the three shapes
// the tree uses: the goroutine takes the caller's context, reports on a channel,
// counts itself in a wait group, or starts a method of a type that knows how to
// stop.
package goroutine

import (
	"context"
	"sync"
)

// Waiting takes the context and the wait group that counts it.
func Waiting(ctx context.Context, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		work(ctx)
	}()
}

// Reported sends its result on the channel the caller waits on.
func Reported(done chan<- struct{}) {
	go func() {
		work(context.Background())
		done <- struct{}{}
	}()
}

// Owned starts a method of a type that declares how to stop: the receiver knows
// the lifecycle, so the goroutine has an owner even when the statement is short.
func Owned(reporter *Reporter) {
	go reporter.run()
}

// Served starts the function that owns the connection it was handed: the owner is
// read from the declaration of `serveConnection`, which closes what it received,
// so the statement does not have to repeat the context or the channel.
func Served(connection *Connection, target string) {
	go serveConnection(connection, target)
}

// serveConnection closes the value it was given, which is what makes it an owner
// to this gate.
func serveConnection(connection *Connection, target string) {
	defer connection.Close()
	work(context.Background())
}

// Connection is the resource the served function owns.
type Connection struct{}

func (connection *Connection) Close() {}

// Reporter is the owner: it declares Stop next to run, which is what makes `run`
// an owned method to this gate.
type Reporter struct{}

func (reporter *Reporter) run() {}

func (reporter *Reporter) Stop() {}

// work honours the context it is given: a helper that took one and ignored it
// would be the other rule's finding, and this fixture is about ownership.
func work(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
}
