package domain

import (
	"fmt"
	"sort"
)

// BookFingerprint is the whole observable state of one season book at
// one instant: the conserved supply, every custody balance keyed by
// kind and label, the history counts, the archive receipt and the
// active reign. Two snapshots of the same instant must be identical;
// any difference — one milliINK included — is a divergence.
type BookFingerprint struct {
	Season             SeasonKey
	SupplyMillis       int64
	Custodies          map[string]int64
	Legs               int64
	Intentions         int64
	ArchiveDigest      string
	ArchiveSealed      bool
	ActiveReignHolder  string
	ActiveReignVersion int64
	HasActiveReign     bool
	FormerHolders      []string
}

// RestoreDivergence is one judged difference between the expected
// snapshot (taken before the disaster) and the observed snapshot
// (taken after the restore). Every divergence carries severity,
// owner, runbook and action: resumption is blocked until each one is
// resolved by compensation, never by silence.
type RestoreDivergence struct {
	Code     string
	Severity string
	Owner    string
	Runbook  string
	Action   string
	Detail   string
}

// RestoreVerdict is the resumption decision: allowed exactly when no
// divergence exists. A simulated corruption must therefore block
// resumption by construction, not by convention.
type RestoreVerdict struct {
	Divergences []RestoreDivergence
}

// ResumeAllowed reports whether the restored cluster may resume.
func (v RestoreVerdict) ResumeAllowed() bool { return len(v.Divergences) == 0 }

// restoreDivergenceSpec is the registry row for one divergence code.
type restoreDivergenceSpec struct {
	severity string
	owner    string
	runbook  string
	action   string
}

// restoreRegistry maps every divergence code to its handling contract.
// Owners are roles, never people; runbooks are the R-series the
// threat model already cites for monetary incidents.
var restoreRegistry = map[string]restoreDivergenceSpec{
	"supply-diverged":      {severity: "critical", owner: "tesouro", runbook: "R3", action: "freeze-and-investigate"},
	"custody-diverged":     {severity: "critical", owner: "tesouro", runbook: "R3", action: "freeze-and-investigate"},
	"history-rewritten":    {severity: "critical", owner: "livros", runbook: "R4", action: "freeze-and-investigate"},
	"archive-violated":     {severity: "critical", owner: "livros", runbook: "R4", action: "freeze-and-block-successor"},
	"succession-rewritten": {severity: "critical", owner: "coroa", runbook: "R1", action: "freeze-and-investigate"},
	"ex-king-restored":     {severity: "critical", owner: "coroa", runbook: "R1", action: "freeze-and-investigate"},
}

