package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// SeasonDurationSeconds is the exact season length: 90 days in
// seconds. Civil months never define the calendar; the window is
// pure UTC arithmetic from the published start.
const SeasonDurationSeconds = 90 * 24 * 60 * 60

// seasonDuration is the duration form of the season length.
const seasonDuration = SeasonDurationSeconds * time.Second

// maxSeasonRunes bounds opaque season tokens: long enough for
// operation ids and custody labels, short enough to stay out of log
// abuse.
const maxSeasonRunes = 128

// SeasonState is the closed vocabulary of the staged machine:
// prepared, active, closing, sealed, archived. A security freeze is
// a separate control and never appears here: it cannot extend the
// calendar because the window is a pure function of the start.
type SeasonState string

const (
	// StatePrepared names a season whose manifesto is published
	// but whose economy is not open yet.
	StatePrepared SeasonState = "prepared"
	// StateActive names the single season whose economy admits
	// mutations.
	StateActive SeasonState = "active"
	// StateClosing names a season past its end draining admitted
	// operations before the seal.
	StateClosing SeasonState = "closing"
	// StateSealed names a season whose archive is sealed: its book
	// is readable history, never a live ledger.
	StateSealed SeasonState = "sealed"
	// StateArchived names a sealed season handed to long-term
	// retention.
	StateArchived SeasonState = "archived"
)

// ParseSeasonState validates a state token. Matching is exact: no
// trimming, no case folding.
func ParseSeasonState(raw string) (SeasonState, error) {
	state := SeasonState(raw)
	switch state {
	case StatePrepared, StateActive, StateClosing, StateSealed, StateArchived:
		return state, nil
	default:
		return "", ErrInvalidSeason
	}
}

// String returns the stored state value.
func (s SeasonState) String() string { return string(s) }

// Next reports the single forward step from one state. Terminal
// states have no next: the archive never reopens.
func (s SeasonState) Next() (SeasonState, error) {
	switch s {
	case StatePrepared:
		return StateActive, nil
	case StateActive:
		return StateClosing, nil
	case StateClosing:
		return StateSealed, nil
	case StateSealed:
		return StateArchived, nil
	default:
		return "", ErrInvalidTransition
	}
}

// parseSeasonToken validates one opaque season token: exact match,
// no control characters, bounded length.
func parseSeasonToken(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidSeason
	}
	if utf8.RuneCountInString(raw) > maxSeasonRunes {
		return "", ErrInvalidSeason
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidSeason
		}
	}
	return raw, nil
}

// parseCharterVersion validates one charter version token: ASCII
// `vN` with N from 1. The seasons module reads the shape, never the
// charter module: domains stay disjoint by architecture.
func parseCharterVersion(raw string) (string, error) {
	if len(raw) < 2 || raw[0] != 'v' {
		return "", ErrInvalidSeason
	}
	for _, r := range raw[1:] {
		if r < '0' || r > '9' {
			return "", ErrInvalidSeason
		}
	}
	if strings.TrimSpace(raw) != raw {
		return "", ErrInvalidSeason
	}
	return raw, nil
}

// Manifest is the published pre-opening document of one season: the
// immutable id, the ordinal from 1, the UTC start, the charter
// version, the economy policy reference and the initial holder data,
// sealed by hash. The initial monarch and the regent travel as
// plain account data: a game office, never a technical credential,
// and the seal binds them before anything opens.
type Manifest struct {
	ID             string
	Ordinal        int
	StartsAt       time.Time
	CharterVersion string
	PolicyRef      string
	InitialMonarch string
	Regent         string
	Hash           string
}

// ManifestRequest carries one manifesto. Every value arrives from
// the caller: no id, ordinal, start, version or policy lives here.
type ManifestRequest struct {
	ID             string
	Ordinal        int
	StartsAt       time.Time
	CharterVersion string
	PolicyRef      string
	InitialMonarch string
	Regent         string
}

// sealManifest binds id, ordinal, start, charter, policy, monarch
// and regent: any term changed after publication breaks the seal
// path, so later stages never run on terms nobody published.
func sealManifest(manifest Manifest) string {
	canonical := fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s",
		manifest.ID, manifest.Ordinal,
		manifest.StartsAt.UTC().Format(time.RFC3339Nano),
		manifest.CharterVersion, manifest.PolicyRef,
		manifest.InitialMonarch, manifest.Regent)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// seasonEnd derives the exclusive end from a start, refusing date
// overflow: the calendar cannot hold what UTC cannot name.
func seasonEnd(start time.Time) (time.Time, error) {
	end := start.Add(seasonDuration)
	if end.Before(start) || end.Year() > 9999 {
		return time.Time{}, ErrInvalidSeason
	}
	return end, nil
}

