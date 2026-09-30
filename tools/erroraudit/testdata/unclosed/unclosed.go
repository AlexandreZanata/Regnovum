// Package unclosed is the fixture of the resource nobody releases: the response,
// the file and the row set are acquired and never closed. The scanner of this
// gate reads these files by name — the Go toolchain skips testdata in every ./...
// pattern — and requires each rule to refuse its own fixture.
package unclosed

import (
	"context"
	"database/sql"
	"net/http"
	"os"
)

// Fetch acquires a response and never closes the body, which is the leak that
// costs a connection per call.
func Fetch(client *http.Client, request *http.Request) (int, error) {
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	return response.StatusCode, nil
}

// Read opens a file and drops it.
func Read(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	return file.Name(), nil
}

// Rows runs a query and never closes the row set.
func Rows(ctx context.Context, database *sql.DB) error {
	rows, err := database.Query(ctx, "select 1")
	if err != nil {
		return err
	}
	return rows.Err()
}