// RestoreDivergenceCodes returns every code the comparison can emit.
func RestoreDivergenceCodes() []string {
	codes := make([]string, 0, len(restoreRegistry))
	for code := range restoreRegistry {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

func restoreDivergence(code, detail string) (RestoreDivergence, error) {
	spec, ok := restoreRegistry[code]
	if !ok {
		return RestoreDivergence{}, ErrUnknownRestoreDivergence
	}
	return RestoreDivergence{
		Code:     code,
		Severity: spec.severity,
		Owner:    spec.owner,
		Runbook:  spec.runbook,
		Action:   spec.action,
		Detail:   detail,
	}, nil
}

// Redacted renders the divergence as a private log line: codes and
// counts only, never account labels beyond kind/label keys the books
// themselves carry, and no email or secret.
func (d RestoreDivergence) Redacted(season SeasonKey) string {
	return fmt.Sprintf("code=%s severity=%s season=%s owner=%s runbook=%s action=%s detail=%s",
		d.Code, d.Severity, season, d.Owner, d.Runbook, d.Action, d.Detail)
}

// validateRestoreSnapshot refuses shapes the comparison cannot judge:
// an unnamed book, negative money or negative history counts fail
// closed instead of comparing.
func validateRestoreSnapshot(snapshot BookFingerprint) error {
	if _, err := ParseSeasonKey(snapshot.Season.String()); err != nil {
		return err
	}
	if snapshot.SupplyMillis < 0 || snapshot.Legs < 0 || snapshot.Intentions < 0 {
		return ErrInvalidRestoreSnapshot
	}
	for key, balance := range snapshot.Custodies {
		if key == "" || balance < 0 {
			return ErrInvalidRestoreSnapshot
		}
	}
	return nil
}

type restoreDivergeFunc func(code, detail string) error

func judgeRestoreMoney(expected, observed BookFingerprint, diverge restoreDivergeFunc) error {
	if expected.SupplyMillis != observed.SupplyMillis {
		if err := diverge("supply-diverged", fmt.Sprintf("expected=%d observed=%d", expected.SupplyMillis, observed.SupplyMillis)); err != nil {
			return err
		}
	}
	for key, expectedBalance := range expected.Custodies {
		observedBalance, ok := observed.Custodies[key]
		if !ok || observedBalance != expectedBalance {
			if err := diverge("custody-diverged", fmt.Sprintf("custody=%s expected=%d observed=%d present=%v", key, expectedBalance, observedBalance, ok)); err != nil {
				return err
			}
		}
	}
	for key := range observed.Custodies {
		if _, ok := expected.Custodies[key]; !ok {
			if err := diverge("custody-diverged", fmt.Sprintf("custody=%s unexpected", key)); err != nil {
				return err
			}
		}
	}
	return nil
}

func judgeRestoreHistory(expected, observed BookFingerprint, diverge restoreDivergeFunc) error {
	if expected.Legs == observed.Legs && expected.Intentions == observed.Intentions {
		return nil
	}
	return diverge("history-rewritten", fmt.Sprintf("legs=%d/%d intentions=%d/%d", expected.Legs, observed.Legs, expected.Intentions, observed.Intentions))
}

func judgeRestoreArchive(expected, observed BookFingerprint, diverge restoreDivergeFunc) error {
	if expected.ArchiveSealed == observed.ArchiveSealed && expected.ArchiveDigest == observed.ArchiveDigest {
		return nil
	}
	return diverge("archive-violated", fmt.Sprintf("sealed=%v/%v", expected.ArchiveSealed, observed.ArchiveSealed))
}

func judgeRestoreSuccession(expected, observed BookFingerprint, diverge restoreDivergeFunc) error {
	reignChanged := expected.HasActiveReign != observed.HasActiveReign ||
		expected.ActiveReignHolder != observed.ActiveReignHolder ||
		expected.ActiveReignVersion != observed.ActiveReignVersion
	if !reignChanged {
		return nil
	}
	code := "succession-rewritten"
	for _, former := range expected.FormerHolders {
		if observed.HasActiveReign && observed.ActiveReignHolder == former {
			code = "ex-king-restored"
		}
	}
	return diverge(code, fmt.Sprintf("holder=%s/%s version=%d/%d", expected.ActiveReignHolder, observed.ActiveReignHolder, expected.ActiveReignVersion, observed.ActiveReignVersion))
}

// CompareBookRestore judges the restored snapshot against the
// pre-disaster one. Conservation is strict: a one-milliINK drift, a
// missing or extra custody, a rewritten history count, a violated
// archive seal, or a rewritten succession each blocks resumption.
func CompareBookRestore(expected, observed BookFingerprint) (RestoreVerdict, error) {
	var verdict RestoreVerdict
	if err := validateRestoreSnapshot(expected); err != nil {
		return verdict, err
	}
	if err := validateRestoreSnapshot(observed); err != nil {
		return verdict, err
	}
	diverge := func(code, detail string) error {
		divergence, err := restoreDivergence(code, detail)
		if err != nil {
			return err
		}
		verdict.Divergences = append(verdict.Divergences, divergence)
		return nil
	}
	for _, judge := range []func(BookFingerprint, BookFingerprint, restoreDivergeFunc) error{
		judgeRestoreMoney,
		judgeRestoreHistory,
		judgeRestoreArchive,
		judgeRestoreSuccession,
	} {
		if err := judge(expected, observed, diverge); err != nil {
			return verdict, err
		}
	}
	return verdict, nil
}
