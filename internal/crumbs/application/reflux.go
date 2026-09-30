package application

import (
	"context"
	"time"
)

// RefluxResult is the classified regular Treasury reflux of one
// [start, end) window: own services plus tithe minus corresponding
// refunds, every leg counted by its database posted instant. The net
// may be negative — a week of refunds outrunning revenue owes
// nothing and hides nothing.
type RefluxResult struct {
	ServiceCharges    int64
	Tithe             int64
	MeteringReversals int64
	TitheReversals    int64
	Net               int64
}

// RefluxRepository sums classified regular reflux with the rows it
// reads: publications, tithe legs of liquidating settlements and
// both refund kinds in one window, or nothing at all. Reads never
// mutate and never infer: unknown transfers stay out.
type RefluxRepository interface {
	// RegularReflux nets the regular Treasury reflux posted inside
	// [start, end). Sales, seizures, death, corrections, vault
	// moves, gifts, holds and unknown transfers never enter.
	RegularReflux(ctx context.Context, start, end time.Time) (*RefluxResult, error)
}
