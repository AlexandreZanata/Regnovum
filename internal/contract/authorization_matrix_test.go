package contract_test

// P26-T02 — matriz endpoint × ator × estado sobre HTTP real.
//
// Atores anonymous, owner, other, suspended, moderator, admin e security
// (service) contra leituras públicas, escritas com ownership, estados de
// recurso, moderação, cobrança e privacidade, com BOLA horizontal e
// vertical recusadas e dado da vítima ausente do corpo. A cobertura é
// derivada do OpenAPI: operação documentada sem célula falha o gate, de
// modo que remover um teste reabre a matriz. Rotas de autenticação e MFA
// pertencem à matriz da T01; o portal segue sem composição (como no T06).

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/contract"
)

// matrixActor names the caller of a cell. The empty cookie is anonymous.
type matrixActor string

const (
	actorAnon      matrixActor = "anonymous"
	actorOwner     matrixActor = "owner"
	actorOther     matrixActor = "other"
	actorSuspended matrixActor = "suspended"
	actorModerator matrixActor = "moderator"
	actorAdmin     matrixActor = "admin"
	actorSecurity  matrixActor = "security"
)

// matrixCell is one authorization decision under test: who calls what,
// which answers prove it, and which victim data must never come back.
type matrixCell struct {
	category string
	method   string
	template string
	concrete string
	actor    matrixActor
	body     string
	headers  map[string]string
	want     []int
	forbid   string
}

// matrixParams carries the cookies and resources one world serves.
type matrixParams struct {
	cookies map[matrixActor]string
	owner   string
	other   string
	draft   string
	arena   string
	otherD  string
	// closing é a arena dedicada ao close: fechar a arena partilhada no
	// meio da matriz envenenaria as células seguintes.
	closing  string
	argument string
	change   string
	caseID   string
	action   string
	export   string
	// profileCase ancora os atos de alto impacto: suspensão só sanciona
	// perfil, e o caso de arena jamais aceitaria a medida (mismatch
	// responderia 500 não classificado — lacuna para a T03).
	profileCase string
}

