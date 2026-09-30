package fakeemail

import (
	"context"
	"sync"

	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// SentVerificationEmail records an email message sent to a user.
type SentVerificationEmail struct {
	Email domain.Email
	Token string
}

// SentResetEmail records a password recovery email message sent to a user.
type SentResetEmail struct {
	Email domain.Email
	Token string
}

// SentPasswordChangedEmail records the notice that an account's password
// changed (P16-T06). It has no token by construction, and the change
// identifier is recorded so a test can tell two changes apart.
type SentPasswordChangedEmail struct {
	Email    domain.Email
	ChangeID string
}

// Sender implements application.EmailSender in-memory for testing and non-production environments.
type Sender struct {
	mu            sync.Mutex
	emails        []SentVerificationEmail
	resetEmails   []SentResetEmail
	changedEmails []SentPasswordChangedEmail
	failOn        error
}

var _ application.EmailSender = (*Sender)(nil)

// NewSender constructs a new fake email sender.
func NewSender() *Sender {
	return &Sender{}
}

// SendVerificationEmail records the sent verification email.
func (s *Sender) SendVerificationEmail(ctx context.Context, email domain.Email, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failOn != nil {
		return s.failOn
	}

	s.emails = append(s.emails, SentVerificationEmail{
		Email: email,
		Token: token,
	})
	return nil
}

// SendPasswordResetEmail records the sent password reset email.
func (s *Sender) SendPasswordResetEmail(ctx context.Context, email domain.Email, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failOn != nil {
		return s.failOn
	}

	s.resetEmails = append(s.resetEmails, SentResetEmail{
		Email: email,
		Token: token,
	})
	return nil
}

// SendPasswordChangedEmail records the notice that the password changed.
func (s *Sender) SendPasswordChangedEmail(ctx context.Context, email domain.Email, changeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failOn != nil {
		return s.failOn
	}

	s.changedEmails = append(s.changedEmails, SentPasswordChangedEmail{
		Email:    email,
		ChangeID: changeID,
	})
	return nil
}

// SentEmails returns a copy of all recorded verification emails.
func (s *Sender) SentEmails() []SentVerificationEmail {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]SentVerificationEmail, len(s.emails))
	copy(out, s.emails)
	return out
}

// SentResetEmails returns a copy of all recorded password reset emails.
func (s *Sender) SentResetEmails() []SentResetEmail {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]SentResetEmail, len(s.resetEmails))
	copy(out, s.resetEmails)
	return out
}

// LastTokenForEmail finds the most recent verification token sent to the given email address.
func (s *Sender) LastTokenForEmail(email domain.Email) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := len(s.emails) - 1; i >= 0; i-- {
		if s.emails[i].Email.Equals(email) {
			return s.emails[i].Token, true
		}
	}
	return "", false
}

// LastResetTokenForEmail finds the most recent password reset token sent to the given email address.
func (s *Sender) LastResetTokenForEmail(email domain.Email) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := len(s.resetEmails) - 1; i >= 0; i-- {
		if s.resetEmails[i].Email.Equals(email) {
			return s.resetEmails[i].Token, true
		}
	}
	return "", false
}

// SetFailure configures the sender to return an error on subsequent send attempts.
func (s *Sender) SetFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failOn = err
}

// SentPasswordChangedEmails returns a copy of all recorded password change notices.
func (s *Sender) SentPasswordChangedEmails() []SentPasswordChangedEmail {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]SentPasswordChangedEmail, len(s.changedEmails))
	copy(out, s.changedEmails)
	return out
}

// Reset clears all recorded messages and error states.
func (s *Sender) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emails = nil
	s.resetEmails = nil
	s.changedEmails = nil
	s.failOn = nil
}
