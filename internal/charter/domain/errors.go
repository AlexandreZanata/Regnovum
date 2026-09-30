package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	// CodeInvalidCharter names a malformed charter claim: a bad
	// version token, an unknown locale, a non-hash content digest, a
	// missing effective instant or a broken chain link.
	CodeInvalidCharter ErrorCode = "CHARTER_INVALID_CHARTER"
	// CodeUnknownVersion names a charter version the chain cannot
	// produce: nothing published, nothing yet effective, or a link
	// to a release that never sealed. Missing versions block new
	// acceptances instead of inferring rules.
	CodeUnknownVersion ErrorCode = "CHARTER_UNKNOWN_VERSION"
	// CodeHarmfulRetroactivity names a newer procedure proposed for
	// an older fact with prejudice: the contemporary rule stays, the
	// new procedure waits for new facts.
	CodeHarmfulRetroactivity ErrorCode = "CHARTER_HARMFUL_RETROACTIVITY"
	// CodeRetroactiveCorrection names a correction backdated before
	// now: corrections take effect prospectively only.
	CodeRetroactiveCorrection ErrorCode = "CHARTER_RETROACTIVE_CORRECTION"
)

// DomainError represents an invariant or rule failure in the charter domain.
type DomainError struct {
	Code    ErrorCode
	Message string
}

func (e DomainError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e DomainError) Is(target error) bool {
	t, ok := target.(DomainError)
	if !ok {
		return false
	}
	return e.Code == t.Code
}

var (
	// ErrInvalidCharter refuses a charter release that cannot seal:
	// abusive tokens, a non-digest hash, a missing instant or a link
	// that does not continue the locale line.
	ErrInvalidCharter = DomainError{
		Code:    CodeInvalidCharter,
		Message: "charter release needs a vN version, a pt/en locale, a hex64 content digest, an effective instant and the tip version as previous",
	}
	// ErrUnknownVersion refuses to judge or accept under a version
	// the chain cannot produce: the reader waits for publication,
	// never invents rules.
	ErrUnknownVersion = DomainError{
		Code:    CodeUnknownVersion,
		Message: "no charter version covers this locale and instant: unpublished versions block new acceptances",
	}
	// ErrHarmfulRetroactivity refuses a newer procedure for an older
	// fact with prejudice: facts keep their contemporary rule.
	ErrHarmfulRetroactivity = DomainError{
		Code:    CodeHarmfulRetroactivity,
		Message: "new procedure with prejudice never reaches older facts: the contemporary rule judges",
	}
	// ErrRetroactiveCorrection refuses a backdated correction:
	// corrections bind future facts only.
	ErrRetroactiveCorrection = DomainError{
		Code:    CodeRetroactiveCorrection,
		Message: "charter corrections take effect prospectively: backdated corrections refuse",
	}
)
