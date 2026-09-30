package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Severity is how much a finding blocks: critical and high fail the run,
// medium is reported, and waived findings are listed but do not block.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
)

// Rule names the violated contract. The set is closed so a waiver names
// something the tool actually checks; adding a probe adds a rule here.
const (
	RuleNo5xx           = "no-5xx"
	RuleNoReflection    = "no-reflection"
	RuleAuthBypass      = "auth-bypass"
	RuleUnexpectedWrite = "unexpected-write"
	RuleOpenRedirect    = "open-redirect"
	RuleSecurityHeaders = "security-headers"
	RuleCookieFlags     = "cookie-flags"
)

// Config is a validated scan: the loopback target, the operations, the
// profiles, the waivers and the budget.
type Config struct {
	Base       *url.URL
	Operations []Operation
	Profiles   []Profile
	Waivers    []Waiver
	Timeout    time.Duration
	Client     *http.Client
}

// Operation is one documented endpoint under test.
type Operation struct {
	Method string
	Path   string
}

// Profile is one caller identity: anonymous carries nothing, the others a
// session cookie of a disposable account.
type Profile struct {
	Name   string
	Cookie string
}

// Finding is one violated rule at one address.
type Finding struct {
	Severity Severity
	Rule     string
	Method   string
	Path     string
	Detail   string
	Waived   bool
}

// Report is the whole scan: every finding, waived or not.
type Report struct {
	Target   string
	Findings []Finding
}

// Blocking reports whether unwaived critical or high findings remain.
func (report *Report) Blocking() bool {
	for _, finding := range report.Findings {
		if finding.Waived {
			continue
		}
		if finding.Severity == SeverityCritical || finding.Severity == SeverityHigh {
			return true
		}
	}
	return false
}

// loadConfig validates the target (loopback only), reads the operations
// and the waivers, and builds the profiles.
func loadConfig(target, openapi, userCookie, adminCookie, waivers string, timeout time.Duration) (*Config, error) {
	base, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("parse target: %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("target scheme %q is not http or https", base.Scheme)
	}
	if !isLoopbackHost(base.Hostname()) {
		return nil, fmt.Errorf("target host %q is not loopback: the scanner never leaves this machine", base.Hostname())
	}
	operations, err := loadOperations(openapi)
	if err != nil {
		return nil, err
	}
	parsed, err := loadWaivers(waivers)
	if err != nil {
		return nil, err
	}
	profiles := []Profile{{Name: "anonymous"}}
	if userCookie != "" {
		profiles = append(profiles, Profile{Name: "user", Cookie: userCookie})
	}
	if adminCookie != "" {
		profiles = append(profiles, Profile{Name: "admin", Cookie: adminCookie})
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &Config{
		Base:       base,
		Operations: operations,
		Profiles:   profiles,
		Waivers:    parsed,
		Timeout:    timeout,
		Client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// isLoopbackHost reports whether the host is a loopback literal. Plain
// hostnames (not even "localhost") are refused: DNS is an egress.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// loadOperations enumerates method+path pairs of the OpenAPI document. It
// reads only the route table (paths and method names), never schemas: the
// scanner probes transport and protocol behavior, not business validity,
// which the T02 matrix owns.
func loadOperations(path string) ([]Operation, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read openapi: %w", err)
	}
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode openapi: %w", err)
	}
	if len(document.Paths) == 0 {
		return nil, fmt.Errorf("openapi declares no paths")
	}
	operations := make([]Operation, 0, len(document.Paths))
	for path, methods := range document.Paths {
		names := make([]string, 0, len(methods))
		for method := range methods {
			names = append(names, method)
		}
		sort.Strings(names)
		for _, method := range names {
			operations = append(operations, Operation{Method: strings.ToUpper(method), Path: path})
		}
	}
	sort.Slice(operations, func(i, j int) bool {
		if operations[i].Path != operations[j].Path {
			return operations[i].Path < operations[j].Path
		}
		return operations[i].Method < operations[j].Method
	})
	return operations, nil
}
