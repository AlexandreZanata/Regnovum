// Package resource is the clean half of the resource rule: the response is closed
// where it is acquired, the file is closed before the function returns, and the
// transaction is handed to the call that owns it. Each of the three is a shape the
// gate has to accept — a rule that refused them would be a rule against using
// resources at all.
package resource

import (
	"context"
	"net/http"
	"os"
)

// Stream closes the body on the way out, which is the shape the tree uses.
func Stream(client *http.Client, request *http.Request) error {
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return consume(response)
}

// Read closes the file before it reads it into memory and returns.
func Read(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return file.Name(), nil
}

// HandOver gives the transaction to run, which owns the commit and the rollback.
func HandOver(ctx context.Context, pool *pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	return run(tx)
}

type pool struct{}

func consume(response *http.Response) error { return nil }

func run(handle *handle) error { return nil }

type handle struct{}
