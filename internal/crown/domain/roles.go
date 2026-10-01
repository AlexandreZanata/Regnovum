package domain

import "time"

// OfficeKind is the closed vocabulary of game offices: the eight
// charges the phase names. Matching is exact with no accent and
// no inference: any other string refuses with ErrUnknownOffice.
type OfficeKind string

const (
	// OfficeInquisitor opens the inquiry.
	OfficeInquisitor OfficeKind = "inquisidor"
	// OfficeJustice decides as justiceiro.
	OfficeJustice OfficeKind = "justiceiro"
	// OfficeDefender defends the accused.
	OfficeDefender OfficeKind = "defensor"
	// OfficeWitness testifies without interest.
	OfficeWitness OfficeKind = "testemunha"
	// OfficeArbiter decides as arbitro.
	OfficeArbiter OfficeKind = "arbitro"
	// OfficeAuditor reviews the procedure independently.
	OfficeAuditor OfficeKind = "auditor"
	// OfficeExecutor carries the digital execution.
	OfficeExecutor OfficeKind = "carrasco"
	// OfficeCertifier certifies the record.
	OfficeCertifier OfficeKind = "certificador"
)

// ParseOfficeKind validates an office token. Matching is exact:
// no trimming, no case folding, no inference.
func ParseOfficeKind(raw string) (OfficeKind, error) {
	switch OfficeKind(raw) {
	case OfficeInquisitor, OfficeJustice, OfficeDefender, OfficeWitness,
		OfficeArbiter, OfficeAuditor, OfficeExecutor, OfficeCertifier:
		return OfficeKind(raw), nil
	default:
		return "", ErrUnknownOffice
	}
}

// String returns the stored office value.
func (o OfficeKind) String() string { return string(o) }

// AppointmentID identifies one office designation.
type AppointmentID string

// ParseAppointmentID validates one appointment token.
func ParseAppointmentID(raw string) (AppointmentID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", err
	}
	return AppointmentID(token), nil
}

// String returns the stored appointment value.
func (a AppointmentID) String() string { return string(a) }

// ProceedingID identifies one proceeding where offices act.
type ProceedingID string

// ParseProceedingID validates one proceeding token.
func ParseProceedingID(raw string) (ProceedingID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", err
	}
	return ProceedingID(token), nil
}

// String returns the stored proceeding value.
func (p ProceedingID) String() string { return string(p) }

// FeeID identifies one service fee.
type FeeID string

// ParseFeeID validates one fee token.
func ParseFeeID(raw string) (FeeID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", err
	}
	return FeeID(token), nil
}

// String returns the stored fee value.
func (f FeeID) String() string { return string(f) }

// Appointment is one game office designation for one season and
// reign: which office, held by whom, designated by the reigning
// holder, under which competence, inside which window. It moves
// no value and grants no wealth: later panels judge conflicts
// and fees judge payment per service.
type Appointment struct {
	ID         AppointmentID
	Office     OfficeKind
	Holder     HolderSubject
	Season     SeasonID
	Reign      ReignVersion
	Competence Competence
	Appointer  HolderSubject
	StartsAt   time.Time
	EndsAt     time.Time
}

// checkAppointmentIdentity validates office tokens: whole
// identifiers, a closed office, a whole competence.
func checkAppointmentIdentity(app Appointment) error {
	if _, err := ParseAppointmentID(string(app.ID)); err != nil {
		return err
	}
	if _, err := ParseOfficeKind(string(app.Office)); err != nil {
		return err
	}
	if _, err := ParseHolderSubject(string(app.Holder)); err != nil {
		return err
	}
	if _, err := ParseHolderSubject(string(app.Appointer)); err != nil {
		return err
	}
	if _, err := ParseCompetence(string(app.Competence)); err != nil {
		return err
	}
	return nil
}

// checkAppointmentMandate binds the mandate to the invested reign:
// same book and reign still invested, designated by the current
// holder, never held by the King itself. Ordinary game offices
// never carry sovereignty by accident.
func checkAppointmentMandate(app Appointment, current CurrentReign) error {
	if app.Season != current.Season {
		return ErrSeasonMismatch
	}
	switch {
	case app.Reign == current.Reign:
	case app.Reign < current.Reign:
		return ErrStaleReign
	default:
		return ErrFutureReign
	}
	if app.Appointer != current.Holder {
		return ErrNotHolder
	}
	if app.Holder == app.Appointer {
		return ErrConflictedOffice
	}
	return nil
}

