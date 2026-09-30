// Package application defines the use cases, orchestrations and
// consumer-oriented ports of the commerce module.
package application

import "errors"

var (
	// ErrInvalidTransferConfig indicates incomplete wiring of the
	// transfer use case: a nil repository settles nothing.
	ErrInvalidTransferConfig = errors.New("application: transfer repository is required")
)
