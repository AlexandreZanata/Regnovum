package domain

import (
	"sort"
	"strings"
)

// SuccessionReason explains the deterministic cause of the sovereign selection.
type SuccessionReason string

const (
	// SuccessionReasonConquest indicates that an eligible citizen participant with P > C
	// won the throne by wealth superiority (or tie-breaking rules).
	SuccessionReasonConquest SuccessionReason = "conquest"

	// SuccessionReasonIncumbentRetained indicates that the reigning monarch was retained
	// because no rival surpassed C, or the incumbent won the tie among leaders,
	// or rivals with P > C were poorer than the incumbent.
	SuccessionReasonIncumbentRetained SuccessionReason = "incumbent-retained"

	// SuccessionReasonRegency indicates that the incumbent was impeded, renounced or ineligible,
	// and no qualified candidate exceeded C; the designated limited regent assumed authority.
	SuccessionReasonRegency SuccessionReason = "regency"
)

// Candidate represents one subject evaluated for succession.
type Candidate struct {
	Subject          HolderSubject
	Kind             BeneficiaryKind
	Wealth           int64
	AttainedRevision int64
	Eligible         bool
}

// IncumbentSnapshot represents the incumbent monarch's operational state.
type IncumbentSnapshot struct {
	Subject          HolderSubject
	Wealth           int64
	AttainedRevision int64
	Eligible         bool
}

// SuccessionInput holds the parameters and snapshot for sovereign selection.
type SuccessionInput struct {
	Policy              WealthPolicyVersion
	Season              SeasonID
	SeasonOpen          bool
	InstitutionalWealth int64
	Incumbent           IncumbentSnapshot
	Regent              HolderSubject
	Candidates          []Candidate
}

// SuccessionOutcome holds the deterministic result of the sovereign selection.
type SuccessionOutcome struct {
	Season             SeasonID
	SelectedSovereign  HolderSubject
	IsRegent           bool
	Reason             SuccessionReason
	InstitutionalC     int64
	WinningWealth      int64
	AttainedRevision   int64
	Predecessor        HolderSubject
	ReignVersionChange bool
}

// validate checks the shape, boundaries, and consistency of the succession input.
func (in SuccessionInput) validate() error {
	if in.Season == "" || strings.TrimSpace(string(in.Season)) != string(in.Season) {
		return ErrAmbiguousFixture
	}
	if _, err := ParseSeasonID(string(in.Season)); err != nil {
		return err
	}
	if !in.SeasonOpen {
		return ErrSeasonClosed
	}
	if _, err := ParseWealthPolicyVersion(string(in.Policy)); err != nil {
		return err
	}
	if in.InstitutionalWealth < 0 {
		return ErrInvalidWealthAmount
	}
	if in.InstitutionalWealth > MaxSeasonalSupplyMillis {
		return ErrWealthOverflow
	}

	if in.Incumbent.Subject != "" {
		if _, err := ParseHolderSubject(string(in.Incumbent.Subject)); err != nil {
			return err
		}
		if in.Incumbent.Subject == InstitutionalCrownSubject {
			return ErrAmbiguousFixture
		}
		if in.Incumbent.Wealth < 0 {
			return ErrInvalidWealthAmount
		}
		if in.Incumbent.Wealth > MaxSeasonalSupplyMillis {
			return ErrWealthOverflow
		}
		if in.Incumbent.AttainedRevision < 0 {
			return ErrAmbiguousFixture
		}
	}

	if in.Regent != "" {
		if _, err := ParseHolderSubject(string(in.Regent)); err != nil {
			return err
		}
		if in.Regent == InstitutionalCrownSubject {
			return ErrAmbiguousFixture
		}
	}

	return nil
}

// sanitizeCandidates validates each candidate, rejects duplicates, and filters out
// institutional/technical accounts that cannot compete as citizens.
func sanitizeCandidates(raw []Candidate) ([]Candidate, error) {
	seen := make(map[HolderSubject]bool, len(raw))
	var citizens []Candidate

	for _, c := range raw {
		if c.Subject == "" || strings.TrimSpace(string(c.Subject)) != string(c.Subject) {
			return nil, ErrAmbiguousFixture
		}
		if _, err := ParseHolderSubject(string(c.Subject)); err != nil {
			return nil, err
		}
		if seen[c.Subject] {
			return nil, ErrDuplicateCandidate
		}
		seen[c.Subject] = true

		if c.Wealth < 0 {
			return nil, ErrInvalidWealthAmount
		}
		if c.Wealth > MaxSeasonalSupplyMillis {
			return nil, ErrWealthOverflow
		}
		if c.AttainedRevision < 0 {
			return nil, ErrAmbiguousFixture
		}

		// Institutional Crown and technical accounts never compete as citizen candidates:
		if c.Subject == InstitutionalCrownSubject || c.Kind == BeneficiaryKindInstitutional || c.Kind == BeneficiaryKindTechnical {
			continue
		}

		citizens = append(citizens, c)
	}

	return citizens, nil
}

