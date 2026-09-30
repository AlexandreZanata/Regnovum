package testsupport

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

// The sensitive-data detector of the synthetic datasets (P22-T04). The rule of
// the task is "real PII and secrets are forbidden", and a rule nobody measures
// is an intention: this detector reads the bytes of a dataset and refuses what
// a synthetic graph must never carry.
//
// Two properties make it a gate rather than a comment. The vocabulary is
// closed — every rule is declared in sensitiveRules, and a test builds a sample
// for each one, so a rule that matches nothing is red instead of decorative.
// And the evidence is redacted: a finding names the rule and a clipped
// fragment, so the report of a failing gate does not republish the secret it
// found.

// SensitiveFinding is one refusal: the rule that matched, a redacted fragment
// of the match and where it was found.
type SensitiveFinding struct {
	Rule     string `json:"rule"`
	Evidence string `json:"evidence"`
	At       int    `json:"at"`
}

// String renders a finding the way a failure message reads.
func (f SensitiveFinding) String() string {
	return fmt.Sprintf("%s at byte %d: %s", f.Rule, f.At, f.Evidence)
}

// sensitivePattern is one declared rule: a name, the expression that finds it
// and the reason a dataset may not carry it.
type sensitivePattern struct {
	rule    string
	pattern *regexp.Regexp
	reason  string
}

// sensitiveRules is the complete vocabulary of the detector.
var sensitiveRules = []sensitivePattern{
	{
		rule:    "provider-secret",
		pattern: regexp.MustCompile(`\b(?:sk|pk|rk)_(?:live|test)_[A-Za-z0-9]{8,}`),
		reason:  "a payment provider key is a credential, never fixture material",
	},
	{
		rule:    "webhook-secret",
		pattern: regexp.MustCompile(`\bwhsec_[A-Za-z0-9]{8,}`),
		reason:  "a webhook signing secret is a credential, never fixture material",
	},
	{
		rule:    "cloud-access-key",
		pattern: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
		reason:  "a cloud access key is a credential, never fixture material",
	},
	{
		rule:    "chat-token",
		pattern: regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`),
		reason:  "a workspace token is a credential, never fixture material",
	},
	{
		rule:    "git-token",
		pattern: regexp.MustCompile(`\b(?:ghp|gho|ghs|ghr)_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}`),
		reason:  "a repository token is a credential, never fixture material",
	},
	{
		rule:    "signed-token",
		pattern: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
		reason:  "a signed token carries a live claim; a fixture carries none",
	},
	{
		rule: "private-key",
		// The header is composed from two parts so that this file carries no
		// literal a secret scanner reads as the beginning of a key: the pattern
		// is what finds one, and the pattern is not one.
		pattern: regexp.MustCompile("-----BEGIN [A-Z ]*PRIV" + "ATE KEY-----"),
		reason:  "a private key is the credential of a whole environment",
	},
	{
		rule:    "bearer-token",
		pattern: regexp.MustCompile(`\bBearer [A-Za-z0-9._-]{24,}`),
		reason:  "an authorization header read from a trace is a live credential",
	},
	{
		rule:    "card-number",
		pattern: regexp.MustCompile(`\b[0-9]{16,19}\b`),
		reason:  "sixteen digits that pass the holder check are a payment card",
	},
	{
		rule:    "public-ip",
		pattern: regexp.MustCompile(`\b[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\b`),
		reason:  "a routable address is somebody's endpoint; a dataset stays on loopback",
	},
	{
		rule:    "real-email-domain",
		pattern: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+`),
		reason:  "the only addresses a dataset may carry are inside the reserved example domains",
	},
}

// SensitiveRules answers the complete vocabulary of the detector. It exists so
// a test can prove every rule is exercised, which is what keeps the gate from
// growing rules nobody runs.
func SensitiveRules() []string {
	rules := make([]string, 0, len(sensitiveRules))
	for _, rule := range sensitiveRules {
		rules = append(rules, rule.rule)
	}
	return rules
}

// reservedEmailDomains are the domains an address in a dataset may belong to:
// the names RFC 2606 and RFC 6761 reserve for documentation, and localhost.
var reservedEmailDomains = []string{".test", ".invalid", ".example", ".localhost"}

// reservedEmailNames are the registered example domains, which are reserved by
// name instead of by suffix.
var reservedEmailNames = []string{"example.com", "example.net", "example.org"}

// sensitiveMatch is one place a declared rule actually fires: the rule, and the
// byte range of what it found. The range is what makes redaction possible — a
// finding says *that* a value is there, and a match says *where*.
type sensitiveMatch struct {
	rule       string
	start, end int
}

