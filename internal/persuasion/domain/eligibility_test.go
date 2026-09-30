package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

var testInstant = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

func mustChange(t *testing.T) domain.Change {
	t.Helper()
	changeID, err := domain.ParseChangeID("018f6b2a-0000-7000-8000-000000000001")
	if err != nil {
		t.Fatalf("ParseChangeID: %v", err)
	}
	arenaID, err := domain.ParseArenaID("018f6b2a-0000-7000-8000-000000000002")
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	attributorID, err := domain.ParseAttributorID("018f6b2a-0000-7000-8000-000000000003")
	if err != nil {
		t.Fatalf("ParseAttributorID: %v", err)
	}
	return domain.Change{ID: changeID, ArenaID: arenaID, AttributorID: attributorID, ChangedAt: testInstant}
}

func mustCandidate(t *testing.T, id string, mutate func(candidate *domain.Candidate)) domain.Candidate {
	t.Helper()
	argumentID, err := domain.ParseArgumentID(id)
	if err != nil {
		t.Fatalf("ParseArgumentID(%q): %v", id, err)
	}
	authorID, err := domain.ParseAuthorID("018f6b2a-0000-7000-8000-0000000000aa")
	if err != nil {
		t.Fatalf("ParseAuthorID: %v", err)
	}
	candidate := domain.Candidate{
		ID:        argumentID,
		ArenaID:   mustChange(t).ArenaID,
		AuthorID:  authorID,
		CreatedAt: testInstant.Add(-time.Hour),
		Status:    domain.ArgumentStatusPublished,
	}
	if mutate != nil {
		mutate(&candidate)
	}
	return candidate
}

func TestEligibilityPolicyAcceptsEligibleSelections(t *testing.T) {
	policy := domain.DefaultEligibilityPolicy()
	if !policy.IsValid() || policy.MaxAttributions != 3 {
		t.Fatalf("default policy = %+v, want at most three", policy)
	}
	change := mustChange(t)

	selections := map[string][]domain.Candidate{
		"empty selection": nil,
		"one argument":    {mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", nil)},
		"three arguments": {
			mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", nil),
			mustCandidate(t, "018f6b2a-0000-7000-8000-000000000011", nil),
			mustCandidate(t, "018f6b2a-0000-7000-8000-000000000012", nil),
		},
		"relation never restricts": {
			mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", func(c *domain.Candidate) {
				// Relation is deliberately not part of the candidate: any
				// declared relation stays eligible (BUSINESS_RULES §5).
				c.Status = domain.ArgumentStatusPublished
			}),
		},
	}
	for name, selection := range selections {
		t.Run(name, func(t *testing.T) {
			if err := policy.Validate(change, selection); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestEligibilityPolicyRejectsViolations(t *testing.T) {
	policy := domain.DefaultEligibilityPolicy()
	change := mustChange(t)
	otherArena, err := domain.ParseArenaID("018f6b2a-0000-7000-8000-000000000099")
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	attributor, err := domain.ParseAuthorID(change.AttributorID.String())
	if err != nil {
		t.Fatalf("ParseAuthorID: %v", err)
	}

	tests := []struct {
		name      string
		selection []domain.Candidate
		want      error
	}{
		{
			name: "four arguments",
			selection: []domain.Candidate{
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", nil),
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000011", nil),
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000012", nil),
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000013", nil),
			},
			want: domain.ErrTooManyAttributions,
		},
		{
			name: "duplicate argument",
			selection: []domain.Candidate{
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", nil),
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", nil),
			},
			want: domain.ErrDuplicateAttribution,
		},
		{
			name: "cross arena argument",
			selection: []domain.Candidate{
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", func(c *domain.Candidate) { c.ArenaID = otherArena }),
			},
			want: domain.ErrCrossArenaArgument,
		},
		{
			name: "self attribution",
			selection: []domain.Candidate{
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", func(c *domain.Candidate) { c.AuthorID = attributor }),
			},
			want: domain.ErrSelfAttribution,
		},
		{
			name: "argument after the change",
			selection: []domain.Candidate{
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", func(c *domain.Candidate) { c.CreatedAt = testInstant.Add(time.Minute) }),
			},
			want: domain.ErrArgumentNotBeforeChange,
		},
		{
			name: "argument at the same instant",
			selection: []domain.Candidate{
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", func(c *domain.Candidate) { c.CreatedAt = testInstant }),
			},
			want: domain.ErrArgumentNotBeforeChange,
		},
		{
			name: "withdrawn argument",
			selection: []domain.Candidate{
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", func(c *domain.Candidate) { c.Status = domain.ArgumentStatusWithdrawn }),
			},
			want: domain.ErrArgumentNotEligible,
		},
		{
			name: "removed argument",
			selection: []domain.Candidate{
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", func(c *domain.Candidate) { c.Status = domain.ArgumentStatusRemoved }),
			},
			want: domain.ErrArgumentNotEligible,
		},
		{
			name: "unknown status",
			selection: []domain.Candidate{
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", func(c *domain.Candidate) { c.Status = "maybe" }),
			},
			want: domain.ErrInvalidStatus,
		},
		{
			name: "missing argument id",
			selection: []domain.Candidate{
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", func(c *domain.Candidate) { c.ID = domain.ArgumentID{} }),
			},
			want: domain.ErrEmptyArgumentID,
		},
		{
			name: "missing author",
			selection: []domain.Candidate{
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", func(c *domain.Candidate) { c.AuthorID = domain.AuthorID{} }),
			},
			want: domain.ErrEmptyAuthorID,
		},
		{
			name: "missing creation instant",
			selection: []domain.Candidate{
				mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", func(c *domain.Candidate) { c.CreatedAt = time.Time{} }),
			},
			want: domain.ErrInvalidInstant,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := policy.Validate(change, test.selection); !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestEligibilityPolicyValidatesChangeAndPolicy(t *testing.T) {
	policy := domain.DefaultEligibilityPolicy()
	candidate := mustCandidate(t, "018f6b2a-0000-7000-8000-000000000010", nil)

	t.Run("invalid policy", func(t *testing.T) {
		invalid := domain.EligibilityPolicy{Version: "", MaxAttributions: 3}
		if err := invalid.Validate(mustChange(t), []domain.Candidate{candidate}); !errors.Is(err, domain.ErrInvalidPolicy) {
			t.Fatalf("error = %v, want ErrInvalidPolicy", err)
		}
		if (domain.EligibilityPolicy{Version: "2026-09", MaxAttributions: 0}).IsValid() {
			t.Fatal("zero limit must be invalid")
		}
	})

	t.Run("invalid change", func(t *testing.T) {
		broken := mustChange(t)
		broken.ID = domain.ChangeID{}
		if err := policy.Validate(broken, nil); !errors.Is(err, domain.ErrEmptyChangeID) {
			t.Fatalf("error = %v, want ErrEmptyChangeID", err)
		}
		broken = mustChange(t)
		broken.ChangedAt = time.Time{}
		if err := policy.Validate(broken, nil); !errors.Is(err, domain.ErrInvalidInstant) {
			t.Fatalf("error = %v, want ErrInvalidInstant", err)
		}
	})
}
