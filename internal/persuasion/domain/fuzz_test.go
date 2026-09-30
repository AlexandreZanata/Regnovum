package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

// fuzzCandidate builds one candidate from a deterministic token: the fuzz
// corpus can repeat tokens to create duplicate sets, and unknown tokens
// become fresh eligible arguments.
func fuzzCandidate(t *testing.T, change domain.Change, token string, index int) domain.Candidate {
	t.Helper()
	id, err := domain.ParseArgumentID("argument-" + token)
	if err != nil {
		t.Fatalf("ParseArgumentID(%q): %v", token, err)
	}
	authorID, err := domain.ParseAuthorID("author-" + token)
	if err != nil {
		t.Fatalf("ParseAuthorID(%q): %v", token, err)
	}
	candidate := domain.Candidate{
		ID:        id,
		ArenaID:   change.ArenaID,
		AuthorID:  authorID,
		CreatedAt: change.ChangedAt.Add(-time.Duration(index+1) * time.Minute),
		Status:    domain.ArgumentStatusPublished,
	}
	switch token {
	case "self":
		candidate.AuthorID, _ = domain.ParseAuthorID(change.AttributorID.String())
	case "cross":
		candidate.ArenaID, _ = domain.ParseArenaID("arena-cross")
	case "late":
		candidate.CreatedAt = change.ChangedAt.Add(time.Minute)
	case "same":
		candidate.CreatedAt = change.ChangedAt
	case "withdrawn":
		candidate.Status = domain.ArgumentStatusWithdrawn
	case "removed":
		candidate.Status = domain.ArgumentStatusRemoved
	}
	return candidate
}

// FuzzEligibilitySelection is the P11-T02 fuzz proof over duplicate sets:
// arbitrary token lists must never panic, and every accepted selection must
// be free of duplicates, within the limit and made only of eligible tokens.
func FuzzEligibilitySelection(f *testing.F) {
	f.Add("")
	f.Add("alpha")
	f.Add("alpha,alpha")
	f.Add("alpha,beta,gamma")
	f.Add("alpha,beta,gamma,delta")
	f.Add("self,cross,late")
	f.Add("withdrawn,removed")
	f.Add("same")
	f.Add("alpha,alpha,alpha,alpha")

	f.Fuzz(func(t *testing.T, spec string) {
		policy := domain.DefaultEligibilityPolicy()
		change := mustChange(t)

		tokens := []string{}
		for _, token := range strings.Split(spec, ",") {
			// Tokens must be printable ASCII to form valid opaque ids; the
			// fuzz target skips anything else instead of failing on inputs
			// the use cases could never build.
			token = strings.Map(func(r rune) rune {
				if r >= 0x21 && r <= 0x7e {
					return r
				}
				return -1
			}, strings.TrimSpace(token))
			if token == "" {
				continue
			}
			if len(token) > 40 {
				token = token[:40]
			}
			tokens = append(tokens, token)
		}

		selection := make([]domain.Candidate, 0, len(tokens))
		for index, token := range tokens {
			selection = append(selection, fuzzCandidate(t, change, token, index))
		}

		err := policy.Validate(change, selection)
		if err != nil {
			return
		}

		// Accepted selections must respect every rule; duplicates and
		// ineligible tokens can never slip through.
		if len(selection) > policy.MaxAttributions {
			t.Fatalf("accepted %d candidates above the limit", len(selection))
		}
		seen := map[string]bool{}
		for _, candidate := range selection {
			if seen[candidate.ID.String()] {
				t.Fatalf("accepted duplicate argument %q", candidate.ID.String())
			}
			seen[candidate.ID.String()] = true
			if !candidate.ArenaID.Equals(change.ArenaID) {
				t.Fatalf("accepted cross-arena argument %q", candidate.ID.String())
			}
			if candidate.AuthorID.String() == change.AttributorID.String() {
				t.Fatalf("accepted self attribution %q", candidate.ID.String())
			}
			if !candidate.CreatedAt.Before(change.ChangedAt) {
				t.Fatalf("accepted argument not published before the change %q", candidate.ID.String())
			}
			if candidate.Status != domain.ArgumentStatusPublished {
				t.Fatalf("accepted ineligible status %q", candidate.Status.String())
			}
		}
	})
}
