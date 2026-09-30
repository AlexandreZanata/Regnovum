package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

func mustUsername(t *testing.T, raw string) domain.Username {
	t.Helper()
	username, err := domain.ParseUsername(raw)
	if err != nil {
		t.Fatalf("parse username %q: %v", raw, err)
	}
	return username
}

func TestDefaultUsernamePolicyReserved(t *testing.T) {
	policy := domain.DefaultUsernamePolicy()

	entries := domain.DefaultReservedUsernames()
	if len(entries) == 0 {
		t.Fatal("DefaultReservedUsernames() is empty")
	}
	for _, entry := range entries {
		if _, err := domain.ParseUsername(entry); err != nil {
			t.Errorf("reserved entry %q is not a valid username: %v", entry, err)
		}
	}

	for _, raw := range []string{"admin", "Admin", "ADMIN", "api", "Arena", "support", "System"} {
		username := mustUsername(t, raw)
		if !policy.IsReserved(username) {
			t.Errorf("%q should be reserved", raw)
		}
		if err := policy.ValidateReserved(username); !errors.Is(err, domain.ErrUsernameReserved) {
			t.Errorf("ValidateReserved(%q) = %v, want ErrUsernameReserved", raw, err)
		}
	}

	for _, raw := range []string{"arenauser", "arena-fan", "inkwell", "supportive", "admiral"} {
		username := mustUsername(t, raw)
		if policy.IsReserved(username) {
			t.Errorf("%q should not be reserved", raw)
		}
		if err := policy.ValidateReserved(username); err != nil {
			t.Errorf("ValidateReserved(%q) = %v, want nil", raw, err)
		}
	}
}

func TestDefaultReservedUsernamesReturnsCopy(t *testing.T) {
	first := domain.DefaultReservedUsernames()
	first[0] = "mutated"
	second := domain.DefaultReservedUsernames()
	if second[0] == "mutated" {
		t.Fatal("DefaultReservedUsernames() exposes the internal slice")
	}
}

func TestNewUsernamePolicyValidation(t *testing.T) {
	if _, err := domain.NewUsernamePolicy(-time.Hour, nil); !errors.Is(err, domain.ErrInvalidCooldown) {
		t.Errorf("negative cooldown: got %v, want ErrInvalidCooldown", err)
	}
	if _, err := domain.NewUsernamePolicy(time.Hour, []string{"ab"}); !errors.Is(err, domain.ErrInvalidReservedUsername) {
		t.Errorf("invalid reserved entry: got %v, want ErrInvalidReservedUsername", err)
	}

	policy, err := domain.NewUsernamePolicy(time.Hour, []string{"ADMIN"})
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	if !policy.IsReserved(mustUsername(t, "admin")) {
		t.Error("reserved entries must be normalized before matching")
	}
	if policy.Cooldown() != time.Hour {
		t.Errorf("Cooldown() = %v, want 1h", policy.Cooldown())
	}
}

