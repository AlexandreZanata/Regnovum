package logging

// P29-T02 — hostile content stays inside its log record: newlines,
// bidi overrides, zero-width spaces and invalid UTF-8 in messages and
// fields must never split a JSON line, break decoding, or leak across
// records. Structured logging carries user text; line-delimited
// collectors depend on one object per line.

import (
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
)

// TestHostileContentStaysInItsRecord logs the P29-T02 hostile set and
// proves every record still decodes as exactly one JSON object with its
// fields intact.
func TestHostileContentStaysInItsRecord(t *testing.T) {
	t.Parallel()

	hostile := []string{
		"linha um\nlinha dois",
		"texto\u202Ereordenado",
		"arena\u200Bcom zero width",
		"café e café e 👩🏽‍🚀",
		"not json: {\"a\": 1}, trailing",
		string([]byte{0xff, 0xfe, 0x41}),
	}

	for _, value := range hostile {
		t.Run("field", func(t *testing.T) {
			t.Parallel()

			logger, buffer := captureLogger(t, config.LogLevelInfo)
			logger.Info("request handled", "content", value)

			records := decodeRecords(t, buffer)
			if len(records) != 1 {
				t.Fatalf("hostile field produced %d records, want exactly 1", len(records))
			}
			if _, ok := records[0]["content"]; !ok {
				t.Fatalf("record lost its content field: %v", records[0])
			}
		})
	}

	logger, buffer := captureLogger(t, config.LogLevelInfo)
	logger.Info("linha um\nlinha dois com \u202Ereordenado")

	records := decodeRecords(t, buffer)
	if len(records) != 1 {
		t.Fatalf("hostile message produced %d records, want exactly 1", len(records))
	}
	message, ok := records[0]["msg"].(string)
	if !ok || message == "" {
		t.Fatalf("record lost its message: %v", records[0])
	}
}
