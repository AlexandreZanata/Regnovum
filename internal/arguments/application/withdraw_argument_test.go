package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
)

func seedPublishedArgument(t *testing.T, repo *fakeArgumentRepo, status string) *application.PublishedArgument {
	t.Helper()
	content, err := domain.ParseContent("A AGI existirá até 2040", runeCounter)
	if err != nil {
		t.Fatalf("ParseContent: %v", err)
	}
	stored := &application.PublishedArgument{
		ID:        mustArgumentID(t, "018f6b2a-0000-7000-8000-0000000000ff"),
		ArenaID:   mustArena(t, testArenaRaw),
		AuthorID:  mustAccount(t, testAccountRaw),
		Relation:  mustRelation(t, domain.RelationSupport),
		Content:   content,
		Status:    status,
		CreatedAt: testInstant,
	}
	repo.seed(t, stored, "attempt-1")
	return stored
}

func withdrawCommand() application.WithdrawArgumentCommand {
	return application.WithdrawArgumentCommand{
		AccountID:  testAccountRaw,
		ArgumentID: "018f6b2a-0000-7000-8000-0000000000ff",
	}
}

func TestWithdrawArgumentRetractsForTheAuthor(t *testing.T) {
	repo := newFakeArgumentRepo()
	seedPublishedArgument(t, repo, "published")
	useCase := application.NewWithdrawArgumentUseCase(repo, fixedClock{now: testInstant.Add(time.Hour)})

	result, err := useCase.Execute(context.Background(), withdrawCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed || result.Argument.Status != "withdrawn" {
		t.Fatalf("result = %+v, want a fresh withdrawal", result)
	}
	if result.Argument.WithdrawnAt == nil || !result.Argument.WithdrawnAt.Equal(testInstant.Add(time.Hour)) {
		t.Fatalf("withdrawn_at = %v, want the injected clock instant", result.Argument.WithdrawnAt)
	}
	if result.Argument.Content.GraphemeCost() != 23 {
		t.Fatal("withdrawal must preserve the historical content")
	}

	// The repeat resolves the recorded withdrawal with its original
	// instant: the action is idempotent and auditable.
	retry, err := useCase.Execute(context.Background(), withdrawCommand())
	if err != nil {
		t.Fatalf("retry Execute() error = %v", err)
	}
	if !retry.Replayed || retry.Argument.Status != "withdrawn" {
		t.Fatalf("retry = %+v, want a replay", retry)
	}
	if retry.Argument.WithdrawnAt == nil || !retry.Argument.WithdrawnAt.Equal(testInstant.Add(time.Hour)) {
		t.Fatalf("retry withdrawn_at = %v, want the first recorded instant", retry.Argument.WithdrawnAt)
	}
}

func TestWithdrawArgumentIsOwnerScoped(t *testing.T) {
	repo := newFakeArgumentRepo()
	seedPublishedArgument(t, repo, "published")
	useCase := application.NewWithdrawArgumentUseCase(repo, fixedClock{now: testInstant})

	command := withdrawCommand()
	command.AccountID = "018f6b2a-0000-7000-8000-0000000000aa"
	if _, err := useCase.Execute(context.Background(), command); !errors.Is(err, application.ErrArgumentNotFound) {
		t.Fatalf("error = %v, want ErrArgumentNotFound for a foreign author", err)
	}

	stored, err := repo.GetForAuthor(context.Background(), mustArgumentID(t, command.ArgumentID), mustAccount(t, testAccountRaw))
	if err != nil {
		t.Fatalf("read stored: %v", err)
	}
	if stored.Status != "published" {
		t.Fatal("a foreign withdrawal must not change the argument")
	}
}

func TestWithdrawArgumentNeverOverridesModeration(t *testing.T) {
	repo := newFakeArgumentRepo()
	seedPublishedArgument(t, repo, "removed")
	useCase := application.NewWithdrawArgumentUseCase(repo, fixedClock{now: testInstant})

	if _, err := useCase.Execute(context.Background(), withdrawCommand()); !errors.Is(err, application.ErrArgumentNotWithdrawable) {
		t.Fatalf("error = %v, want ErrArgumentNotWithdrawable", err)
	}

	stored, err := repo.GetForAuthor(context.Background(), mustArgumentID(t, withdrawCommand().ArgumentID), mustAccount(t, testAccountRaw))
	if err != nil {
		t.Fatalf("read stored: %v", err)
	}
	if stored.Status != "removed" {
		t.Fatal("a removed argument must stay removed")
	}
}

func TestWithdrawArgumentResolvesConcurrentStatusMoves(t *testing.T) {
	t.Run("concurrent withdrawal replays", func(t *testing.T) {
		repo := newFakeArgumentRepo()
		seedPublishedArgument(t, repo, "withdrawn")
		instant := testInstant
		repo.mu.Lock()
		stored := repo.arguments[argumentKey(mustAccount(t, testAccountRaw), mustKey(t, "attempt-1"))]
		stored.WithdrawnAt = &instant
		repo.firstGetStatus = "published"
		repo.withdrawNotTransitioned = true
		repo.mu.Unlock()

		useCase := application.NewWithdrawArgumentUseCase(repo, fixedClock{now: testInstant.Add(time.Hour)})
		result, err := useCase.Execute(context.Background(), withdrawCommand())
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if !result.Replayed || result.Argument.WithdrawnAt == nil || !result.Argument.WithdrawnAt.Equal(testInstant) {
			t.Fatalf("result = %+v, want the concurrent withdrawal resolved as a replay", result)
		}
	})

	t.Run("concurrent removal refuses", func(t *testing.T) {
		repo := newFakeArgumentRepo()
		seedPublishedArgument(t, repo, "removed")
		repo.mu.Lock()
		repo.firstGetStatus = "published"
		repo.withdrawNotTransitioned = true
		repo.mu.Unlock()

		useCase := application.NewWithdrawArgumentUseCase(repo, fixedClock{now: testInstant})
		if _, err := useCase.Execute(context.Background(), withdrawCommand()); !errors.Is(err, application.ErrArgumentNotWithdrawable) {
			t.Fatalf("error = %v, want ErrArgumentNotWithdrawable", err)
		}
	})
}

func TestWithdrawArgumentValidatesInputsWithoutWriting(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(command *application.WithdrawArgumentCommand)
		want   error
	}{
		{name: "empty account", mutate: func(c *application.WithdrawArgumentCommand) { c.AccountID = "" }, want: domain.ErrEmptyAccountID},
		{name: "empty argument", mutate: func(c *application.WithdrawArgumentCommand) { c.ArgumentID = "" }, want: domain.ErrEmptyArgumentID},
		{name: "invalid argument", mutate: func(c *application.WithdrawArgumentCommand) { c.ArgumentID = "with space" }, want: domain.ErrInvalidArgumentID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newFakeArgumentRepo()
			seedPublishedArgument(t, repo, "published")
			useCase := application.NewWithdrawArgumentUseCase(repo, fixedClock{now: testInstant})

			command := withdrawCommand()
			test.mutate(&command)
			if _, err := useCase.Execute(context.Background(), command); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}

			stored, err := repo.GetForAuthor(context.Background(), mustArgumentID(t, withdrawCommand().ArgumentID), mustAccount(t, testAccountRaw))
			if err != nil {
				t.Fatalf("read stored: %v", err)
			}
			if stored.Status != "published" {
				t.Fatal("invalid input must not change the argument")
			}
		})
	}
}
