// Composition of the guarded legacy billing writes (P49-T06): the hosted
// checkout and the customer portal for the versioned catalog product,
// plus the provider-only webhook settlement pipeline that turns a verified
// provider event into the granted benefit.
//
// Why one surface for writes and settlement together: a checkout without a
// verified settlement path would take money it can never convert into a
// benefit, so the writes mount only beside the pipeline that settles them.
// The pipeline answers no browser route — webhook ingress is a
// server-to-server contract authenticated by the provider signature, and no
// client UI is ever given to it. Reads stay with the P49-T05 entitlement
// surface; no seasonal INK and no new currency enter here.
package bootstrap

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	billinghttp "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/http"
	billingpostgres "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	billingstripe "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/stripe"
	billingwallet "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/wallet"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	identitypostgres "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/sessionvalidator"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clientip"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
	"github.com/AlexandreZanata/Regnovum/internal/platform/ratelimit"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
	walletpostgres "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
)

// BillingConfig are the payment edges one billing-writes surface is composed
// from. Every field is required: a payment composition with a missing piece
// is a refusal, never a checkout that cannot settle.
type BillingConfig struct {
	// Gateway reaches the payment provider. Tests inject a deterministic
	// synthetic gateway; production hands the Stripe gateway built from the
	// configured secret. An unavailable provider fails the checkout instead
	// of granting credit.
	Gateway billingapp.PaymentGateway
	// Catalog is the versioned price list in force, the only source of the
	// product, the amount and the currency the browser can never propose.
	Catalog *billingdomain.Catalog
	// SuccessURL and CancelURL are the allowlisted destinations the provider
	// sends the buyer back to. They come from configuration, never from the
	// browser, and visiting them grants nothing: only a verified webhook
	// event settles an intent.
	SuccessURL string
	CancelURL  string
	// PortalReturnURL is where the provider sends the buyer after the hosted
	// portal. Allowlisted configuration, never browser input.
	PortalReturnURL string
	// WebhookSecret authenticates the server-to-server event ingress. Without
	// it no event can be verified, so the whole surface is refused instead
	// of mounting writes that could never settle.
	WebhookSecret string
}

// BillingSurface is the composed guarded billing surface, ready to be mounted
// on the platform mux beside the other journeys.
type BillingSurface struct {
	routes   []httpserver.Route
	register func(mux *http.ServeMux)
	pipeline *billingapp.ProcessWebhookUseCase

	mu      sync.Mutex
	mounted bool
}

// billingWriteRoutes are the two writes this surface mounts. They repeat the
// canonical billing declarations (the P48 inventory collects route literals
// from the adapter routes files only), so the surface answers exactly the
// guarded writes and never a second copy of the reads.
var billingWriteRoutes = []httpserver.Route{
	{Method: http.MethodPost, Path: "/api/v1/me/billing/checkout"},
	{Method: http.MethodPost, Path: "/api/v1/me/billing/portal"},
}

// ComposeBillingWrites builds the guarded billing surface: the hosted
// checkout and the customer portal over the real PostgreSQL repositories,
// the shared security manager and the shared session validator, plus the
// provider-only webhook pipeline with every grant path the catalog sells
// (INK, passes and Member) so no verified payment is left unsettled.
//
// It fails closed: without the gateway, the catalog, the allowlisted URLs
// or the webhook secret the writes stay unmounted, and production refuses
// the incomplete composition instead of faking a payment.
func ComposeBillingWrites(options Options, billing BillingConfig) (*BillingSurface, error) {
	if err := validateBillingWrites(options, billing); err != nil {
		return nil, err
	}
	manager, err := options.securityManager()
	if err != nil {
		return nil, fmt.Errorf("bootstrap: billing writes: security manager: %w", err)
	}
	validator, err := composeBillingValidator(options)
	if err != nil {
		return nil, err
	}
	handler, pipeline, err := buildBillingWriters(options, billing, manager, validator)
	if err != nil {
		return nil, err
	}
	routes := append([]httpserver.Route(nil), billingWriteRoutes...)
	options.Logger.Info("billing writes: composed",
		slog.String("env", string(options.Env)),
		slog.Int("routes", len(routes)),
	)
	return &BillingSurface{
		routes:   routes,
		pipeline: pipeline,
		register: func(mux *http.ServeMux) {
			handler.RegisterBillingWriteRoutes(mux)
		},
	}, nil
}

