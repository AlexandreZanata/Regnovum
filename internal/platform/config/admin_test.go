package config_test

import (
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
)

func TestAdminAddressIsOptionalAndLoopbackOnly(t *testing.T) {
	cfg, err := config.Load([]string{"ARENA_ADMIN_ADDR=127.0.0.1:6060"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminAddr() != "127.0.0.1:6060" {
		t.Fatalf("AdminAddr = %q", cfg.AdminAddr())
	}

	cfg, err = config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminAddr() != "" {
		t.Fatalf("default AdminAddr = %q, want disabled", cfg.AdminAddr())
	}
}

func TestAdminAddressRejectsPublicBind(t *testing.T) {
	_, err := config.Load([]string{"ARENA_ADMIN_ADDR=0.0.0.0:6060"})
	if err == nil || !strings.Contains(err.Error(), "ARENA_ADMIN_ADDR") || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("error = %v, want loopback-only validation", err)
	}
}
