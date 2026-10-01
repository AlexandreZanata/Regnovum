package domain

import "time"

// PetitionID identifies one appeal filing. Petitions bind the act
// digest so a filing cannot drift onto another payload.
type PetitionID string

// ParsePetitionID validates one petition token.
func ParsePetitionID(raw string) (PetitionID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", err
	}
	return PetitionID(token), nil
}

// String returns the stored petition value.
func (p PetitionID) String() string { return string(p) }

// ReviewID identifies one independent audit review.
type ReviewID string

// ParseReviewID validates one review token.
func ParseReviewID(raw string) (ReviewID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", err
	}
	return ReviewID(token), nil
}

// String returns the stored review value.
func (r ReviewID) String() string { return string(r) }

// VerdictKind is the closed vocabulary of audit verdicts: uphold
// the act, repair it by a new act, or dismiss the petition. Harm
// and obligation travel only with a repair verdict.
type VerdictKind string

const (
	// VerdictUphold confirms the act: no repair, no obligation.
	VerdictUphold VerdictKind = "confirmado"
	// VerdictRepair orders a correction by new act: with third-party
	// harm it also orders a restitution funded by the responsible.
	VerdictRepair VerdictKind = "reparar"
	// VerdictDismissed rejects the petition: the act stands as is.
	VerdictDismissed VerdictKind = "improcedente"
)

// ParseVerdictKind validates a verdict token. Matching is exact:
// no trimming, no case folding, no inference.
func ParseVerdictKind(raw string) (VerdictKind, error) {
	switch VerdictKind(raw) {
	case VerdictUphold, VerdictRepair, VerdictDismissed:
		return VerdictKind(raw), nil
	default:
		return "", ErrInvalidAuthority
	}
}

// String returns the stored verdict value.
func (v VerdictKind) String() string { return string(v) }

// Petition is one appeal filing by an affected holder against a
// recorded act: which act over which sealed digest, by whom, in
// which book, why, and when. The act author never petitions: own
// errors are corrected by new linked acts, never by appeal.
type Petition struct {
	ID         PetitionID
	Act        ActID
	Petitioner HolderSubject
	Season     SeasonID
	Reason     string
	Digest     string
	FiledAt    time.Time
}

// AuditReview is one independent procedure audit of a petition:
// which filing and act over which digest, by which auditor, in
// which book and reign, with which verdict, and — only for a
// repair verdict with third-party harm — who answers and who is
// owed. The author, the petitioner and the responsible never
// audit: an interested auditor authorizes nothing.
type AuditReview struct {
	ID          ReviewID
	Petition    PetitionID
	Act         ActID
	Auditor     HolderSubject
	Season      SeasonID
	Reign       ReignVersion
	Verdict     VerdictKind
	Harm        bool
	Responsible HolderSubject
	Harmed      HolderSubject
	Digest      string
	At          time.Time
}

// Restitution is one repair obligation from a repair verdict with
// third-party harm: which act and corrective act, who pays and
// who receives, from which own custody, how much, in which book.
// The free treasury never funds it: the responsible repairs from
// own custody, never the public vault.
type Restitution struct {
	Act        ActID
	Corrective ActID
	Debtor     HolderSubject
	Creditor   HolderSubject
	Origin     string
	Amount     int64
	Season     SeasonID
}

// RepairOrder is one authorized correction: the new act repairing
// the original under a repair verdict, with its restitution when
// the verdict declares third-party harm. It rewrites nothing: the
// original stays in the book beside the correction.
type RepairOrder struct {
	Original   ActID
	Corrective ActID
	Review     ReviewID
	Season     SeasonID
	Reign      ReignVersion
	Digest     string
	Restored   bool
}

// FilePetition opens the review channel for one affected holder
// against one recorded act. The act must be published, the
// petitioner well-formed and distinct from the author, the filing
// season the act book, and the filing instant at or after the
// vigour start. Appeal windows beyond that arrive by policy, not
// by inference here.
func FilePetition(book RoyalBook, id PetitionID, act ActID, petitioner HolderSubject, reason string, now time.Time) (Petition, error) {
	if _, err := ParsePetitionID(string(id)); err != nil {
		return Petition{}, err
	}
	record, err := book.Find(act)
	if err != nil {
		return Petition{}, err
	}
	claimant, err := ParseHolderSubject(string(petitioner))
	if err != nil {
		return Petition{}, err
	}
	if claimant == record.Author {
		return Petition{}, ErrImproperPetitioner
	}
	motive, err := parseActText(reason)
	if err != nil {
		return Petition{}, err
	}
	if now.IsZero() {
		return Petition{}, ErrInvalidAuthority
	}
	if now.UTC().Before(record.Effective.UTC()) {
		return Petition{}, ErrInvalidAuthority
	}
	return Petition{
		ID: id, Act: record.Act, Petitioner: claimant,
		Season: record.Season, Reason: motive, Digest: record.Digest,
		FiledAt: now.UTC(),
	}, nil
}