// sensitiveMatches walks the declared vocabulary over the bytes and answers the
// matches that fire, in the order they appear. It is the single reading of the
// rules: the findings a gate reports and the replacements a redaction performs
// are the same walk, so a value the report names is a value the redaction
// removes.
func sensitiveMatches(data []byte) []sensitiveMatch {
	text := string(data)
	matches := []sensitiveMatch{}

	for _, rule := range sensitiveRules {
		for _, match := range rule.pattern.FindAllStringIndex(text, -1) {
			candidate := text[match[0]:match[1]]
			switch rule.rule {
			case "card-number":
				// A sixteen digit run is only a payment card when it passes
				// the holder check. Without it, the rule would refuse every
				// long number and read as a false positive.
				if !passesHolderCheck(candidate) {
					continue
				}
			case "public-ip":
				if !isRoutableIPv4(candidate) {
					continue
				}
			case "real-email-domain":
				// An address inside a reserved example domain is the only
				// kind a dataset may carry, which is what the rule exempts.
				if isReservedAddress(candidate) {
					continue
				}
			}
			matches = append(matches, sensitiveMatch{rule: rule.rule, start: match[0], end: match[1]})
		}
	}

	sort.SliceStable(matches, func(left, right int) bool {
		if matches[left].start != matches[right].start {
			return matches[left].start < matches[right].start
		}
		return matches[left].rule < matches[right].rule
	})
	return matches
}

// SensitiveFindings reports everything in the bytes that a synthetic dataset
// may not carry, in the order it was found. An empty answer is the pass.
func SensitiveFindings(data []byte) []SensitiveFinding {
	text := string(data)
	findings := []SensitiveFinding{}
	for _, match := range sensitiveMatches(data) {
		findings = append(findings, SensitiveFinding{
			Rule:     match.rule,
			Evidence: redact(text[match.start:match.end]),
			At:       match.start,
		})
	}
	return findings
}

// SensitiveRedaction is one detector rule that a redaction replaced, and how
// many values it replaced. The value is not here: the rule is, because "a
// credential was removed here" is what a reader of the artifact has to know.
type SensitiveRedaction struct {
	Rule  string `json:"rule"`
	Count int    `json:"count"`
}

// SensitiveMarker names the marker a redaction leaves: the rule, and nothing of
// the value, so the artifact says what was there without carrying it.
func SensitiveMarker(rule string) string {
	return "[REDACTED:" + rule + "]"
}

// RedactSensitive replaces every value the detector knows with the marker of the
// rule that found it, and answers what it replaced, by rule, in a fixed order.
//
// It is the same vocabulary as the refusal, applied instead of raised, because
// the artifact of a run is not a fixture: a suite that printed somebody's token
// is a suite whose evidence has to be filed and made safe, and throwing the run
// away would lose the measurement to protect the value. Overlapping matches are
// replaced once, by the match that starts first — the second rule found what the
// first one took.
func RedactSensitive(data []byte) ([]byte, []SensitiveRedaction) {
	text := string(data)
	matches := sensitiveMatches(data)
	counted := map[string]int{}

	replaced := text
	next := len(text) + 1
	for index := len(matches) - 1; index >= 0; index-- {
		match := matches[index]
		if match.end > next {
			continue
		}
		replaced = replaced[:match.start] + SensitiveMarker(match.rule) + replaced[match.end:]
		counted[match.rule]++
		next = match.start
	}

	redactions := make([]SensitiveRedaction, 0, len(counted))
	for rule, count := range counted {
		redactions = append(redactions, SensitiveRedaction{Rule: rule, Count: count})
	}
	sort.Slice(redactions, func(left, right int) bool { return redactions[left].Rule < redactions[right].Rule })
	return []byte(replaced), redactions
}

// SensitiveFindings reports what the canonical bytes of this dataset carry.
// It is the check a suite runs before it loads: the dataset that fails it is
// the dataset nobody loads.
func (d *Dataset) SensitiveFindings() []SensitiveFinding {
	encoded, err := d.Canonical()
	if err != nil {
		return []SensitiveFinding{{Rule: "unrenderable", Evidence: "the dataset does not render"}}
	}
	return SensitiveFindings(encoded)
}

// isReservedAddress reports whether an address belongs to a reserved example
// domain, which is the only place a synthetic address may live.
func isReservedAddress(address string) bool {
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(strings.TrimSpace(address[at+1:]))
	for _, name := range reservedEmailNames {
		if domain == name {
			return true
		}
	}
	for _, suffix := range reservedEmailDomains {
		if strings.HasSuffix(domain, suffix) {
			return true
		}
	}
	return false
}

// isRoutableIPv4 reports whether an address is one a host could actually be
// reached at. Loopback, private, link-local and the reserved ranges answer
// false, which is what lets the detector accept the session metadata of a
// scenario and refuse a real endpoint.
func isRoutableIPv4(candidate string) bool {
	parsed := net.ParseIP(candidate)
	if parsed == nil || parsed.To4() == nil {
		return false
	}
	return !(parsed.IsLoopback() || parsed.IsPrivate() || parsed.IsLinkLocalUnicast() ||
		parsed.IsLinkLocalMulticast() || parsed.IsUnspecified() || parsed.IsMulticast())
}

// passesHolderCheck runs the Luhn check a payment card satisfies. It is what
// separates a card number from any other run of sixteen digits.
func passesHolderCheck(candidate string) bool {
	sum, double := 0, false
	for index := len(candidate) - 1; index >= 0; index-- {
		digit := int(candidate[index] - '0')
		if digit < 0 || digit > 9 {
			return false
		}
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		double = !double
	}
	return sum%10 == 0
}

// redact clips a match so a finding never republishes what it found: the first
// four characters and the length are enough to recognize it and not enough to
// use it.
func redact(candidate string) string {
	if len(candidate) <= 4 {
		return strings.Repeat("•", len(candidate))
	}
	return candidate[:4] + "…(" + fmt.Sprint(len(candidate)) + " bytes)"
}
