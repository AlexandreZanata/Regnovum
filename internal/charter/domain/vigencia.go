package domain

import "time"

// PublishCorrection seals one corrective release that takes effect
// prospectively: its effective instant must not precede now. The now
// instant arrives per call — the domain never reads the wall clock —
// and backdated corrections refuse instead of rewriting settled
// facts. Chain-link rules still apply on top.
func PublishCorrection(chain []Release, req PublishRequest, now time.Time) (Release, error) {
	if now.IsZero() || req.EffectiveAt.UTC().Before(now.UTC()) {
		return Release{}, ErrRetroactiveCorrection
	}
	return Publish(chain, req)
}

// ApplyProcedure decides which release judges one fact when a
// procedure is proposed: the contemporary version, unless a newer
// procedure reaches the fact without prejudice. Prejudice arrives per
// call — no ratified severity scale lives here — and a prejudicial
// newer procedure for an older fact refuses with
// ErrHarmfulRetroactivity. A non-prejudicial newer procedure applies;
// an older procedure never revives for a newer fact, and the same
// version is its own contemporary rule.
func ApplyProcedure(chain []Release, locale CharterLocale, factVersion, procedureVersion Version, prejudicial bool) (Release, error) {
	fact, err := findRelease(chain, factVersion, locale)
	if err != nil {
		return Release{}, err
	}
	procedure, err := findRelease(chain, procedureVersion, locale)
	if err != nil {
		return Release{}, err
	}
	if procedure.EffectiveAt.After(fact.EffectiveAt) {
		if prejudicial {
			return Release{}, ErrHarmfulRetroactivity
		}
		return procedure, nil
	}
	return fact, nil
}
