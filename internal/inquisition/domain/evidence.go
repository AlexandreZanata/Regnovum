package domain

import (
	"time"
)

// EvidenceKind is the closed vocabulary of evidence pieces
// that may circulate sealed: documents, images, audio, video
// and records. Executables and scripts are named only to be
// refused: they never circulate as proof, even sealed.
type EvidenceKind string

const (
	// EvidenceDocument names a sealed document.
	EvidenceDocument EvidenceKind = "documento"
	// EvidenceImage names a sealed image.
	EvidenceImage EvidenceKind = "imagem"
	// EvidenceAudio names sealed audio.
	EvidenceAudio EvidenceKind = "audio"
	// EvidenceVideo names sealed video.
	EvidenceVideo EvidenceKind = "video"
	// EvidenceRecord names a sealed record.
	EvidenceRecord EvidenceKind = "registro"
)

// ParseEvidenceKind validates a safe piece token. Matching
// is exact: no trimming, no case folding, no inference.
func ParseEvidenceKind(raw string) (EvidenceKind, error) {
	switch EvidenceKind(raw) {
	case EvidenceDocument, EvidenceImage, EvidenceAudio,
		EvidenceVideo, EvidenceRecord:
		return EvidenceKind(raw), nil
	default:
		return "", ErrUnsafeEvidence
	}
}

// String returns the stored evidence kind value.
func (k EvidenceKind) String() string { return string(k) }

// isDangerousKind reports whether raw names a dangerous
// file: executables and scripts never circulate as proof.
func isDangerousKind(raw string) bool {
	return raw == "executavel" || raw == "script"
}

// EvidenceID identifies one sealed envelope.
type EvidenceID string

// ParseEvidenceID validates one envelope token.
func ParseEvidenceID(raw string) (EvidenceID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", ErrInvalidReport
	}
	return EvidenceID(token), nil
}

// String returns the stored envelope value.
func (e EvidenceID) String() string { return string(e) }

// SealedEnvelope is one sealed proof: which severe case it
// belongs to, the sealed digest with the sealing instant and
// sealer, and the declared piece shape. The seal carries the
// binding only, never the raw payload: victim or minor data
// discloses solely redacted through a notified defense.
type SealedEnvelope struct {
	ID       EvidenceID
	Case     CaseID
	Kind     EvidenceKind
	Digest   string
	SealedAt time.Time
	Sealer   Subject
	Victim   bool
	Minor    bool
}

// SealRequest carries one sealing request: the envelope
// identity, the case it belongs to, the piece kind with the
// sealed digest and instant, who seals, and whether the
// piece carries victim or minor data.
type SealRequest struct {
	ID       EvidenceID
	Case     CaseID
	KindRaw  string
	Digest   string
	SealedAt time.Time
	Sealer   Subject
	Victim   bool
	Minor    bool
}

// checkSealKind binds the piece kind: dangerous files refuse
// as unsafe, unknown tokens refuse as malformed shape.
func checkSealKind(raw string) (EvidenceKind, error) {
	if isDangerousKind(raw) {
		return "", ErrUnsafeEvidence
	}
	return ParseEvidenceKind(raw)
}

// SealEvidence seals one proof for a severe case. Every
// refusal arrives before any record: dangerous files,
// unknown kinds, shapeless tokens and unsealed digests all
// stop here.
func SealEvidence(req SealRequest) (SealedEnvelope, error) {
	if _, err := ParseEvidenceID(string(req.ID)); err != nil {
		return SealedEnvelope{}, err
	}
	if _, err := ParseCaseID(string(req.Case)); err != nil {
		return SealedEnvelope{}, err
	}
	kind, err := checkSealKind(req.KindRaw)
	if err != nil {
		return SealedEnvelope{}, err
	}
	if !isDigest(req.Digest) {
		return SealedEnvelope{}, ErrUnprovenAccusation
	}
	if _, err := ParseSubject(string(req.Sealer)); err != nil {
		return SealedEnvelope{}, err
	}
	if req.SealedAt.IsZero() {
		return SealedEnvelope{}, ErrInvalidReport
	}
	return SealedEnvelope{
		ID: req.ID, Case: req.Case, Kind: kind, Digest: req.Digest,
		SealedAt: req.SealedAt.UTC(), Sealer: req.Sealer,
		Victim: req.Victim, Minor: req.Minor,
	}, nil
}

