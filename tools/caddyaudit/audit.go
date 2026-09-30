// Package main holds the audit of the Caddy origin configuration (P19-T03).
//
// Why this exists at all: the ingress is the only component of the deployment
// that the application cannot defend by itself. A reverse proxy decides who is
// believed about a visitor's address, what leaves with the response, whether
// the connection is refused before a request exists, and what a visitor sees
// when the application never answered. Each of those is a decision this
// repository writes down, so each of them is a decision something has to check.
//
// What it reads: the file as committed, parsed into statements. Not the
// rendered JSON — `caddy adapt` is what turns one into the other, and the gate
// that runs Caddy (`verify.sh`) is where the rendered form and the behaviour
// are measured. This half is the cheap one, and it is where a rule can say
// which line it is about.
//
// The rules are deliberately opinionated. Where a rule encodes a choice that
// another operator might make differently, the violation text carries the
// reason rather than the preference, because the reason is what makes the
// refusal fixable.
package main

import (
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// Violation is one broken rule, named as the rule that broke.
type Violation struct {
	Rule   string
	Line   int
	Detail string
}

func (violation Violation) String() string {
	if violation.Line > 0 {
		return fmt.Sprintf("%s: line %d: %s", violation.Rule, violation.Line, violation.Detail)
	}
	return fmt.Sprintf("%s: %s", violation.Rule, violation.Detail)
}

// Report is the measurement. It is always produced, even when rules are broken:
// a gate that only says "no" is a gate nobody can act on, and a deployment's
// own operator needs to see what the gate saw.
type Report struct {
	SiteAddress     string
	Upstream        string
	TrustedPrefixes []string
	ClientIPHeaders []string
	TLSProtocols    string
	ErrorPolicy     []string
	Violations      []Violation
}

// statement is one line of a Caddyfile: its arguments and, when the line opens
// a block, the statements inside it.
type statement struct {
	args     []string
	children []statement
	line     int
}

// token is one lexed token, carrying the line it came from so a violation can
// name it.
type token struct {
	text string
	line int
}

// Options is what the audit is told.
type Options struct {
	// File is the Caddyfile as committed.
	File string
	// SiteAddressVariable is the environment placeholder the site address must
	// come from, so that one file serves every environment. It is passed in
	// rather than hard-coded because the gate's fixtures are built from this
	// file and a fixture that renamed the variable would otherwise look like a
	// rule violation.
	SiteAddressVariable string
	// UpstreamVariable is the environment placeholder the upstream must come
	// from, for the same reason.
	UpstreamVariable string
}

// AdminAddressEnv is the environment variable Caddy itself reads when no
// `admin` option is declared. A file that does not declare the option cannot be
// audited for where the endpoint is, so the rule refuses the omission.
const adminPort = "2019"

// trustedProxyModule is the only module this audit accepts: the literal list.
// Any other module (a file, a plugin, a database) could hold values the audit
// cannot read, and a rule about an unreadable list is not a rule.
const trustedProxyModule = "static"

// Audit reads the file and answers every question.
func Audit(options Options) (Report, error) {
	raw, err := os.ReadFile(options.File)
	if err != nil {
		return Report{}, fmt.Errorf("read Caddyfile: %w", err)
	}
	statements, err := parse(raw)
	if err != nil {
		return Report{}, fmt.Errorf("parse Caddyfile: %w", err)
	}
	if len(statements) == 0 {
		return Report{}, fmt.Errorf("the file declares nothing")
	}

	report := Report{}
	violations := &report.Violations

	global := globalOptions(statements)
	if global == nil {
		*violations = append(*violations, Violation{
			Rule:   "global_options",
			Detail: "the file opens no global options block; the admin endpoint, the trusted proxies and the server's own limits are configured there and nowhere else",
		})
	}

	sites := siteBlocks(statements)
	switch len(sites) {
	case 1:
	case 0:
		*violations = append(*violations, Violation{
			Rule:   "site_declared",
			Detail: "the file declares no site block; there is nothing to answer for",
		})
	default:
		*violations = append(*violations, Violation{
			Rule:   "site_declared",
			Detail: fmt.Sprintf("the file declares %d site blocks (%s); this file answers for one name, and a second block is a second name nobody reviewed", len(sites), siteAddresses(sites)),
		})
	}

	servers := childByName(global, "servers")

	// ---------------------------------------------------------------------
	// The admin endpoint: present, and on loopback.
	// ---------------------------------------------------------------------
	if global != nil {
		admin := childByName(global, "admin")
		switch {
		case admin == nil:
			*violations = append(*violations, Violation{
				Rule:   "admin_declared",
				Line:   global.line,
				Detail: "the admin endpoint is not declared, so it is wherever the environment says it is; declare it on loopback so that the one surface which can rewrite this process is a decision rather than an inheritance",
			})
		case len(admin.args) != 1:
			*violations = append(*violations, Violation{
				Rule:   "admin_loopback",
				Line:   admin.line,
				Detail: fmt.Sprintf("admin takes %d argument(s) (%s); it takes one address", len(admin.args), strings.Join(admin.args, " ")),
			})
		default:
			if detail := adminAddressProblem(admin.args[0]); detail != "" {
				*violations = append(*violations, Violation{Rule: "admin_loopback", Line: admin.line, Detail: detail})
			}
		}
	}

	// ---------------------------------------------------------------------
	// The peer, and whose claim about a visitor is believed.
	// ---------------------------------------------------------------------
	if servers != nil {
		trusted := childByName(servers, "trusted_proxies")
		switch {
		case trusted == nil:
			*violations = append(*violations, Violation{
				Rule:   "trusted_proxies_declared",
				Line:   servers.line,
				Detail: "no trusted proxy is declared, so the proxy treats every peer as a client and every forwarding header as a client's claim; the CDN in front of this process is the one peer whose claim is evidence",
			})
		default:
			prefixes, detail := trustedPrefixes(*trusted)
			if detail != "" {
				*violations = append(*violations, Violation{Rule: "trusted_proxies_declared", Line: trusted.line, Detail: detail})
			}
			report.TrustedPrefixes = prefixes
		}

		headers := childByName(servers, "client_ip_headers")
		switch {
		case headers == nil || len(headers.args) == 0:
			*violations = append(*violations, Violation{
				Rule:   "client_ip_narrowed",
				Line:   servers.line,
				Detail: "no client IP header is declared, so the default chain is consulted: name the single header the CDN writes, and nothing else",
			})
		default:
			report.ClientIPHeaders = append([]string(nil), headers.args...)
			for _, name := range headers.args {
				if strings.EqualFold(name, "X-Forwarded-For") {
					*violations = append(*violations, Violation{
						Rule:   "client_ip_narrowed",
						Line:   headers.line,
						Detail: "X-Forwarded-For is a chain a client may pad, so a single-value claim cannot be checked in it; the CDN's own one-value header is the header that can be believed",
					})
				}
			}
		}
	}

	// ---------------------------------------------------------------------
	// The listener: SNI, protocols, timeouts.
	// ---------------------------------------------------------------------
	if servers == nil {
		*violations = append(*violations, Violation{
			Rule:   "servers_declared",
			Detail: "the global options declare no servers block; the listener's own settings live there",
		})
	} else {
		sni := childByName(servers, "strict_sni_host")
		if sni == nil || len(sni.args) != 1 || sni.args[0] != "on" {
			*violations = append(*violations, Violation{
				Rule:   "sni_strict",
				Line:   servers.line,
				Detail: "strict_sni_host is not on: a handshake whose name does not match a served certificate would be answered with whatever certificate comes first, which makes the name binding a convention instead of a fact",
			})
		}

		protocols := childByName(servers, "protocols")
		switch {
		case protocols == nil || len(protocols.args) == 0:
			*violations = append(*violations, Violation{
				Rule:   "protocols_bounded",
				Line:   servers.line,
				Detail: "the listener does not declare its protocols, so it accepts the default set, which includes UDP",
			})
		default:
			for _, protocol := range protocols.args {
				switch protocol {
				case "h1", "h2":
				case "h3", "h2c":
					*violations = append(*violations, Violation{
						Rule:   "protocols_bounded",
						Line:   protocols.line,
						Detail: fmt.Sprintf("%s requires a listener nothing on the real path uses: the CDN speaks TCP to the origin, so this is surface without a caller", protocol),
					})
				default:
					*violations = append(*violations, Violation{
						Rule:   "protocols_bounded",
						Line:   protocols.line,
						Detail: fmt.Sprintf("%q is not a protocol this audit knows, and a rule cannot judge what it cannot name", protocol),
					})
				}
			}
		}

		timeouts := childByName(servers, "timeouts")
		for _, required := range []string{"read_header", "idle"} {
			if option := childByName(timeouts, required); option == nil || len(option.args) != 1 || strings.TrimSpace(option.args[0]) == "" {
				*violations = append(*violations, Violation{
					Rule:   "edge_timeouts",
					Line:   servers.line,
					Detail: fmt.Sprintf("the listener declares no %s budget: a peer that never finishes a request, or holds a connection open forever, spends this process's resources for free", required),
				})
			}
		}
	}

	// ---------------------------------------------------------------------
	// The site: certificate, route, error policy.
	// ---------------------------------------------------------------------
	if len(sites) == 1 {
		site := sites[0]
		report.SiteAddress = strings.Join(site.args, " ")
		if !strings.HasPrefix(report.SiteAddress, "{") || !strings.Contains(report.SiteAddress, options.SiteAddressVariable) {
			*violations = append(*violations, Violation{
				Rule:   "site_address_from_environment",
				Line:   site.line,
				Detail: fmt.Sprintf("the site address is %q; it comes from %s so that one committed file serves every environment and the gate's fixtures are the same file", report.SiteAddress, options.SiteAddressVariable),
			})
		}

		auditTLS(&site, &report, violations)
		route := childByName(&site, "route")
		if route == nil {
			*violations = append(*violations, Violation{
				Rule:   "route_explicit",
				Line:   site.line,
				Detail: "the site block declares no route; the order the directives then run in is the order Caddy's table assigns, which is not a decision anyone wrote down",
			})
		} else {
			auditRoute(route, options, &report, violations)
		}
		auditErrors(&site, &report, violations)
		auditNoCacheOverride(&site, violations)
	}

	// The admin route rule reads the sites, not the whole file: the global
	// options block is where the admin endpoint is *declared*, and a rule that
	// walked it would refuse the very statement that keeps the endpoint on
	// loopback.
	for _, site := range sites {
		auditNoAdminRoute(site.children, violations)
	}

	return report, nil
}

// auditTLS answers the certificate questions: where the certificate comes from
// and how old a client may be.
func auditTLS(site *statement, report *Report, violations *[]Violation) {
	tlsDirective := childByName(site, "tls")
	if tlsDirective == nil {
		*violations = append(*violations, Violation{
			Rule:   "tls_declared",
			Line:   site.line,
			Detail: "the site declares no tls directive, so Caddy's automatic HTTPS decides: an ACME issuance needs the origin reachable from the internet, and this origin is not",
		})
		return
	}

	files := make([]string, 0, 2)
	for _, argument := range tlsDirective.args {
		if strings.HasPrefix(argument, "{") {
			continue
		}
		files = append(files, argument)
	}
	switch {
	case len(files) == 2:
		if !strings.HasPrefix(files[0], "/run/secrets/") || !strings.HasPrefix(files[1], "/run/secrets/") {
			*violations = append(*violations, Violation{
				Rule:   "tls_from_files",
				Line:   tlsDirective.line,
				Detail: fmt.Sprintf("the certificate is read from %s; a file secret mounted by the topology is the only source whose rotation is a deployment decision rather than a runtime one", strings.Join(files, " ")),
			})
		}
	case len(files) == 0:
		*violations = append(*violations, Violation{
			Rule:   "tls_from_files",
			Line:   tlsDirective.line,
			Detail: "the tls directive names no certificate file, so the certificate comes from an issuer rather than from the operator's secret",
		})
	default:
		*violations = append(*violations, Violation{
			Rule:   "tls_from_files",
			Line:   tlsDirective.line,
			Detail: fmt.Sprintf("the tls directive names %d file(s) (%s); it takes the certificate and its key, and a value that is neither is a certificate nobody promised", len(files), strings.Join(files, " ")),
		})
	}

	protocols := childByName(tlsDirective, "protocols")
	if protocols == nil || len(protocols.args) == 0 {
		*violations = append(*violations, Violation{
			Rule:   "tls_protocols",
			Line:   tlsDirective.line,
			Detail: "the tls directive declares no protocols, so the floor is whatever the build happens to ship; state the floor the deployment supports",
		})
		return
	}
	report.TLSProtocols = strings.Join(protocols.args, " ")
	for _, protocol := range protocols.args {
		switch protocol {
		case "tls1.2", "tls1.3":
		default:
			*violations = append(*violations, Violation{
				Rule:   "tls_protocols",
				Line:   protocols.line,
				Detail: fmt.Sprintf("%s is below the floor Cloudflare's Full (strict) mode requires of an origin, or is not a protocol this audit knows", protocol),
			})
		}
	}
}

// auditRoute answers what the proxied path does: the response headers it
// decides, the request headers it corrects, and how the upstream is asked.
func auditRoute(route *statement, options Options, report *Report, violations *[]Violation) {
	proxy := childByName(route, "reverse_proxy")
	if proxy == nil {
		*violations = append(*violations, Violation{
			Rule:   "upstream_declared",
			Line:   route.line,
			Detail: "the route declares no reverse_proxy; the site is an origin for an application and this file has to say where that application is",
		})
		return
	}
	if len(proxy.args) == 0 {
		*violations = append(*violations, Violation{Rule: "upstream_declared", Line: proxy.line, Detail: "the reverse_proxy names no upstream"})
	} else {
		report.Upstream = proxy.args[0]
		if !strings.HasPrefix(report.Upstream, "{") || !strings.Contains(report.Upstream, options.UpstreamVariable) {
			*violations = append(*violations, Violation{
				Rule:   "upstream_from_environment",
				Line:   proxy.line,
				Detail: fmt.Sprintf("the upstream is %q; it comes from %s so that the gate can point the same file at its own stub, which is what makes the assertions about pass-through measurable", report.Upstream, options.UpstreamVariable),
			})
		}
	}

	// server_header_removed
	removesServer := false
	for _, directive := range namedChildren(route.children, "header") {
		for _, argument := range directive.args {
			if argument == "-Server" {
				removesServer = true
			}
		}
	}
	if !removesServer {
		*violations = append(*violations, Violation{
			Rule:   "server_header_removed",
			Line:   route.line,
			Detail: "the response still names the software that served it; a version in a header is a version an attacker does not have to guess",
		})
	}

	// compression_bounded
	encode := childByName(route, "encode")
	switch {
	case encode == nil:
		*violations = append(*violations, Violation{
			Rule:   "compression_bounded",
			Line:   route.line,
			Detail: "the edge does not compress: the CDN's own compression is not a reason to hand it everything uncompressed, and the connection to the CDN is a connection over a network",
		})
	case len(encode.args) == 0:
		*violations = append(*violations, Violation{Rule: "compression_bounded", Line: encode.line, Detail: "encode declares no algorithm, so it accepts the default set"})
	default:
		for _, algorithm := range encode.args {
			switch algorithm {
			case "zstd", "gzip":
			default:
				*violations = append(*violations, Violation{
					Rule:   "compression_bounded",
					Line:   encode.line,
					Detail: fmt.Sprintf("%q is not an algorithm this audit knows, and each algorithm declared is CPU spent on every response", algorithm),
				})
			}
		}
	}

	// The request headers the upstream is allowed to see.
	upstreamForwards, stripsCF, stripsReal := false, false, false
	for _, directive := range namedChildren(proxy.children, "header_up") {
		switch directive.args[0] {
		case "X-Forwarded-For":
			if len(directive.args) == 2 && directive.args[1] == "{client_ip}" {
				upstreamForwards = true
			} else {
				*violations = append(*violations, Violation{
					Rule:   "forwarded_truthful",
					Line:   directive.line,
					Detail: fmt.Sprintf("X-Forwarded-For is set to %q; it is set to {client_ip}, which is the address this process determined, so that a chain the client padded cannot reach the application", strings.Join(directive.args, " ")),
				})
			}
		case "-CF-Connecting-IP":
			stripsCF = true
		case "-X-Real-IP":
			stripsReal = true
		}
	}
	if !upstreamForwards {
		*violations = append(*violations, Violation{
			Rule:   "forwarded_truthful",
			Line:   proxy.line,
			Detail: "the upstream is not told the address this process determined; without it every request from every visitor reaches the application as the CDN",
		})
	}
	if !stripsCF {
		*violations = append(*violations, Violation{
			Rule:   "forwarded_truthful",
			Line:   proxy.line,
			Detail: "the client's own CF-Connecting-IP is not removed, so a header the application is documented to ignore arrives with a value a client chose",
		})
	}
	if !stripsReal {
		*violations = append(*violations, Violation{
			Rule:   "forwarded_truthful",
			Line:   proxy.line,
			Detail: "the client's own X-Real-IP is not removed, for the same reason: no chain, so nothing in it can be checked",
		})
	}

	// upstream_health
	for _, required := range []string{"health_uri", "health_interval", "health_timeout"} {
		if option := childByName(proxy, required); option == nil || len(option.args) < 1 {
			*violations = append(*violations, Violation{
				Rule:   "upstream_health",
				Line:   proxy.line,
				Detail: fmt.Sprintf("the upstream declares no %s: an application that is running but not ready answers, and a proxy that cannot tell the difference sends visitors to it", required),
			})
		}
	}

	// upstream_timeouts
	transport := childByName(proxy, "transport")
	if transport == nil || len(transport.args) == 0 || transport.args[0] != "http" {
		*violations = append(*violations, Violation{
			Rule:   "upstream_timeouts",
			Line:   proxy.line,
			Detail: "the upstream declares no transport; without it a connection that never answers holds a request until the client gives up",
		})
		return
	}
	for _, required := range []string{"dial_timeout", "response_header_timeout"} {
		if option := childByName(transport, required); option == nil || len(option.args) != 1 {
			*violations = append(*violations, Violation{
				Rule:   "upstream_timeouts",
				Line:   transport.line,
				Detail: fmt.Sprintf("the transport declares no %s, so an upstream that accepts a connection and then says nothing is indistinguishable from a slow one", required),
			})
		}
	}
}

// auditErrors answers the one class of response the application cannot cover:
// the ones this process writes itself.
func auditErrors(site *statement, report *Report, violations *[]Violation) {
	errors := childByName(site, "handle_errors")
	if errors == nil {
		*violations = append(*violations, Violation{
			Rule:   "error_policy",
			Line:   site.line,
			Detail: "the site declares no handle_errors, so the responses this process writes when the application never answered carry no policy at all — and, worse for a proxy, no cache directive",
		})
		return
	}

	expected := map[string]string{}
	for _, field := range securityheaders.Policy(true) {
		expected[field.Name] = field.Value
	}
	expected["Cache-Control"] = "no-store"

	// A field that is removed (`-Name`) is deliberately not recorded as
	// declared: the rule below counts what the response carries.
	declared := map[string]string{}
	declaredLines := map[string]int{}
	for _, directive := range headerDirectives(childrenOf(errors)) {
		for _, field := range headerFields(directive) {
			if len(field.args) < 2 {
				continue
			}
			name := strings.TrimPrefix(strings.TrimPrefix(field.args[0], "+"), "?")
			if strings.HasPrefix(name, "-") {
				continue
			}
			declared[name] = strings.Join(field.args[1:], " ")
			declaredLines[name] = field.line
		}
	}

	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		report.ErrorPolicy = append(report.ErrorPolicy, name)
	}

	expectedNames := make([]string, 0, len(expected))
	for name := range expected {
		expectedNames = append(expectedNames, name)
	}
	sort.Strings(expectedNames)
	for _, name := range expectedNames {
		value, ok := declared[name]
		if !ok {
			*violations = append(*violations, Violation{
				Rule:   "error_policy",
				Line:   errors.line,
				Detail: fmt.Sprintf("the error responses do not carry %s, which every response the application emits does: the edge's own answers must not be a hole in the policy", name),
			})
			continue
		}
		if value != expected[name] {
			*violations = append(*violations, Violation{
				Rule:   "error_policy",
				Line:   declaredLines[name],
				Detail: fmt.Sprintf("%s is declared as %q and the application delivers %q; a second copy of a policy is a copy that drifts", name, value, expected[name]),
			})
		}
	}
	for _, name := range names {
		if _, ok := expected[name]; !ok {
			*violations = append(*violations, Violation{
				Rule:   "error_policy",
				Line:   declaredLines[name],
				Detail: fmt.Sprintf("the error responses carry %s, which the application does not: a header only the edge sets is a header nobody maintains", name),
			})
		}
	}

	responds := false
	for _, directive := range errors.children {
		if len(directive.args) > 0 && directive.args[0] == "respond" {
			responds = true
		}
	}
	if !responds {
		*violations = append(*violations, Violation{
			Rule:   "error_policy",
			Line:   errors.line,
			Detail: "handle_errors writes no response, so the headers above are attached to a body nothing produced",
		})
	}

	// error_server_hidden
	//
	// The strip on the proxied path is a directive of that path, and a response
	// this block writes never passes through it. The two rules are separate for
	// that reason: one of them can hold while the other does not, and an outage
	// is the response a reader would least expect to name the software.
	hidesServer := false
	for _, directive := range headerDirectives(childrenOf(errors)) {
		for _, field := range headerFields(directive) {
			if len(field.args) > 0 && strings.EqualFold(field.args[0], "-Server") {
				hidesServer = true
			}
		}
	}
	if !hidesServer {
		*violations = append(*violations, Violation{
			Rule:   "error_server_hidden",
			Line:   errors.line,
			Detail: "the error responses name the software that served them: the strip on the proxied path belongs to that path, and a response this block writes never reaches it",
		})
	}
}

