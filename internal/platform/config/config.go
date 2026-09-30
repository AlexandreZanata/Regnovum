// Package config loads and validates the typed runtime configuration of
// Regnovum (P02-T01).
//
// Design constraints from docs/ARCHITECTURE.md and the platform plan:
//   - the resulting Config is immutable after Load;
//   - environment reading is restricted to this package (and cmd/bootstrap),
//     enforced by internal/architecture_test.go;
//   - validation accumulates every problem instead of failing on the first,
//     so operators see the full list at once;
//   - Secret fields redact themselves: the raw value is never printable nor
//     loggable, and it is exposed only through an explicit accessor that is
//     inconvenient to call by accident.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the immutable runtime configuration of the application. All
// fields are unexported so callers cannot construct a partially validated
// instance; access happens through documented getters.
type Config struct {
	env               Env
	addr              string
	adminAddr         string
	assetsDir         string
	emailSinkDir      string
	cursorSecret      Secret
	databaseURL       Secret
	logLevel          LogLevel
	dbMaxConns        int32
	dbMinConns        int32
	dbMaxConnLifetime time.Duration
	dbMaxConnIdleTime time.Duration
	dbAcquireTimeout  time.Duration
	billingMarkets    []BillingMarket
	billingPrices     []BillingPrice
	billingSuccessURL string
	billingCancelURL  string
	stripeSecretKey   Secret
	stripeTimeout     time.Duration
	resendAPIKey      Secret
	emailFrom         string

	sentryDSN           Secret
	posthogAPIKey       Secret
	posthogHost         string
	analyticsSampleRate int
}

// Env is the deployment environment of the process.
type Env string

const (
	// EnvDevelopment is the local development environment.
	EnvDevelopment Env = "development"
	// EnvTest is the automated test environment.
	EnvTest Env = "test"
	// EnvProduction is the public production environment.
	EnvProduction Env = "production"
)

// LogLevel is the minimum severity the structured logger emits.
type LogLevel string

const (
	// LogLevelDebug enables verbose development logging.
	LogLevelDebug LogLevel = "debug"
	// LogLevelInfo is the standard operational level.
	LogLevelInfo LogLevel = "info"
	// LogLevelWarn keeps warnings and errors only.
	LogLevelWarn LogLevel = "warn"
	// LogLevelError keeps errors only.
	LogLevelError LogLevel = "error"
)

const envPrefix = "ARENA_"

// Secret wraps a configuration value that must never appear in logs, error
// messages or String output. Secrets are only readable through Unredacted,
// which returns the value wrapped in a type with no String method, so
// accidental interpolation into logs is difficult.
type Secret struct {
	value string
}

// NewSecret builds a Secret from a raw value.
func NewSecret(raw string) Secret {
	return Secret{value: raw}
}

// IsSet reports whether the secret carries a non-empty value.
func (secret Secret) IsSet() bool {
	return secret.value != ""
}

// String implements fmt.Stringer with an always-redacted representation.
func (secret Secret) String() string {
	return "[REDACTED]"
}

// GoString implements fmt.GoStringer with an always-redacted representation,
// closing the %#v leak path.
func (secret Secret) GoString() string {
	return "[REDACTED]"
}

// Unredacted exposes the secret value wrapped in a printable-nothing type.
// Callers must pass the value straight to the component that needs it and
// must never store or log the result.
func (secret Secret) Unredacted() UnredactedSecret {
	return UnredactedSecret(secret.value)
}

// UnredactedSecret is the raw secret value. It has no String or GoString
// method, so fmt verbs render it via reflection only when explicitly
// requested; it exists to make the hand-off point deliberate.
type UnredactedSecret string

// ValidationError describes one configuration problem with the exact
// variable name, so operators can fix the environment without reading code.
type ValidationError struct {
	Variable string
	Problem  string
}

// Error renders a single configuration problem.
func (validationError ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", validationError.Variable, validationError.Problem)
}

// ValidationErrors accumulates every configuration problem found by Load.
type ValidationErrors []ValidationError

