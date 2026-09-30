package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

const testAccountID = "018f6b2a-0000-7000-8000-000000000010"

var testClockInstant = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

type fakePassLotRepo struct {
	requests []application.GrantPassLotRequest
	result   *application.GrantPassLotResult
	err      error
}

func (r *fakePassLotRepo) GrantPassLot(_ context.Context, request application.GrantPassLotRequest) (*application.GrantPassLotResult, error) {
	r.requests = append(r.requests, request)
	if r.err != nil {
		return nil, r.err
	}
	return r.result, nil
}

func validGrantCommand() application.GrantArenaPassesCommand {
	return application.GrantArenaPassesCommand{
		AccountID: testAccountID,
		Origin:    "PURCHASE",
		Quantity:  5,
		Reference: "stripe:evt_purchase_1",
	}
}

func TestGrantArenaPassesUseCaseValidatesInput(t *testing.T) {
	expiry := testClockInstant.Add(30 * 24 * time.Hour)

	tests := []struct {
		name   string
		mutate func(*application.GrantArenaPassesCommand)
		want   error
	}{
		{name: "empty account", mutate: func(cmd *application.GrantArenaPassesCommand) { cmd.AccountID = "" }, want: domain.ErrEmptyAccountID},
		{name: "invalid origin", mutate: func(cmd *application.GrantArenaPassesCommand) { cmd.Origin = "GIFT" }, want: domain.ErrInvalidPassOrigin},
		{name: "zero quantity", mutate: func(cmd *application.GrantArenaPassesCommand) { cmd.Quantity = 0 }, want: domain.ErrInvalidQuantity},
		{name: "negative quantity", mutate: func(cmd *application.GrantArenaPassesCommand) { cmd.Quantity = -1 }, want: domain.ErrInvalidQuantity},
		{name: "empty reference", mutate: func(cmd *application.GrantArenaPassesCommand) { cmd.Reference = "  " }, want: domain.ErrEmptyReference},
		{name: "purchases do not expire", mutate: func(cmd *application.GrantArenaPassesCommand) { cmd.ExpiresAt = &expiry }, want: domain.ErrExpirationForbidden},
		{name: "member requires expiration", mutate: func(cmd *application.GrantArenaPassesCommand) { cmd.Origin = "MEMBER" }, want: domain.ErrExpirationRequired},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakePassLotRepo{}
			useCase := application.NewGrantArenaPassesUseCase(repo, &fakeClock{now: testClockInstant})

			command := validGrantCommand()
			tc.mutate(&command)

			result, err := useCase.Execute(context.Background(), command)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Execute() error = %v, want %v", err, tc.want)
			}
			if result != nil {
				t.Fatal("validation failure must return no result")
			}
			if len(repo.requests) != 0 {
				t.Fatalf("repository was called %d times on validation error", len(repo.requests))
			}
		})
	}
}

