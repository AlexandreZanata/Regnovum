package turnstile_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/turnstile"
)

// testSecret is a literal that stands in for a configured secret. It is not a
// credential and it never leaves this process: the point of the redaction and
// reachability tests below is that a real one could not either.
const testSecret = "turnstile-test-secret-not-a-credential"

// codeOf reports the stable code of a refusal, or the empty string when the
// error is not a refusal at all.
func codeOf(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	var appError *apperr.Error
	if !errors.As(err, &appError) {
		t.Fatalf("error %v is not an apperr.Error", err)
	}
	return appError.Code()
}

// TestEveryDeclaredActionHasARequirement is the guard against a vacuous
// policy: a table that forgot a row would be a route nobody decided about.
func TestEveryDeclaredActionHasARequirement(t *testing.T) {
	t.Parallel()

	actions := turnstile.Actions()
	if len(actions) == 0 {
		t.Fatal("the policy table declares no action; every assertion below would pass vacuously")
	}

	for _, action := range actions {
		requirement, declared := turnstile.RequirementFor(action)
		if !declared {
			t.Errorf("%s: declared by Actions() but not by the table", action)
		}
		if requirement != turnstile.Required && requirement != turnstile.RequiredWhenElevated {
			t.Errorf("%s: requirement = %q, want a declared requirement", action, requirement)
		}
	}

	if requirement, declared := turnstile.RequirementFor(turnstile.Action("not-a-declared-action")); declared {
		t.Errorf("an undeclared action reported declared = true (requirement %q)", requirement)
	} else if requirement != turnstile.Required {
		t.Errorf("an undeclared action resolved to %q, want the conservative Required", requirement)
	}
}

// TestThePolicyChallengesTheActionsThePlanNames asserts the four integrations
// the task is about, so a policy edit that quietly drops one fails here rather
// than in production.
func TestThePolicyChallengesTheActionsThePlanNames(t *testing.T) {
	t.Parallel()

	expected := map[turnstile.Action]turnstile.Requirement{
		turnstile.ActionSignup:        turnstile.Required,
		turnstile.ActionPasswordReset: turnstile.Required,
		turnstile.ActionArenaPublish:  turnstile.Required,
		turnstile.ActionLoginElevated: turnstile.RequiredWhenElevated,
	}

	for action, want := range expected {
		requirement, declared := turnstile.RequirementFor(action)
		if !declared {
			t.Errorf("%s: not declared in the policy table", action)
			continue
		}
		if requirement != want {
			t.Errorf("%s: requirement = %q, want %q", action, requirement, want)
		}
	}
}

// TestConfigNeverPrintsTheSecret is the local half of "no secret in the
// browser": the value a request handler composes must be safe to log, because
// a config that leaks through a log line reaches an operator's dashboard.
func TestConfigNeverPrintsTheSecret(t *testing.T) {
	t.Parallel()

	configuration := turnstile.Config{
		SecretKey:  testSecret,
		Hostname:   "arena.example",
		FailPolicy: turnstile.FailClosed,
	}

	renditions := map[string]string{
		"%v":             fmt.Sprintf("%v", configuration),
		"%+v":            fmt.Sprintf("%+v", configuration),
		"%#v":            fmt.Sprintf("%#v", configuration),
		"String()":       configuration.String(),
		"GoString()":     configuration.GoString(),
		"pointer %v":     fmt.Sprintf("%v", &configuration),
		"sprintf in log": fmt.Sprintf("configured: %v", configuration),
	}
	for name, rendition := range renditions {
		if strings.Contains(rendition, testSecret) {
			t.Errorf("%s leaked the secret: %s", name, rendition)
		}
	}

	// The redaction must not be so eager that it hides whether a secret is
	// configured at all: an operator looking at a log line has to be able to
	// tell a missing secret from a present one.
	if !strings.Contains(configuration.String(), "[REDACTED]") {
		t.Errorf("Config.String() = %q, want it to report a redacted secret", configuration.String())
	}
	if empty := (turnstile.Config{}).String(); !strings.Contains(empty, "[UNSET]") {
		t.Errorf("empty Config.String() = %q, want it to report the secret as unset", empty)
	}
}

