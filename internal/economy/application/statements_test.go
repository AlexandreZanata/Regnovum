package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubStatementRepository stands in for the PostgreSQL adapter where no
// database behavior is under test: query validation happens before it is
// ever called, and canned pages prove the mappings.
type stubStatementRepository struct {
	called int
	page   *application.StatementPage
	err    error
}

func (s *stubStatementRepository) ReadStatement(_ context.Context, _ application.StatementRequest) (*application.StatementPage, error) {
	s.called++
	return s.page, s.err
}

func (s *stubStatementRepository) RebuildAll(_ context.Context, _ domain.SeasonKey) ([]application.CustodyProjection, error) {
	return nil, nil
}

func statementCommand() application.StatementCommand {
	return application.StatementCommand{
		Season: domain.CompatSeasonKey,
		Kind:   "user", Label: "ana", CallerAccountID: "account-ana", Limit: 10,
	}
}

func TestReadStatementUseCaseRefusesInvalidQueries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*application.StatementCommand)
		err    error
	}{
		{"unknown kind", func(c *application.StatementCommand) { c.Kind = "vault" }, domain.ErrUnknownCustody},
		{"blank label", func(c *application.StatementCommand) { c.Label = "" }, domain.ErrInvalidStatement},
		{"blank caller", func(c *application.StatementCommand) { c.CallerAccountID = "" }, domain.ErrInvalidStatement},
		{"zero limit", func(c *application.StatementCommand) { c.Limit = 0 }, domain.ErrInvalidStatement},
		{"huge limit", func(c *application.StatementCommand) { c.Limit = 101 }, domain.ErrInvalidStatement},
		{"broken cursor", func(c *application.StatementCommand) { c.Cursor = "not-a-cursor" }, domain.ErrInvalidStatement},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubStatementRepository{}
			useCase := application.NewReadStatementUseCase(stub)
			cmd := statementCommand()
			test.mutate(&cmd)
			if _, err := useCase.Execute(context.Background(), cmd); !errors.Is(err, test.err) {
				t.Errorf("Execute = %v, want %v", err, test.err)
			}
			if stub.called != 0 {
				t.Errorf("invalid query reached the repository: validation never touches storage")
			}
		})
	}
}

func TestStatementCursorRoundTrip(t *testing.T) {
	t.Parallel()

	if _, err := application.ParseStatementCursor(""); err != nil {
		t.Fatalf("empty cursor: %v", err)
	}
	for _, raw := range []string{"no-separator", "|id-only", "2026-13-99T99:99:99Z|id", "2026-01-01T00:00:00Z|"} {
		if _, err := application.ParseStatementCursor(raw); !errors.Is(err, domain.ErrInvalidStatement) {
			t.Errorf("ParseStatementCursor(%q) = %v, want ErrInvalidStatement", raw, err)
		}
	}
}

func TestSealedProjectionVerifies(t *testing.T) {
	t.Parallel()

	balance, err := domain.NewMilliInk(1500)
	if err != nil {
		t.Fatalf("NewMilliInk(1500): %v", err)
	}
	kind, err := domain.ParseCustodyKind("user")
	if err != nil {
		t.Fatalf("ParseCustodyKind(user): %v", err)
	}
	sealed := application.SealProjection("custody-id", kind, "ana", balance, 3, "entry-id")
	if err := sealed.Verify(); err != nil {
		t.Fatalf("fresh checkpoint: %v", err)
	}
	tampered := sealed
	tampered.Balance, _ = domain.NewMilliInk(1501)
	if err := tampered.Verify(); !errors.Is(err, domain.ErrInvalidStatement) {
		t.Fatalf("tampered checkpoint = %v, want ErrInvalidStatement", err)
	}
	emptied := sealed
	emptied.Digest = ""
	if err := emptied.Verify(); !errors.Is(err, domain.ErrInvalidStatement) {
		t.Fatalf("undigested checkpoint = %v, want ErrInvalidStatement", err)
	}
}