// checkAppointmentWindow binds duration to the season: a live
// ordered window inside the book, ending at or before the book
// end. Nothing survives past it without a fresh designation.
func checkAppointmentWindow(app Appointment, current CurrentReign) error {
	if app.StartsAt.IsZero() || app.EndsAt.IsZero() {
		return ErrInvalidAuthority
	}
	start, end := app.StartsAt.UTC(), app.EndsAt.UTC()
	if !end.After(start) {
		return ErrInvalidAuthority
	}
	if start.Before(current.StartsAt.UTC()) || end.After(current.EndsAt.UTC()) {
		return ErrSeasonClosed
	}
	return nil
}

// DefineAppointment seals one office designation before any act.
// Every refusal arrives before effect: unknown offices, foreign
// books or reigns, designation outside the reigning holder, the
// King occupying an ordinary office, and windows outside the
// season all stop here.
func DefineAppointment(app Appointment, current CurrentReign) (Appointment, error) {
	if err := current.validShape(); err != nil {
		return Appointment{}, err
	}
	if err := checkAppointmentIdentity(app); err != nil {
		return Appointment{}, err
	}
	if err := checkAppointmentMandate(app, current); err != nil {
		return Appointment{}, err
	}
	if err := checkAppointmentWindow(app, current); err != nil {
		return Appointment{}, err
	}
	sealed := app
	sealed.StartsAt = app.StartsAt.UTC()
	sealed.EndsAt = app.EndsAt.UTC()
	return sealed, nil
}

// IsAppointmentActive judges reuse of a sealed mandate: same book
// and reign still invested, book open, instant inside both the
// season window and the mandate window. Past the end it refuses
// with ErrOfficeExpired: a new designation is required.
func IsAppointmentActive(app Appointment, current CurrentReign, now time.Time) error {
	if now.IsZero() {
		return ErrInvalidAuthority
	}
	if err := current.validShape(); err != nil {
		return err
	}
	if app.Season != current.Season {
		return ErrSeasonMismatch
	}
	switch {
	case app.Reign == current.Reign:
	case app.Reign < current.Reign:
		return ErrStaleReign
	default:
		return ErrFutureReign
	}
	moment := now.UTC()
	if !current.Open || !current.contains(moment) {
		return ErrSeasonClosed
	}
	if moment.Before(app.StartsAt.UTC()) || !moment.Before(app.EndsAt.UTC()) {
		return ErrOfficeExpired
	}
	return nil
}

// Panel is one proceeding roster: who accuses, who answers, who
// judges, defends, witnesses, executes and audits, with the
// interested set that disqualifies witnesses and executors. It
// moves nothing: it only names the separation one instant judges.
type Panel struct {
	Proceeding ProceedingID
	Season     SeasonID
	Reign      ReignVersion
	Inquisitor HolderSubject
	Accuser    HolderSubject
	Accused    HolderSubject
	Judge      HolderSubject
	Defender   HolderSubject
	Witnesses  []HolderSubject
	Executor   HolderSubject
	Auditor    HolderSubject
	Interested []HolderSubject
}

// checkPanelIdentity validates roster tokens: whole proceeding
// and holders, every named office holder present.
func checkPanelIdentity(panel Panel) error {
	if _, err := ParseProceedingID(string(panel.Proceeding)); err != nil {
		return err
	}
	for _, holder := range []HolderSubject{
		panel.Inquisitor, panel.Accuser, panel.Accused, panel.Judge,
		panel.Defender, panel.Executor, panel.Auditor,
	} {
		if _, err := ParseHolderSubject(string(holder)); err != nil {
			return err
		}
	}
	for _, witness := range panel.Witnesses {
		if _, err := ParseHolderSubject(string(witness)); err != nil {
			return err
		}
	}
	for _, party := range panel.Interested {
		if _, err := ParseHolderSubject(string(party)); err != nil {
			return err
		}
	}
	return nil
}

// checkPanelCurrency binds the roster to the invested book: same
// season and reign, book open at the instant.
func checkPanelCurrency(panel Panel, current CurrentReign, now time.Time) error {
	if now.IsZero() {
		return ErrInvalidAuthority
	}
	if panel.Season != current.Season {
		return ErrSeasonMismatch
	}
	switch {
	case panel.Reign == current.Reign:
	case panel.Reign < current.Reign:
		return ErrStaleReign
	default:
		return ErrFutureReign
	}
	if !current.Open || !current.contains(now.UTC()) {
		return ErrSeasonClosed
	}
	return nil
}

// interestedIn reports whether holder belongs to the interested set.
func interestedIn(holder HolderSubject, interested []HolderSubject) bool {
	for _, party := range interested {
		if holder == party {
			return true
		}
	}
	return false
}

// checkPanelAccusation refuses the accuser judging: the inquisitor
// or the accuser never sits as judge of the same proceeding.
func checkPanelAccusation(panel Panel) error {
	if panel.Accuser == panel.Judge || panel.Inquisitor == panel.Judge {
		return ErrConflictedOffice
	}
	return nil
}

