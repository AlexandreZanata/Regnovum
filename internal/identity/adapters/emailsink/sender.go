// Package emailsink delivers the identity messages to a directory instead of a
// provider.
//
// Why it exists (P18-T07): the in-memory sink of `adapters/fakeemail` records
// the tokens inside the process that sent them, which is enough for a test that
// owns that process and useless for the browser harness, which runs beside it.
// The end-to-end journeys are driven by another process that has to read the
// code the message carries, so the development and test environments need one
// place where a delivery is observable from outside — and nothing else. The
// directory is the whole interface: no port, no endpoint, no reader in the
// server, and production refuses the variable that names it (see
// `internal/platform/config`).
//
// The contract a reader (the `tools/e2e` harness, or a person completing a
// journey by hand) can rely on:
//
//   - one file per delivered message, named `<sequence>-<kind>.json` with a
//     zero-padded sequence, so sorting the directory by name is the delivery
//     order;
//   - the file is renamed into place after it is complete, so a reader never
//     observes a half-written document, and files whose name starts with a dot
//     are in-flight and not messages;
//   - the document is the JSON encoding of Message, and nothing in it is
//     derived: it is what the use case handed to the port.
package emailsink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
)

// Kind identifies which message was delivered. It is part of the file name and
// of the document, so a reader filters by it instead of guessing from the
// fields that happen to be present.
type Kind string

const (
	// KindVerification is the message that carries an account's verification token.
	KindVerification Kind = "verification"
	// KindPasswordReset is the message that carries a password recovery token.
	KindPasswordReset Kind = "password-reset"
	// KindPasswordChanged is the notice that an account's password changed.
	KindPasswordChanged Kind = "password-changed"
)

// Message is the document written for one delivered message.
//
// The token fields are the unhashed values the identity use cases generate:
// this directory exists so a development or test journey can be completed, and
// it is refused outside those environments for exactly that reason.
type Message struct {
	Kind        Kind      `json:"kind"`
	Email       string    `json:"email"`
	Token       string    `json:"token,omitempty"`
	ChangeID    string    `json:"change_id,omitempty"`
	DeliveredAt time.Time `json:"delivered_at"`
}

// Options are the edges the sink needs. All of them are required: a sink that
// guessed its directory would write somewhere nobody looks, and one without a
// clock could not say when a message was delivered.
type Options struct {
	// Directory is the directory the messages are written to. It is created if
	// it does not exist.
	Directory string
	// Clock is the process clock the delivery instant is read from.
	Clock ports.Clock
	// Logger records that the sink is installed and where it writes.
	Logger *slog.Logger
}

// Sender implements application.EmailSender by writing each message to a file
// in a directory.
type Sender struct {
	directory string
	clock     ports.Clock
	logger    *slog.Logger

	mu       sync.Mutex
	sequence int
}

var _ application.EmailSender = (*Sender)(nil)

// NewSender validates the edges, creates the directory and returns the sink.
// A directory that cannot be created fails here — at composition — instead of
// failing at the first registration, which is the moment a person is waiting.
func NewSender(options Options) (*Sender, error) {
	if options.Directory == "" {
		return nil, errors.New("emailsink: directory is required")
	}
	if options.Clock == nil {
		return nil, errors.New("emailsink: clock is required")
	}
	if options.Logger == nil {
		return nil, errors.New("emailsink: logger is required")
	}
	if err := os.MkdirAll(options.Directory, 0o750); err != nil {
		return nil, fmt.Errorf("emailsink: create directory %s: %w", options.Directory, err)
	}

	return &Sender{directory: options.Directory, clock: options.Clock, logger: options.Logger}, nil
}

// Directory returns the directory the messages are written to.
func (sender *Sender) Directory() string { return sender.directory }

// SendVerificationEmail writes the verification message the use case produced.
func (sender *Sender) SendVerificationEmail(_ context.Context, email domain.Email, token string) error {
	return sender.write(Message{Kind: KindVerification, Email: email.String(), Token: token})
}

// SendPasswordResetEmail writes the recovery message the use case produced.
func (sender *Sender) SendPasswordResetEmail(_ context.Context, email domain.Email, token string) error {
	return sender.write(Message{Kind: KindPasswordReset, Email: email.String(), Token: token})
}

// SendPasswordChangedEmail writes the notice that the password changed. It
// carries no secret: the change identifier is what lets a reader tell two
// changes apart.
func (sender *Sender) SendPasswordChangedEmail(_ context.Context, email domain.Email, changeID string) error {
	return sender.write(Message{Kind: KindPasswordChanged, Email: email.String(), ChangeID: changeID})
}

// write serialises delivery: the sequence that names the files is shared, and
// two concurrent registrations must not claim the same one.
func (sender *Sender) write(message Message) error {
	sender.mu.Lock()
	defer sender.mu.Unlock()

	sender.sequence++
	message.DeliveredAt = sender.clock.Now().UTC()

	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("emailsink: encode %s message: %w", message.Kind, err)
	}

	// The file becomes visible complete or not at all: a reader polling the
	// directory may see it at any moment, and a partial document would be
	// indistinguishable from a message without a token.
	inFlight, err := os.CreateTemp(sender.directory, ".in-flight-*")
	if err != nil {
		return fmt.Errorf("emailsink: create message file: %w", err)
	}
	name := filepath.Join(sender.directory, fmt.Sprintf("%06d-%s.json", sender.sequence, message.Kind))

	if _, err := inFlight.Write(append(payload, '\n')); err != nil {
		inFlight.Close()
		os.Remove(inFlight.Name())
		return fmt.Errorf("emailsink: write %s message: %w", message.Kind, err)
	}
	if err := inFlight.Close(); err != nil {
		os.Remove(inFlight.Name())
		return fmt.Errorf("emailsink: close %s message: %w", message.Kind, err)
	}
	if err := os.Rename(inFlight.Name(), name); err != nil {
		os.Remove(inFlight.Name())
		return fmt.Errorf("emailsink: publish %s message: %w", message.Kind, err)
	}

	sender.logger.Info("email sink: message delivered",
		slog.String("kind", string(message.Kind)),
		slog.String("email", message.Email),
		slog.String("file", filepath.Base(name)),
	)

	return nil
}