// Error renders all accumulated problems.
func (validationErrors ValidationErrors) Error() string {
	lines := make([]string, 0, len(validationErrors))
	for _, validationError := range validationErrors {
		lines = append(lines, "  - "+validationError.Error())
	}
	return fmt.Sprintf("invalid configuration (%d problem(s)):\n%s",
		len(validationErrors), strings.Join(lines, "\n"))
}

// Load reads the ARENA_* environment, validates it accumulating every
// problem, and returns an immutable Config. Unknown ARENA_* variables are
// rejected to catch typos instead of silently ignoring them.
func Load(environ []string) (Config, error) {
	values := make(map[string]string, len(environ))
	for _, entry := range environ {
		name, rawValue, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if strings.HasPrefix(name, envPrefix) {
			values[name] = rawValue
		}
	}

	known := map[string]bool{
		"ARENA_ENV":                   true,
		"ARENA_ADDR":                  true,
		"ARENA_ADMIN_ADDR":            true,
		"ARENA_ASSETS_DIR":            true,
		EmailSinkDirVariable:          true,
		"ARENA_CURSOR_SECRET":         true,
		"ARENA_DATABASE_URL":          true,
		"ARENA_LOG_LEVEL":             true,
		"ARENA_DB_MAX_CONNS":          true,
		"ARENA_DB_MIN_CONNS":          true,
		"ARENA_DB_MAX_CONN_LIFETIME":  true,
		"ARENA_DB_MAX_CONN_IDLE_TIME": true,
		"ARENA_DB_ACQUIRE_TIMEOUT":    true,
		billingMarketsVariable:        true,
		billingPriceIDsVariable:       true,
		billingSuccessURLVariable:     true,
		billingCancelURLVariable:      true,
		stripeSecretKeyVariable:       true,
		stripeTimeoutVariable:         true,
		ResendAPIKeyVariable:          true,
		EmailFromVariable:             true,
		SentryDSNVariable:             true,
		PostHogAPIKeyVariable:         true,
		PostHogHostVariable:           true,
		AnalyticsSampleRateVariable:   true,
	}
	var validationErrors ValidationErrors
	for name := range values {
		if !known[name] {
			validationErrors = append(validationErrors, ValidationError{
				Variable: name,
				Problem:  "unknown variable (typo? see .env.example for the supported list)",
			})
		}
	}

	config := Config{
		env:               EnvDevelopment,
		addr:              "127.0.0.1:8080",
		assetsDir:         DefaultAssetsDir,
		logLevel:          LogLevelInfo,
		dbMaxConns:        10,
		dbMinConns:        2,
		dbMaxConnLifetime: 1 * time.Hour,
		dbMaxConnIdleTime: 30 * time.Minute,
		dbAcquireTimeout:  5 * time.Second,
		stripeTimeout:     10 * time.Second,

		analyticsSampleRate: DefaultAnalyticsSampleRate,
	}

	if raw, present := values["ARENA_ENV"]; present {
		switch Env(raw) {
		case EnvDevelopment, EnvTest, EnvProduction:
			config.env = Env(raw)
		default:
			validationErrors = append(validationErrors, ValidationError{
				Variable: "ARENA_ENV",
				Problem:  fmt.Sprintf("invalid value %q (allowed: development, test, production)", raw),
			})
		}
	}

	if raw, present := values["ARENA_ADDR"]; present {
		config.addr = raw
		problem := validateAddr(raw)
		if problem != "" {
			validationErrors = append(validationErrors, ValidationError{
				Variable: "ARENA_ADDR",
				Problem:  problem,
			})
		}
	}

	if raw, present := values["ARENA_ADMIN_ADDR"]; present {
		config.adminAddr = raw
		if problem := validateAdminAddr(raw); problem != "" {
			validationErrors = append(validationErrors, ValidationError{Variable: "ARENA_ADMIN_ADDR", Problem: problem})
		}
	}

	if raw, present := values["ARENA_ASSETS_DIR"]; present {
		if strings.TrimSpace(raw) == "" {
			validationErrors = append(validationErrors, ValidationError{
				Variable: "ARENA_ASSETS_DIR",
				Problem:  "must name the directory of an asset build (for example web/dist, produced by 'make build-web')",
			})
		} else {
			config.assetsDir = raw
		}
	}

	if raw, present := values[EmailSinkDirVariable]; present {
		if strings.TrimSpace(raw) == "" {
			validationErrors = append(validationErrors, ValidationError{
				Variable: EmailSinkDirVariable,
				Problem:  "must name the directory the local email sink writes to (for example .tmp/email-sink)",
			})
		} else {
			config.emailSinkDir = raw
		}
	}

	if raw, present := values[CursorSecretVariable]; present {
		problem := validateCursorSecret(raw)
		if problem == "" {
			config.cursorSecret = NewSecret(raw)
		} else {
			validationErrors = append(validationErrors, ValidationError{
				Variable: CursorSecretVariable,
				Problem:  problem,
			})
		}
	}

	if raw, present := values["ARENA_DATABASE_URL"]; present {
		config.databaseURL = NewSecret(raw)
		problem := validateDatabaseURL(raw)
		if problem != "" {
			validationErrors = append(validationErrors, ValidationError{
				Variable: "ARENA_DATABASE_URL",
				Problem:  problem,
			})
		}
	}

	if raw, present := values["ARENA_LOG_LEVEL"]; present {
		switch LogLevel(raw) {
		case LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError:
			config.logLevel = LogLevel(raw)
		default:
			validationErrors = append(validationErrors, ValidationError{
				Variable: "ARENA_LOG_LEVEL",
				Problem:  fmt.Sprintf("invalid value %q (allowed: debug, info, warn, error)", raw),
			})
		}
	}

	if raw, present := values["ARENA_DB_MAX_CONNS"]; present {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n <= 0 {
			validationErrors = append(validationErrors, ValidationError{
				Variable: "ARENA_DB_MAX_CONNS",
				Problem:  fmt.Sprintf("invalid value %q (must be a positive integer)", raw),
			})
		} else {
			config.dbMaxConns = int32(n)
		}
	}

	if raw, present := values["ARENA_DB_MIN_CONNS"]; present {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 0 {
			validationErrors = append(validationErrors, ValidationError{
				Variable: "ARENA_DB_MIN_CONNS",
				Problem:  fmt.Sprintf("invalid value %q (must be a non-negative integer)", raw),
			})
		} else {
			config.dbMinConns = int32(n)
		}
	}

	if config.dbMinConns > config.dbMaxConns {
		validationErrors = append(validationErrors, ValidationError{
			Variable: "ARENA_DB_MIN_CONNS",
			Problem:  fmt.Sprintf("cannot be greater than ARENA_DB_MAX_CONNS (%d > %d)", config.dbMinConns, config.dbMaxConns),
		})
	}

	if raw, present := values["ARENA_DB_MAX_CONN_LIFETIME"]; present {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			validationErrors = append(validationErrors, ValidationError{
				Variable: "ARENA_DB_MAX_CONN_LIFETIME",
				Problem:  fmt.Sprintf("invalid duration %q (must be positive, for example 1h, 30m)", raw),
			})
		} else {
			config.dbMaxConnLifetime = d
		}
	}

	if raw, present := values["ARENA_DB_MAX_CONN_IDLE_TIME"]; present {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			validationErrors = append(validationErrors, ValidationError{
				Variable: "ARENA_DB_MAX_CONN_IDLE_TIME",
				Problem:  fmt.Sprintf("invalid duration %q (must be positive, for example 30m, 10m)", raw),
			})
		} else {
			config.dbMaxConnIdleTime = d
		}
	}

	if raw, present := values["ARENA_DB_ACQUIRE_TIMEOUT"]; present {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			validationErrors = append(validationErrors, ValidationError{
				Variable: "ARENA_DB_ACQUIRE_TIMEOUT",
				Problem:  fmt.Sprintf("invalid duration %q (must be positive, for example 5s, 10s)", raw),
			})
		} else {
			config.dbAcquireTimeout = d
		}
	}

	if raw, present := values[billingMarketsVariable]; present {
		markets, problems := parseBillingMarkets(raw)
		config.billingMarkets = markets
		validationErrors = append(validationErrors, problems...)
	}

	if raw, present := values[billingPriceIDsVariable]; present {
		prices, problems := parseBillingPrices(raw)
		config.billingPrices = prices
		validationErrors = append(validationErrors, problems...)
	}

	if raw, present := values[billingSuccessURLVariable]; present {
		validated, problems := parseBillingReturnURL(billingSuccessURLVariable, raw, config.env == EnvProduction)
		config.billingSuccessURL = validated
		validationErrors = append(validationErrors, problems...)
	}

	if raw, present := values[billingCancelURLVariable]; present {
		validated, problems := parseBillingReturnURL(billingCancelURLVariable, raw, config.env == EnvProduction)
		config.billingCancelURL = validated
		validationErrors = append(validationErrors, problems...)
	}

	// The two return URLs are one allowlist: both must send the buyer back to
	// the same origin, and a mismatch is a misconfiguration that could leak a
	// buyer to another site.
	if config.billingSuccessURL != "" && config.billingCancelURL != "" &&
		!sameOrigin(config.billingSuccessURL, config.billingCancelURL) {
		validationErrors = append(validationErrors, ValidationError{
			Variable: billingCancelURLVariable,
			Problem:  "must share the origin of " + billingSuccessURLVariable,
		})
	}

	if raw, present := values[stripeSecretKeyVariable]; present {
		secret, problems := parseStripeSecretKey(raw)
		config.stripeSecretKey = secret
		validationErrors = append(validationErrors, problems...)
	}

	if raw, present := values[stripeTimeoutVariable]; present {
		timeout, problems := parseStripeTimeout(raw)
		if len(problems) == 0 {
			config.stripeTimeout = timeout
		}
		validationErrors = append(validationErrors, problems...)
	}

	if raw, present := values[ResendAPIKeyVariable]; present {
		secret, problems := parseResendAPIKey(raw)
		config.resendAPIKey = secret
		validationErrors = append(validationErrors, problems...)
	}

	if raw, present := values[EmailFromVariable]; present {
		address, problems := parseEmailFrom(raw)
		if len(problems) == 0 {
			config.emailFrom = address
		}
		validationErrors = append(validationErrors, problems...)
	}

	if raw, present := values[SentryDSNVariable]; present {
		secret, problems := parseSentryDSN(raw)
		config.sentryDSN = secret
		validationErrors = append(validationErrors, problems...)
	}

	if raw, present := values[PostHogAPIKeyVariable]; present {
		secret, problems := parsePostHogAPIKey(raw)
		config.posthogAPIKey = secret
		validationErrors = append(validationErrors, problems...)
	}

	if raw, present := values[PostHogHostVariable]; present {
		host, problems := parsePostHogHost(raw)
		if len(problems) == 0 {
			config.posthogHost = host
		}
		validationErrors = append(validationErrors, problems...)
	}

	if raw, present := values[AnalyticsSampleRateVariable]; present {
		rate, problems := parseAnalyticsSampleRate(raw)
		if len(problems) == 0 {
			config.analyticsSampleRate = rate
		}
		validationErrors = append(validationErrors, problems...)
	}

	// Production-specific safety rules: the plan forbids insecure production
	// defaults, so required secrets must be present in that environment.
	if config.env == EnvProduction && !config.databaseURL.IsSet() {
		validationErrors = append(validationErrors, ValidationError{
			Variable: "ARENA_DATABASE_URL",
			Problem:  "required when ARENA_ENV=production",
		})
	}

	// The local email sink is a development convenience: it writes the codes
	// of verification and recovery messages to disk so a journey can be
	// completed without a provider. No environment that serves real accounts
	// may install it, whichever layer asks, so the variable is refused here
	// instead of being ignored (P18-T07).
	if config.env == EnvProduction && config.emailSinkDir != "" {
		validationErrors = append(validationErrors, ValidationError{
			Variable: EmailSinkDirVariable,
			Problem:  "is refused when ARENA_ENV=production: it would put the codes of real accounts on disk",
		})
	}

	// Selling is impossible without the payment provider credential, and the
	// versioned catalog already refuses to build in production without an
	// enabled commercial region, so production always sells: the credential is
	// part of the production safety rules.
	if config.env == EnvProduction && !config.stripeSecretKey.IsSet() {
		validationErrors = append(validationErrors, ValidationError{
			Variable: stripeSecretKeyVariable,
			Problem:  "required when ARENA_ENV=production",
		})
	}

	// Production always delivers email: every registration sends a confirmation
	// link, so the environment that serves real accounts always has a provider
	// credential and a verified sender. Requiring them here — rather than
	// letting the composition discover it — makes the refusal name the variable
	// an operator has to set (P19-T02A).
	if config.env == EnvProduction && !config.resendAPIKey.IsSet() {
		validationErrors = append(validationErrors, ValidationError{
			Variable: ResendAPIKeyVariable,
			Problem:  "required when ARENA_ENV=production",
		})
	}

	if config.env == EnvProduction && config.emailFrom == "" {
		validationErrors = append(validationErrors, ValidationError{
			Variable: EmailFromVariable,
			Problem:  "required when ARENA_ENV=production",
		})
	}

	if len(validationErrors) > 0 {
		return Config{}, validationErrors
	}
	return config, nil
}

