package application

import (
	"context"
	"sort"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

// RecordAttributionsCommand holds one attribution selection: zero to three
// argument identifiers chosen after a position change. An empty selection
// is a valid skip and never creates a fake entry.
type RecordAttributionsCommand struct {
	AccountID   string
	ChangeID    string
	ArgumentIDs []string
}

// RecordAttributionsResult is the recorded selection and whether the call
// resolved an existing recording instead of writing.
type RecordAttributionsResult struct {
	ArgumentIDs []domain.ArgumentID
	Replayed    bool
}

// RecordAttributionsUseCase records the influence attribution of one
// position change. It is owner-only (the change is loaded scoped to the
// account), transactional (the change row is locked FOR UPDATE, so the
// cumulative three-argument limit holds under concurrency) and idempotent
// per argument: retrying the same selection resolves the recorded set
// without duplicating rows. Selections grow only up to the policy limit:
// once the change holds attributions, adding more is accepted while the
// total stays within the limit.
type RecordAttributionsUseCase struct {
	attributions AttributionRepository
	policy       domain.EligibilityPolicy
	uow          UnitOfWork
}

// NewRecordAttributionsUseCase creates an instance of
// RecordAttributionsUseCase.
func NewRecordAttributionsUseCase(attributions AttributionRepository, policy domain.EligibilityPolicy, uow UnitOfWork) *RecordAttributionsUseCase {
	return &RecordAttributionsUseCase{
		attributions: attributions,
		policy:       policy,
		uow:          uow,
	}
}

// Execute records the selection.
func (uc *RecordAttributionsUseCase) Execute(ctx context.Context, cmd RecordAttributionsCommand) (*RecordAttributionsResult, error) {
	if !uc.policy.IsValid() {
		return nil, domain.ErrInvalidPolicy
	}

	attributorID, err := domain.ParseAttributorID(cmd.AccountID)
	if err != nil {
		return nil, err
	}
	changeID, err := domain.ParseChangeID(cmd.ChangeID)
	if err != nil {
		return nil, err
	}
	requested, err := parseArgumentIDs(cmd.ArgumentIDs)
	if err != nil {
		return nil, err
	}
	if len(requested) > uc.policy.MaxAttributions {
		return nil, domain.ErrTooManyAttributions
	}

	var result *RecordAttributionsResult
	err = uc.uow.WithinTransaction(ctx, func(txCtx context.Context) error {
		change, err := uc.attributions.LockChangeForAttributor(txCtx, changeID, attributorID)
		if err != nil {
			return err
		}
		existing, err := uc.attributions.ListAttributedArgumentIDs(txCtx, changeID)
		if err != nil {
			return err
		}

		existingSet := make(map[string]bool, len(existing))
		for _, argumentID := range existing {
			existingSet[argumentID.String()] = true
		}

		// The request is a set: an argument repeated inside it is refused
		// (the policy owns the rule), while arguments already credited are
		// simply resolved.
		newIDs := make([]domain.ArgumentID, 0, len(requested))
		seen := make(map[string]bool, len(requested))
		for _, argumentID := range requested {
			if existingSet[argumentID.String()] {
				continue
			}
			if seen[argumentID.String()] {
				return domain.ErrDuplicateAttribution
			}
			seen[argumentID.String()] = true
			newIDs = append(newIDs, argumentID)
		}

		if len(existing)+len(newIDs) > uc.policy.MaxAttributions {
			return domain.ErrTooManyAttributions
		}

		if len(newIDs) == 0 {
			// Nothing new to write: the recorded selection is the result.
			result = &RecordAttributionsResult{
				ArgumentIDs: sortedArgumentIDs(existing),
				Replayed:    len(existing) > 0,
			}
			return nil
		}

		candidates, err := uc.attributions.ListCandidates(txCtx, newIDs)
		if err != nil {
			return err
		}
		if err := uc.policy.Validate(*change, candidates); err != nil {
			return err
		}
		if err := uc.attributions.CreateAttributions(txCtx, changeID, attributorID, candidates); err != nil {
			return err
		}

		result = &RecordAttributionsResult{
			ArgumentIDs: sortedArgumentIDs(append(append([]domain.ArgumentID{}, existing...), newIDs...)),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// parseArgumentIDs parses the requested identifiers preserving order.
func parseArgumentIDs(raw []string) ([]domain.ArgumentID, error) {
	ids := make([]domain.ArgumentID, 0, len(raw))
	for _, value := range raw {
		argumentID, err := domain.ParseArgumentID(value)
		if err != nil {
			return nil, err
		}
		ids = append(ids, argumentID)
	}
	return ids, nil
}

// sortedArgumentIDs returns a deterministic order of the recorded set.
func sortedArgumentIDs(ids []domain.ArgumentID) []domain.ArgumentID {
	sorted := append([]domain.ArgumentID{}, ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].String() < sorted[j].String() })
	return sorted
}
