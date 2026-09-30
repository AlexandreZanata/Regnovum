package dbpool_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbpool"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func devDatabaseURL() string {
	if dsn := os.Getenv("ARENA_DATABASE_URL"); dsn != "" {
		return dsn
	}
	return "postgres://arena:arena-local-dev@127.0.0.1:54329/arena?sslmode=disable"
}

func TestIntegrationPoolLifecycleAndCancellation(t *testing.T) {
	dsn := devDatabaseURL()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := dbpool.DefaultConfig()
	cfg.MaxConns = 5
	cfg.MinConns = 1
	cfg.PingTimeout = 2 * time.Second

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	clock := clockseed.NewClock()

	// 1. Connect to PostgreSQL
	pool, err := dbpool.New(ctx, dsn, cfg, logger, clock)
	if err != nil {
		t.Fatalf("PostgreSQL is required for the integration suite and the pool could not connect to %s: %v", dsn, err)
	}

	// 2. Query execution
	var one int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		pool.Close()
		t.Fatalf("query SELECT 1 failed: %v", err)
	}
	if one != 1 {
		pool.Close()
		t.Fatalf("got %d, want 1", one)
	}

	// 3. Cancel slow query via context timeout
	slowCtx, slowCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer slowCancel()

	start := time.Now()
	_, err = pool.Exec(slowCtx, "SELECT pg_sleep(2)")
	elapsed := time.Since(start)

	if err == nil {
		pool.Close()
		t.Fatal("expected slow query to be cancelled, got nil error")
	}
	if elapsed >= 1500*time.Millisecond {
		pool.Close()
		t.Fatalf("query took %v, was not cancelled promptly", elapsed)
	}

	// 4. Goroutine leak check upon Close
	beforeCloseRoutines := runtime.NumGoroutine()
	pool.Close()

	// Allow brief settling time for connection cleanup
	deadline := time.Now().Add(1 * time.Second)
	leakDetected := true
	for time.Now().Before(deadline) {
		runtime.Gosched()
		if runtime.NumGoroutine() <= beforeCloseRoutines {
			leakDetected = false
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if leakDetected {
		t.Errorf("goroutines after pool.Close() = %d, before = %d (potential leak)",
			runtime.NumGoroutine(), beforeCloseRoutines)
	}
}

func TestIntegrationReadinessChangesWithDatabaseState(t *testing.T) {
	dsn := devDatabaseURL()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := dbpool.DefaultConfig()
	cfg.PingTimeout = 1 * time.Second
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	clock := clockseed.NewClock()

	// 1. Healthy database pool
	healthyPool, err := dbpool.New(ctx, dsn, cfg, logger, clock)
	if err != nil {
		t.Fatalf("PostgreSQL is required for the integration suite and the healthy pool could not be opened: %v", err)
	}
	defer healthyPool.Close()

	if err := healthyPool.CheckReadiness(ctx); err != nil {
		t.Fatalf("healthy pool CheckReadiness failed: %v", err)
	}

	// Wire healthy pool into ReadyHandler
	healthyHandler := httpserver.ReadyHandler(healthyPool)
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()
	healthyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("healthy /health/ready status = %d, want 200", rec.Code)
	}

	var healthyBody struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &healthyBody); err != nil {
		t.Fatalf("parse healthy body: %v", err)
	}
	if healthyBody.Status != "ready" {
		t.Errorf("healthy body status = %q, want %q", healthyBody.Status, "ready")
	}

	// 2. Unreachable / down database pool (non-existent host port)
	const downDSN = "postgres://arena:arena-local-dev@127.0.0.1:54399/arena?sslmode=disable&connect_timeout=1"
	downCfg := dbpool.DefaultConfig()
	downCfg.PingTimeout = 500 * time.Millisecond

	downPool, err := dbpool.New(context.Background(), downDSN, downCfg, logger, clock)
	if err != nil {
		t.Fatalf("create down pool: %v", err)
	}
	defer downPool.Close()

	// CheckReadiness must fail
	downErr := downPool.CheckReadiness(context.Background())
	if downErr == nil {
		t.Fatal("expected down pool CheckReadiness to fail")
	}

	// Wire down pool into ReadyHandler
	downHandler := httpserver.ReadyHandler(downPool)
	downReq := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	downRec := httptest.NewRecorder()
	downHandler.ServeHTTP(downRec, downReq)

	if downRec.Code != http.StatusServiceUnavailable {
		t.Errorf("down /health/ready status = %d, want 503", downRec.Code)
	}

	var downBody struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(downRec.Body.Bytes(), &downBody); err != nil {
		t.Fatalf("parse down body: %v", err)
	}
	if downBody.Status != "unavailable" {
		t.Errorf("down body status = %q, want %q", downBody.Status, "unavailable")
	}

	// Must never leak DSN, password or driver error into the response body
	bodyStr := downRec.Body.String()
	if strings.Contains(bodyStr, "arena-local-dev") ||
		strings.Contains(bodyStr, "54399") ||
		strings.Contains(bodyStr, "connection refused") {
		t.Fatalf("down /health/ready response leaked internal details: %s", bodyStr)
	}
}