// MustLoad is the bootstrap helper for cmd/: it loads from the process
// environment and terminates with a clear multi-line error on any problem.
func MustLoad() Config {
	config, err := Load(os.Environ())
	if err != nil {
		panic(err)
	}
	return config
}

// Env returns the deployment environment.
func (config Config) Env() Env { return config.env }

// IsProduction reports whether the process runs with production safety rules.
func (config Config) IsProduction() bool { return config.env == EnvProduction }

// Addr returns the HTTP listen address.
func (config Config) Addr() string { return config.addr }

// AdminAddr returns the optional loopback-only administrative listener address.
// An empty value disables profiling and other administrative endpoints.
func (config Config) AdminAddr() string { return config.adminAddr }

// DefaultAssetsDir is the directory the frontend asset pipeline writes to
// (P18-T01). The server reads its manifest back at boot; a deployment that
// lays the build out elsewhere points ARENA_ASSETS_DIR at it.
const DefaultAssetsDir = "web/dist"

// AssetsDir returns the directory of the hashed frontend build whose manifest
// the server resolves through its templates.
func (config Config) AssetsDir() string { return config.assetsDir }

// EmailSinkDirVariable names the directory the local email sink writes to
// (P18-T07). It exists so a journey driven by another process — the browser
// harness — can read the code a message carries, and it is meaningful only in
// development and test: production refuses the variable, because a directory
// of account codes is not a delivery mechanism.
const EmailSinkDirVariable = "ARENA_EMAIL_SINK_DIR"