// DefenseNotice is the prior notice to the defense: which
// envelope, notified to whom, when. Disclosure without a
// notice that precedes it discloses nothing.
type DefenseNotice struct {
	Envelope   EvidenceID
	Notified   Subject
	NotifiedAt time.Time
}

// NotifyDefense records one defense notice for a sealed
// envelope. The notice names a whole envelope and holder
// with a live instant.
func NotifyDefense(envelope EvidenceID, notified Subject, at time.Time) (DefenseNotice, error) {
	if _, err := ParseEvidenceID(string(envelope)); err != nil {
		return DefenseNotice{}, err
	}
	if _, err := ParseSubject(string(notified)); err != nil {
		return DefenseNotice{}, err
	}
	if at.IsZero() {
		return DefenseNotice{}, ErrInvalidReport
	}
	return DefenseNotice{
		Envelope: envelope, Notified: notified,
		NotifiedAt: at.UTC(),
	}, nil
}

// RevealRequest carries one defense disclosure request: the
// sealed envelope, the prior notice, who views, the digest
// as presented again, whether the disclosed view is redacted
// and when the disclosure happens.
type RevealRequest struct {
	Envelope SealedEnvelope
	Notice   DefenseNotice
	Viewer   Subject
	Digest   string
	Redacted bool
	At       time.Time
}

// DisclosureView is what the defense receives: the case and
// digest binding with the sealing and disclosure instants.
// It carries no raw payload and no victim identity: access
// is minimal by construction.
type DisclosureView struct {
	Case       CaseID
	Digest     string
	SealedAt   time.Time
	RevealedAt time.Time
	Viewer     Subject
	Redacted   bool
}

// checkRevealBinding binds the disclosure to the seal and
// the notice: same envelope, the notified holder views,
// the notice precedes the disclosure, and the presented
// digest still matches the seal.
func checkRevealBinding(req RevealRequest) error {
	if req.Envelope.ID != req.Notice.Envelope {
		return ErrUnnotifiedDefense
	}
	if _, err := ParseSubject(string(req.Viewer)); err != nil {
		return ErrUnnotifiedDefense
	}
	if req.Viewer != req.Notice.Notified {
		return ErrUnnotifiedDefense
	}
	if req.Notice.NotifiedAt.IsZero() || req.At.IsZero() {
		return ErrUnnotifiedDefense
	}
	if req.At.UTC().Before(req.Notice.NotifiedAt.UTC()) {
		return ErrUnnotifiedDefense
	}
	if !isDigest(req.Digest) {
		return ErrTamperedEvidence
	}
	if req.Digest != req.Envelope.Digest {
		return ErrTamperedEvidence
	}
	return nil
}

// RevealForDefense discloses sealed proof to the notified
// defense with minimal access. Swapped digests, missing or
// late notices, strangers to the notice, unsafe pieces and
// unredacted victim or minor data all refuse before
// anything is shown.
func RevealForDefense(req RevealRequest) (DisclosureView, error) {
	if err := checkRevealBinding(req); err != nil {
		return DisclosureView{}, err
	}
	if _, err := ParseEvidenceKind(string(req.Envelope.Kind)); err != nil {
		return DisclosureView{}, err
	}
	if req.Envelope.Victim || req.Envelope.Minor {
		if !req.Redacted {
			return DisclosureView{}, ErrExposedVictim
		}
	}
	return DisclosureView{
		Case: req.Envelope.Case, Digest: req.Envelope.Digest,
		SealedAt:   req.Envelope.SealedAt.UTC(),
		RevealedAt: req.At.UTC(), Viewer: req.Viewer,
		Redacted: req.Redacted,
	}, nil
}

