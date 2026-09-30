package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// SeasonBooks is the internal port authorizing book writes against
// the season lifecycle: Genesis runs only in a prepared book, and
// mutations run only while the book is not closing, sealed or
// archived. Activation itself is a separate step the port never
// performs: it only judges. Production leaves the economy disabled;
// tests fake this port with fixed books.
type SeasonBooks interface {
	// RequirePrepared refuses Genesis outside a prepared book: an
	// unknown book, or a book with any active, closing, sealed or
	// archived lifecycle event, never mints.
	RequirePrepared(ctx context.Context, season domain.SeasonKey) error
	// RequireActive refuses mutations on books past admission: an
	// unknown book, or a book with any closing, sealed or archived
	// lifecycle event, moves nothing.
	RequireActive(ctx context.Context, season domain.SeasonKey) error
}
