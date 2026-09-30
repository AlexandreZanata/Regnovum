package http_test

// P39-T08 — the staged private case API serves the lifecycle on a
// local mux without mounting anything on the process router.
//
// One journey case carries a half-accepted proposal the second party
// accepts and defends; one ruled case carries a reasoned decision
// the parties read and contest once. Missing sessions refuse with
// 401, strangers with 403 on moves and 404 on reads, divergent moves
// with 409; every answer is private no-store, and the same file
// prices identically in pt and en. The route list and the OpenAPI
// fragment describe each other exactly, and no proof digest, ground,
// object or account ever reaches a log line. The suite stores
// nothing outside memory and activates nothing: the package
// registers no route on import and the tests mount only a local mux.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adapterhttp "github.com/AlexandreZanata/Regnovum/internal/disputes/adapters/http"
	disputesapp "github.com/AlexandreZanata/Regnovum/internal/disputes/application"
	disputesdomain "github.com/AlexandreZanata/Regnovum/internal/disputes/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

const (
	disputeClaimantToken   = "dispute-claimant-session-token"
	disputeRespondentToken = "dispute-respondent-session-token"
	disputeStrangerToken   = "dispute-stranger-session-token"
)

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time { return c.now }

func disputeInstant() time.Time {
	return time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
}

type memoryCaseRecords struct {
	files map[string]disputesapp.CaseRecord
}

func newMemoryCaseRecords() *memoryCaseRecords {
	return &memoryCaseRecords{files: map[string]disputesapp.CaseRecord{}}
}

func (m *memoryCaseRecords) Get(key string) (disputesapp.CaseRecord, error) {
	record, ok := m.files[key]
	if !ok {
		return disputesapp.CaseRecord{}, disputesdomain.ErrUnknownCase
	}
	return record, nil
}

func (m *memoryCaseRecords) Put(record disputesapp.CaseRecord) error {
	m.files[record.Proposal.Key] = record
	return nil
}

type disputeHarness struct {
	mux        http.Handler
	sec        *security.Manager
	logs       *[]string
	clock      fixedClock
	claimant   string
	respondent string
}

func mustDisputeSecurity(t *testing.T) *security.Manager {
	t.Helper()
	secMgr, err := security.New(security.Options{
		Env:            config.EnvTest,
		AllowedOrigins: []string{"http://example.com"},
		RequireOrigin:  false,
		Clock:          clockseed.NewClock(),
		Random:         clockseed.NewRandom(),
	})
	if err != nil {
		t.Fatalf("setup security manager: %v", err)
	}
	return secMgr
}