// NewManifest seals one season manifesto. The start is normalized
// to UTC and the end must fit the calendar: overflow refuses before
// anything is published.
func NewManifest(req ManifestRequest) (Manifest, error) {
	id, err := parseSeasonToken(req.ID)
	if err != nil {
		return Manifest{}, err
	}
	if req.Ordinal < 1 {
		return Manifest{}, ErrInvalidSeason
	}
	if req.StartsAt.IsZero() {
		return Manifest{}, ErrInvalidSeason
	}
	charter, err := parseCharterVersion(req.CharterVersion)
	if err != nil {
		return Manifest{}, err
	}
	policy, err := parseSeasonToken(req.PolicyRef)
	if err != nil {
		return Manifest{}, err
	}
	monarch, err := parseSeasonToken(req.InitialMonarch)
	if err != nil {
		return Manifest{}, err
	}
	regent, err := parseSeasonToken(req.Regent)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		ID: id, Ordinal: req.Ordinal, StartsAt: req.StartsAt.UTC(),
		CharterVersion: charter, PolicyRef: policy,
		InitialMonarch: monarch, Regent: regent,
	}
	if _, err := seasonEnd(manifest.StartsAt); err != nil {
		return Manifest{}, err
	}
	manifest.Hash = sealManifest(manifest)
	return manifest, nil
}

// VerifyManifestHash recomputes the seal and refuses manifests whose
// values no longer agree.
func (m Manifest) VerifyManifestHash() error {
	if m.Hash == "" || sealManifest(m) != m.Hash {
		return ErrInvalidSeason
	}
	return nil
}

// Season is one staged season: its sealed manifesto, the exclusive
// UTC end, the machine state and the instant of the last move. The
// clock and the policy arrive per call as values — the domain never
// reads the wall clock and never imports another module.
type Season struct {
	Manifest  Manifest
	EndsAt    time.Time
	State     SeasonState
	UpdatedAt time.Time
}

// NewSeason opens one season in PREPARED from its published
// manifesto. Tampered manifests refuse before anything exists.
func NewSeason(manifest Manifest) (Season, error) {
	if err := manifest.VerifyManifestHash(); err != nil {
		return Season{}, err
	}
	end, err := seasonEnd(manifest.StartsAt)
	if err != nil {
		return Season{}, err
	}
	return Season{
		Manifest: manifest, EndsAt: end,
		State: StatePrepared, UpdatedAt: manifest.StartsAt,
	}, nil
}

// Contains reports whether one instant falls in the half-open
// window [starts_at, ends_at): the start admits, the exact end
// already belongs to the successor.
func (s Season) Contains(at time.Time) bool {
	moment := at.UTC()
	return !moment.Before(s.Manifest.StartsAt) && moment.Before(s.EndsAt)
}

// Transition moves one season exactly one step forward at one
// instant: jumps, returns and reopenings refuse, and instants
// behind the record refuse as a regressive clock. Downtime changes
// nothing here: planning the successor is an explicit separate
// call, never an automatic fill.
func (s Season) Transition(next SeasonState, at time.Time) (Season, error) {
	if err := s.Manifest.VerifyManifestHash(); err != nil {
		return Season{}, err
	}
	want, err := s.State.Next()
	if err != nil {
		return Season{}, err
	}
	if next != want {
		return Season{}, ErrInvalidTransition
	}
	if at.IsZero() || at.UTC().Before(s.UpdatedAt) {
		return Season{}, ErrStaleClock
	}
	moved := s
	moved.State = next
	moved.UpdatedAt = at.UTC()
	return moved, nil
}

// PlanNext plans the immediate successor of one season from its
// manifesto request. Continuity is exact: the successor starts at
// the predecessor end (previous start plus ninety days) with the
// next ordinal and its own id. Gaps refuse, so a downtime spanning
// lapsed seasons plans one explicit successor at a time instead of
// simulating empty seasons or stretching the calendar.
func PlanNext(previous Season, req ManifestRequest) (Season, error) {
	if err := previous.Manifest.VerifyManifestHash(); err != nil {
		return Season{}, err
	}
	if req.Ordinal != previous.Manifest.Ordinal+1 {
		return Season{}, ErrInvalidSeason
	}
	if req.ID == previous.Manifest.ID {
		return Season{}, ErrInvalidSeason
	}
	if !req.StartsAt.UTC().Equal(previous.EndsAt) {
		return Season{}, ErrInvalidSeason
	}
	manifest, err := NewManifest(req)
	if err != nil {
		return Season{}, err
	}
	return NewSeason(manifest)
}
