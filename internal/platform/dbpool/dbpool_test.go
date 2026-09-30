package dbpool_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbpool"
)

func TestConfigValidation(t *testing.T) {
	t.Parallel()

	valid := dbpool.DefaultConfig()
	if err := valid.Validate(); err != nil {
		t.Fatalf("DefaultConfig() should be valid, got: %v", err)
	}

	tests := []struct {
		name    string
		modify  func(c *dbpool.Config)
		wantErr string
	}{
		{
			name:    "zero MaxConns",
			modify:  func(c *dbpool.Config) { c.MaxConns = 0 },
			wantErr: "MaxConns must be at least 1",
		},
		{
			name:    "negative MinConns",
			modify:  func(c *dbpool.Config) { c.MinConns = -1 },
			wantErr: "MinConns must be non-negative",
		},
		{
			name:    "MinConns greater than MaxConns",
			modify:  func(c *dbpool.Config) { c.MinConns = 15; c.MaxConns = 10 },
			wantErr: "cannot exceed MaxConns",
		},
		{
			name:    "zero MaxConnLifetime",
			modify:  func(c *dbpool.Config) { c.MaxConnLifetime = 0 },
			wantErr: "MaxConnLifetime must be positive",
		},
		{
			name:    "zero MaxConnIdleTime",
			modify:  func(c *dbpool.Config) { c.MaxConnIdleTime = 0 },
			wantErr: "MaxConnIdleTime must be positive",
		},
		{
			name:    "zero HealthCheckPeriod",
			modify:  func(c *dbpool.Config) { c.HealthCheckPeriod = 0 },
			wantErr: "HealthCheckPeriod must be positive",
		},
		{
			name:    "zero AcquireTimeout",
			modify:  func(c *dbpool.Config) { c.AcquireTimeout = 0 },
			wantErr: "AcquireTimeout must be positive",
		},
		{
			name:    "zero PingTimeout",
			modify:  func(c *dbpool.Config) { c.PingTimeout = 0 },
			wantErr: "PingTimeout must be positive",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := dbpool.DefaultConfig()
			tc.modify(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestFromConfigDerivesValues(t *testing.T) {
	t.Parallel()

	appCfg, err := config.Load([]string{
		"ARENA_DB_MAX_CONNS=30",
		"ARENA_DB_MIN_CONNS=8",
		"ARENA_DB_MAX_CONN_LIFETIME=3h",
		"ARENA_DB_MAX_CONN_IDLE_TIME=1h",
		"ARENA_DB_ACQUIRE_TIMEOUT=15s",
	})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	poolCfg := dbpool.FromConfig(appCfg)
	if poolCfg.MaxConns != 30 {
		t.Errorf("MaxConns = %d, want 30", poolCfg.MaxConns)
	}
	if poolCfg.MinConns != 8 {
		t.Errorf("MinConns = %d, want 8", poolCfg.MinConns)
	}
	if poolCfg.MaxConnLifetime != 3*time.Hour {
		t.Errorf("MaxConnLifetime = %v, want 3h", poolCfg.MaxConnLifetime)
	}
	if poolCfg.MaxConnIdleTime != 1*time.Hour {
		t.Errorf("MaxConnIdleTime = %v, want 1h", poolCfg.MaxConnIdleTime)
	}
	if poolCfg.AcquireTimeout != 15*time.Second {
		t.Errorf("AcquireTimeout = %v, want 15s", poolCfg.AcquireTimeout)
	}
}

func TestNewRejectsEmptyOrMalformedDSNWithoutLeakingCredentials(t *testing.T) {
	t.Parallel()

	cfg := dbpool.DefaultConfig()

	// Empty DSN
	_, err := dbpool.New(context.Background(), "", cfg, nil, nil)
	if err == nil {
		t.Fatal("expected error for empty DSN")
	}

	// Malformed DSN containing password
	const secretDSN = "postgres://user:super-secret-password@invalid-host:not-a-port/db"
	_, err = dbpool.New(context.Background(), secretDSN, cfg, nil, nil)
	if err == nil {
		t.Fatal("expected error for malformed DSN")
	}
	if strings.Contains(err.Error(), "super-secret-password") {
		t.Fatalf("error leaked secret password: %v", err)
	}
}