func disputePropose(t *testing.T, key string, deadline time.Time) disputesdomain.Proposal {
	t.Helper()
	proposal, err := disputesdomain.Propose(disputesdomain.ProposalRequest{
		Key: key, Version: 1, Object: "entrega do lote 7",
		Claimant: "requerente", Respondent: "requerida", ValueMilli: 20000,
		EscrowRef: "caucao-" + key, Rite: "rito-acordo-v1", Evidence: "regras-prova-v1",
		Costs: disputesdomain.CostsSplit, ExpiresAt: deadline, Execution: "liberar caucao ao adimplente",
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	return proposal
}

// newDisputeHarness files three cases on one timeline (now is noon,
// the staged clock reads 13:00): a proposal case with one
// acceptance (the respondent accepts over HTTP), a journey case
// with an instructed hearing (both sides defend over HTTP) and a
// ruled case with a reasoned decision the parties read and appeal
// once.
func newDisputeHarness(t *testing.T) *disputeHarness {
	t.Helper()
	secMgr := mustDisputeSecurity(t)
	stores := newMemoryCaseRecords()
	now := disputeInstant()
	clock := fixedClock{now: now.Add(time.Hour)}

	proposal, err := disputePropose(t, "caso-proposta", now.Add(48*time.Hour)).Accept("requerente", now)
	if err != nil {
		t.Fatalf("proposal first Accept: %v", err)
	}
	if err := stores.Put(disputesapp.CaseRecord{Proposal: proposal}); err != nil {
		t.Fatalf("store proposal: %v", err)
	}

	journeyProposal := disputePropose(t, "caso-jornada", now.Add(48*time.Hour))
	journeyOnce, err := journeyProposal.Accept("requerente", now)
	if err != nil {
		t.Fatalf("journey first Accept: %v", err)
	}
	journeyBound, err := journeyOnce.Accept("requerida", now)
	if err != nil {
		t.Fatalf("journey second Accept: %v", err)
	}
	journeyEntry, err := disputesdomain.OpenConsentCase(disputesdomain.CaseArbitration, journeyBound, "requerente", now)
	if err != nil {
		t.Fatalf("journey OpenConsentCase: %v", err)
	}
	journeyHearing, err := disputesdomain.OpenHearing(journeyEntry, journeyBound, "arbitro-1", now.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("journey OpenHearing: %v", err)
	}
	if err := stores.Put(disputesapp.CaseRecord{
		Proposal: journeyBound, Entry: journeyEntry, Hearing: &journeyHearing,
	}); err != nil {
		t.Fatalf("store journey: %v", err)
	}

	ruledProposal := disputePropose(t, "caso-sentenca", now.Add(48*time.Hour))
	once, err := ruledProposal.Accept("requerente", now)
	if err != nil {
		t.Fatalf("ruled first Accept: %v", err)
	}
	bound, err := once.Accept("requerida", now)
	if err != nil {
		t.Fatalf("ruled second Accept: %v", err)
	}
	entry, err := disputesdomain.OpenConsentCase(disputesdomain.CaseArbitration, bound, "requerente", now)
	if err != nil {
		t.Fatalf("ruled OpenConsentCase: %v", err)
	}
	hearing, err := disputesdomain.OpenHearing(entry, bound, "arbitro-1", now.Add(20*time.Minute))
	if err != nil {
		t.Fatalf("ruled OpenHearing: %v", err)
	}
	hearing, err = hearing.SubmitEvidence("requerente", "prova-requerente", now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("ruled claimant evidence: %v", err)
	}
	hearing, err = hearing.SubmitEvidence("requerida", "prova-requerida", now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("ruled respondent evidence: %v", err)
	}
	decision, err := hearing.Decide("arbitro-1", disputesdomain.VerdictUpholdClaimant, 10000,
		"aplica o termo aceito ao lote 7", now.Add(30*time.Minute), now.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("ruled Decide: %v", err)
	}
	if err := stores.Put(disputesapp.CaseRecord{
		Proposal: bound, Entry: entry, Hearing: &hearing, Decision: &decision,
	}); err != nil {
		t.Fatalf("store ruled: %v", err)
	}

	accept, err := disputesapp.NewAcceptCaseUseCase(stores)
	if err != nil {
		t.Fatalf("NewAcceptCaseUseCase: %v", err)
	}
	defend, err := disputesapp.NewDefendCaseUseCase(stores)
	if err != nil {
		t.Fatalf("NewDefendCaseUseCase: %v", err)
	}
	appeal, err := disputesapp.NewAppealCaseUseCase(stores)
	if err != nil {
		t.Fatalf("NewAppealCaseUseCase: %v", err)
	}
	read, err := disputesapp.NewReadCaseFileUseCase(stores)
	if err != nil {
		t.Fatalf("NewReadCaseFileUseCase: %v", err)
	}
	var logs []string
	handler, err := adapterhttp.NewHandler(adapterhttp.HandlerConfig{
		Accept: accept, Defend: defend, Appeal: appeal, Read: read,
		Clock: clock, Security: secMgr, Logger: func(entry string) { logs = append(logs, entry) },
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if _, err := adapterhttp.NewHandler(adapterhttp.HandlerConfig{}); err == nil {
		t.Fatal("nil use cases must refuse composition")
	}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	validator := security.SessionValidatorFunc(func(_ context.Context, rawToken string) (security.AuthIdentity, error) {
		switch rawToken {
		case disputeClaimantToken:
			return security.AuthIdentity{AccountID: "requerente", SessionID: "session-claimant"}, nil
		case disputeRespondentToken:
			return security.AuthIdentity{AccountID: "requerida", SessionID: "session-respondent"}, nil
		case disputeStrangerToken:
			return security.AuthIdentity{AccountID: "estranha", SessionID: "session-stranger"}, nil
		default:
			return security.AuthIdentity{}, errors.New("unknown session")
		}
	})
	return &disputeHarness{
		mux: secMgr.AuthenticateMiddleware(validator)(mux), sec: secMgr,
		logs: &logs, clock: clock, claimant: "requerente", respondent: "requerida",
	}
}

func disputeCSRF(t *testing.T, h *disputeHarness) (cookie, header string) {
	t.Helper()
	token, err := h.sec.CSRF().GenerateToken()
	if err != nil {
		t.Fatalf("generate CSRF token: %v", err)
	}
	return token, token
}

func disputeRequest(t *testing.T, h *disputeHarness, method, path, token, locale string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, reader)
	if token != "" {
		request.AddCookie(&http.Cookie{Name: "arena_session", Value: token})
	}
	if method == http.MethodPost {
		csrfCookie, csrfHeader := disputeCSRF(t, h)
		request.AddCookie(&http.Cookie{Name: "arena_csrf", Value: csrfCookie})
		request.Header.Set("X-CSRF-Token", csrfHeader)
	}
	if locale != "" {
		request.Header.Set("Accept-Language", locale)
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

func disputeDecode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode JSON body: %v (body: %s)", err, string(body))
	}
	return object
}

func assertDisputeNoStore(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
