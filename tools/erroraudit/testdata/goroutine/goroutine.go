// Package goroutine is the fixture of the goroutine nobody owns: the statement
// names no context, no channel, no wait group and no method of a type that knows
// how to stop, so nothing can wait for it, cancel it or prove it ever finished.
package goroutine

// Start launches a worker that no one can reach again.
func Start() {
	go deliver()
}

// Anonymous launches the same shape without even a name for it.
func Anonymous() {
	go func() {
		flush()
	}()
}

func deliver() { flush() }

func flush() {}