// checkReviewVerdict judges verdict payload shape: repair carries
// responsible and harmed only with harm declared; upheld and
// dismissed verdicts carry neither and declare no harm.
func checkReviewVerdict(verdict VerdictKind, harm bool, responsible, harmed HolderSubject) error {
	if _, err := ParseVerdictKind(string(verdict)); err != nil {
		return err
	}
	if verdict != VerdictRepair {
		if harm || responsible != "" || harmed != "" {
			return ErrIncompleteAct
		}
		return nil
	}
	if !harm {
		if responsible != "" || harmed != "" {
			return ErrIncompleteAct
		}
		return nil
	}
	answerable, err := ParseHolderSubject(string(responsible))
	if err != nil {
		return err
	}
	owed, err := ParseHolderSubject(string(harmed))
	if err != nil {
		return err
	}
	if answerable == owed {
		return ErrInvalidAuthority
	}
	return nil
}

// ReviewInput carries one independent audit decision: its
// identity and auditor, the verdict with harm and parties, and
// the decision instant. The struct keeps the review call narrow:
// the domain judges values, never arities.
type ReviewInput struct {
	ID          ReviewID
	Auditor     HolderSubject
	Verdict     VerdictKind
	Harm        bool
	Responsible HolderSubject
	Harmed      HolderSubject
	At          time.Time
}

// ReviewPetition audits one petition independently: the filing
// must bind the still-current record digest, the auditor must be
// a stranger to author, petitioner and responsible, and the
// verdict payload must arrive whole. An interested auditor
// authorizes nothing.
func ReviewPetition(book RoyalBook, petition Petition, input ReviewInput) (AuditReview, error) {
	if _, err := ParseReviewID(string(input.ID)); err != nil {
		return AuditReview{}, err
	}
	if _, err := ParsePetitionID(string(petition.ID)); err != nil {
		return AuditReview{}, err
	}
	record, err := book.Find(petition.Act)
	if err != nil {
		return AuditReview{}, err
	}
	if petition.Digest != record.Digest || petition.Season != record.Season {
		return AuditReview{}, ErrTamperedAct
	}
	reviewer, err := ParseHolderSubject(string(input.Auditor))
	if err != nil {
		return AuditReview{}, err
	}
	if reviewer == record.Author || reviewer == petition.Petitioner {
		return AuditReview{}, ErrInterestedAuditor
	}
	if err := checkReviewVerdict(input.Verdict, input.Harm, input.Responsible, input.Harmed); err != nil {
		return AuditReview{}, err
	}
	if input.Verdict == VerdictRepair && input.Harm && reviewer == input.Responsible {
		return AuditReview{}, ErrInterestedAuditor
	}
	if input.At.IsZero() || input.At.UTC().Before(petition.FiledAt.UTC()) {
		return AuditReview{}, ErrInvalidAuthority
	}
	return AuditReview{
		ID: input.ID, Petition: petition.ID, Act: record.Act,
		Auditor: reviewer, Season: record.Season, Reign: record.Reign,
		Verdict: input.Verdict, Harm: input.Harm,
		Responsible: input.Responsible, Harmed: input.Harmed,
		Digest: record.Digest, At: input.At.UTC(),
	}, nil
}

// checkRestitution judges one repair obligation against its
// verdict: same act, debtor answering as the verdict orders,
// creditor owed as declared, positive amount from the
// responsible's own custody. The free treasury funds nothing.
func checkRestitution(review AuditReview, corrective ActID, restitution Restitution) error {
	if restitution.Act != review.Act || restitution.Corrective != corrective {
		return ErrTamperedAct
	}
	if restitution.Debtor != review.Responsible || restitution.Creditor != review.Harmed {
		return ErrInvalidAuthority
	}
	if restitution.Debtor == restitution.Creditor {
		return ErrInvalidAuthority
	}
	origin, err := parseToken(restitution.Origin)
	if err != nil {
		return ErrUnknownOrigin
	}
	if origin == freeTreasuryOrigin {
		return ErrTreasuryFundedRestitution
	}
	if restitution.Amount <= 0 {
		return ErrIncompleteAct
	}
	if restitution.Season != review.Season {
		return ErrSeasonMismatch
	}
	return nil
}

// OrderRepair authorizes one correction by new act under a repair
// verdict, with its restitution when the verdict declares
// third-party harm. The corrective act must link the original,
// postdate the review, and arrive whole; harm without a funded
// obligation stops here, and so does largesse without harm.
func OrderRepair(corrective RoyalAct, review AuditReview, restitution *Restitution) (RepairOrder, error) {
	if _, err := ParseReviewID(string(review.ID)); err != nil {
		return RepairOrder{}, err
	}
	if review.Verdict != VerdictRepair {
		return RepairOrder{}, ErrUnreviewedRepair
	}
	sealed, err := DefineAct(corrective)
	if err != nil {
		return RepairOrder{}, err
	}
	if sealed.Corrects != review.Act {
		return RepairOrder{}, ErrTamperedAct
	}
	if sealed.DecreedAt.UTC().Before(review.At.UTC()) {
		return RepairOrder{}, ErrInvalidAuthority
	}
	restored := false
	if review.Harm {
		if restitution == nil {
			return RepairOrder{}, ErrIncompleteAct
		}
		if err := checkRestitution(review, sealed.ID, *restitution); err != nil {
			return RepairOrder{}, err
		}
		restored = true
	} else if restitution != nil {
		return RepairOrder{}, ErrIncompleteAct
	}
	return RepairOrder{
		Original: review.Act, Corrective: sealed.ID, Review: review.ID,
		Season: review.Season, Reign: review.Reign, Digest: review.Digest,
		Restored: restored,
	}, nil
}
