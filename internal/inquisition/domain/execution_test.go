package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func executionAnchor() time.Time {
	return time.Date(2026, time.February, 7, 10, 0, 0, 0, time.UTC)
}

func executionProof() string {
	return strings.Repeat("ab", 32)
}

func executionSentence() Sentence {
	return Sentence{
		ID: "sentenca-1", Case: "caso-1", Accused: "ana",
		Sanction:   SanctionRestriction,
		Competence: "inquisicao-severa: fraude contra a Carta",
		Charter:    "v2", FactDigest: executionProof(),
		Motive:  "fraude provada pelo selo, restricao temporaria proporcional",
		Decider: "arbitro-1", DecidedAt: executionAnchor(),
	}
}

func authorizeExecution(id string) Execution {
	exec, err := AuthorizeExecution(AuthorizeRequest{
		ID: ExecutionID(id), Sentence: executionSentence(),
		Authority: "inquisidor-1", At: executionAnchor().Add(time.Hour),
		Window: 48 * time.Hour, MaxWindow: 72 * time.Hour,
	})
	if err != nil {
		panic(err)
	}
	return exec
}

func grantExecutionPardon(id string) Pardon {
	pardon, err := GrantPardon(PardonRequest{
		ID: PardonID(id), Sentence: executionSentence(),
		Granter: "rei-1", Detail: "perdao real anterior ao ato, so a execucao",
		At: executionAnchor().Add(2 * time.Hour),
	})
	if err != nil {
		panic(err)
	}
	return pardon
}

