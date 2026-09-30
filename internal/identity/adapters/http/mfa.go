package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httperror"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// maxMFABodyBytes bounds the small JSON bodies of this surface: the largest
// document is a code and an account identifier.
const maxMFABodyBytes = 4 << 10

// BeginMFAEnrollment handles POST /api/v1/me/mfa/enrollment.
//
// The response carries the secret and the `otpauth` URI, and it is the only
// time they are ever sent: nothing stores them in the clear, and the enrollment
// stays pending until a code proves the transfer.
func (h *Handler) BeginMFAEnrollment(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok || identity.AccountID == "" {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}
	if h.beginMFA == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "second factor unavailable"))
		return
	}

	result, err := h.beginMFA.Execute(r.Context(), application.BeginMFAEnrollmentCommand{AccountID: identity.AccountID})
	if err != nil {
		writeMFAProblem(w, r, err)
		return
	}

	writeJSONBody(w, http.StatusOK, map[string]string{
		"secret": result.Secret,
		"uri":    result.URI,
	})
}

// ConfirmMFAEnrollment handles POST /api/v1/me/mfa/enrollment/confirm.
func (h *Handler) ConfirmMFAEnrollment(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok || identity.AccountID == "" {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	var body struct {
		Code string `json:"code"`
	}
	if !decodeMFABody(w, r, &body) {
		return
	}
	if h.confirmMFA == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "second factor unavailable"))
		return
	}

	result, err := h.confirmMFA.Execute(r.Context(), application.ConfirmMFAEnrollmentCommand{
		AccountID: identity.AccountID,
		Code:      body.Code,
	})
	if err != nil {
		writeMFAProblem(w, r, err)
		return
	}

	// The recovery codes are shown exactly once: they are stored hashed, so
	// this response is the only place they exist in the clear.
	writeJSONBody(w, http.StatusOK, map[string]any{"backup_codes": result.BackupCodes})
}

// StepUpMFA handles POST /api/v1/me/mfa/step-up: it verifies a code and
// elevates the calling session.
func (h *Handler) StepUpMFA(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok || identity.AccountID == "" {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	var body struct {
		Code string `json:"code"`
	}
	if !decodeMFABody(w, r, &body) {
		return
	}
	if h.stepUpMFA == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "second factor unavailable"))
		return
	}

	if err := h.stepUpMFA.Execute(r.Context(), application.StepUpMFACommand{
		AccountID: identity.AccountID,
		SessionID: identity.SessionID,
		Code:      body.Code,
	}); err != nil {
		writeMFAProblem(w, r, err)
		return
	}

	writeJSONBody(w, http.StatusOK, map[string]string{"status": "elevated"})
}

// RecoverMFA handles POST /api/v1/me/mfa/recovery: it spends one recovery code
// and elevates the calling session.
//
// It is a separate route from the step-up on purpose. Both end in an elevated
// session, but only one of them spends a one-time code, and the difference is
// the fact the audit trail records.
func (h *Handler) RecoverMFA(w http.ResponseWriter, r *http.Request) {
	setPrivateNoStoreHeaders(w)

	identity, ok := security.FromContext(r.Context())
	if !ok || identity.AccountID == "" {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
		return
	}

	var body struct {
		Code string `json:"code"`
	}
	if !decodeMFABody(w, r, &body) {
		return
	}
	if h.recoverMFA == nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "second factor unavailable"))
		return
	}

	if err := h.recoverMFA.Execute(r.Context(), application.RecoverMFACommand{
		AccountID: identity.AccountID,
		SessionID: identity.SessionID,
		Code:      body.Code,
	}); err != nil {
		writeMFAProblem(w, r, err)
		return
	}

	writeJSONBody(w, http.StatusOK, map[string]string{"status": "elevated"})
}

// decodeMFABody decodes one small JSON body, answering the caller itself when
// it cannot. A body that is not JSON is a validation problem, not an internal
// one, so the code is stable and the caller can fix it.
func decodeMFABody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxMFABodyBytes)
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindValidation, "invalid_json", "request body must be valid JSON"))
		return false
	}
	return true
}

// writeJSONBody writes a JSON document with the private, no-store headers the
// surface requires.
func writeJSONBody(w http.ResponseWriter, status int, document any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(document)
}

// writeMFAProblem maps the module's refusals to the product's problem codes.
//
// The mapping lives here and not in the use cases because the code is a wire
// fact: the application layer names what happened in its own vocabulary, and
// this adapter is the only layer that knows which HTTP problem that is. The
// switch is deliberately explicit — a new refusal that nobody mapped falls to
// the default and is reported as an internal failure, never as a success.
func writeMFAProblem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrMFAAuthenticationRequired):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindUnauthorized, "unauthorized", "authentication required"))
	case errors.Is(err, application.ErrMFANotEnrolled):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "mfa_not_enrolled", "this account has no confirmed second factor"))
	case errors.Is(err, application.ErrMFAAlreadyEnrolled):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindConflict, "mfa_already_enrolled", "this account already has a confirmed second factor"))
	case errors.Is(err, application.ErrMFAEnrollmentMissing), errors.Is(err, application.ErrMFAPendingEnrollmentMissing):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindNotFound, "mfa_enrollment_missing", "start the enrollment before confirming it"))
	case errors.Is(err, application.ErrMFACodeReplayed):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "mfa_code_replayed", "this second factor code was already used"))
	case errors.Is(err, application.ErrMFACodeInvalid):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindForbidden, "mfa_code_invalid", "the second factor code is not valid"))
	case errors.Is(err, application.ErrMFAUnavailable), errors.Is(err, application.ErrMFASecretUnavailable):
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "mfa_unavailable", "the second factor could not be verified"))
	default:
		_ = httperror.WriteProblem(w, r, apperr.New(apperr.KindInternal, "server_error", "the second factor could not be processed"))
	}
}
