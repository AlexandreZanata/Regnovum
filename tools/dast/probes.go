package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Markers travel as hostile input; any of them coming back in a body,
// header or redirect target is a reflection finding. They are inert
// strings, never executed: the scanner reads answers, it does not run
// them.
var markers = []string{
	`' OR '1'='1`,
	`<script>alert(1)</script>`,
	`../../etc/passwd`,
	`;id;`,
	`dast-canary-marker`,
}

var tamperMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// credentialDestroyingOps end the caller's own session: driving them
// with a profile cookie would log the scan out mid-run and turn every
// later authenticated answer into a false 401. They run anonymously
// (logout is idempotent by design) and the session lifecycle stays with
// the dedicated T01 matrix, which owns credentials instead of borrowing
// them.
var credentialDestroyingOps = map[string]bool{
	"POST /api/v1/auth/logout":                 true,
	"POST /api/v1/me/sessions/rotation":        true,
	"POST /api/v1/me/sessions/revocation":      true,
	"POST /api/v1/auth/password-reset/confirm": true,
}

// scan drives every operation through every profile and judges the
// answers, bounded by the configured budget.
func scan(config *Config) (*Report, error) {
	deadline := time.Now().Add(config.Timeout)
	report := &Report{Target: config.Base.String()}
	byProfile := map[string]map[string]int{}
	for _, operation := range config.Operations {
		concrete := concretize(operation.Path)
		for _, profile := range config.Profiles {
			if skipsCredentials(operation, profile) {
				continue
			}
			if time.Now().After(deadline) {
				return report, fmt.Errorf("scan budget exhausted at %s %s", operation.Method, operation.Path)
			}
			outcome := request(config, operation.Method, concrete, profile, "", nil)
			judgeBase(report, operation, profile, outcome)
			key := operation.Method + " " + operation.Path
			if byProfile[profile.Name] == nil {
				byProfile[profile.Name] = map[string]int{}
			}
			byProfile[profile.Name][key] = outcome.status
		}
		judgeAuthBypass(report, operation, byProfile)
		judgeTamperedVerbs(report, config, operation, deadline)
		judgePayloads(report, config, operation, deadline)
	}
	applyWaivers(report, config.Waivers)
	return report, nil
}

// outcome is one answered request.
type outcome struct {
	status   int
	header   http.Header
	body     []byte
	location string
	cookies  []*http.Cookie
	failed   string
}