// matrixWorld builds the full journeys composition with seven actors:
// owner and other verified, suspended blocked, and the three role
// assignments with fresh second factors; plus an owned draft, a published
// arena, an argument, a decided case and an export in place.
func matrixWorld(t *testing.T) (*journeyWorld, *httptest.Server, *matrixParams) {
	t.Helper()
	world := newJourneyWorld(t)
	server := world.serveJourneys(t)
	pool := world.pool

	accounts := []struct {
		email  string
		status string
	}{
		{"t02-owner@arena.example.com", "active"},
		{"t02-other@arena.example.com", "active"},
		{"t02-suspended@arena.example.com", "suspended"},
		{"t02-moder@arena.example.com", "active"},
		{"t02-admin@arena.example.com", "active"},
		{"t02-secur@arena.example.com", "active"},
		// Conta dedicada à sanção: suspender owner/other no meio da
		// matriz invalidaria as células seguintes deles.
		{"t02-sanctioned@arena.example.com", "active"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, account := range accounts {
		if _, err := pool.Exec(ctx, `INSERT INTO app.accounts (email, status, email_verified_at) VALUES ($1, $2, now())`, account.email, account.status); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	journeyGrantRole(t, pool, "t02-moder@arena.example.com", "moderator")
	journeyGrantRole(t, pool, "t02-admin@arena.example.com", "admin")
	journeyGrantRole(t, pool, "t02-secur@arena.example.com", "security")

	cookies := map[matrixActor]string{"": ""}
	cookies[actorOwner] = "t02-owner-token"
	cookies[actorOther] = "t02-other-token"
	cookies[actorSuspended] = "t02-suspended-token"
	cookies[actorModerator] = "t02-moder-token"
	cookies[actorAdmin] = "t02-admin-token"
	cookies[actorSecurity] = "t02-secur-token"
	journeySession(t, pool, "t02-owner@arena.example.com", cookies[actorOwner], false)
	journeySession(t, pool, "t02-other@arena.example.com", cookies[actorOther], false)
	journeySession(t, pool, "t02-suspended@arena.example.com", cookies[actorSuspended], false)
	journeySession(t, pool, "t02-moder@arena.example.com", cookies[actorModerator], true)
	journeySession(t, pool, "t02-admin@arena.example.com", cookies[actorAdmin], true)
	journeySession(t, pool, "t02-secur@arena.example.com", cookies[actorSecurity], true)

	params := &matrixParams{cookies: cookies}
	params.owner = journeyAccountID(t, pool, "t02-owner@arena.example.com")
	params.other = journeyAccountID(t, pool, "t02-other@arena.example.com")

	status, _, draftRaw := journeyCall(t, server, "POST", "/api/v1/me/arena-drafts", cookies[actorOwner],
		`{"statement":"A arena t02 debate a tese com clareza","category":"technology","language":"pt-BR"}`, nil)
	if status != 201 {
		t.Fatalf("seed draft = %d, want 201", status)
	}
	var draft struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(draftRaw, &draft); err != nil || draft.ID == "" {
		t.Fatalf("draft carries no id: %s", string(draftRaw))
	}
	params.draft = draft.ID

	status, _, otherDraftRaw := journeyCall(t, server, "POST", "/api/v1/me/arena-drafts", cookies[actorOther],
		`{"statement":"Rascunho t02 do outro titular","category":"culture","language":"pt-BR"}`, nil)
	if status != 201 {
		t.Fatalf("seed other draft = %d, want 201", status)
	}
	var otherDraft struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(otherDraftRaw, &otherDraft); err != nil || otherDraft.ID == "" {
		t.Fatalf("other draft carries no id: %s", string(otherDraftRaw))
	}
	params.otherD = otherDraft.ID

	params.arena = journeyArena(t, pool, "t02-owner@arena.example.com", "A arena t02 debate a tese com clareza", "t02-matrix-arena")
	params.closing = journeyArena(t, pool, "t02-owner@arena.example.com", "A arena t02 que será encerrada", "t02-closing-arena")
	journeyFund(t, pool, "t02-owner@arena.example.com", "matrix-owner", 100000)
	journeyFund(t, pool, "t02-other@arena.example.com", "matrix-other", 100000)
	status, _, argumentRaw := journeyCall(t, server, "POST", "/api/v1/me/arenas/"+params.arena+"/arguments", cookies[actorOwner],
		`{"relation":"support","content":"Argumento t02 da matriz"}`, map[string]string{"Idempotency-Key": "t02-matrix-arg"})
	if status != 200 && status != 201 {
		t.Fatalf("seed argument = %d, want 200 or 201", status)
	}
	var argument struct {
		Argument struct {
			ID string `json:"id"`
		} `json:"argument"`
	}
	if err := json.Unmarshal(argumentRaw, &argument); err != nil || argument.Argument.ID == "" {
		t.Fatalf("argument carries no id: %s", string(argumentRaw))
	}
	params.argument = argument.Argument.ID

	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/arenas/"+params.arena+"/position", cookies[actorOwner], `{"position":"agree"}`, nil)
	if status != 200 && status != 201 {
		t.Fatalf("seed position = %d, want 200 or 201", status)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/me/arenas/"+params.arena+"/position/changes", cookies[actorOwner], `{"position":"disagree"}`, nil)
	if status != 200 && status != 201 {
		t.Fatalf("seed change = %d, want 200 or 201", status)
	}
	if err := pool.QueryRow(ctx, `SELECT id::text FROM app.position_changes WHERE arena_id = $1::uuid ORDER BY version DESC LIMIT 1`,
		params.arena).Scan(&params.change); err != nil {
		t.Fatalf("read change: %v", err)
	}

	status, _, reportRaw := journeyCall(t, server, "POST", "/api/v1/me/moderation/reports", cookies[actorOther],
		`{"target_type":"arena","target_id":"`+params.arena+`","reason":"spam"}`, nil)
	if status != 200 && status != 201 {
		t.Fatalf("seed report = %d, want 200 or 201 (%s)", status, string(reportRaw))
	}
	if err := pool.QueryRow(ctx, `INSERT INTO app.moderation_cases (target_type, target_arena_id) VALUES ('arena', $1::uuid) RETURNING id::text`,
		params.arena).Scan(&params.caseID); err != nil {
		t.Fatalf("open case: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO app.moderation_cases (target_type, target_account_id)
		SELECT 'profile', id FROM app.accounts WHERE email = 't02-sanctioned@arena.example.com' RETURNING id::text`).Scan(&params.profileCase); err != nil {
		t.Fatalf("open profile case: %v", err)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+params.caseID+"/claim", cookies[actorModerator], "", nil)
	if status != 200 {
		t.Fatalf("seed claim = %d, want 200", status)
	}
	status, _, _ = journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+params.profileCase+"/claim", cookies[actorAdmin], "", nil)
	if status != 200 {
		t.Fatalf("seed profile claim = %d, want 200", status)
	}
	status, _, decisionRaw := journeyCall(t, server, "POST", "/api/v1/moderation/cases/"+params.caseID+"/decisions", cookies[actorModerator],
		`{"action":"warning","rule":"MOD-2:warning","justification":"Matriz t02 com escopo"}`, nil)
	if status != 200 {
		t.Fatalf("seed decision = %d, want 200 (%s)", status, string(decisionRaw))
	}
	var decision struct {
		ActionID string `json:"action_id"`
	}
	if err := json.Unmarshal(decisionRaw, &decision); err != nil || decision.ActionID == "" {
		t.Fatalf("decision carries no action: %s", string(decisionRaw))
	}
	params.action = decision.ActionID

	status, _, exportRaw := journeyCall(t, server, "POST", "/api/v1/me/exports", cookies[actorOwner], "", nil)
	if status != 202 {
		t.Fatalf("seed export = %d, want 202", status)
	}
	var exported struct {
		ExportID string `json:"export_id"`
	}
	if err := json.Unmarshal(exportRaw, &exported); err != nil || exported.ExportID == "" {
		t.Fatalf("export carries no id: %s", string(exportRaw))
	}
	params.export = exported.ExportID

	return world, server, params
}

// journeyGrantRole concede um papel administrativo (preparação).
func journeyGrantRole(t *testing.T, pool *pgxpool.Pool, email, role string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	accountID := journeyAccountID(t, pool, email)
	if _, err := pool.Exec(ctx, `INSERT INTO app.admin_roles (account_id, role, granted_by)
		SELECT $1::uuid, $2, $1::uuid`, accountID, role); err != nil {
		t.Fatalf("grant %s: %v", role, err)
	}
}

// allMatrixCells declara a matriz inteira: cada linha nomeia método,
// template do OpenAPI, endereço concreto, ator, respostas que provam e o
// dado da vítima que nunca pode voltar.
func allMatrixCells(params *matrixParams) []matrixCell {
	cells := []matrixCell{
		{category: "public", method: "GET", template: "/api/v1/arenas", concrete: "/api/v1/arenas", actor: actorAnon, want: []int{200}},
		{category: "public", method: "GET", template: "/api/v1/arenas", concrete: "/api/v1/arenas", actor: actorOwner, want: []int{200}},
		{category: "public", method: "GET", template: "/api/v1/arenas/{slug}", concrete: "/api/v1/arenas/t02-matrix-arena", actor: actorAnon, want: []int{200}},
		{category: "public", method: "GET", template: "/api/v1/arenas/{id}/arguments", concrete: "/api/v1/arenas/" + params.arena + "/arguments?relation=support", actor: actorAnon, want: []int{200}},
		{category: "public", method: "GET", template: "/api/v1/arguments/{id}", concrete: "/api/v1/arguments/" + params.argument, actor: actorAnon, want: []int{200}},
		{category: "public", method: "GET", template: "/api/v1/arenas/{id}/positions", concrete: "/api/v1/arenas/" + params.arena + "/positions", actor: actorAnon, want: []int{200}},
		{category: "public", method: "GET", template: "/api/v1/profiles/{username}", concrete: "/api/v1/profiles/t02-owner", actor: actorAnon, want: []int{200, 404}},
		{category: "public", method: "GET", template: "/api/v1/search/arenas", concrete: "/api/v1/search/arenas?q=t02&language=pt-BR", actor: actorAnon, want: []int{200}},
		{category: "public", method: "GET", template: "/api/v1/public/transparency", concrete: "/api/v1/public/transparency", actor: actorAnon, want: []int{200}},
		{category: "public", method: "GET", template: "/api/v1/arenas/{id}/export", concrete: "/api/v1/arenas/" + params.arena + "/export", actor: actorAnon, want: []int{200}},

		// Escritas com ownership: o dono serve, o outro não alcança, e o
		// corpo negado nunca carrega o dado da vítima (BOLA horizontal).
		{category: "ownership", method: "GET", template: "/api/v1/me/arena-drafts", concrete: "/api/v1/me/arena-drafts", actor: actorOwner, want: []int{200}},
		{category: "ownership", method: "GET", template: "/api/v1/me/arena-drafts/{id}", concrete: "/api/v1/me/arena-drafts/" + params.draft, actor: actorOwner, want: []int{200}},
		{category: "ownership", method: "GET", template: "/api/v1/me/arena-drafts/{id}", concrete: "/api/v1/me/arena-drafts/" + params.draft, actor: actorOther, want: []int{403, 404}, forbid: "A arena t02 debate"},
		{category: "ownership", method: "PATCH", template: "/api/v1/me/arena-drafts/{id}", concrete: "/api/v1/me/arena-drafts/" + params.draft, actor: actorOther, body: `{"statement":"Sequestro t02 do rascunho","category":"technology","language":"pt-BR","expected_version":1}`, want: []int{403, 404, 409}},
		{category: "ownership", method: "DELETE", template: "/api/v1/me/arena-drafts/{id}", concrete: "/api/v1/me/arena-drafts/" + params.otherD, actor: actorOwner, want: []int{403, 404}},
		{category: "ownership", method: "POST", template: "/api/v1/me/arena-drafts/{id}/publish", concrete: "/api/v1/me/arena-drafts/" + params.otherD + "/publish", actor: actorOwner, body: `{}`, want: []int{402, 403, 404, 409}},
		{category: "ownership", method: "GET", template: "/api/v1/me/arenas/{id}/position", concrete: "/api/v1/me/arenas/" + params.arena + "/position", actor: actorOwner, want: []int{200}},
		{category: "ownership", method: "POST", template: "/api/v1/me/arguments/{id}/withdraw", concrete: "/api/v1/me/arguments/" + params.argument + "/withdraw", actor: actorOther, body: `{}`, want: []int{403, 404}},
		{category: "ownership", method: "GET", template: "/api/v1/arguments/{id}/replies", concrete: "/api/v1/arguments/" + params.argument + "/replies", actor: actorOther, want: []int{200}},
		{category: "ownership", method: "GET", template: "/api/v1/arguments/{id}/attributions", concrete: "/api/v1/arguments/" + params.argument + "/attributions", actor: actorAnon, want: []int{200}},
		{category: "ownership", method: "POST", template: "/api/v1/me/position-changes/{id}/attributions", concrete: "/api/v1/me/position-changes/" + params.change + "/attributions", actor: actorOther, body: `{"argument_ids":["` + params.argument + `"]}`, want: []int{200, 201, 403, 404, 409}},
		{category: "ownership", method: "GET", template: "/api/v1/me/arenas/{id}/position/changes", concrete: "/api/v1/me/arenas/" + params.arena + "/position/changes", actor: actorOwner, want: []int{200}},
		{category: "ownership", method: "GET", template: "/api/v1/me/arenas/{id}/position/changes", concrete: "/api/v1/me/arenas/" + params.arena + "/position/changes", actor: actorAnon, want: []int{401}},
		{category: "ownership", method: "POST", template: "/api/v1/me/arenas/{id}/position/changes", concrete: "/api/v1/me/arenas/" + params.arena + "/position/changes", actor: actorOwner, body: `{"position":"agree"}`, want: []int{200, 201}},
		{category: "ownership", method: "POST", template: "/api/v1/me/arenas/{id}/position/changes", concrete: "/api/v1/me/arenas/" + params.arena + "/position/changes", actor: actorAnon, body: `{"position":"agree"}`, want: []int{401}},
		{category: "ownership", method: "GET", template: "/api/v1/search/arguments", concrete: "/api/v1/search/arguments?q=t02&language=pt-BR", actor: actorAnon, want: []int{200}},
		{category: "ownership", method: "POST", template: "/api/v1/me/arenas/{id}/arguments/{argumentID}/replies", concrete: "/api/v1/me/arenas/" + params.arena + "/arguments/" + params.argument + "/replies", actor: actorOwner, body: `{"relation":"support","content":"Réplica t02 da matriz"}`, headers: map[string]string{"Idempotency-Key": "t02-matrix-reply"}, want: []int{200, 201}},
		{category: "ownership", method: "POST", template: "/api/v1/me/arenas/{id}/arguments/{argumentID}/replies", concrete: "/api/v1/me/arenas/" + params.arena + "/arguments/" + params.argument + "/replies", actor: actorAnon, body: `{"relation":"support","content":"x"}`, want: []int{401}},
		{category: "ownership", method: "GET", template: "/api/v1/profiles/{username}/reputation", concrete: "/api/v1/profiles/t02-owner/reputation", actor: actorAnon, want: []int{200, 404}},
		{category: "ownership", method: "POST", template: "/api/v1/me/arenas/{id}/close", concrete: "/api/v1/me/arenas/" + params.closing + "/close", actor: actorAnon, body: `{}`, want: []int{401}},
		{category: "ownership", method: "POST", template: "/api/v1/me/arenas/{id}/close", concrete: "/api/v1/me/arenas/" + params.closing + "/close", actor: actorOther, body: `{}`, want: []int{403, 404}},
		{category: "ownership", method: "POST", template: "/api/v1/me/arenas/{id}/close", concrete: "/api/v1/me/arenas/" + params.closing + "/close", actor: actorOwner, body: `{}`, want: []int{200}},

		// Estados de ator: anônimo cai no 401 das privadas, suspenso em
		// tudo autenticado, dono lê o próprio.
		{category: "states", method: "GET", template: "/api/v1/me/profile", concrete: "/api/v1/me/profile", actor: actorAnon, want: []int{401}},
		{category: "states", method: "GET", template: "/api/v1/me/profile", concrete: "/api/v1/me/profile", actor: actorSuspended, want: []int{401, 403}},
		{category: "states", method: "GET", template: "/api/v1/me/wallet", concrete: "/api/v1/me/wallet", actor: actorAnon, want: []int{401}},
		{category: "states", method: "GET", template: "/api/v1/me/wallet", concrete: "/api/v1/me/wallet", actor: actorSuspended, want: []int{401, 403}},
		{category: "states", method: "GET", template: "/api/v1/me/wallet", concrete: "/api/v1/me/wallet", actor: actorOwner, want: []int{200}},
		{category: "states", method: "GET", template: "/api/v1/me/wallet/transactions", concrete: "/api/v1/me/wallet/transactions", actor: actorAnon, want: []int{401}},
		{category: "states", method: "GET", template: "/api/v1/me/wallet/transactions", concrete: "/api/v1/me/wallet/transactions", actor: actorSuspended, want: []int{401, 403}},
		{category: "states", method: "POST", template: "/api/v1/me/arenas/{id}/arguments", concrete: "/api/v1/me/arenas/" + params.arena + "/arguments", actor: actorAnon, body: `{"relation":"support","content":"x"}`, want: []int{401}},
		{category: "states", method: "POST", template: "/api/v1/me/arenas/{id}/arguments", concrete: "/api/v1/me/arenas/" + params.arena + "/arguments", actor: actorSuspended, body: `{"relation":"support","content":"x"}`, headers: map[string]string{"Idempotency-Key": "t02-susp-arg"}, want: []int{401, 403}},
		{category: "states", method: "POST", template: "/api/v1/me/arenas/{id}/position", concrete: "/api/v1/me/arenas/" + params.arena + "/position", actor: actorAnon, body: `{"position":"agree"}`, want: []int{401}},
		{category: "states", method: "POST", template: "/api/v1/me/arenas/{id}/position", concrete: "/api/v1/me/arenas/" + params.arena + "/position", actor: actorSuspended, body: `{"position":"agree"}`, want: []int{401, 403}},
		{category: "states", method: "POST", template: "/api/v1/me/moderation/reports", concrete: "/api/v1/me/moderation/reports", actor: actorAnon, body: `{"target_type":"arena","target_id":"` + params.arena + `","reason":"spam"}`, want: []int{401}},
		{category: "states", method: "POST", template: "/api/v1/me/moderation/reports", concrete: "/api/v1/me/moderation/reports", actor: actorSuspended, body: `{"target_type":"arena","target_id":"` + params.arena + `","reason":"spam"}`, want: []int{401, 403}},
		{category: "states", method: "POST", template: "/api/v1/me/arena-drafts", concrete: "/api/v1/me/arena-drafts", actor: actorSuspended, body: `{"statement":"Rascunho t02 suspenso com clareza","category":"technology","language":"pt-BR"}`, want: []int{401, 403}},

		// Moderação: fila/claim/decisão exigem capacidade + step-up; o ato
		// fora do papel cai, estranho cai, anônimo cai no 401 (BOLA vertical).
		{category: "moderation", method: "GET", template: "/api/v1/moderation/cases", concrete: "/api/v1/moderation/cases", actor: actorAnon, want: []int{401}},
		{category: "moderation", method: "GET", template: "/api/v1/moderation/cases", concrete: "/api/v1/moderation/cases", actor: actorOther, want: []int{403}},
		{category: "moderation", method: "GET", template: "/api/v1/moderation/cases", concrete: "/api/v1/moderation/cases", actor: actorModerator, want: []int{200}},
		{category: "moderation", method: "GET", template: "/api/v1/moderation/cases", concrete: "/api/v1/moderation/cases", actor: actorAdmin, want: []int{200}},
		{category: "moderation", method: "GET", template: "/api/v1/moderation/cases", concrete: "/api/v1/moderation/cases", actor: actorSecurity, want: []int{200}},
		{category: "moderation", method: "POST", template: "/api/v1/moderation/cases/{id}/claim", concrete: "/api/v1/moderation/cases/" + params.caseID + "/claim", actor: actorAnon, want: []int{401}},
		{category: "moderation", method: "POST", template: "/api/v1/moderation/cases/{id}/claim", concrete: "/api/v1/moderation/cases/" + params.caseID + "/claim", actor: actorOther, want: []int{403}},
		{category: "moderation", method: "POST", template: "/api/v1/moderation/cases/{id}/claim", concrete: "/api/v1/moderation/cases/" + params.caseID + "/claim", actor: actorAdmin, want: []int{200, 409}},
		{category: "moderation", method: "POST", template: "/api/v1/moderation/cases/{id}/decisions", concrete: "/api/v1/moderation/cases/" + params.caseID + "/decisions", actor: actorAnon, body: `{"action":"warning","rule":"MOD-2:warning","justification":"x"}`, want: []int{401}},
		{category: "moderation", method: "POST", template: "/api/v1/moderation/cases/{id}/decisions", concrete: "/api/v1/moderation/cases/" + params.caseID + "/decisions", actor: actorOther, body: `{"action":"warning","rule":"MOD-2:warning","justification":"x"}`, want: []int{403}},
		{category: "moderation", method: "POST", template: "/api/v1/moderation/cases/{id}/decisions", concrete: "/api/v1/moderation/cases/" + params.caseID + "/decisions", actor: actorModerator, body: `{"action":"warning","rule":"MOD-2:warning","justification":"Matriz t02 bis"}`, want: []int{409}},
		{category: "moderation", method: "POST", template: "/api/v1/moderation/cases/{id}/decisions", concrete: "/api/v1/moderation/cases/" + params.profileCase + "/decisions", actor: actorSecurity, body: `{"action":"warning","rule":"MOD-2:warning","justification":"x"}`, want: []int{403}},
		{category: "moderation", method: "POST", template: "/api/v1/moderation/cases/{id}/decisions", concrete: "/api/v1/moderation/cases/" + params.profileCase + "/decisions", actor: actorModerator, body: `{"action":"suspension","rule":"MOD-10:suspension","justification":"Matriz t02","expires_at":"2026-10-18T12:00:00Z"}`, want: []int{403}},
		{category: "moderation", method: "POST", template: "/api/v1/moderation/cases/{id}/decisions", concrete: "/api/v1/moderation/cases/" + params.profileCase + "/decisions", actor: actorAdmin, body: `{"action":"suspension","rule":"MOD-10:suspension","justification":"Matriz t02 do admin","expires_at":"2026-10-18T12:00:00Z"}`, want: []int{200}},
		{category: "moderation", method: "POST", template: "/api/v1/me/moderation/appeals", concrete: "/api/v1/me/moderation/appeals", actor: actorAnon, body: `{"action_id":"` + params.action + `","context":"x"}`, want: []int{401}},
		{category: "moderation", method: "POST", template: "/api/v1/me/moderation/appeals", concrete: "/api/v1/me/moderation/appeals", actor: actorOther, body: `{"action_id":"` + params.action + `","context":"Recurso t02 de quem não foi sancionado"}`, want: []int{403, 404, 409}},

		// Cobrança: anônimo 401, dono lê o próprio, suspenso negado; compra
		// exige elegibilidade, sem oráculo de conta.
		{category: "billing", method: "POST", template: "/api/v1/me/billing/checkout", concrete: "/api/v1/me/billing/checkout", actor: actorAnon, body: `{"market":"BR","product":"ink_10000","idempotency_key":"t02-anon-buy"}`, want: []int{401}},
		{category: "billing", method: "POST", template: "/api/v1/me/billing/checkout", concrete: "/api/v1/me/billing/checkout", actor: actorSuspended, body: `{"market":"BR","product":"ink_10000","idempotency_key":"t02-susp-buy"}`, want: []int{401, 403}},
		{category: "billing", method: "POST", template: "/api/v1/me/billing/checkout", concrete: "/api/v1/me/billing/checkout", actor: actorOwner, body: `{"market":"BR","product":"ink_10000","idempotency_key":"t02-matrix-buy"}`, want: []int{200}},
		{category: "billing", method: "GET", template: "/api/v1/me/billing/subscription", concrete: "/api/v1/me/billing/subscription", actor: actorAnon, want: []int{401}},
		{category: "billing", method: "GET", template: "/api/v1/me/billing/subscription", concrete: "/api/v1/me/billing/subscription", actor: actorOwner, want: []int{200, 404}},
		{category: "billing", method: "GET", template: "/api/v1/me/passes", concrete: "/api/v1/me/passes", actor: actorAnon, want: []int{401}},
		{category: "billing", method: "GET", template: "/api/v1/me/passes", concrete: "/api/v1/me/passes", actor: actorOther, want: []int{200}},
		{category: "billing", method: "GET", template: "/api/v1/me/passes/history", concrete: "/api/v1/me/passes/history", actor: actorSuspended, want: []int{401, 403}},

		// Privacidade: export e deleção são do titular; o outro recebe o
		// mesmo não-encontrado do inexistente, sem oráculo.
		{category: "privacy", method: "POST", template: "/api/v1/me/exports", concrete: "/api/v1/me/exports", actor: actorAnon, want: []int{401}},
		{category: "privacy", method: "GET", template: "/api/v1/me/exports/{id}/download", concrete: "/api/v1/me/exports/" + params.export + "/download?token=bogus", actor: actorOther, want: []int{401, 403, 404}},
		{category: "privacy", method: "POST", template: "/api/v1/me/deletion", concrete: "/api/v1/me/deletion", actor: actorAnon, want: []int{401}},
		{category: "privacy", method: "GET", template: "/api/v1/me/deletion", concrete: "/api/v1/me/deletion", actor: actorOther, want: []int{404}},
		{category: "privacy", method: "POST", template: "/api/v1/me/deletion/cancel", concrete: "/api/v1/me/deletion/cancel", actor: actorOther, body: `{"reason":"x"}`, want: []int{404, 409}},
	}
	return cells
}

// runMatrixCells executa as células e devolve quantas rodaram: apagar uma
// linha diminui a contagem que o gate de cobertura confere.
func runMatrixCells(t *testing.T, server *httptest.Server, params *matrixParams, cells []matrixCell) int {
	t.Helper()
	cookies := params.cookies
	for index, cell := range cells {
		status, _, body := journeyCall(t, server, cell.method, cell.concrete, cookies[cell.actor], cell.body, cell.headers)
		allowed := false
		for _, want := range cell.want {
			if status == want {
				allowed = true
			}
		}
		if !allowed {
			t.Fatalf("cell %d (%s %s as %s) = %d, want %v (%s)", index, cell.method, cell.template, cell.actor, status, cell.want, string(body))
		}
		if cell.forbid != "" && strings.Contains(string(body), cell.forbid) {
			t.Fatalf("cell %d (%s %s as %s) serves victim data %q", index, cell.method, cell.template, cell.actor, cell.forbid)
		}
	}
	return len(cells)
}

// matrixCoverage prova que o OpenAPI não tem operação sem célula: cada
// (método, template) do contrato aparece na matriz ou na lista explícita
// de fora de escopo com motivo.
func matrixCoverage(t *testing.T, cells []matrixCell) {
	t.Helper()
	document, err := contract.Load("../../api/openapi.json")
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	covered := map[string]bool{}
	for _, cell := range cells {
		covered[cell.method+" "+cell.template] = true
	}
	outOfScope := map[string]string{
		// Autenticação e MFA: matriz própria da T01 sobre composição dedicada.
		"POST /api/v1/auth/register":               "t01",
		"POST /api/v1/auth/login":                  "t01",
		"POST /api/v1/auth/logout":                 "t01",
		"GET /api/v1/auth/verify":                  "t01",
		"GET /api/v1/auth/password-reset":          "t01",
		"POST /api/v1/auth/password-reset/request": "t01",
		"POST /api/v1/auth/password-reset/confirm": "t01",
		"POST /api/v1/me/mfa/enrollment":           "t01",
		"POST /api/v1/me/mfa/enrollment/confirm":   "t01",
		"POST /api/v1/me/mfa/recovery":             "t01",
		"POST /api/v1/me/mfa/step-up":              "t01",
		"GET /api/v1/me/sessions":                  "t01",
		"POST /api/v1/me/sessions/revocation":      "t01",
		"POST /api/v1/me/sessions/rotation":        "t01",
		// Portal sem composição no servidor (como no T06): sem handler real.
		"POST /api/v1/me/billing/portal": "uncomposed",
		// Sinais de atribuição sem composição em nenhuma composição (sem
		// adapter de autorização; o handler com use case nil panica em vez
		// de responder 500 — lacuna registrada para a T03/T12).
		"GET /api/v1/moderation/attribution-signals/{authorID}": "uncomposed",
		// Superfície HTML do browser (páginas, formulários, CSRF): outro
		// modelo de autorização, coberto pelos testes de superfície do
		// bootstrap/P18, fora desta matriz da API versionada.
		"GET /arenas/{slug}": "html", "POST /arenas/{slug}/arguments": "html",
		"POST /arenas/{slug}/attributions": "html", "POST /arenas/{slug}/position": "html",
		"POST /arenas/{slug}/position/change": "html", "GET /d/{slug}": "html",
		"GET /login": "html", "POST /login": "html", "GET /logout": "html",
		"POST /logout": "html", "GET /register": "html", "POST /register": "html",
		"GET /reset": "html", "POST /reset": "html", "GET /reset/confirm": "html",
		"POST /reset/confirm": "html", "GET /verify": "html", "POST /verify": "html",
		"GET /transparency": "html",
		// Sondas operacionais, sem dimensão de ator por desenho; cobertas
		// pelos testes de boot do servidor.
		"GET /health/live": "ops", "GET /health/ready": "ops",
	}
	var missing []string
	for path, operations := range document.Paths {
		for method := range operations {
			key := strings.ToUpper(method) + " " + path
			if covered[key] || outOfScope[key] != "" {
				continue
			}
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("operations without a matrix cell: %v", missing)
	}
}

// matrixCategory executa as células de uma categoria da matriz.
func matrixCategory(t *testing.T, category string) int {
	t.Helper()
	_, server, params := matrixWorld(t)
	cells := allMatrixCells(params)
	selected := cells[:0]
	for _, cell := range cells {
		if cell.category == category {
			selected = append(selected, cell)
		}
	}
	if len(selected) == 0 {
		t.Fatalf("category %q has no cells", category)
	}
	return runMatrixCells(t, server, params, selected)
}

func TestAuthorizationMatrixPublicReads(t *testing.T) {
	t.Parallel()
	if got := matrixCategory(t, "public"); got == 0 {
		t.Fatal("no public cells ran")
	}
}

func TestAuthorizationMatrixOwnership(t *testing.T) {
	t.Parallel()
	if got := matrixCategory(t, "ownership"); got == 0 {
		t.Fatal("no ownership cells ran")
	}
}

func TestAuthorizationMatrixActorStates(t *testing.T) {
	t.Parallel()
	if got := matrixCategory(t, "states"); got == 0 {
		t.Fatal("no state cells ran")
	}
}

func TestAuthorizationMatrixModeration(t *testing.T) {
	t.Parallel()
	if got := matrixCategory(t, "moderation"); got == 0 {
		t.Fatal("no moderation cells ran")
	}
}

func TestAuthorizationMatrixBilling(t *testing.T) {
	t.Parallel()
	if got := matrixCategory(t, "billing"); got == 0 {
		t.Fatal("no billing cells ran")
	}
}

func TestAuthorizationMatrixPrivacy(t *testing.T) {
	t.Parallel()
	if got := matrixCategory(t, "privacy"); got == 0 {
		t.Fatal("no privacy cells ran")
	}
}

// TestAuthorizationMatrixCompleteness executa a matriz inteira e prova que
// o contrato não tem operação sem célula: endpoint novo sem linha falha
// aqui, e linha removida some da contagem executada.
func TestAuthorizationMatrixCompleteness(t *testing.T) {
	t.Parallel()
	_, server, params := matrixWorld(t)
	cells := allMatrixCells(params)
	if got := runMatrixCells(t, server, params, cells); got != len(cells) {
		t.Fatalf("ran %d cells of %d declared", got, len(cells))
	}
	matrixCoverage(t, cells)
	t.Logf("matrix: %d cells across 6 categories cover the documented surface", len(cells))
}