// checkPanelKing refuses the King judging a rival dispute in own
// cause: when the reigning holder accuses, answers or holds an
// interest, it never sits as judge.
func checkPanelKing(panel Panel, current CurrentReign) error {
	if panel.Judge != current.Holder {
		return nil
	}
	if current.Holder == panel.Accuser || current.Holder == panel.Accused {
		return ErrConflictedOffice
	}
	if interestedIn(current.Holder, panel.Interested) {
		return ErrConflictedOffice
	}
	return nil
}

// checkPanelWitness refuses an interested witness: anyone in the
// interested set, or either party, never testifies as neutral.
func checkPanelWitness(panel Panel) error {
	for _, witness := range panel.Witnesses {
		if witness == panel.Accuser || witness == panel.Accused {
			return ErrConflictedOffice
		}
		if interestedIn(witness, panel.Interested) {
			return ErrConflictedOffice
		}
	}
	return nil
}

// checkPanelExecutor refuses an interested executor: anyone in
// the interested set, or either party, never carries the act.
func checkPanelExecutor(panel Panel) error {
	if panel.Executor == panel.Accuser || panel.Executor == panel.Accused {
		return ErrConflictedOffice
	}
	if interestedIn(panel.Executor, panel.Interested) {
		return ErrConflictedOffice
	}
	return nil
}

// checkPanelAuditor keeps the review independent: the auditor is
// a stranger to accuser, inquisitor, judge and executor.
func checkPanelAuditor(panel Panel) error {
	if panel.Auditor == panel.Accuser || panel.Auditor == panel.Inquisitor {
		return ErrConflictedOffice
	}
	if panel.Auditor == panel.Judge || panel.Auditor == panel.Executor {
		return ErrConflictedOffice
	}
	return nil
}

// SeatPanel seats one proceeding roster against the invested
// reign at one instant. Every refusal arrives before any act:
// accuser as judge, interested witness or executor, the King
// judging a rival in own cause, and a dependent auditor all
// stop here.
func SeatPanel(panel Panel, current CurrentReign, now time.Time) (Panel, error) {
	if err := current.validShape(); err != nil {
		return Panel{}, err
	}
	if err := checkPanelIdentity(panel); err != nil {
		return Panel{}, err
	}
	if err := checkPanelCurrency(panel, current, now); err != nil {
		return Panel{}, err
	}
	if err := checkPanelAccusation(panel); err != nil {
		return Panel{}, err
	}
	if err := checkPanelKing(panel, current); err != nil {
		return Panel{}, err
	}
	if err := checkPanelWitness(panel); err != nil {
		return Panel{}, err
	}
	if err := checkPanelExecutor(panel); err != nil {
		return Panel{}, err
	}
	if err := checkPanelAuditor(panel); err != nil {
		return Panel{}, err
	}
	return panel, nil
}

// Honorarium is one payment order for a game office: who is
// paid, for which office and proceeding, in which book, for
// which named service, how much, and whether it waits on a
// conviction. Service pay authorizes; conviction pay refuses.
type Honorarium struct {
	ID            FeeID
	Proceeding    ProceedingID
	Recipient     HolderSubject
	Office        OfficeKind
	Season        SeasonID
	Reign         ReignVersion
	Service       string
	Amount        int64
	ForConviction bool
}

// AuthorizeHonorarium authorizes one per-service payment: whole
// identifiers, a closed office, a named service, a positive
// amount in the same book. A fee conditioned on conviction
// refuses with ErrOutcomePay: offices are paid per service,
// never per result.
func AuthorizeHonorarium(fee Honorarium) (Honorarium, error) {
	if _, err := ParseFeeID(string(fee.ID)); err != nil {
		return Honorarium{}, err
	}
	if _, err := ParseProceedingID(string(fee.Proceeding)); err != nil {
		return Honorarium{}, err
	}
	if _, err := ParseHolderSubject(string(fee.Recipient)); err != nil {
		return Honorarium{}, err
	}
	if _, err := ParseOfficeKind(string(fee.Office)); err != nil {
		return Honorarium{}, err
	}
	if _, err := ParseSeasonID(string(fee.Season)); err != nil {
		return Honorarium{}, err
	}
	if _, err := ParseReignVersion(int(fee.Reign)); err != nil {
		return Honorarium{}, err
	}
	if _, err := parseActText(fee.Service); err != nil {
		return Honorarium{}, err
	}
	if fee.Amount <= 0 {
		return Honorarium{}, ErrIncompleteAct
	}
	if fee.ForConviction {
		return Honorarium{}, ErrOutcomePay
	}
	return fee, nil
}