// auditNoCacheOverride refuses a cache directive on the proxied path.
//
// The application decides how long each response may live — a hashed asset for
// a year, a document not at all — because it is the component that knows what
// the response is. An edge that adds its own directive does not add a decision,
// it replaces one, and the failure it produces is the quiet kind: a private
// document served from a cache to the next visitor.
//
// The error responses are the exception, and they are checked by their own rule:
// they are the one class of response the application never produces.
func auditNoCacheOverride(site *statement, violations *[]Violation) {
	// The walk descends into every block of the site, because a cache directive
	// is just as effective nested inside a route as it is on the site's own
	// line — and `handle_errors` is skipped, because that block is the one
	// place where this process answers instead of the application.
	var visit func(children []statement)
	visit = func(children []statement) {
		for _, directive := range children {
			if len(directive.args) == 0 {
				continue
			}
			if directive.args[0] == "handle_errors" {
				continue
			}
			switch directive.args[0] {
			case "header", "header_down":
				for _, field := range headerFields(directive) {
					name := strings.TrimPrefix(strings.TrimPrefix(field.args[0], "+"), "?")
					name = strings.TrimPrefix(name, "-")
					if !strings.EqualFold(name, "Cache-Control") {
						continue
					}
					*violations = append(*violations, Violation{
						Rule:   "no_cache_override",
						Line:   field.line,
						Detail: "the edge declares its own Cache-Control for the proxied path; the application decides how long each response may live, and a directive here replaces that decision instead of adding to it",
					})
				}
			case "cache":
				*violations = append(*violations, Violation{
					Rule:   "no_cache_override",
					Line:   directive.line,
					Detail: "the edge declares a cache; this deployment's cache is the CDN's, and a second one in front of the application stores responses nobody classified",
				})
			}
			visit(directive.children)
		}
	}
	visit(site.children)
}