// request performs one call: the concrete address, the profile cookie and
// optional body and headers. Transport failures are recorded, not
// returned: an unreachable stack is a finding about the harness, and the
// scan answers it the same way for every operation.
func request(config *Config, method, concrete string, profile Profile, body string, headers map[string]string) outcome {
	target := strings.TrimSuffix(config.Base.String(), "/") + concrete
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, target, reader)
	if err != nil {
		return outcome{failed: fmt.Sprintf("build request: %v", err)}
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if profile.Cookie != "" {
		request.AddCookie(&http.Cookie{Name: "arena_session", Value: profile.Cookie})
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := config.Client.Do(request)
	if err != nil {
		return outcome{failed: fmt.Sprintf("do request: %v", err)}
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return outcome{failed: fmt.Sprintf("read body: %v", err)}
	}
	var cookies []*http.Cookie
	for _, setCookie := range response.Header.Values("Set-Cookie") {
		parsed, err := http.ParseSetCookie(setCookie)
		if err == nil {
			cookies = append(cookies, parsed)
		}
	}
	return outcome{
		status:   response.StatusCode,
		header:   response.Header.Clone(),
		body:     answer,
		location: response.Header.Get("Location"),
		cookies:  cookies,
	}
}

// concretize fills path templates with inert values: a fixed UUID for
// identifiers and "test" for slugs. Unknown names never reach a real
// object, so the scan exercises routing, parsing and authorization
// instead of business state, which the T02 matrix owns.
func concretize(path string) string {
	concrete := path
	for {
		start := strings.Index(concrete, "{")
		end := strings.Index(concrete, "}")
		if start < 0 || end < 0 || end < start {
			break
		}
		name := concrete[start+1 : end]
		replacement := "00000000-0000-0000-0000-000000000000"
		if strings.Contains(strings.ToLower(name), "slug") || strings.Contains(strings.ToLower(name), "username") {
			replacement = "test"
		}
		concrete = concrete[:start] + replacement + concrete[end+1:]
	}
	return concrete
}

// judgeBase applies the per-answer rules: status family, reflection,
// redirects, headers and cookies.
func judgeBase(report *Report, operation Operation, profile Profile, outcome outcome) {
	if outcome.failed != "" {
		report.add(SeverityCritical, RuleNo5xx, operation, "transport failure as "+profile.Name+": "+outcome.failed)
		return
	}
	if outcome.status >= 500 {
		report.add(SeverityCritical, RuleNo5xx, operation, fmt.Sprintf("%s answered %d", profile.Name, outcome.status))
	}
	for _, marker := range markers {
		if strings.Contains(string(outcome.body), marker) || strings.Contains(strings.ToLower(outcome.location), strings.ToLower(marker)) {
			report.add(SeverityCritical, RuleNoReflection, operation, fmt.Sprintf("%s reflects %q", profile.Name, marker))
		}
	}
	if outcome.status >= 300 && outcome.status < 400 && outcome.location != "" {
		if target, err := url.Parse(outcome.location); err == nil && target.IsAbs() {
			if !isLoopbackHost(target.Hostname()) && target.Hostname() != "" {
				report.add(SeverityHigh, RuleOpenRedirect, operation, fmt.Sprintf("redirects to %q", outcome.location))
			}
		}
	}
	if outcome.header.Get("X-Content-Type-Options") != "nosniff" || outcome.header.Get("Content-Security-Policy") == "" {
		report.add(SeverityMedium, RuleSecurityHeaders, operation, fmt.Sprintf("%s misses security headers status=%d xcto=%q csp-len=%d failed=%q", profile.Name, outcome.status, outcome.header.Get("X-Content-Type-Options"), len(outcome.header.Get("Content-Security-Policy")), outcome.failed))
	}
	for _, cookie := range outcome.cookies {
		lowered := strings.ToLower(cookie.Raw)
		if strings.Contains(lowered, "domain=") {
			report.add(SeverityHigh, RuleCookieFlags, operation, fmt.Sprintf("cookie %q pins a Domain", cookie.Name))
		}
		if !cookie.HttpOnly {
			report.add(SeverityMedium, RuleCookieFlags, operation, fmt.Sprintf("cookie %q without HttpOnly", cookie.Name))
		}
	}
}

// judgeAuthBypass compares the anonymous answer with the strongest
// authenticated one: a write that serves the world the way it serves an
// account is an open door. Public reads serve both by design and stay
// silent; public writes are findings the operator waives with a reason.
func judgeAuthBypass(report *Report, operation Operation, byProfile map[string]map[string]int) {
	key := operation.Method + " " + operation.Path
	anonymous, anonymousSeen := byProfile["anonymous"][key]
	best := 0
	for profile, outcomes := range byProfile {
		if profile == "anonymous" {
			continue
		}
		if status, seen := outcomes[key]; seen && status/100 == 2 && (best == 0 || status < best) {
			best = status
		}
	}
	if !anonymousSeen || best == 0 {
		return
	}
	if anonymous/100 == 2 && (operation.Method == "POST" || operation.Method == "PUT" || operation.Method == "PATCH" || operation.Method == "DELETE") {
		report.add(SeverityHigh, RuleAuthBypass, operation, "anonymous and authenticated both succeed on a write")
	}
}

// skipsCredentials returns false for the anonymous profile: destructive
// operations still run without credentials, where they are harmless and
// their answer is still judged.
func skipsCredentials(operation Operation, profile Profile) bool {
	return profile.Name != "anonymous" && credentialDestroyingOps[operation.Method+" "+operation.Path]
}

// judgeTamperedVerbs replays the operation under the other methods: a
// served tampered verb is a route confusion finding, unless the
// method+path pair is itself a documented operation (sibling verbs on one
// address are design, not confusion).
func judgeTamperedVerbs(report *Report, config *Config, operation Operation, deadline time.Time) {
	strongest := Profile{Name: "anonymous"}
	for _, profile := range config.Profiles {
		if profile.Name == "admin" {
			strongest = profile
		}
	}
	if strongest.Name == "anonymous" && len(config.Profiles) > 1 {
		strongest = config.Profiles[len(config.Profiles)-1]
	}
	documented := map[string]bool{}
	for _, candidate := range config.Operations {
		documented[candidate.Method+" "+candidate.Path] = true
	}
	for _, method := range tamperMethods {
		if method == operation.Method || time.Now().After(deadline) {
			continue
		}
		if documented[method+" "+operation.Path] {
			continue
		}
		if skipsCredentials(operation, strongest) {
			continue
		}
		outcome := request(config, method, concretize(operation.Path), strongest, "", nil)
		if outcome.failed != "" || outcome.status/100 == 2 {
			if outcome.failed == "" {
				report.add(SeverityMedium, RuleUnexpectedWrite, operation, fmt.Sprintf("%s serves %s", operation.Path, method))
			}
		}
	}
}

// judgePayloads posts the hostile corpus to operations with a body and
// appends hostile queries to reads, judging 5xx and reflection.
func judgePayloads(report *Report, config *Config, operation Operation, deadline time.Time) {
	profile := Profile{Name: "anonymous"}
	if len(config.Profiles) > 1 {
		profile = config.Profiles[1]
	}
	if skipsCredentials(operation, profile) {
		return
	}
	bodies := []string{
		`{"q":"' OR '1'='1","debug":"<script>alert(1)</script>"}`,
		`{"input":"../../etc/passwd","cmd":";id;"}`,
		fmt.Sprintf(`{"marker":%q}`, markers[len(markers)-1]),
	}
	concrete := concretize(operation.Path)
	queries := []string{
		concrete + fmt.Sprintf("?q=%s&debug=%s", url.QueryEscape(markers[0]), url.QueryEscape(markers[1])),
		concrete + "?limit=abc",
	}
	if operation.Method == "POST" || operation.Method == "PUT" || operation.Method == "PATCH" {
		for _, body := range bodies {
			if time.Now().After(deadline) {
				return
			}
			outcome := request(config, operation.Method, concrete, profile, body, nil)
			judgeHostileAnswer(report, operation, outcome)
		}
		return
	}
	if operation.Method == "GET" {
		for _, query := range queries {
			if time.Now().After(deadline) {
				return
			}
			outcome := request(config, operation.Method, query, profile, "", nil)
			judgeHostileAnswer(report, operation, outcome)
		}
	}
}

// judgeHostileAnswer judges one hostile answer: 5xx and reflection fail,
// anything else is the product refusing or ignoring the payload.
func judgeHostileAnswer(report *Report, operation Operation, outcome outcome) {
	if outcome.failed != "" {
		report.add(SeverityCritical, RuleNo5xx, operation, "transport failure on hostile input: "+outcome.failed)
		return
	}
	if outcome.status >= 500 {
		report.add(SeverityCritical, RuleNo5xx, operation, fmt.Sprintf("hostile input answered %d", outcome.status))
	}
	for _, marker := range markers {
		if strings.Contains(string(outcome.body), marker) {
			report.add(SeverityCritical, RuleNoReflection, operation, fmt.Sprintf("hostile input reflects %q", marker))
		}
	}
}

// add records one finding.
func (report *Report) add(severity Severity, rule string, operation Operation, detail string) {
	report.Findings = append(report.Findings, Finding{
		Severity: severity,
		Rule:     rule,
		Method:   operation.Method,
		Path:     operation.Path,
		Detail:   detail,
	})
}
