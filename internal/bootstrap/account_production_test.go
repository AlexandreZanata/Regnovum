// Tests of the transactional email pipeline the production composition wires
// (P19-T02A): the account journey hands every message to the durable queue and
// it is the worker that renders and delivers it.
//
// The two halves are asserted together because they are one decision: a
// registration answered without a queued message is a form whose link goes
// nowhere, and a job queued for an event that was rolled back is work for
// something that never happened. PostgreSQL is real and disposable, so the
// claim is made against the same transactional story production runs.
package bootstrap_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/outbox"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/locale"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// queuedEmail is one row of the durable queue, reduced to the facts this test
// asserts. The payload's code is deliberately not read: what has to be proven
// is that the message is durable work addressed to the right person, not what
// the one-time code happens to be.
type queuedEmail struct {
	version   int32
	template  string
	recipient string
	eventKey  string
}

// readQueuedEmails is the reader's view of the queue: a query, not an
// in-process handle, so a composition that queued nothing cannot pass by
// holding something in memory.
func readQueuedEmails(t *testing.T, database *dbtest.TestDB) []queuedEmail {
	t.Helper()

	rows, err := database.Pool.Pool().Query(context.Background(),
		`SELECT version, parameters ->> 'template', parameters ->> 'recipient', coalesce(idempotency_key, '')
		   FROM app.jobs
		  WHERE type = 'email_delivery'
		  ORDER BY created_at, id`)
	if err != nil {
		t.Fatalf("query app.jobs: %v", err)
	}
	defer rows.Close()

	var queued []queuedEmail
	for rows.Next() {
		var row queuedEmail
		if err := rows.Scan(&row.version, &row.template, &row.recipient, &row.eventKey); err != nil {
			t.Fatalf("scan app.jobs row: %v", err)
		}
		queued = append(queued, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read app.jobs: %v", err)
	}
	return queued
}

// TestTheProductionCompositionQueuesTheRegistration is the validation of
// P19-T02A: with the sender address and the provider credential configured,
// production serves the registration form, the account is created and its
// confirmation link becomes a durable `email_delivery` job — and the local sink
// is not installed in any of its two forms, because a directory of account
// codes is not a delivery mechanism.
func TestTheProductionCompositionQueuesTheRegistration(t *testing.T) {
	t.Parallel()

	const credential = "re_live_never_print_me"
	const sender = "Arena <no-reply@arena.invalid>"
	const email = "queued@example.test"
	const password = "correct horse battery staple"

	// The composition is the one place the credential is unredacted, so its
	// log is read here rather than discarded: a secret that reaches a log line
	// has left the process, and this is where it would leave.
	logs := &bytes.Buffer{}
	database := dbtest.New(t)
	surface, err := bootstrap.ComposeAccount(bootstrap.Options{
		Env:         config.EnvProduction,
		Logger:      slog.New(slog.NewJSONHandler(logs, nil)),
		Pool:        database.Pool.Pool(),
		Clock:       clockseed.NewClock(),
		Random:      clockseed.NewRandom(),
		Assets:      manifestFixture(t),
		EmailFrom:   sender,
		EmailAPIKey: config.NewSecret(credential),
	})
	if err != nil {
		t.Fatalf("ComposeAccount() error = %v", err)
	}

	if surface.LocalSink() != nil {
		t.Fatal("the production composition installed the in-process email sink")
	}
	if surface.SinkDirectory() != "" {
		t.Fatal("the production composition installed a directory of account codes")
	}

	clock := clockseed.NewClock()
	ids := clockseed.NewIDGenerator("req", clockseed.NewRandom(), clock)
	mux, err := httpserver.NewMuxWith(ids, locale.NewResolver(), securityheaders.Config{}, []httpserver.Surface{surface.Surface()})
	if err != nil {
		t.Fatalf("httpserver.NewMuxWith() error = %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := browser(t)
	register := url.Values{
		"email":      {email},
		"password":   {password},
		"csrf_token": {csrfToken(t, openPage(t, client, server, "/register"))},
	}
	response := submit(t, client, server, "/register", register)
	if response.StatusCode != http.StatusOK {
		body := make([]byte, 200)
		read, _ := response.Body.Read(body)
		t.Fatalf("POST /register status = %d, want 200 (body: %.200s)", response.StatusCode, body[:read])
	}

	// The account exists and it is waiting for a code, which is the state the
	// queued message is the only way out of.
	var status string
	if err := database.Pool.Pool().QueryRow(context.Background(),
		`SELECT status FROM app.accounts WHERE lower(email) = lower($1)`, email).Scan(&status); err != nil {
		t.Fatalf("query the created account: %v", err)
	}
	if status != "pending" {
		t.Fatalf("account status = %q, want pending until the code is spent", status)
	}

	queued := readQueuedEmails(t, database)
	if len(queued) != 1 {
		t.Fatalf("the queue holds %d email deliveries, want exactly the one the registration queued", len(queued))
	}
	if queued[0].version != outbox.PayloadVersion {
		t.Errorf("payload version = %d, want the version the handler reads (%d)", queued[0].version, outbox.PayloadVersion)
	}
	if queued[0].template != "verification" {
		t.Errorf("queued template = %q, want the verification message", queued[0].template)
	}
	if queued[0].recipient != email {
		t.Errorf("queued recipient = %q, want the address that registered", queued[0].recipient)
	}
	if queued[0].eventKey == "" {
		t.Error("the queued message has no event key: a retried registration would send twice")
	}

	if strings.Contains(logs.String(), credential) {
		t.Errorf("the composition logged the provider credential: %q", logs.String())
	}
}