// headerDirectives returns the header directives of a block, unmodified: their
// arguments start with the directive's own name, which is what tells the two
// forms apart.
func headerDirectives(children []statement) []statement {
	directives := []statement{}
	for _, child := range children {
		if len(child.args) > 0 && child.args[0] == "header" {
			directives = append(directives, child)
		}
	}
	return directives
}

// headerFields reads the fields of a header directive in either of its forms:
// `header Name "value"` on one line, and `header { Name "value" }` with the
// fields as statements inside. A field is returned with its arguments intact,
// including a leading +, ? or - on its name, because the callers differ on what
// those mean and none of them may guess.
func headerFields(directive statement) []statement {
	fields := append([]statement{}, directive.children...)
	if len(directive.args) >= 2 {
		fields = append(fields, statement{args: directive.args[1:], line: directive.line})
	}
	return fields
}

// auditNoAdminRoute refuses any path from the site to the admin endpoint. The
// endpoint is on loopback inside the container, and a route through the site
// would be the accident that makes it public without publishing a port.
func auditNoAdminRoute(statements []statement, violations *[]Violation) {
	walk(statements, func(current statement) {
		for _, argument := range current.args {
			if argument == ":"+adminPort || strings.HasSuffix(argument, ":"+adminPort) {
				*violations = append(*violations, Violation{
					Rule:   "no_admin_route",
					Line:   current.line,
					Detail: fmt.Sprintf("%q names the admin endpoint inside the site; the surface that can rewrite this process is reachable from the container's own namespace and from nowhere else", argument),
				})
			}
			if strings.HasPrefix(argument, "/config/") || argument == "/config" {
				*violations = append(*violations, Violation{
					Rule:   "no_admin_route",
					Line:   current.line,
					Detail: fmt.Sprintf("the path matcher %q reserves an address the admin endpoint answers on", argument),
				})
			}
		}
	})
}