// TestNewRefusesTheConfigurationsThatCannotVerify protects the boot: a
// production process without a secret must not start serving challenged
// actions unprotected.
func TestNewRefusesTheConfigurationsThatCannotVerify(t *testing.T) {
	t.Parallel()

	_, err := turnstile.New(turnstile.Config{}, config.EnvProduction)
	if !errors.Is(err, turnstile.ErrMissingSecretKey) {
		t.Errorf("production without a secret: err = %v, want ErrMissingSecretKey", err)
	}

	_, err = turnstile.New(turnstile.Config{SecretKey: testSecret}, config.EnvProduction)
	if !errors.Is(err, turnstile.ErrMissingHostname) {
		t.Errorf("secret without hostname: err = %v, want ErrMissingHostname", err)
	}

	_, err = turnstile.New(turnstile.Config{FailPolicy: turnstile.FailPolicy("maybe")}, config.EnvDevelopment)
	if !errors.Is(err, turnstile.ErrUnknownFailPolicy) {
		t.Errorf("unknown fail policy: err = %v, want ErrUnknownFailPolicy", err)
	}
}

// TestNewBuildsALocalFakeOutsideProductionAndARealVerifierInProduction is the
// "ambiente local fake" item, stated as the two halves that matter: the fake
// appears exactly where it is allowed, and the real verifier appears where a
// secret exists (including local, where a developer exercises the provider's
// test keys).
func TestNewBuildsALocalFakeOutsideProductionAndARealVerifierInProduction(t *testing.T) {
	t.Parallel()

	for _, env := range []config.Env{config.EnvDevelopment, config.EnvTest} {
		verifier, err := turnstile.New(turnstile.Config{}, env)
		if err != nil {
			t.Fatalf("%s without a secret: err = %v, want a local fake", env, err)
		}
		if _, ok := verifier.(*turnstile.LocalFake); !ok {
			t.Errorf("%s without a secret: verifier = %T, want *LocalFake", env, verifier)
		}
	}

	// A real verifier is built when a secret exists, in any environment. It is
	// pointed at a closed port so the assertion stays offline: the fake would
	// accept the test token without a network call, and the real verifier
	// cannot.
	verifier, err := turnstile.New(turnstile.Config{
		SecretKey: testSecret,
		Hostname:  "arena.example",
		Endpoint:  "http://127.0.0.1:1/turnstile/v0/siteverify",
		Timeout:   testTimeout,
	}, config.EnvProduction)
	if err != nil {
		t.Fatalf("production with a secret: err = %v, want a verifier", err)
	}
	if _, ok := verifier.(*turnstile.LocalFake); ok {
		t.Fatal("production with a secret built the local fake")
	}
	if err := verifier.Verify(context.Background(), turnstile.Verification{
		Token:  turnstile.LocalFakeTokenPrefix + "1",
		Action: turnstile.ActionSignup,
	}); err == nil {
		t.Error("the provider-backed verifier accepted a token with the local fake's endpoint unreachable")
	}
}

// TestLocalFakeAcceptsOnlyTheTestTokensAndSpendsThemOnce states exactly what
// the fake verifies: nothing, except that a developer wired a token and that a
// token is single use.
func TestLocalFakeAcceptsOnlyTheTestTokensAndSpendsThemOnce(t *testing.T) {
	t.Parallel()

	fake := turnstile.NewLocalFake()

	token := turnstile.LocalFakeTokenPrefix + "abcdef"
	if err := fake.Verify(context.Background(), turnstile.Verification{Token: token, Action: turnstile.ActionSignup}); err != nil {
		t.Fatalf("a test token was refused: %v", err)
	}

	if err := fake.Verify(context.Background(), turnstile.Verification{Token: token, Action: turnstile.ActionSignup}); codeOf(t, err) != turnstile.CodeChallengeReplayed {
		t.Errorf("replaying a test token: code = %q, want %q", codeOf(t, err), turnstile.CodeChallengeReplayed)
	}

	if err := fake.Verify(context.Background(), turnstile.Verification{Token: "not-a-challenge-token", Action: turnstile.ActionSignup}); codeOf(t, err) != turnstile.CodeChallengeInvalid {
		t.Errorf("an arbitrary token: code = %q, want %q", codeOf(t, err), turnstile.CodeChallengeInvalid)
	}

	if err := fake.Verify(context.Background(), turnstile.Verification{Action: turnstile.ActionSignup}); codeOf(t, err) != turnstile.CodeChallengeRequired {
		t.Errorf("a missing token: code = %q, want %q", codeOf(t, err), turnstile.CodeChallengeRequired)
	}

	// Different tokens are different challenges, so local development is not a
	// one-shot flow.
	if err := fake.Verify(context.Background(), turnstile.Verification{Token: turnstile.LocalFakeTokenPrefix + "other", Action: turnstile.ActionSignup}); err != nil {
		t.Errorf("a second test token was refused: %v", err)
	}
}