func TestAuthorizedExecutionPerformsOnce(t *testing.T) {
	pending := authorizeExecution("execucao-1")
	if !pending.Deadline.Equal(pending.AuthorizedAt.Add(48 * time.Hour)) {
		t.Fatalf("execution = %+v, want the bounded window", pending)
	}
	done, err := PerformExecution(PerformRequest{
		Execution: pending, Executor: "carrasco-1",
		Office: OfficeCarrasco, At: executionAnchor().Add(3 * time.Hour),
	})
	if err != nil {
		t.Fatalf("PerformExecution: %v", err)
	}
	if !done.Executed || done.Lapsed || done.Pardoned {
		t.Fatalf("execution = %+v, want a single executed state", done)
	}
	if done.Executor != "carrasco-1" {
		t.Fatalf("execution = %+v, want the named carrasco", done)
	}
	retry, err := PerformExecution(PerformRequest{
		Execution: done, Executor: "carrasco-1",
		Office: OfficeCarrasco, At: done.ExecutedAt,
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !retry.Executed || retry.Executor != "carrasco-1" {
		t.Fatalf("retry = %+v, want the same single state", retry)
	}
}

func TestExecutionWindowLimitAndExactTick(t *testing.T) {
	pending := authorizeExecution("execucao-2")
	edge, err := PerformExecution(PerformRequest{
		Execution: pending, Executor: "carrasco-1",
		Office: OfficeCarrasco, At: pending.Deadline.Add(-time.Nanosecond),
	})
	if err != nil {
		t.Fatalf("one nanosecond before the deadline: %v", err)
	}
	if !edge.Executed {
		t.Fatalf("edge = %+v, want the act inside the window", edge)
	}
	fresh := authorizeExecution("execucao-3")
	if _, err := PerformExecution(PerformRequest{
		Execution: fresh, Executor: "carrasco-1",
		Office: OfficeCarrasco, At: fresh.Deadline,
	}); !errors.Is(err, ErrUntimelyExecution) {
		t.Fatalf("act at the exact tick = %v, want ErrUntimelyExecution", err)
	}
	if _, err := PerformExecution(PerformRequest{
		Execution: fresh, Executor: "carrasco-1",
		Office: OfficeCarrasco, At: fresh.Deadline.Add(time.Hour),
	}); !errors.Is(err, ErrUntimelyExecution) {
		t.Fatalf("late act = %v, want ErrUntimelyExecution", err)
	}
	if _, err := PerformExecution(PerformRequest{
		Execution: fresh, Executor: "carrasco-1",
		Office: OfficeCarrasco, At: fresh.AuthorizedAt.Add(-time.Nanosecond),
	}); !errors.Is(err, ErrUntimelyExecution) {
		t.Fatalf("early act = %v, want ErrUntimelyExecution", err)
	}
	overcap := AuthorizeRequest{
		ID: "execucao-4", Sentence: executionSentence(),
		Authority: "inquisidor-1", At: executionAnchor().Add(time.Hour),
		Window: 73 * time.Hour, MaxWindow: 72 * time.Hour,
	}
	if _, err := AuthorizeExecution(overcap); !errors.Is(err, ErrUntimelyExecution) {
		t.Fatalf("overcap window = %v, want ErrUntimelyExecution", err)
	}
}

func TestTwoExecutionersReachSingleState(t *testing.T) {
	pending := authorizeExecution("execucao-5")
	done, err := PerformExecution(PerformRequest{
		Execution: pending, Executor: "carrasco-1",
		Office: OfficeCarrasco, At: executionAnchor().Add(3 * time.Hour),
	})
	if err != nil {
		t.Fatalf("first carrasco: %v", err)
	}
	rival := PerformRequest{
		Execution: done, Executor: "carrasco-2",
		Office: OfficeCarrasco, At: executionAnchor().Add(4 * time.Hour),
	}
	if _, err := PerformExecution(rival); !errors.Is(err, ErrDuplicateExecution) {
		t.Fatalf("second carrasco = %v, want ErrDuplicateExecution", err)
	}
	if done.Executor != "carrasco-1" || !done.Executed {
		t.Fatalf("execution = %+v, want the first carrasco standing alone", done)
	}
	stranger := authorizeExecution("execucao-6")
	if _, err := PerformExecution(PerformRequest{
		Execution: stranger, Executor: "ana",
		Office: OfficeCarrasco, At: executionAnchor().Add(3 * time.Hour),
	}); !errors.Is(err, ErrInterestedProsecution) {
		t.Fatalf("accused executing = %v, want ErrInterestedProsecution", err)
	}
	if _, err := PerformExecution(PerformRequest{
		Execution: stranger, Executor: "algoz-1",
		Office: "algoz", At: executionAnchor().Add(3 * time.Hour),
	}); !errors.Is(err, ErrMissingExecutioner) {
		t.Fatalf("non-carrasco office = %v, want ErrMissingExecutioner", err)
	}
}

func TestConcurrentPardonBlocksOnlyExecution(t *testing.T) {
	pending := authorizeExecution("execucao-7")
	pardon := grantExecutionPardon("perdao-1")
	blocked, err := ApplyPardon(pending, pardon)
	if err != nil {
		t.Fatalf("ApplyPardon: %v", err)
	}
	if !blocked.Pardoned || blocked.Executed || blocked.Lapsed {
		t.Fatalf("blocked = %+v, want pardon stopping only the execution", blocked)
	}
	if _, err := PerformExecution(PerformRequest{
		Execution: blocked, Executor: "carrasco-1",
		Office: OfficeCarrasco, At: executionAnchor().Add(3 * time.Hour),
	}); !errors.Is(err, ErrPardonedExecution) {
		t.Fatalf("pardoned act = %v, want ErrPardonedExecution", err)
	}
	replay, err := ApplyPardon(blocked, pardon)
	if err != nil {
		t.Fatalf("pardon replay: %v", err)
	}
	if !replay.Pardoned || replay.Pardon != "perdao-1" {
		t.Fatalf("replay = %+v, want the same blocked state", replay)
	}
	acted := authorizeExecution("execucao-8")
	done, err := PerformExecution(PerformRequest{
		Execution: acted, Executor: "carrasco-1",
		Office: OfficeCarrasco, At: executionAnchor().Add(3 * time.Hour),
	})
	if err != nil {
		t.Fatalf("act before pardon: %v", err)
	}
	late := grantExecutionPardon("perdao-2")
	if _, err := ApplyPardon(done, late); !errors.Is(err, ErrDuplicateExecution) {
		t.Fatalf("late pardon = %v, want ErrDuplicateExecution", err)
	}
	selfPardon := PardonRequest{
		ID: "perdao-3", Sentence: executionSentence(),
		Granter: "ana", Detail: "perdao em causa propria",
		At: executionAnchor().Add(2 * time.Hour),
	}
	if _, err := GrantPardon(selfPardon); !errors.Is(err, ErrInterestedProsecution) {
		t.Fatalf("self pardon = %v, want ErrInterestedProsecution", err)
	}
	deciderPardon := PardonRequest{
		ID: "perdao-4", Sentence: executionSentence(),
		Granter: "arbitro-1", Detail: "decisor perdoando a propria sentenca",
		At: executionAnchor().Add(2 * time.Hour),
	}
	if _, err := GrantPardon(deciderPardon); !errors.Is(err, ErrSelfReview) {
		t.Fatalf("decider pardon = %v, want ErrSelfReview", err)
	}
}

func TestExpiredWithoutCarrascoLapsesAndMovesNoValue(t *testing.T) {
	pending := authorizeExecution("execucao-9")
	if _, err := ExpireExecution(pending, pending.Deadline.Add(-time.Nanosecond)); !errors.Is(err, ErrUntimelyExecution) {
		t.Fatalf("early expiry = %v, want ErrUntimelyExecution", err)
	}
	lapsed, err := ExpireExecution(pending, pending.Deadline)
	if err != nil {
		t.Fatalf("ExpireExecution: %v", err)
	}
	if !lapsed.Lapsed || lapsed.Executed || lapsed.Pardoned {
		t.Fatalf("lapsed = %+v, want a lapse without execution", lapsed)
	}
	again, err := ExpireExecution(lapsed, pending.Deadline.Add(time.Hour))
	if err != nil {
		t.Fatalf("lapse retry: %v", err)
	}
	if !again.Lapsed || again.Executed {
		t.Fatalf("retry = %+v, want the same single lapsed state", again)
	}
	if _, err := PerformExecution(PerformRequest{
		Execution: lapsed, Executor: "carrasco-1",
		Office: OfficeCarrasco, At: pending.Deadline.Add(time.Hour),
	}); !errors.Is(err, ErrUntimelyExecution) {
		t.Fatalf("act after lapse = %v, want ErrUntimelyExecution", err)
	}
	banned := []string{"Amount", "Balance", "Ledger", "Treasury", "Money", "Price", "Mint", "Currency", "Payment", "Vault", "Wealth", "Tithe", "Fund", "Value"}
	for _, value := range []any{Execution{}, Pardon{}, AuthorizeRequest{}, PardonRequest{}, PerformRequest{}} {
		fields := []string{}
		for i := 0; i < reflect.TypeOf(value).NumField(); i++ {
			fields = append(fields, reflect.TypeOf(value).Field(i).Name)
		}
		for _, field := range fields {
			for _, deny := range banned {
				if strings.Contains(field, deny) {
					t.Fatalf("%T carries %s: execution moves no economic value", value, field)
				}
			}
		}
	}
}