// adminAddressProblem answers whether an admin address is one this deployment
// accepts, or why it is not.
func adminAddressProblem(address string) string {
	if address == "off" {
		return "the admin endpoint is switched off, which also removes the config reload path and the health probe the topology declares; loopback is reachable from outside the container by nothing already"
	}
	if strings.HasPrefix(address, "unix/") {
		return ""
	}
	host, port, ok := splitHostPort(address)
	if !ok {
		return fmt.Sprintf("the admin endpoint address %q is not an address this audit can read, and a rule cannot judge what it cannot read", address)
	}
	if port != adminPort {
		return fmt.Sprintf("the admin endpoint listens on %s; the topology's health probe asks %s, and a second port is a second thing to keep closed", port, adminPort)
	}
	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return ""
	}
	return fmt.Sprintf("the admin endpoint listens on %q, which is not loopback: on a wildcard address the surface that can rewrite this process is on every interface of the container", host)
}

// trustedPrefixes reads the prefix list of a trusted_proxies statement.
func trustedPrefixes(trusted statement) ([]string, string) {
	if len(trusted.args) == 0 {
		return nil, "trusted_proxies declares no module, so nothing is trusted and every forwarding header is a client's claim"
	}
	if trusted.args[0] != trustedProxyModule {
		return nil, fmt.Sprintf("trusted_proxies uses the %q module; this audit reads only %q, and a list it cannot read is a list it cannot judge", trusted.args[0], trustedProxyModule)
	}
	prefixes := trusted.args[1:]
	if len(prefixes) == 0 {
		return nil, fmt.Sprintf("trusted_proxies %s declares no prefix, which trusts nobody while looking like a decision", trustedProxyModule)
	}
	for _, raw := range prefixes {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return prefixes, fmt.Sprintf("%q is not a CIDR prefix, so the trust it is meant to express is not the trust in force", raw)
		}
		if prefix.Bits() == 0 {
			return prefixes, fmt.Sprintf("%q trusts every address, which deletes the difference between a peer that is the CDN and a peer that is anybody", raw)
		}
	}
	return prefixes, ""
}

