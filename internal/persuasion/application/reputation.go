package application

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

// refusal wraps one projection defect with the stable error code.
func refusal(detail string) error {
	return fmt.Errorf("%w: %s", ErrInvalidReputationProjection, detail)
}

// ArenaReputation is one Arena slice of an author reputation projection: the
// eligible people the author influenced there and the valid attributions the
// author received there. It carries counts only — the attributor identity is
// private by default (BR §5.1) and never appears in any projection.
type ArenaReputation struct {
	ArenaID domain.ArenaID
	// Category is the editorial category slug of the Arena; it is the
	// category dimension of the public distribution.
	Category string
	// Language is the content language of the Arena; it is the language
	// dimension of the public distribution.
	Language string
	// DistinctPeople counts eligible attributors once per author and Arena
	// (BR §6): repeated attributions by the same person in the same Arena
	// never inflate the headline.
	DistinctPeople int64
	// ValidAttributions counts valid attribution events, so detailed counts
	// can show the additional events separately (BR §6).
	ValidAttributions int64
}

// DimensionReputation is one bucket of a reputation distribution: the label
// of the dimension value plus the same two factual counts.
type DimensionReputation struct {
	Label             string
	DistinctPeople    int64
	ValidAttributions int64
}

// AuthorReputation is the derived reputation of one author: the per-Arena
// facts, the derivation instant and nothing else. No attributor, account or
// payment identifier is part of it by construction, so it is safe to publish.
type AuthorReputation struct {
	AuthorID domain.AuthorID
	// Username is the canonical public handle of the author, set when the
	// query is addressed by username (P11-T06). It is the identity public
	// documents carry; the internal author identifier never leaves the
	// module.
	Username  string
	Arenas    []ArenaReputation
	CheckedAt time.Time
}

// InfluencedPeople returns the headline "people influenced" (BR §6): each
// participant counts at most once per author in each Arena, so the headline
// is the sum of the per-Arena distinct counts — never a global distinct
// count, which would let one person count only once across Arenas.
func (r AuthorReputation) InfluencedPeople() int64 {
	var total int64
	for _, arena := range r.Arenas {
		total += arena.DistinctPeople
	}
	return total
}

// TotalValidAttributions returns the valid attributions the author received,
// including the additional events of a person already counted by the
// headline.
func (r AuthorReputation) TotalValidAttributions() int64 {
	var total int64
	for _, arena := range r.Arenas {
		total += arena.ValidAttributions
	}
	return total
}

// ByCategory aggregates the per-Arena facts by Arena category, ordered by
// label. A person attributed in two Arenas of the same category still counts
// once per Arena, as the headline rule requires.
func (r AuthorReputation) ByCategory() []DimensionReputation {
	return aggregateDimension(r.Arenas, func(arena ArenaReputation) string { return arena.Category })
}

// ByLanguage aggregates the per-Arena facts by Arena content language,
// ordered by label, under the same per-Arena counting rule.
func (r AuthorReputation) ByLanguage() []DimensionReputation {
	return aggregateDimension(r.Arenas, func(arena ArenaReputation) string { return arena.Language })
}

// aggregateDimension folds the Arena slices into deterministic buckets.
func aggregateDimension(arenas []ArenaReputation, label func(ArenaReputation) string) []DimensionReputation {
	buckets := make(map[string]DimensionReputation, len(arenas))
	for _, arena := range arenas {
		key := label(arena)
		bucket := buckets[key]
		bucket.Label = key
		bucket.DistinctPeople += arena.DistinctPeople
		bucket.ValidAttributions += arena.ValidAttributions
		buckets[key] = bucket
	}

	distribution := make([]DimensionReputation, 0, len(buckets))
	for _, bucket := range buckets {
		distribution = append(distribution, bucket)
	}
	sort.Slice(distribution, func(i, j int) bool { return distribution[i].Label < distribution[j].Label })
	return distribution
}

// Validate checks the coherence of the projection before it leaves the
// module: a malformed projection silently inflating the headline is refused
// instead of published. The rule that a repeated Arena, a missing dimension
// label or a negative count is a defect holds for every adapter.
func (r AuthorReputation) Validate() error {
	if r.AuthorID.IsZero() {
		return refusal("author identifier is missing")
	}
	if r.CheckedAt.IsZero() {
		return refusal("derivation instant is missing")
	}

	seen := make(map[string]bool, len(r.Arenas))
	for _, arena := range r.Arenas {
		if arena.ArenaID.IsZero() {
			return refusal("arena identifier is missing")
		}
		if seen[arena.ArenaID.String()] {
			return refusal("arena " + arena.ArenaID.String() + " appears more than once")
		}
		seen[arena.ArenaID.String()] = true

		if strings.TrimSpace(arena.Category) == "" {
			return refusal("arena " + arena.ArenaID.String() + " has no category")
		}
		if strings.TrimSpace(arena.Language) == "" {
			return refusal("arena " + arena.ArenaID.String() + " has no language")
		}
		if arena.DistinctPeople < 0 || arena.ValidAttributions < 0 {
			return refusal("arena " + arena.ArenaID.String() + " carries a negative count")
		}
		if arena.DistinctPeople > arena.ValidAttributions {
			return refusal("arena " + arena.ArenaID.String() + " counts more people than events")
		}
	}
	return nil
}

// AuthorReputationRepository derives the reputation projection of one
// author. Counts only cross the port: attributor identifiers never leave the
// database.
type AuthorReputationRepository interface {
	// ListAuthorArenaReputation returns one row per Arena where the author
	// received at least one valid attribution, counting eligible attributors
	// (active accounts with a verified email, per docs/BUSINESS_RULES.md §7)
	// once per Arena. An author without valid attributions returns no rows;
	// an identifier that cannot address an account reports ErrInvalidAuthorID.
	ListAuthorArenaReputation(ctx context.Context, authorID domain.AuthorID) ([]ArenaReputation, error)
}
