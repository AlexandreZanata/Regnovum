// Package client is the clean half of the client rule: the client that declares
// its ceiling in the literal, and the client somebody else built — the rule
// refuses the literal that leaves `Timeout` out, and this package names both
// neighbours it has to accept.
package client

import (
	"net/http"
	"time"
)

// Bounded declares the timeout the rule requires of every client this
// repository builds.
func Bounded() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

// BoundedWithTransport declares the timeout next to the transport, which is the
// same decision with more fields written down.
func BoundedWithTransport(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: transport,
	}
}

// Borrowed is a client somebody else built: this package neither creates nor
// configures it, so the ceiling is that other place's decision to make.
func Borrowed(client *http.Client) *http.Client {
	return client
}
