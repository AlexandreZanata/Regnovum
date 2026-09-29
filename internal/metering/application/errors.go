// Package application defines the use cases, orchestrations and
// consumer-oriented ports of the metering module.
package application

import "errors"

var (
	// ErrInvalidPublishConfig indicates incomplete wiring of the
	// publish use case: a nil repository settles nothing.
	ErrInvalidPublishConfig = errors.New("application: publish repository is required")
)