func TestGrantArenaPassesUseCaseBuildsValidatedRequest(t *testing.T) {
	quantity, err := domain.NewQuantity(5)
	if err != nil {
		t.Fatalf("NewQuantity: %v", err)
	}
	reference, err := domain.ParseReference("stripe:evt_purchase_1")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	lot, err := domain.ReconstitutePassLot(
		domain.LotID("lot-1"),
		domain.AccountID(testAccountID),
		domain.OriginPurchase,
		quantity,
		5,
		nil,
		reference,
		testClockInstant,
	)
	if err != nil {
		t.Fatalf("ReconstitutePassLot: %v", err)
	}

	repo := &fakePassLotRepo{result: &application.GrantPassLotResult{Lot: *lot}}
	useCase := application.NewGrantArenaPassesUseCase(repo, &fakeClock{now: testClockInstant})

	result, err := useCase.Execute(context.Background(), validGrantCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed || result.Lot.ID().String() != "lot-1" {
		t.Fatalf("result = %+v", result)
	}
	if len(repo.requests) != 1 {
		t.Fatalf("repository calls = %d, want 1", len(repo.requests))
	}

	request := repo.requests[0]
	if request.AccountID.String() != testAccountID || request.Origin != domain.OriginPurchase {
		t.Errorf("request = %s/%s", request.AccountID, request.Origin)
	}
	if request.Quantity.Int32() != 5 || request.Reference.String() != "stripe:evt_purchase_1" {
		t.Errorf("request quantity/reference = %d/%q", request.Quantity.Int32(), request.Reference)
	}
	if request.ExpiresAt != nil {
		t.Errorf("purchase request must not carry an expiration, got %v", request.ExpiresAt)
	}
	if !request.GrantedAt.Equal(testClockInstant) {
		t.Errorf("GrantedAt = %v, want %v", request.GrantedAt, testClockInstant)
	}
}

func TestGrantArenaPassesUseCaseExpirationRules(t *testing.T) {
	memberEnd := time.Date(2026, 10, 1, 0, 0, 0, 0, time.FixedZone("BRT", -3*3600))

	repo := &fakePassLotRepo{}
	useCase := application.NewGrantArenaPassesUseCase(repo, &fakeClock{now: testClockInstant})

	command := validGrantCommand()
	command.Origin = "MEMBER"
	command.Reference = "member:2026-09"
	command.ExpiresAt = &memberEnd

	if _, err := useCase.Execute(context.Background(), command); err != nil {
		t.Fatalf("member grant error = %v", err)
	}
	memberRequest := repo.requests[0]
	if memberRequest.ExpiresAt == nil {
		t.Fatal("member request must carry the period end")
	}
	if memberRequest.ExpiresAt.Location() != time.UTC {
		t.Errorf("expiration location = %v, want UTC", memberRequest.ExpiresAt.Location())
	}
	if !memberRequest.ExpiresAt.Equal(memberEnd) {
		t.Errorf("expiration instant drifted: %v vs %v", memberRequest.ExpiresAt, memberEnd)
	}

	// Admin grants may or may not expire.
	adminWithExpiry := validGrantCommand()
	adminWithExpiry.Origin = "ADMIN"
	adminWithExpiry.Reference = "admin:ticket-1"
	adminWithExpiry.ExpiresAt = &memberEnd
	if _, err := useCase.Execute(context.Background(), adminWithExpiry); err != nil {
		t.Fatalf("admin grant with expiry error = %v", err)
	}
	if repo.requests[1].ExpiresAt == nil {
		t.Fatal("admin request must keep the informed expiration")
	}

	adminNoExpiry := validGrantCommand()
	adminNoExpiry.Origin = "ADMIN"
	adminNoExpiry.Reference = "admin:ticket-2"
	if _, err := useCase.Execute(context.Background(), adminNoExpiry); err != nil {
		t.Fatalf("admin grant without expiry error = %v", err)
	}
	if repo.requests[2].ExpiresAt != nil {
		t.Fatal("admin request must accept a missing expiration")
	}
}

func TestGrantArenaPassesUseCasePropagatesReplayAndErrors(t *testing.T) {
	quantity, _ := domain.NewQuantity(1)
	reference, _ := domain.ParseReference("stripe:evt_replay")
	lot, err := domain.ReconstitutePassLot(
		domain.LotID("lot-replay"),
		domain.AccountID(testAccountID),
		domain.OriginPurchase,
		quantity,
		1,
		nil,
		reference,
		testClockInstant,
	)
	if err != nil {
		t.Fatalf("ReconstitutePassLot: %v", err)
	}

	repo := &fakePassLotRepo{result: &application.GrantPassLotResult{Lot: *lot, Replayed: true}}
	useCase := application.NewGrantArenaPassesUseCase(repo, &fakeClock{now: testClockInstant})

	result, err := useCase.Execute(context.Background(), validGrantCommand())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Replayed || result.Lot.ID().String() != "lot-replay" {
		t.Fatalf("result = %+v, want the replayed original lot", result)
	}

	repo.err = errors.New("storage unavailable")
	if _, err := useCase.Execute(context.Background(), validGrantCommand()); !errors.Is(err, repo.err) {
		t.Fatalf("repository error not propagated: %v", err)
	}
}