// buildBillingWriters wires the checkout and portal handlers plus the
// provider-only settlement pipeline over the shared edges: the same pool the
// account journey wrote to, the same security boundary, and one session
// validator for both writes.
func buildBillingWriters(options Options, billing BillingConfig, manager *security.Manager, validator security.SessionValidator) (*billinghttp.BillingHandler, *billingapp.ProcessWebhookUseCase, error) {
	// The repository carries the process clock because the webhook event
	// lifecycle stamps it: a repository without a clock would panic on the
	// first verified settlement instead of recording it.
	repository := billingpostgres.NewRepositoryWithClock(options.Pool, options.Clock)
	inker := billingwallet.NewInker(walletpostgres.NewRepository(options.Pool), options.Clock)

	checkout, err := billingapp.NewCreateCheckoutUseCase(billingapp.CheckoutDependencies{
		Catalog:    billing.Catalog,
		Gateway:    billing.Gateway,
		Purchasers: repository,
		Customers:  repository,
		Intents:    repository,
		Clock:      options.Clock,
		Returns: billingapp.CheckoutReturnURLs{
			Success: billing.SuccessURL,
			Cancel:  billing.CancelURL,
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: billing writes: checkout: %w", err)
	}
	portal, err := billingapp.NewGetBillingPortalUseCase(billingapp.PortalDependencies{
		Customers: repository,
		Gateway:   billing.Gateway,
		ReturnURL: billing.PortalReturnURL,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: billing writes: portal: %w", err)
	}
	settler, err := billingapp.NewSettleCheckoutUseCase(billingapp.SettleCheckoutDependencies{
		Catalog: billing.Catalog,
		Intents: repository,
		Inker:   inker,
		Clock:   options.Clock,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: billing writes: INK settler: %w", err)
	}
	passSettler, err := billingapp.NewSettleArenaPassUseCase(billingapp.SettleArenaPassDependencies{
		Catalog: billing.Catalog,
		Intents: repository,
		Lots:    repository,
		Clock:   options.Clock,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: billing writes: pass settler: %w", err)
	}
	memberSettler, err := billingapp.NewApplyMemberEntitlementsUseCase(billingapp.MemberEntitlementsDependencies{
		Catalog:       billing.Catalog,
		Subscriptions: repository,
		Customers:     repository,
		PassLots:      repository,
		Inker:         inker,
		Clock:         options.Clock,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: billing writes: member settler: %w", err)
	}
	verifier, err := billingstripe.NewWebhookVerifier(billing.WebhookSecret, 5*time.Minute, options.Clock)
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: billing writes: webhook verifier: %w", err)
	}
	pipeline, err := billingapp.NewProcessWebhookUseCase(billingapp.ProcessWebhookDependencies{
		Verifier:      verifier,
		Events:        repository,
		Settler:       settler,
		PassSettler:   passSettler,
		MemberSettler: memberSettler,
		Clock:         options.Clock,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: billing writes: webhook pipeline: %w", err)
	}
	resolver := clientip.New(nil)
	handler := billinghttp.NewBillingHandler(billinghttp.BillingHandlerConfig{
		CreateCheckout:        checkout,
		GetSubscriptionStatus: billingapp.NewGetSubscriptionStatusUseCase(repository),
		GetBillingPortal:      portal,
		SecurityManager:       manager,
		RateLimit:             ratelimit.New(ratelimit.NewLimiter(ratelimit.Options{Now: options.Clock.Now}), resolver),
		SessionValidator:      validator,
	})
	return handler, pipeline, nil
}

// WebhookPipeline is the provider-only settlement pipeline: it verifies the
// provider signature, claims the event idempotently and settles the matching
// intent. It is intentionally not mounted on the browser mux — the ingress
// stays a server-to-server contract, and the accessor exists for the future
// ingress and for the composition proof, never for a client UI.
func (surface *BillingSurface) WebhookPipeline() *billingapp.ProcessWebhookUseCase {
	return surface.pipeline
}

// composeBillingValidator resolves the session cookie through the identity
// module: one validator for both writes of the surface, over the same pool
// the login wrote to.
func composeBillingValidator(options Options) (security.SessionValidator, error) {
	identityRepository := identitypostgres.NewRepository(options.Pool)
	authenticate := identityapp.NewAuthenticateSessionUseCase(
		identityRepository, identityRepository, options.Clock, identitydomain.DefaultSessionPolicy(), 0,
	)
	validator, err := sessionvalidator.New(authenticate)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: billing writes: session validator: %w", err)
	}
	return validator, nil
}

// Routes is the canonical route list of the surface.
func (surface *BillingSurface) Routes() []httpserver.Route {
	return append([]httpserver.Route(nil), surface.routes...)
}

// Surface is what the platform router mounts.
func (surface *BillingSurface) Surface() httpserver.Surface {
	return httpserver.Surface{Routes: surface.Routes(), Register: surface.Mount}
}

// Mount registers the surface on the mux, refusing a second mount instead of
// panicking on duplicate patterns.
func (surface *BillingSurface) Mount(mux *http.ServeMux) error {
	if mux == nil {
		return fmt.Errorf("%w: billing writes: mux is required", ErrIncompleteComposition)
	}
	surface.mu.Lock()
	defer surface.mu.Unlock()
	if surface.mounted {
		return fmt.Errorf("%w: billing writes are already mounted; mounting them again would register duplicate patterns", ErrIncompleteComposition)
	}
	surface.register(mux)
	surface.mounted = true
	return nil
}

// validateBillingWrites reports every missing edge at once, sorted. The
// payment edges are as required as the process ones: a checkout that cannot
// settle is worse than no checkout route at all.
func validateBillingWrites(options Options, billing BillingConfig) error {
	missing := make([]string, 0, 8)
	if options.Logger == nil {
		missing = append(missing, "logger")
	}
	if options.Clock == nil {
		missing = append(missing, "clock")
	}
	if options.Pool == nil {
		missing = append(missing, "postgres pool")
	}
	if billing.Gateway == nil {
		missing = append(missing, "payment gateway")
	}
	if billing.Catalog == nil {
		missing = append(missing, "versioned catalog")
	}
	if strings.TrimSpace(billing.SuccessURL) == "" {
		missing = append(missing, "billing success URL")
	}
	if strings.TrimSpace(billing.CancelURL) == "" {
		missing = append(missing, "billing cancel URL")
	}
	if strings.TrimSpace(billing.PortalReturnURL) == "" {
		missing = append(missing, "billing portal return URL")
	}
	if strings.TrimSpace(billing.WebhookSecret) == "" {
		missing = append(missing, "provider webhook secret")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: billing writes: missing %s", ErrIncompleteComposition, strings.Join(missing, ", "))
	}
	switch options.Env {
	case config.EnvDevelopment, config.EnvTest, config.EnvProduction:
		return nil
	default:
		return fmt.Errorf("%w: billing writes: environment %q is not one of development, test, production", ErrIncompleteComposition, options.Env)
	}
}
