package postgres

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
)

var (
	_ application.ActivationFreezeReader = (*Repository)(nil)
	_ application.ActivationHealthSource = (*Repository)(nil)
	_ application.KillSwitchSource       = (*Repository)(nil)
	_ application.KillSwitchStore        = (*Repository)(nil)
)

// IsEconomyFrozen reports whether new mutations are frozen on a
// conservation break. A missing mode row means a book that never
// froze, which reads as open. Reads never call it to refuse.
func (r *Repository) IsEconomyFrozen(ctx context.Context) (bool, error) {
	return isFrozen(ctx, r.pool)
}