// EmailSinkDir returns the directory the local email sink writes to, and the
// empty string when nothing configures the sink. Development and test install
// it; production refuses the variable above.
func (config Config) EmailSinkDir() string { return config.emailSinkDir }

// CursorSecretVariable signs the pagination cursors of the public lists
// (P18-T07B). It enters configuration together with the composition that
// consumes it: a cursor signed with an ephemeral key would stop resolving
// after a restart, which a person experiences as a page that broke rather
// than as a security property.
const CursorSecretVariable = "ARENA_CURSOR_SECRET"

// minCursorSecretLength is the key size the cursor codecs of the modules
// require. It is repeated here so that a short secret is refused at boot, with
// the variable named, instead of failing later inside a use case constructor.
const minCursorSecretLength = 32

// validateCursorSecret enforces the key size of the cursor signing secret.
func validateCursorSecret(raw string) string {
	if len(raw) < minCursorSecretLength {
		return fmt.Sprintf("must be at least %d bytes of entropy (got %d)", minCursorSecretLength, len(raw))
	}
	return ""
}

// CursorSecret returns the redacted signing secret of the pagination cursors.
// It is unset when nothing configures it, and the composition that needs it
// refuses to build rather than minting one of its own.
func (config Config) CursorSecret() Secret { return config.cursorSecret }

