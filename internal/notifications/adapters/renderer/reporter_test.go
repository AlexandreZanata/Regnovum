package renderer_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/renderer"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

// TestLogFallbackReporterNamesTheDefectWithoutPersonalData: the metric an
// operator alerts on carries the template, the locales and the key, and it
// cannot carry anything else — the event it receives has no field for a
// recipient, a display name or a one-time code.
func TestLogFallbackReporterNamesTheDefectWithoutPersonalData(t *testing.T) {
	var buffer bytes.Buffer
	reporter := renderer.NewLogFallbackReporter(slog.New(slog.NewJSONHandler(&buffer, nil)))
	reporter.ReportFallback(renderer.FallbackEvent{
		Template:  domain.TemplateVerification,
		Requested: domain.LocaleAmericanEnglish,
		Key:       "email.verification.lead",
		Source:    domain.LocaleDefault,
	})
	line := buffer.String()
	for _, want := range []string{
		`"template":"verification"`,
		`"requested_locale":"en-US"`,
		`"fallback_locale":"pt-BR"`,
		`"message_key":"email.verification.lead"`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("log line = %s, want %s", line, want)
		}
	}
	if level := `"level":"WARN"`; !strings.Contains(line, level) {
		t.Errorf("log line = %s, want %s: a fallback is a defect, not an event", line, level)
	}
}

// TestLogFallbackReporterNeverDiscards: a nil logger means "the process
// default", not "nobody sees this" — a silent fallback is exactly what the
// standard forbids.
func TestLogFallbackReporterNeverDiscards(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	var buffer bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, nil)))

	renderer.NewLogFallbackReporter(nil).ReportFallback(renderer.FallbackEvent{
		Template:  domain.TemplatePasswordReset,
		Requested: domain.LocaleAmericanEnglish,
		Key:       "email.password_reset.subject",
		Source:    domain.LocaleDefault,
	})
	if !strings.Contains(buffer.String(), `"message_key":"email.password_reset.subject"`) {
		t.Errorf("log = %q, want the fallback reported on the default logger", buffer.String())
	}
}

// TestLogFallbackReporterSurvivesAnUnwiredValue: the reporter is called from a
// delivery, so it must tolerate the zero value instead of panicking mid-send.
func TestLogFallbackReporterSurvivesAnUnwiredValue(t *testing.T) {
	var unwired *renderer.LogFallbackReporter
	unwired.ReportFallback(renderer.FallbackEvent{})
}
