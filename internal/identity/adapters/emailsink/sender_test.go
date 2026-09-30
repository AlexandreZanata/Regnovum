// Tests of the directory email sink (P18-T07): the journeys of another
// process depend on this contract, so it is asserted as a contract — the file
// name order is the delivery order, the document is complete, and the key set
// of each kind is fixed.
package emailsink_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/emailsink"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// fixedClock is the delivery instant the tests assert, so a message documents
// when it was delivered instead of when the test happened to run.
type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

func newSender(t *testing.T, directory string) *emailsink.Sender {
	t.Helper()

	sender, err := emailsink.NewSender(emailsink.Options{
		Directory: directory,
		Clock:     fixedClock{now: time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)},
		Logger:    slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewSender() error = %v", err)
	}
	return sender
}

func address(t *testing.T, value string) domain.Email {
	t.Helper()

	parsed, err := domain.ParseEmail(value)
	if err != nil {
		t.Fatalf("ParseEmail(%q) error = %v", value, err)
	}
	return parsed
}

// readDirectory returns the messages of a directory in delivery order, which is
// the order the names declare — the property the harness relies on.
func readDirectory(t *testing.T, directory string) ([]emailsink.Message, []string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", directory, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	messages := make([]emailsink.Message, 0, len(names))
	for _, name := range names {
		if strings.HasPrefix(name, ".") {
			t.Fatalf("directory holds an in-flight file %q: a completed delivery never leaves one behind", name)
		}

		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", name, err)
		}

		var message emailsink.Message
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatalf("message %s is not a complete JSON document: %v (raw: %s)", name, err, raw)
		}
		messages = append(messages, message)
	}
	return messages, names
}

func TestTheSinkWritesEveryKindInDeliveryOrder(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	sender := newSender(t, directory)
	ctx := context.Background()

	if err := sender.SendVerificationEmail(ctx, address(t, "first@arena.test"), "verification-token"); err != nil {
		t.Fatalf("SendVerificationEmail() error = %v", err)
	}
	if err := sender.SendPasswordResetEmail(ctx, address(t, "first@arena.test"), "recovery-token"); err != nil {
		t.Fatalf("SendPasswordResetEmail() error = %v", err)
	}
	if err := sender.SendPasswordChangedEmail(ctx, address(t, "first@arena.test"), "change-1"); err != nil {
		t.Fatalf("SendPasswordChangedEmail() error = %v", err)
	}

	messages, names := readDirectory(t, directory)
	want := []string{"000001-verification.json", "000002-password-reset.json", "000003-password-changed.json"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", names, want)
	}

	kinds := []emailsink.Kind{emailsink.KindVerification, emailsink.KindPasswordReset, emailsink.KindPasswordChanged}
	for index, message := range messages {
		if message.Kind != kinds[index] {
			t.Errorf("message %d kind = %q, want %q", index, message.Kind, kinds[index])
		}
		if message.Email != "first@arena.test" {
			t.Errorf("message %d email = %q", index, message.Email)
		}
		if !message.DeliveredAt.Equal(time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)) {
			t.Errorf("message %d delivered at %s, want the clock instant", index, message.DeliveredAt)
		}
	}

	if messages[0].Token != "verification-token" {
		t.Errorf("verification token = %q", messages[0].Token)
	}
	if messages[1].Token != "recovery-token" {
		t.Errorf("recovery token = %q", messages[1].Token)
	}
	if messages[2].ChangeID != "change-1" || messages[2].Token != "" {
		t.Errorf("password change = change %q token %q, want the identifier and no token", messages[2].ChangeID, messages[2].Token)
	}
}

// The key set is the contract a reader parses: a field that appears or
// disappears silently would break the harness instead of failing a test.
func TestTheDocumentKeySetIsFixedPerKind(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	sender := newSender(t, directory)
	ctx := context.Background()

	if err := sender.SendVerificationEmail(ctx, address(t, "keys@arena.test"), "token"); err != nil {
		t.Fatalf("SendVerificationEmail() error = %v", err)
	}
	if err := sender.SendPasswordChangedEmail(ctx, address(t, "keys@arena.test"), "change-2"); err != nil {
		t.Fatalf("SendPasswordChangedEmail() error = %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(directory, "000001-verification.json"))
	if err != nil {
		t.Fatalf("ReadFile(verification) error = %v", err)
	}
	var verification map[string]any
	if err := json.Unmarshal(raw, &verification); err != nil {
		t.Fatalf("Unmarshal(verification) error = %v", err)
	}
	assertKeys(t, "verification", verification, "delivered_at", "email", "kind", "token")

	raw, err = os.ReadFile(filepath.Join(directory, "000002-password-changed.json"))
	if err != nil {
		t.Fatalf("ReadFile(password-changed) error = %v", err)
	}
	var changed map[string]any
	if err := json.Unmarshal(raw, &changed); err != nil {
		t.Fatalf("Unmarshal(password-changed) error = %v", err)
	}
	assertKeys(t, "password-changed", changed, "change_id", "delivered_at", "email", "kind")
}

func assertKeys(t *testing.T, kind string, document map[string]any, want ...string) {
	t.Helper()

	keys := make([]string, 0, len(document))
	for key := range document {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	sort.Strings(want)
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("%s document keys = %v, want %v", kind, keys, want)
	}
}

// Concurrent deliveries are the ordinary case in a server: two registrations
// arriving together must not claim the same name or interleave their bytes.
func TestConcurrentDeliveriesAreDistinctAndComplete(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	sender := newSender(t, directory)

	const deliveries = 40
	var group sync.WaitGroup
	group.Add(deliveries)
	for index := 0; index < deliveries; index++ {
		go func(index int) {
			defer group.Done()
			token := "token-" + string(rune('a'+index%26))
			if err := sender.SendVerificationEmail(context.Background(), address(t, "concurrent@arena.test"), token); err != nil {
				t.Errorf("SendVerificationEmail(%d) error = %v", index, err)
			}
		}(index)
	}
	group.Wait()

	messages, names := readDirectory(t, directory)
	if len(messages) != deliveries {
		t.Fatalf("delivered messages = %d, want %d", len(messages), deliveries)
	}

	seen := make(map[string]bool, deliveries)
	for _, name := range names {
		if seen[name] {
			t.Fatalf("file %s was written twice", name)
		}
		seen[name] = true
	}
}

func TestTheSinkCreatesItsDirectoryAndIsReadableFromOutside(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "not-created-yet", "email")
	sender := newSender(t, directory)

	if sender.Directory() != directory {
		t.Fatalf("Directory() = %q, want %q", sender.Directory(), directory)
	}

	if err := sender.SendVerificationEmail(context.Background(), address(t, "outside@arena.test"), "token"); err != nil {
		t.Fatalf("SendVerificationEmail() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "000001-verification.json")); err != nil {
		t.Fatalf("the delivered file is not readable from outside the process: %v", err)
	}
}

func TestIncompleteOptionsAreRefused(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	clock := fixedClock{now: time.Now()}

	cases := map[string]emailsink.Options{
		"without directory": {Clock: clock, Logger: logger},
		"without clock":     {Directory: t.TempDir(), Logger: logger},
		"without logger":    {Directory: t.TempDir(), Clock: clock},
	}

	for name, options := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := emailsink.NewSender(options); err == nil {
				t.Fatal("NewSender() accepted an incomplete composition")
			}
		})
	}
}
