// Package buildinfo holds the reproducible build metadata of the arena
// binary. Values are injected at link time via -ldflags and fall back to
// deterministic defaults when absent, so two builds from the same inputs
// produce identical metadata (P01-T05).
//
// This package belongs to the platform layer: it is consumed by cmd/ and
// must not import domain or application packages.
package buildinfo

import (
	"strconv"
	"strings"
	"time"
)

// Link-time variables. The Makefile/CI injects real values with, for example:
//
//	go build -ldflags "-X github.com/AlexandreZanata/Regnovum/internal/buildinfo.version=1.2.3 ..."
//
// Unset values keep the zero values below, which the fallbacks normalize
// to the development defaults.
var (
	version = ""
	commit  = ""
	date    = ""
)

const (
	fallbackVersion = "dev"
	fallbackCommit  = "unknown"
)

// Info is the immutable build metadata of a binary.
type Info struct {
	Version string    `json:"version"`
	Commit  string    `json:"commit"`
	Date    BuildTime `json:"date"`
}

// BuildTime is a UTC build timestamp that serializes as RFC 3339 in JSON.
// The zero value reports "unknown" instead of a fake date.
type BuildTime struct {
	time.Time
}

// MarshalJSON renders the timestamp as an RFC 3339 string, or "unknown" when
// the build did not embed a date.
func (buildTime BuildTime) MarshalJSON() ([]byte, error) {
	if buildTime.IsZero() {
		return []byte(`"unknown"`), nil
	}
	return []byte(`"` + buildTime.UTC().Format(time.RFC3339) + `"`), nil
}

// UnmarshalJSON parses an RFC 3339 timestamp or the "unknown" marker.
func (buildTime *BuildTime) UnmarshalJSON(data []byte) error {
	trimmed := string(data)
	if trimmed == `""` || trimmed == `"unknown"` || trimmed == "null" {
		buildTime.Time = time.Time{}
		return nil
	}
	unquoted, err := strconv.Unquote(trimmed)
	if err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339, unquoted)
	if err != nil {
		return err
	}
	buildTime.Time = parsed
	return nil
}

// nonEmpty returns value when set, falling back to the deterministic default.
func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// Current returns the build metadata with deterministic fallbacks applied:
// without -ldflags injection the binary reports the development version
// "dev", commit "unknown" and the date derived from SOURCE_DATE_EPOCH (when
// present in the given environ) or zero ("unknown") to stay reproducible.
//
// The environ is handed in by the caller (cmd/ is the process edge) instead
// of being read here, keeping this package free of environment access.
func Current(environ []string) Info {
	info := Info{
		Version: nonEmpty(version, fallbackVersion),
		Commit:  nonEmpty(commit, fallbackCommit),
		Date:    ParseBuildDate(date),
	}
	if info.Date.IsZero() {
		info.Date = ParseBuildDate(environValue(environ, "SOURCE_DATE_EPOCH"))
	}
	return info
}

// environValue finds one variable in an environ-style slice without touching
// the process environment.
func environValue(environ []string, name string) string {
	for _, entry := range environ {
		if entryName, value, found := strings.Cut(entry, "="); found && entryName == name {
			return value
		}
	}
	return ""
}

// ParseBuildDate resolves a raw injected date deterministically: a Unix
// number is interpreted as seconds since the epoch; RFC 3339 strings are
// parsed as-is; anything else (including empty) yields the zero time, which
// serializes as "unknown" in JSON. Environment fallbacks are the caller's
// responsibility (see Current).
func ParseBuildDate(raw string) BuildTime {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return BuildTime{}
	}
	if unixSeconds, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return BuildTime{time.Unix(unixSeconds, 0).UTC()}
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return BuildTime{parsed.UTC()}
	}
	return BuildTime{}
}
