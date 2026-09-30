// Package client is the fixture of the HTTP client without a deadline: the
// literal is built without a Timeout, so the call waits for the other side for as
// long as the other side takes.
package client

import (
	"net/http"
	"time"
)

// Gateway builds the client that calls the provider.
func Gateway() *http.Client {
	return &http.Client{}
}

// Reporter builds another one, with the fields around the missing deadline.
func Reporter() *http.Client {
	return &http.Client{
		Transport: &http.Transport{IdleConnTimeout: time.Minute},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return nil
		},
	}
}
