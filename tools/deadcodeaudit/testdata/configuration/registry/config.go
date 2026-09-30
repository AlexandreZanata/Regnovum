// Package config is the fixture registry of the configuration rule: a miniature
// of the delivered loader, with one key of each direction the rule judges. The
// gate reads it through the same function it uses on the tree, with the paths of
// this fixture.
package config

import "strings"

// sinkVariable is the constant the accepted set names one of its keys through: a
// rule that only read string literals would call this key undocumented.
const sinkVariable = "ARENA_SINK_DIR"

// Config is what the fixture loader answers.
type Config struct {
	Env   string
	Sink  string
	Quiet bool
}

// Load accepts four keys and reads three of them, which is the whole point of the
// fixture:
//
//   - ARENA_ENV is accepted, read and documented — the clean key;
//   - ARENA_SINK_DIR is the same key written through a constant;
//   - ARENA_SILENT is accepted and read, and the template does not document it;
//   - ARENA_ORPHAN is accepted and never read: the operator sets it and the
//     process drops the value.
//
// The reads of ARENA_UNACCEPTED and of the value-driven loop are the other two
// directions: the first one is never accepted, so the loop below drops it before
// any read can see it.
func Load(environ []string) (Config, error) {
	values := make(map[string]string, len(environ))
	for _, entry := range environ {
		name, rawValue, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		values[name] = rawValue
	}

	known := map[string]bool{
		"ARENA_ENV":    true,
		sinkVariable:   true,
		"ARENA_SILENT": true,
		"ARENA_ORPHAN": true,
	}

	var config Config
	for name := range values {
		if !known[name] {
			return Config{}, &rejected{variable: name}
		}
	}
	if raw, present := values["ARENA_ENV"]; present {
		config.Env = raw
	}
	if raw, present := values[sinkVariable]; present {
		config.Sink = raw
	}
	if raw, present := values["ARENA_SILENT"]; present {
		config.Quiet = raw == "1"
	}
	if _, present := values["ARENA_UNACCEPTED"]; present {
		config.Env = "unaccepted"
	}
	return config, nil
}

// rejected is the refusal the loader answers with.
type rejected struct{ variable string }

func (e *rejected) Error() string { return e.variable + " is not accepted" }
