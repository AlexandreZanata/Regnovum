package main

// The three executable tabletops of P29-T07 (R1 5xx, R3 pool, R6 lag):
// the runbook's own checks run against an isolated stack — a disposable
// PostgreSQL with all migrations plus an httptest server — and each one
// returns machine-readable evidence (JSON-tagged structs the tests
// marshal and validate, never prose). Every check is read-only, and the
// last tabletop proves the stack holds nothing the tabletops created.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/observability"
)

// fixedClock is the deterministic clock the tabletops measure with: the
// signals, not the wall time, are under test.
type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

// Check is one executed runbook step with its observed output.
type Check struct {
	Name   string `json:"name"`
	Output string `json:"output"`
}

// Evidence is the machine-readable record of one tabletop.
type Evidence struct {
	Tabletop string  `json:"tabletop"`
	Checks   []Check `json:"checks"`
}

// tabletopR1 reproduces RUNBOOKS.md §A against an isolated server: live,
// ready and a synthetic 500 plus an unmatched path, then scrapes the
// exposition the alert expression reads.
func tabletopR1(t *testing.T) Evidence {
	t.Helper()

	metrics := observability.NewMetrics(fixedClock{now: time.Now()})
	telemetry := &observability.Telemetry{Metrics: metrics}
	mux := http.NewServeMux()
	mux.Handle("GET /health/live", httpserver.LiveHandler())
	mux.Handle("GET /health/ready", httpserver.ReadyHandler())
	mux.HandleFunc("GET /boom", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	})
	mux.Handle("GET /metrics", metrics.Handler())
	server := httptest.NewServer(telemetry.HTTPMiddleware(mux))
	defer server.Close()

	get := func(path string) (int, string) {
		t.Helper()
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer response.Body.Close()
		var body strings.Builder
		buffer := make([]byte, 4096)
		for {
			n, err := response.Body.Read(buffer)
			body.Write(buffer[:n])
			if err != nil {
				break
			}
		}
		return response.StatusCode, body.String()
	}

	var evidence Evidence
	evidence.Tabletop = "R1-5xx-signal"
	for _, path := range []string{"/health/live", "/health/ready", "/boom", "/nope"} {
		status, _ := get(path)
		evidence.Checks = append(evidence.Checks, Check{
			Name:   "GET " + path,
			Output: http.StatusText(status),
		})
	}
	_, exposition := get("/metrics")
	for _, want := range []string{
		`http_requests_total{method="GET",route="/health/live",status="200"}`,
		`http_requests_total{method="GET",route="/health/ready",status="200"}`,
		`http_requests_total{method="GET",route="/boom",status="500"}`,
		`http_requests_total{method="GET",route="unmatched",status="404"}`,
	} {
		found := false
		for _, line := range strings.Split(exposition, "\n") {
			if strings.HasPrefix(line, want) {
				evidence.Checks = append(evidence.Checks, Check{Name: "series", Output: line})
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("exposition misses %q:\n%s", want, exposition)
		}
	}
	return evidence
}

// tabletopR3 reproduces RUNBOOKS.md §B: the pool state, the activity
// census and the server ceiling, on a disposable database.
func tabletopR3(t *testing.T) Evidence {
	t.Helper()

	db := dbtest.New(t)
	ctx := context.Background()
	pool := db.Pool.Pool()

	var evidence Evidence
	evidence.Tabletop = "R3-db-pool"

	// The census first: the pool opens connections lazily, so a stat
	// snapshot before the first acquire would read an empty pool.
	rows, err := pool.Query(ctx, `SELECT state, count(*) FROM pg_stat_activity WHERE datname = current_database() GROUP BY state ORDER BY state`)
	if err != nil {
		t.Fatalf("activity census: %v", err)
	}
	var census []string
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			t.Fatal(err)
		}
		census = append(census, state+"="+itoa(int(count)))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	evidence.Checks = append(evidence.Checks, Check{Name: "pg_stat_activity", Output: strings.Join(census, " ")})
	if len(census) == 0 {
		t.Fatal("activity census returned no rows on a live database")
	}

	stat := pool.Stat()
	evidence.Checks = append(evidence.Checks, Check{
		Name: "pool stat",
		Output: strings.Join([]string{
			"total=" + itoa(int(stat.TotalConns())),
			"idle=" + itoa(int(stat.IdleConns())),
			"max=" + itoa(int(stat.MaxConns())),
		}, " "),
	})
	if stat.MaxConns() <= 0 || stat.TotalConns() <= 0 {
		t.Fatalf("pool stat = %+v, want a live pool", stat)
	}

	var ceiling string
	if err := pool.QueryRow(ctx, `SHOW max_connections`).Scan(&ceiling); err != nil {
		t.Fatalf("SHOW max_connections: %v", err)
	}
	evidence.Checks = append(evidence.Checks, Check{Name: "max_connections", Output: ceiling})
	return evidence
}