func TestUsernamePolicyCooldown(t *testing.T) {
	const cooldown = 30 * 24 * time.Hour
	policy, err := domain.NewUsernamePolicy(cooldown, nil)
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}

	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	if err := policy.CheckCooldown(now.Add(-29*24*time.Hour), now); !errors.Is(err, domain.ErrUsernameCooldown) {
		t.Errorf("29 days elapsed: got %v, want ErrUsernameCooldown", err)
	}
	if err := policy.CheckCooldown(now.Add(-cooldown), now); err != nil {
		t.Errorf("exact boundary should be eligible, got %v", err)
	}
	if err := policy.CheckCooldown(now.Add(-31*24*time.Hour), now); err != nil {
		t.Errorf("31 days elapsed should be eligible, got %v", err)
	}
	if err := policy.CheckCooldown(time.Time{}, now); !errors.Is(err, domain.ErrUsernameCooldown) {
		t.Errorf("unknown last change: got %v, want ErrUsernameCooldown", err)
	}
	if err := policy.CheckCooldown(now.Add(time.Hour), now); !errors.Is(err, domain.ErrUsernameCooldown) {
		t.Errorf("clock skew: got %v, want ErrUsernameCooldown", err)
	}

	disabled, err := domain.NewUsernamePolicy(0, nil)
	if err != nil {
		t.Fatalf("build disabled policy: %v", err)
	}
	if err := disabled.CheckCooldown(time.Time{}, now); err != nil {
		t.Errorf("zero cooldown must disable the rule, got %v", err)
	}

	lastChanged := now.Add(-24 * time.Hour)
	if got, want := policy.NextEligibleAt(lastChanged), lastChanged.Add(cooldown); !got.Equal(want) {
		t.Errorf("NextEligibleAt() = %v, want %v", got, want)
	}
	if !policy.NextEligibleAt(time.Time{}).IsZero() {
		t.Error("NextEligibleAt(zero) must be the zero time")
	}
	if domain.DefaultUsernamePolicy().Cooldown() != domain.DefaultUsernameCooldown {
		t.Error("DefaultUsernamePolicy() must use DefaultUsernameCooldown")
	}
}

func TestUsernamePolicyPlanChange(t *testing.T) {
	policy := domain.DefaultUsernamePolicy()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))

	// First assignment bypasses the cooldown but not the reserved list.
	first := mustUsername(t, "ArenaUser")
	change, err := policy.PlanChange(domain.Username{}, first, time.Time{}, now)
	if err != nil {
		t.Fatalf("first assignment: %v", err)
	}
	if !change.IsFirstAssignment() {
		t.Error("first assignment must report IsFirstAssignment()")
	}
	if !change.Previous.IsZero() {
		t.Error("first assignment must keep the previous username zero")
	}
	if !change.Current.Equals(first) {
		t.Error("change.Current does not match the proposed username")
	}
	if !change.ChangedAt.Equal(now.UTC()) || change.ChangedAt.Location() != time.UTC {
		t.Errorf("ChangedAt = %v, want %v in UTC", change.ChangedAt, now.UTC())
	}

	if _, err := policy.PlanChange(domain.Username{}, mustUsername(t, "Admin"), time.Time{}, now); !errors.Is(err, domain.ErrUsernameReserved) {
		t.Errorf("reserved first assignment: got %v, want ErrUsernameReserved", err)
	}
	if _, err := policy.PlanChange(domain.Username{}, domain.Username{}, time.Time{}, now); !errors.Is(err, domain.ErrEmptyUsername) {
		t.Errorf("zero proposal: got %v, want ErrEmptyUsername", err)
	}

	// A change that only re-cases the same handle is unchanged.
	if _, err := policy.PlanChange(first, mustUsername(t, "ARENAUSER"), time.Time{}, now); !errors.Is(err, domain.ErrUsernameUnchanged) {
		t.Errorf("case-only change: got %v, want ErrUsernameUnchanged", err)
	}

	second := mustUsername(t, "ArenaHero")
	if _, err := policy.PlanChange(first, second, now.Add(-10*24*time.Hour), now); !errors.Is(err, domain.ErrUsernameCooldown) {
		t.Errorf("change inside cooldown: got %v, want ErrUsernameCooldown", err)
	}
	if _, err := policy.PlanChange(first, second, time.Time{}, now); !errors.Is(err, domain.ErrUsernameCooldown) {
		t.Errorf("change with unknown last change: got %v, want ErrUsernameCooldown", err)
	}

	change, err = policy.PlanChange(first, second, now.Add(-31*24*time.Hour), now)
	if err != nil {
		t.Fatalf("change after cooldown: %v", err)
	}
	if change.IsFirstAssignment() {
		t.Error("change after cooldown must not report IsFirstAssignment()")
	}
	if !change.Previous.Equals(first) || !change.Current.Equals(second) {
		t.Errorf("audit record = %q -> %q, want %q -> %q",
			change.Previous.Normalized(), change.Current.Normalized(), first.Normalized(), second.Normalized())
	}
}