// resolveQualifiedContenders unifies incumbent data and filters participants who satisfy strict P > C.
func resolveQualifiedContenders(
	citizens []Candidate,
	incumbent *IncumbentSnapshot,
	thresholdC int64,
) []Candidate {
	incumbentFound := false
	for _, c := range citizens {
		if incumbent.Subject != "" && c.Subject == incumbent.Subject {
			incumbentFound = true
			incumbent.Wealth = c.Wealth
			incumbent.AttainedRevision = c.AttainedRevision
			incumbent.Eligible = incumbent.Eligible && c.Eligible
			break
		}
	}

	// If incumbent was not in candidate list, but is an eligible monarch, include in participant pool:
	allParticipants := citizens
	if !incumbentFound && incumbent.Subject != "" && incumbent.Eligible {
		allParticipants = append(allParticipants, Candidate{
			Subject:          incumbent.Subject,
			Kind:             BeneficiaryKindParticipant,
			Wealth:           incumbent.Wealth,
			AttainedRevision: incumbent.AttainedRevision,
			Eligible:         incumbent.Eligible,
		})
	}

	var qualified []Candidate
	for _, c := range allParticipants {
		// Strict P > C: equality does not qualify
		if c.Eligible && c.Wealth > thresholdC {
			qualified = append(qualified, c)
		}
	}

	return qualified
}

// selectBestContender implements the deterministic ranking:
// 1. Highest wealth P
// 2. Tie-break: conserve incumbent if among leaders
// 3. Otherwise: lowest attained_revision
// 4. Otherwise: lexicographically lowest subject ID
func selectBestContender(qualified []Candidate, incumbent IncumbentSnapshot) (Candidate, bool) {
	var maxWealth int64 = -1
	for _, q := range qualified {
		if q.Wealth > maxWealth {
			maxWealth = q.Wealth
		}
	}

	var tiedLeaders []Candidate
	for _, q := range qualified {
		if q.Wealth == maxWealth {
			tiedLeaders = append(tiedLeaders, q)
		}
	}

	// Incumbent conservation in tie among leaders:
	if incumbent.Subject != "" && incumbent.Eligible {
		for _, tl := range tiedLeaders {
			if tl.Subject == incumbent.Subject {
				return tl, true
			}
		}
	}

	// Earliest attained revision, then stable subject ID:
	sort.Slice(tiedLeaders, func(i, j int) bool {
		if tiedLeaders[i].AttainedRevision != tiedLeaders[j].AttainedRevision {
			return tiedLeaders[i].AttainedRevision < tiedLeaders[j].AttainedRevision
		}
		return tiedLeaders[i].Subject < tiedLeaders[j].Subject
	})

	winner := tiedLeaders[0]
	isIncumbent := (incumbent.Subject != "" && winner.Subject == incumbent.Subject)
	return winner, isIncumbent
}

// SelectSovereign evaluates the succession input deterministically according to TEMP-09:
// Strict P > C; highest P; tie conserves incumbent among leaders, else smallest attained_revision and stable ID.
// Without qualified contenders, incumbent remains; fall in wealth alone creates no vacancy.
// Renunciation or impediment activates the designated limited regency.
func SelectSovereign(in SuccessionInput) (SuccessionOutcome, error) {
	if err := in.validate(); err != nil {
		return SuccessionOutcome{}, err
	}

	citizens, err := sanitizeCandidates(in.Candidates)
	if err != nil {
		return SuccessionOutcome{}, err
	}

	incumbent := in.Incumbent
	qualified := resolveQualifiedContenders(citizens, &incumbent, in.InstitutionalWealth)

	// Case 1: No candidate exceeded C
	if len(qualified) == 0 {
		if incumbent.Subject != "" && incumbent.Eligible {
			return SuccessionOutcome{
				Season:             in.Season,
				SelectedSovereign:  incumbent.Subject,
				IsRegent:           false,
				Reason:             SuccessionReasonIncumbentRetained,
				InstitutionalC:     in.InstitutionalWealth,
				WinningWealth:      incumbent.Wealth,
				AttainedRevision:   incumbent.AttainedRevision,
				Predecessor:        incumbent.Subject,
				ReignVersionChange: false,
			}, nil
		}

		// Incumbent renounced, was impeded or ineligible: activate limited regency
		if in.Regent != "" {
			return SuccessionOutcome{
				Season:             in.Season,
				SelectedSovereign:  in.Regent,
				IsRegent:           true,
				Reason:             SuccessionReasonRegency,
				InstitutionalC:     in.InstitutionalWealth,
				WinningWealth:      0,
				AttainedRevision:   0,
				Predecessor:        in.Incumbent.Subject,
				ReignVersionChange: (in.Regent != in.Incumbent.Subject),
			}, nil
		}

		return SuccessionOutcome{}, ErrNoQualifiedSuccessor
	}

	// Case 2: One or more candidates exceeded C
	winner, isIncumbent := selectBestContender(qualified, incumbent)

	if isIncumbent {
		return SuccessionOutcome{
			Season:             in.Season,
			SelectedSovereign:  winner.Subject,
			IsRegent:           false,
			Reason:             SuccessionReasonIncumbentRetained,
			InstitutionalC:     in.InstitutionalWealth,
			WinningWealth:      winner.Wealth,
			AttainedRevision:   winner.AttainedRevision,
			Predecessor:        incumbent.Subject,
			ReignVersionChange: false,
		}, nil
	}

	return SuccessionOutcome{
		Season:             in.Season,
		SelectedSovereign:  winner.Subject,
		IsRegent:           false,
		Reason:             SuccessionReasonConquest,
		InstitutionalC:     in.InstitutionalWealth,
		WinningWealth:      winner.Wealth,
		AttainedRevision:   winner.AttainedRevision,
		Predecessor:        in.Incumbent.Subject,
		ReignVersionChange: true,
	}, nil
}