// tabletopR6 reproduces RUNBOOKS.md §C and §E: the queue aggregation the
// R6 alert reads, on an empty isolated queue, plus the email workload
// census of R7. An idle queue reports zeros; the alert watches for
// change, and the tabletop proves the queries that watch it run.
func tabletopR6(t *testing.T) Evidence {
	t.Helper()

	db := dbtest.New(t)
	ctx := context.Background()
	pool := db.Pool.Pool()

	var evidence Evidence
	evidence.Tabletop = "R6-queue-lag"

	var queued, leased, dead, dueNow int64
	var lag *string
	row := pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE state = 'queued'),
		count(*) FILTER (WHERE state = 'leased'),
		count(*) FILTER (WHERE state = 'dead'),
		count(*) FILTER (WHERE state = 'queued' AND available_at <= now()),
		(min(available_at) FILTER (WHERE state = 'queued' AND available_at <= now()))::text
		FROM app.jobs`)
	if err := row.Scan(&queued, &leased, &dead, &dueNow, &lag); err != nil {
		t.Fatalf("queue aggregation: %v", err)
	}
	evidence.Checks = append(evidence.Checks, Check{
		Name: "queue aggregation",
		Output: strings.Join([]string{
			"queued=" + itoa(int(queued)),
			"leased=" + itoa(int(leased)),
			"dead=" + itoa(int(dead)),
			"due_now=" + itoa(int(dueNow)),
		}, " "),
	})
	if queued != 0 || leased != 0 || dead != 0 || dueNow != 0 {
		t.Fatalf("isolated queue is not empty: %+v", evidence.Checks[0])
	}

	var email int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.jobs WHERE type = 'email_delivery'`).Scan(&email); err != nil {
		t.Fatalf("email census: %v", err)
	}
	evidence.Checks = append(evidence.Checks, Check{Name: "email_delivery rows", Output: itoa(int(email))})

	var jobs int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("tabletops created %d job rows: read-only checks must leave nothing", jobs)
	}
	return evidence
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

// TestTabletopsProduceMachineReadableEvidence runs the three tabletops
// and proves the evidence contract: valid JSON with a tabletop name and
// at least one check carrying observed output each.
func TestTabletopsProduceMachineReadableEvidence(t *testing.T) {
	evidences := []Evidence{tabletopR1(t), tabletopR3(t), tabletopR6(t)}

	raw, err := json.Marshal(evidences)
	if err != nil {
		t.Fatalf("evidence does not marshal: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("evidence is not machine-readable JSON: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("evidence holds %d tabletops, want 3", len(decoded))
	}
	for _, record := range decoded {
		name, _ := record["tabletop"].(string)
		checks, _ := record["checks"].([]any)
		if name == "" || len(checks) == 0 {
			t.Fatalf("evidence record incomplete: %v", record)
		}
		for _, item := range checks {
			check, _ := item.(map[string]any)
			if check["name"] == "" || check["output"] == "" {
				t.Fatalf("evidence check incomplete: %v", item)
			}
		}
	}
}
