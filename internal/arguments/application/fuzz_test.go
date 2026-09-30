package application_test

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/application"
)

// FuzzPublishArgument is the P10-T09 fuzz smoke of the publication path:
// arbitrary content, relation and source values must never panic, and every
// outcome must respect the atomicity invariant — an accepted publication
// debits exactly the content cost, a rejected one writes nothing.
func FuzzPublishArgument(f *testing.F) {
	f.Add("A AGI existirá até 2040", "support", "https://example.com/estudo", "Estudo revisado")
	f.Add("Resposta curta 👩🏽‍🚀", "oppose", "http://example.com/x", "")
	f.Add("", "context", "javascript://alert(1)", strings.Repeat("d", 600))
	f.Add(strings.Repeat("a", 3001), "maybe", "not a url", "")
	f.Add("bandeira 🇧🇷 e acento cafe\u0301", "support", "HTTPS://Example.com/Estudo", "com espaço")
	f.Add("\u202Ereordenado", "context", "http://", "texto")

	f.Fuzz(func(t *testing.T, content, relation, sourceURL, sourceDescription string) {
		repo := newFakeArgumentRepo()
		wallet := newFakeInkDebit()
		uow := newFakeUnitOfWork(repo, wallet)
		useCase := newPublishUseCase(repo, &fakeAccountEligibility{}, &fakeArenaEligibility{}, wallet, uow)

		command := publishCommand()
		command.Content = content
		command.Relation = relation
		if sourceURL != "" || sourceDescription != "" {
			command.Sources = []application.SourceCommand{{URL: sourceURL, Description: sourceDescription}}
		}

		result, err := useCase.Execute(context.Background(), command)
		if err != nil {
			// A refused publication must leave no argument and no debit.
			if repo.argumentCount() != 0 {
				t.Fatalf("rejected publication wrote an argument: %v", err)
			}
			if wallet.requestCount() != 0 {
				t.Fatalf("rejected publication debited: %v", err)
			}
			return
		}

		// An accepted publication records exactly one argument and one debit
		// for the measured cost.
		if result.Replayed {
			t.Fatal("fresh fakes must never report a replay")
		}
		if repo.argumentCount() != 1 {
			t.Fatalf("accepted publication wrote %d arguments, want one", repo.argumentCount())
		}
		if wallet.requestCount() != 1 {
			t.Fatalf("accepted publication debited %d times, want one", wallet.requestCount())
		}

		wallet.mu.Lock()
		debit := wallet.requests[0]
		wallet.mu.Unlock()
		cost := int64(result.Argument.Content.GraphemeCost())
		if debit.Amount != cost {
			t.Fatalf("debit amount = %d, want the measured cost %d", debit.Amount, cost)
		}
		if debit.Amount < 1 || debit.Amount > 3000 {
			t.Fatalf("debit amount out of the product bounds: %d", debit.Amount)
		}
		if debit.Reference != "argument:attempt-1" || debit.IdempotencyKey != "attempt-1" {
			t.Fatalf("debit = %+v, want the attempt key reference", debit)
		}
		if result.Argument.Content.IsZero() || result.Argument.Content.Hash().IsZero() {
			t.Fatal("accepted content must carry its canonical hash")
		}
	})
}
