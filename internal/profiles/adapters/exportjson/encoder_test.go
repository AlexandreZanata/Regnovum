package exportjson_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/exportjson"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
)

func TestEncoderRendersDeterministicVersionedDocument(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	document := application.BuildPersonalExportDocument(now, &application.PersonalExportSections{
		Account: application.PersonalExportAccount{Email: "encoder@arena.example.com", Status: "active", CreatedAt: now},
	})

	first, err := exportjson.NewEncoder().EncodePersonalExport(document)
	if err != nil {
		t.Fatalf("EncodePersonalExport: %v", err)
	}
	second, err := exportjson.NewEncoder().EncodePersonalExport(document)
	if err != nil {
		t.Fatalf("EncodePersonalExport (second run): %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("the encoder must be deterministic")
	}

	var decoded map[string]any
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatalf("encoded document is not valid JSON: %v", err)
	}
	for _, key := range []string{"schema_version", "generated_at", "excluded_categories", "account", "positions", "position_changes", "arena_drafts", "arguments", "wallet", "passes", "billing"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("document is missing top-level key %q", key)
		}
	}
	if !strings.Contains(string(first), `"generated_at":"2026-09-18T12:00:00Z"`) {
		t.Fatalf("generated_at is not RFC 3339 UTC: %s", first)
	}
}
