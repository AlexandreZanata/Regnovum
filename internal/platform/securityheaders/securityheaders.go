// Package securityheaders owns the browser security policy of Regnovum
// (P16-T01): one middleware carries the policy on every response the arena
// binary emits, so no route can opt out — not the module handlers, not the
// health endpoints, not the 404 of an unknown path.
//
// The policy is docs/SECURITY.md section 4: a restrictive
// Content-Security-Policy with the origin's own modules and styles, without
// 'unsafe-inline' or 'unsafe-eval'; HSTS; nosniff; a referrer policy; a
// permissions policy; and framing protection.
package securityheaders

import "net/http"

// Header names of the policy.
const (
	headerContentSecurityPolicy = "Content-Security-Policy"
	headerContentTypeOptions    = "X-Content-Type-Options"
	headerReferrerPolicy        = "Referrer-Policy"
	headerPermissionsPolicy     = "Permissions-Policy"
	headerStrictTransportSec    = "Strict-Transport-Security"
)

// contentSecurityPolicy restricts every fetch to the origin itself. There is
// deliberately no 'unsafe-inline', no 'unsafe-eval' and no nonce.
//
// A nonce is not needed here, and it would actively hurt. The only inline
// <script> the binary renders is the JSON-LD data block of the public Arena
// document, and a data block is never prepared for execution — its type is
// not a JavaScript MIME type (HTML Standard, "prepare the script element") —
// so script-src never applies to it. A nonce, by contrast, must be unique per
// response, so it would put a fresh value in the body of documents that are
// public and cacheable and make their ETags move on every request: the exact
// defect class that made those documents uncacheable. The premise this policy
// rests on — no server-rendered page carries an executable inline script, an
// inline style or an inline event handler — is asserted by the tests in this
// package, so adding one fails the build instead of silently breaking the
// pages.
//
// Every directive is either an origin restriction or a restriction on what
// the document may do with itself: frame-ancestors 'none' and form-action
// 'self' bound framing and form submission, base-uri 'self' neutralizes an
// injected <base>, and object-src 'none' removes plugin content. Widening any
// of them is a reviewed policy change, never a local convenience.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self'; font-src 'self'; connect-src 'self'; " +
	"form-action 'self'; frame-ancestors 'none'; base-uri 'self'; object-src 'none'"

// contentTypeOptions stops content sniffing, which is what turns a document
// served with the wrong type into script execution.
const contentTypeOptions = "nosniff"

// Header is one field of the policy, named and valued exactly as the
// middleware writes it.
type Header struct {
	Name  string
	Value string
}

// Policy returns the policy of one environment as the fields the middleware
// delivers, in the order it delivers them.
//
// It exists so that a component which carries the same policy can be compared
// against this one instead of keeping a second copy of the answer. The edge
// does exactly that: its own error responses — the ones this process emits when
// the application never answered — carry the policy too, and the gate that
// refuses a drifted copy (`tools/caddyaudit`, P19-T03) asks this function what
// the policy is. A gate holding its own copy would measure the copy.
//
// The set is returned rather than written into a writer because a caller that
// is handed a writer is a caller that can write something else.
func Policy(production bool) []Header {
	policy := []Header{
		{headerContentSecurityPolicy, contentSecurityPolicy},
		{headerContentTypeOptions, contentTypeOptions},
		{headerReferrerPolicy, referrerPolicy},
		{headerPermissionsPolicy, permissionsPolicy},
	}
	if production {
		policy = append(policy, Header{headerStrictTransportSec, strictTransportSecurity})
	}
	return policy
}

// ContentSecurityPolicy returns the exact policy the middleware delivers.
//
// It exists for the frontend gate of P18-T08 (`tools/webaudit`): the delivered
// code has to be compatible with the policy of the binary that serves it, and a
// gate cannot ask that question by keeping a second copy of the answer. The
// value is the constant itself, not a parameter: a caller that could change it
// would be changing the policy of every response, which is a reviewed edit to
// this file and to docs/SECURITY.md section 4, never a local convenience.
func ContentSecurityPolicy() string { return contentSecurityPolicy }

// referrerPolicy keeps the referrer inside the origin: a request to another
// origin carries none. Arena addresses carry the user's own published slug in
// the path, and nothing in the product needs to tell a third-party site which
// documents a visitor came from, so nothing leaves. Inside the origin the
// product has no analytics either, and a same-origin referrer costs nothing.
//
// It is deliberately not "no-referrer" (P18-T07D). That policy does more than
// withhold the Referer: the HTML standard also serialises the **Origin** of a
// form submission as "null" under it, and the double submit of the security
// boundary refuses a request whose Origin is present and has no host. With
// "no-referrer" no browser could submit any form of the product —
// registration, sign-in, confirmation, position, publication, attribution —
// while the strict-origin control declared in docs/THREAT_MODEL.md
// (THR-AUTH-03) looked alive, because the Go tests never send an Origin. The
// two halves of this choice are asserted: this package checks the exact value
// delivered, and the middleware's own tests check that a nullified origin is
// still refused while the origin of the serving host is accepted.
const referrerPolicy = "same-origin"

// permissionsPolicy disables the capabilities the product does not use, one
// entry per capability so the set is auditable against the features the MVP
// actually ships. Payment is denied because checkout is a server-created
// Stripe session the browser is redirected to, not an in-page Payment Request
// API call; enabling it back would be a deliberate decision with the payments
// owner, not a default.
const permissionsPolicy = "accelerometer=(), camera=(), display-capture=(), geolocation=(), " +
	"gyroscope=(), magnetometer=(), microphone=(), midi=(), payment=(), " +
	"screen-wake-lock=(), usb=(), xr-spatial-tracking=()"

// strictTransportSecurity pins browsers to HTTPS for a year, subdomains
// included. It is production-only for a reason that is not cosmetic: over
// plain HTTP the header is meaningless at best, and a browser that honors it
// while developing against http://127.0.0.1 would pin that host to HTTPS and
// make the development server unreachable until the max-age expires. There is
// no 'preload' token: preload is a commitment submitted to browser vendors
// and revoked by them, so it is an operational decision for the deployment
// phase, not a default of the middleware.
const strictTransportSecurity = "max-age=31536000; includeSubDomains"

// Config is the policy of one environment.
type Config struct {
	// Production enables the transport security headers, which are only
	// meaningful over TLS and only safe once the deployment actually serves
	// HTTPS end to end (docs/DEPLOYMENT.md: Cloudflare in Full (strict)).
	Production bool
}

// Middleware applies the policy to every response of the wrapped handler.
//
// The headers are written before the wrapped handler runs, so they are
// present on success, on error, on redirect and on 304 alike: a cached
// response is served with the policy the browser stored alongside it, and a
// response that must never be cached is still not allowed to lose its
// framing protection. A handler that wanted to remove one of them would have
// to overwrite the header by name, and the package tests assert the policy on
// every registered route and every class of response, so weakening it is a
// visible edit rather than a silent one.
func Middleware(config Config) func(http.Handler) http.Handler {
	// The policy of this environment is computed once, when the middleware is
	// built, and not per request: it is a constant decision, and rebuilding it
	// inside the handler would put an allocation on the path of every response
	// to say the same thing.
	policy := Policy(config.Production)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			header := writer.Header()
			for _, field := range policy {
				header.Set(field.Name, field.Value)
			}

			next.ServeHTTP(writer, request)
		})
	}
}
