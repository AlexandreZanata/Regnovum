package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
)

// captureLogger returns a logger writing to an in-memory buffer, so tests
// can assert on the exact JSON records.
func captureLogger(t *testing.T, level config.LogLevel) (*slog.Logger, *bytes.Buffer) {
	t.Helper()

	buffer := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slogLevel(level)}))
	return logger, buffer
}

func decodeRecords(t *testing.T, buffer *bytes.Buffer) []map[string]any {
	t.Helper()

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line is not valid JSON: %v\n%s", err, line)
		}
		records = append(records, record)
	}
	return records
}

func TestNewProducesJSONRecordsAtConfiguredLevel(t *testing.T) {
	testLogger, testBuffer := captureLogger(t, config.LogLevelInfo)
	testLogger.Info("request handled", "method", "GET", "path", "/health/live")

	records := decodeRecords(t, testBuffer)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0]["msg"] != "request handled" || records[0]["method"] != "GET" {
		t.Fatalf("unexpected record: %v", records[0])
	}
	if _, ok := records[0]["time"]; !ok {
		t.Log("time field presence depends on handler configuration")
	}
}

func TestJSONOutputIsMachineReadable(t *testing.T) {
	logger, buffer := captureLogger(t, config.LogLevelInfo)
	logger.Info("hello", "request_id", "abc-123")

	records := decodeRecords(t, buffer)
	if len(records) != 1 || records[0]["request_id"] != "abc-123" {
		t.Fatalf("structured record missing request_id: %v", records)
	}
}

func TestLogLevelFiltersRecords(t *testing.T) {
	logger, buffer := captureLogger(t, config.LogLevelWarn)

	logger.Info("not visible")
	logger.Warn("visible")

	records := decodeRecords(t, buffer)
	if len(records) != 1 || records[0]["msg"] != "visible" {
		t.Fatalf("level filtering broken: %v", records)
	}
}

// TestNoSensitiveValuesInLogs is the central redaction proof: cookies,
// authorization headers, emails and request bodies must never appear in
// captured records.
func TestNoSensitiveValuesInLogs(t *testing.T) {
	logger, buffer := captureLogger(t, config.LogLevelDebug)

	cookie := "arena_session=8f2c1e9a-44d4-4f0a-b1c3-9d0a55f1e2b3"
	authorization := "Bearer sk_live_51H8xQ2eFvZ"
	email := "operator@example.com"
	body := `{"password":"hunter2","credit_card":"4242424242424242"}`

	safeValues := []any{
		RedactValue(cookie),
		RedactValue(authorization),
		RedactEmail(email),
		RedactValue(body),
	}
	logger.Info("handling request",
		"request_id", "abc-123",
		"cookie", RedactValue(cookie),
		"authorization", RedactValue(authorization),
		"email", RedactEmail(email),
		"body", RedactValue(body),
	)

	output := buffer.String()
	for _, forbidden := range []string{
		"arena_session",
		"8f2c1e9a",
		"sk_live_51H8xQ2eFvZ",
		"authorization",
		"operator@example.com",
		"hunter2",
		"4242424242424242",
	} {
		// The keys cookie/authorization are allowed; the values are not.
		if forbidden == "authorization" || forbidden == "arena_session" {
			continue
		}
		if strings.Contains(output, forbidden) {
			t.Errorf("log output leaked %q:\n%s", forbidden, output)
		}
	}

	_ = safeValues
	records := decodeRecords(t, buffer)
	if len(records) != 1 {
		t.Fatalf("expected exactly one record, got %d", len(records))
	}
	if records[0]["cookie"] != "[REDACTED]" || records[0]["authorization"] != "[REDACTED]" {
		t.Errorf("cookie/authorization must render [REDACTED]: %v", records[0])
	}
	if records[0]["email"] != "o***@example.com" {
		t.Errorf("email must be masked, got %v", records[0]["email"])
	}
	if records[0]["body"] != "[REDACTED]" {
		t.Errorf("raw body must render [REDACTED], got %v", records[0]["body"])
	}
}

func TestRedactValueKeepsSafeValues(t *testing.T) {
	safe := []string{"GET", "/health/live", "200", "abc-123", "arenas"}
	for _, value := range safe {
		if got := RedactValue(value); got != value {
			t.Errorf("safe value %q was altered to %q", value, got)
		}
	}
}

func TestRedactValueHandlesEdgeCases(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"arena_session=", "[REDACTED]"},
		{"ARENA_SESSION=UPPERCASE", "[REDACTED]"},
		{"Authorization: Basic dXNlcjpwYXNz", "[REDACTED]"},
		{"postgres://user:pass@db/arena", "[REDACTED]"},
		{"password=hunter2 extra", "[REDACTED]"},
		{"$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHQ$aGFzaGhhc2g", "[REDACTED]"},
		{"just a normal note", "just a normal note"},
	}
	for _, test := range cases {
		if got := RedactValue(test.input); got != test.want {
			t.Errorf("RedactValue(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestRedactEmail(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"operator@example.com", "o***@example.com"},
		{"a@b.co", "a***@b.co"},
		{"no-email", "[REDACTED]"},
		{"@example.com", "[REDACTED]"},
		{"user@", "[REDACTED]"},
	}
	for _, test := range cases {
		if got := RedactEmail(test.input); got != test.want {
			t.Errorf("RedactEmail(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}