// SealedPhase is the mandatory sealed stage of a severe
// case: either the envelope bound to the case or a
// motivated waiver naming who excuses the seal and why.
// A severe case never proceeds with neither.
type SealedPhase struct {
	Case      CaseID
	Envelope  EvidenceID
	Waived    bool
	Motive    string
	Authority Subject
	DecidedAt time.Time
}

// PhaseRequest carries one sealed-phase decision: the case,
// the envelope when sealed, or the motivated waiver naming
// authority and reason when the seal is excused.
type PhaseRequest struct {
	Case      CaseID
	Envelope  SealedEnvelope
	HasSeal   bool
	Waived    bool
	Motive    string
	Authority Subject
	DecidedAt time.Time
}

// RequireSealedPhase enforces the mandatory sealed phase of
// a severe case: a sealed envelope bound to the case, or a
// waiver with stated motive and recorded authority. A bare
// case with neither, a waiver without motive, and an
// envelope bound to another case all stop here.
func RequireSealedPhase(req PhaseRequest) (SealedPhase, error) {
	if _, err := ParseCaseID(string(req.Case)); err != nil {
		return SealedPhase{}, err
	}
	if req.DecidedAt.IsZero() {
		return SealedPhase{}, ErrInvalidReport
	}
	if req.Waived {
		if _, err := ParseSubject(string(req.Authority)); err != nil {
			return SealedPhase{}, ErrUnnotifiedDefense
		}
		if _, err := parseText(req.Motive); err != nil {
			return SealedPhase{}, ErrUnnotifiedDefense
		}
		if req.HasSeal {
			return SealedPhase{}, ErrUnsealedPhase
		}
		return SealedPhase{
			Case: req.Case, Waived: true, Motive: req.Motive,
			Authority: req.Authority, DecidedAt: req.DecidedAt.UTC(),
		}, nil
	}
	if !req.HasSeal {
		return SealedPhase{}, ErrUnsealedPhase
	}
	if req.Envelope.Case != req.Case {
		return SealedPhase{}, ErrUnsealedPhase
	}
	if _, err := ParseEvidenceID(string(req.Envelope.ID)); err != nil {
		return SealedPhase{}, err
	}
	if !isDigest(req.Envelope.Digest) {
		return SealedPhase{}, ErrTamperedEvidence
	}
	return SealedPhase{
		Case: req.Case, Envelope: req.Envelope.ID,
		DecidedAt: req.DecidedAt.UTC(),
	}, nil
}

// DisclosureRecord is one immutable log entry: which envelope
// disclosed which digest to whom and when.
type DisclosureRecord struct {
	Envelope   EvidenceID
	Digest     string
	Viewer     Subject
	RevealedAt time.Time
}

// AppendDisclosure appends one disclosure to the immutable log.
// An identical replay returns the log unchanged; a
// divergent entry over the same envelope refuses as
// tampering: the record never rewrites history.
func AppendDisclosure(log []DisclosureRecord, rec DisclosureRecord) ([]DisclosureRecord, error) {
	if _, err := ParseEvidenceID(string(rec.Envelope)); err != nil {
		return nil, err
	}
	if !isDigest(rec.Digest) {
		return nil, ErrTamperedEvidence
	}
	if _, err := ParseSubject(string(rec.Viewer)); err != nil {
		return nil, err
	}
	if rec.RevealedAt.IsZero() {
		return nil, ErrInvalidReport
	}
	for _, entry := range log {
		if entry.Envelope != rec.Envelope {
			continue
		}
		if entry.Digest == rec.Digest && entry.Viewer == rec.Viewer &&
			entry.RevealedAt.Equal(rec.RevealedAt.UTC()) {
			return log, nil
		}
		return nil, ErrTamperedEvidence
	}
	return append(log, DisclosureRecord{
		Envelope: rec.Envelope, Digest: rec.Digest,
		Viewer: rec.Viewer, RevealedAt: rec.RevealedAt.UTC(),
	}), nil
}