// DatabaseURL returns the redacted database DSN.
func (config Config) DatabaseURL() Secret { return config.databaseURL }

// LogLevel returns the minimum log severity.
func (config Config) LogLevel() LogLevel { return config.logLevel }

// DBMaxConns returns the maximum connection limit of the database pool.
func (config Config) DBMaxConns() int32 { return config.dbMaxConns }

// DBMinConns returns the minimum number of idle connections in the pool.
func (config Config) DBMinConns() int32 { return config.dbMinConns }

// DBMaxConnLifetime returns the maximum connection lifetime in the pool.
func (config Config) DBMaxConnLifetime() time.Duration { return config.dbMaxConnLifetime }

// DBMaxConnIdleTime returns the maximum connection idle duration.
func (config Config) DBMaxConnIdleTime() time.Duration { return config.dbMaxConnIdleTime }

// DBAcquireTimeout returns the timeout for acquiring a connection from the pool.
func (config Config) DBAcquireTimeout() time.Duration { return config.dbAcquireTimeout }

// GoString implements fmt.GoStringer on the Config itself, because %#v walks
// the struct fields and would render the secret fields through reflection
// without consulting their own redacting methods (P18-T07B).
func (config Config) GoString() string {
	return config.String()
}

// String implements fmt.Stringer with a fully redacted representation, so a
// Config can be safely logged without leaking any value.
func (config Config) String() string {
	return fmt.Sprintf(
		"config{env:%s addr:%s assets_dir:%s database_url:%s log_level:%s db_max_conns:%d db_min_conns:%d}",
		config.env, config.addr, config.assetsDir, config.databaseURL, config.logLevel, config.dbMaxConns, config.dbMinConns,
	)
}