// globalOptions finds the global options block: the one statement with no
// arguments that opens a block.
func globalOptions(statements []statement) *statement {
	for index := range statements {
		if len(statements[index].args) == 0 && len(statements[index].children) > 0 {
			return &statements[index]
		}
	}
	return nil
}

// siteBlocks returns the statements that declare a site.
func siteBlocks(statements []statement) []statement {
	sites := make([]statement, 0, 1)
	for _, current := range statements {
		if len(current.args) > 0 {
			sites = append(sites, current)
		}
	}
	return sites
}

func siteAddresses(sites []statement) string {
	addresses := make([]string, 0, len(sites))
	for _, site := range sites {
		addresses = append(addresses, strings.Join(site.args, " "))
	}
	return strings.Join(addresses, ", ")
}

// childByName returns the first child statement whose first argument is name,
// with the name itself removed: a rule asks for the option and reads its
// arguments, and an accessor that handed back the name would make every rule
// count from one.
//
// A nil parent has no children, which is what lets a rule read a missing block
// as "the block is missing" rather than as a crash.
func childByName(parent *statement, name string) *statement {
	children := namedChildren(childrenOf(parent), name)
	if len(children) == 0 {
		return nil
	}
	return &children[0]
}

// namedChildren returns every statement in a block that starts with name, with
// the name removed. It is the plural form, for the directives that may appear
// more than once and whose values are arguments rather than children.
func namedChildren(children []statement, name string) []statement {
	matches := []statement{}
	for _, child := range children {
		if len(child.args) == 0 || child.args[0] != name {
			continue
		}
		matches = append(matches, statement{args: child.args[1:], children: child.children, line: child.line})
	}
	return matches
}

// childrenOf returns a block's statements, and nothing for a block that is not
// there: a rule about a missing block reads as a missing block rather than as a
// panic.
func childrenOf(parent *statement) []statement {
	if parent == nil {
		return nil
	}
	return parent.children
}

// walk visits every statement, including nested ones.
func walk(statements []statement, visit func(statement)) {
	for _, current := range statements {
		visit(current)
		walk(current.children, visit)
	}
}

// splitHostPort splits an address into host and port without importing a
// package for it, and reports whether the address had both.
func splitHostPort(address string) (string, string, bool) {
	index := strings.LastIndex(address, ":")
	if index <= 0 || index == len(address)-1 {
		return "", "", false
	}
	return address[:index], address[index+1:], true
}