// sameOrigin reports whether two validated return URLs share scheme, host and
// port, so the checkout can only ever send the buyer back to one place.
func sameOrigin(left, right string) bool {
	leftURL, leftErr := url.Parse(left)
	rightURL, rightErr := url.Parse(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return leftURL.Scheme == rightURL.Scheme && leftURL.Host == rightURL.Host
}

// validateAddr enforces a host:port TCP address with a numeric port.
func validateAdminAddr(raw string) string {
	host, port, err := net.SplitHostPort(raw)
	if err != nil || host == "" || port == "" {
		return fmt.Sprintf("invalid value %q (want a loopback host:port address)", raw)
	}
	if host != "localhost" && net.ParseIP(host) == nil {
		return fmt.Sprintf("invalid host %q (administrative listener must be loopback)", host)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Sprintf("invalid host %q (administrative listener must be loopback)", host)
		}
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return fmt.Sprintf("invalid port %q (want a number between 1 and 65535)", port)
	}
	return ""
}

func validateAddr(raw string) string {
	host, port, found := strings.Cut(raw, ":")
	if !found || host == "" {
		return fmt.Sprintf("invalid value %q (want host:port, for example 127.0.0.1:8080)", raw)
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return fmt.Sprintf("invalid port %q (want a number between 1 and 65535)", port)
	}
	if portNumber == 0 {
		return fmt.Sprintf("invalid port %q (must be between 1 and 65535)", port)
	}
	return ""
}

// validateDatabaseURL enforces a non-local PostgreSQL DSN shape without
// parsing credentials. Local development sockets keep working because the
// check only applies when the value looks like a network DSN.
func validateDatabaseURL(raw string) string {
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		return "" // libpq-style key=value DSNs are accepted as-is.
	}
	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		return ""
	}
	return fmt.Sprintf("unsupported scheme in %q (want postgres:// or postgresql://, or a libpq key=value DSN)", raw)
}
