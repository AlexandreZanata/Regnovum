// Tests of the premise the policy rests on (P16-T01): no server-rendered
// page carries executable inline code. That is what makes `script-src 'self'`
// and `style-src 'self'` sufficient without a nonce — and a nonce is not an
// option here, because a per-request value inside the body of a public,
// cacheable document moves its ETag on every request.
//
// The premise is asserted against the real templates of both server-rendered
// surfaces with adversarial values, so adding an inline event handler or an
// executable inline script fails the build instead of quietly producing pages
// that the policy blocks in the browser.
package securityheaders_test

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html/template"
	"io"
	"strings"
	"testing"

	arenashtml "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/html"
	identityhtml "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/html"
	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
	transparencyhttp "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/http"
)

// dataBlockType is the only script type a server-rendered page may carry. The
// HTML standard never prepares a script element whose type is not a
// JavaScript MIME type for execution — it is a data block — so script-src
// does not govern it and the JSON-LD payload survives a policy without
// 'unsafe-inline'.
const dataBlockType = "application/ld+json"

// hostileValue is a value no template may place anywhere the browser would
// execute: it carries an element, a scheme and two handlers.
const hostileValue = `<script>alert(1)</script> javascript:alert(2) onerror=alert(3) " ' &`

// scanPage walks one rendered document and fails on anything that would need
// a widened policy. It returns the number of script data blocks and the number
// of elements it read, so a caller can prove the scan was not vacuous.
func scanPage(t *testing.T, surface, document string) (dataBlocks, elements int) {
	t.Helper()

	problems, dataBlocks, elements := scanPageProblems(document)
	if problems == nil {
		return dataBlocks, elements
	}
	for _, problem := range problems {
		t.Errorf("%s: %s", surface, problem)
	}
	return dataBlocks, elements
}

// scanPageProblems is the scanner itself, returning what it found instead of
// failing a test: the branches that must speak — an inline script, an inline
// style, an inline handler and a refused scheme — are then provable by feeding
// it hostile markup, rather than taken on trust.
//
// A document that is not well-formed markup cannot be walked, and that is a
// problem like any other: a page the scanner cannot read is a page nobody
// proved anything about.
func scanPageProblems(document string) (problems []string, dataBlocks, elements int) {
	decoder := xml.NewDecoder(strings.NewReader(document))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("the document is not well-formed markup, so it cannot be scanned: %v", err))
			return problems, dataBlocks, elements
		}

		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		elements++

		switch strings.ToLower(start.Name.Local) {
		case "style":
			problems = append(problems, "an inline <style> element would require style-src 'unsafe-inline'")
		case "script":
			switch {
			case strings.EqualFold(attribute(start, "type"), dataBlockType):
				// A data block is never prepared for execution.
				dataBlocks++
			case attribute(start, "src") == "":
				problems = append(problems, fmt.Sprintf("inline <script type=%q> is executable; the policy has no 'unsafe-inline' and no nonce", attribute(start, "type")))
			default:
				// A script element with a `src` is not inline: the browser fetches
				// it, and `script-src 'self'` is exactly the directive that
				// allows the origin's own module. The attribute loop below still
				// refuses a `src` carrying a scheme the policy does not allow.
			}
		}

		for _, attributeValue := range start.Attr {
			name := strings.ToLower(attributeValue.Name.Local)
			value := strings.ToLower(strings.TrimSpace(attributeValue.Value))
			switch {
			case name == "style":
				problems = append(problems, fmt.Sprintf("inline style attribute %q would require style-src 'unsafe-inline'", attributeValue.Value))
			case len(name) > 2 && strings.HasPrefix(name, "on"):
				problems = append(problems, fmt.Sprintf("inline event handler %s would require script-src 'unsafe-inline'", name))
			case strings.HasPrefix(value, "javascript:"), strings.HasPrefix(value, "vbscript:"), strings.HasPrefix(value, "data:"):
				problems = append(problems, fmt.Sprintf("attribute %s carries a scheme the policy does not allow: %q", name, attributeValue.Value))
			}
		}
	}
	return problems, dataBlocks, elements
}

// TestScanPageRefusesInlineCodeAndAllowsTheOriginsOwnModule proves the scanner
// speaks on every branch, so a green run over the real pages means the pages are
// clean rather than the scanner being mute.
func TestScanPageRefusesInlineCodeAndAllowsTheOriginsOwnModule(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		body     string
		problems int
		blocks   int
	}{
		{
			name:     "an inline script is refused",
			body:     `<script>alert(1)</script>`,
			problems: 1,
		},
		{
			name:     "an inline module is refused",
			body:     `<script type="module">alert(1)</script>`,
			problems: 1,
		},
		{
			name:     "the origin's own module is allowed",
			body:     `<script type="module" src="/assets/pages/auth-abc.js"></script>`,
			problems: 0,
		},
		{
			name:     "an external script with a forbidden scheme is refused",
			body:     `<script src="data:text/javascript,alert(1)"></script>`,
			problems: 1,
		},
		{
			name:     "a JSON-LD data block is allowed and counted",
			body:     `<script type="application/ld+json">{}</script>`,
			problems: 0,
			blocks:   1,
		},
		{
			name:     "an inline style element is refused",
			body:     `<style>body { color: red; }</style>`,
			problems: 1,
		},
		{
			name:     "an inline style attribute is refused",
			body:     `<p style="color: red">text</p>`,
			problems: 1,
		},
		{
			name:     "an inline event handler is refused",
			body:     `<button onclick="alert(1)">go</button>`,
			problems: 1,
		},
		{
			name:     "markup that cannot be walked is a problem",
			body:     `<p>unclosed`,
			problems: 1,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			problems, blocks, elements := scanPageProblems(`<html><body>` + testCase.body + `</body></html>`)
			if len(problems) != testCase.problems {
				t.Fatalf("scanPageProblems() reported %v, want %d problems", problems, testCase.problems)
			}
			if blocks != testCase.blocks {
				t.Errorf("scanPageProblems() counted %d data blocks, want %d", blocks, testCase.blocks)
			}
			if elements == 0 {
				t.Error("the scanner read no element, so it is not reading the document")
			}
		})
	}
}

// attribute returns the value of one attribute, or the empty string.
func attribute(element xml.StartElement, name string) string {
	for _, attribute := range element.Attr {
		if strings.EqualFold(attribute.Name.Local, name) {
			return attribute.Value
		}
	}
	return ""
}

// TestArenaDocumentCarriesOnlyItsJSONLDDataBlock scans the cacheable public
// Arena document and its error pages.
func TestArenaDocumentCarriesOnlyItsJSONLDDataBlock(t *testing.T) {
	t.Parallel()

	templates := arenashtml.NewTemplates()

	var document bytes.Buffer
	err := templates.RenderDocument(&document, arenashtml.DocumentData{
		Lang:        "pt-BR",
		PageTitle:   hostileValue,
		Statement:   hostileValue,
		Context:     hostileValue,
		Description: hostileValue,
		Canonical:   "https://goyimarena.example/d/exemplo",
		OGLocale:    "pt_BR",
		StatusLabel: hostileValue,
		PublishedAt: "2026-09-18T12:00:00Z",
		JSONLD:      template.JS(`{"@context":"https://schema.org","@type":"Article"}`),
	})
	if err != nil {
		t.Fatalf("RenderDocument() error = %v", err)
	}

	rendered := document.String()
	if !strings.Contains(rendered, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("the hostile value was not rendered escaped, so the scan is not reading the real document:\n%s", rendered)
	}
	blocks, elements := scanPage(t, "arena document", rendered)
	if blocks != 1 {
		t.Errorf("arena document carries %d script data blocks, want exactly the JSON-LD one", blocks)
	}
	if elements < 5 {
		t.Errorf("arena document scan read %d elements; it is not reading the document", elements)
	}

	for name, errorData := range map[string]arenashtml.ErrorData{
		"not found": {
			Lang:      "pt-BR",
			PageTitle: hostileValue,
			Title:     hostileValue,
			Detail:    hostileValue,
		},
		"gone": {
			Lang:      "en-US",
			PageTitle: hostileValue,
			Title:     hostileValue,
			Detail:    hostileValue,
		},
	} {
		var page bytes.Buffer
		if err := templates.RenderError(&page, errorData); err != nil {
			t.Fatalf("RenderError(%s) error = %v", name, err)
		}
		if !strings.Contains(page.String(), "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Fatalf("arena %s page did not render the hostile value escaped:\n%s", name, page.String())
		}
		blocks, elements := scanPage(t, "arena "+name+" page", page.String())
		if blocks != 0 {
			t.Errorf("arena %s page carries %d script data blocks, want none", name, blocks)
		}
		if elements < 3 {
			t.Errorf("arena %s page scan read %d elements; it is not reading the document", name, elements)
		}
	}
}

// pageManifest resolves the assets the server-rendered journeys load, so the
// templates compile in a test the way they compile in a build.
func pageManifest() assets.Manifest {
	records := make(map[string]assets.Record)
	for _, name := range []string{
		"pages/auth.js",
		"pages/arena.js",
		"styles/reset.css",
		"styles/tokens.css",
		"styles/base.css",
		"styles/primitives.css",
		"styles/auth.css",
		"styles/arena.css",
	} {
		records[name] = assets.Record{Path: "/assets/" + strings.ReplaceAll(name, "/", "-"), SHA256: strings.Repeat("b", 64)}
	}
	return assets.Manifest{Version: 1, Assets: records}
}

// TestAccountJourneyCarriesOnlyItsOwnExternalAssets scans the browser account
// surface (P18-T05): every page of the journey must render without a single
// inline style, inline script or event handler, and the two external things it
// may load — the sheets and the module — must come from the manifest.
//
// The journey is the surface where this matters most, because it is the one
// that carries a CSRF token and a submitted address: a policy widened to
// 'unsafe-inline' for its sake would weaken every other page too.
func TestAccountJourneyCarriesOnlyItsOwnExternalAssets(t *testing.T) {
	t.Parallel()

	templates, err := identityhtml.NewTemplates(pageManifest())
	if err != nil {
		t.Fatalf("NewTemplates() error = %v", err)
	}

	field := identityhtml.FieldData{
		Name:        "email",
		Type:        "email",
		Label:       hostileValue,
		Hint:        hostileValue,
		Error:       hostileValue,
		Value:       hostileValue,
		ControlID:   "email-control",
		HintID:      "email-hint",
		ErrorID:     "email-error",
		DescribedBy: "email-hint email-error",
		Required:    true,
	}

	var form bytes.Buffer
	err = templates.RenderForm(&form, identityhtml.FormPageData{
		DocumentData: identityhtml.DocumentData{
			Lang:      "pt-BR",
			PageTitle: hostileValue,
			Brand:     hostileValue,
			NavLabel:  hostileValue,
			Nav:       []identityhtml.ActionLink{{Label: hostileValue, Href: "/login"}},
		},
		Heading:      hostileValue,
		Intro:        hostileValue,
		Action:       "/register",
		Method:       "POST",
		CSRFName:     "csrf_token",
		CSRFToken:    "token.signature",
		SummaryTitle: hostileValue,
		Summary:      []identityhtml.SummaryItem{{Target: "email-control", Message: hostileValue}},
		Fields:       []identityhtml.FieldData{field},
		SubmitLabel:  hostileValue,
		BusyLabel:    hostileValue,
		After:        &identityhtml.ActionLink{Label: hostileValue, Href: "/reset"},
	})
	if err != nil {
		t.Fatalf("RenderForm() error = %v", err)
	}

	var notice bytes.Buffer
	err = templates.RenderNotice(&notice, identityhtml.NoticePageData{
		DocumentData: identityhtml.DocumentData{Lang: "en-US", PageTitle: hostileValue, Brand: hostileValue, NavLabel: hostileValue},
		Heading:      hostileValue,
		Detail:       hostileValue,
		Actions:      []identityhtml.ActionLink{{Label: hostileValue, Href: "/login"}},
	})
	if err != nil {
		t.Fatalf("RenderNotice() error = %v", err)
	}

	for name, document := range map[string]string{"form": form.String(), "notice": notice.String()} {
		if !strings.Contains(document, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Fatalf("the account %s page did not render the hostile value escaped, so the scan is not reading the real document:\n%s", name, document)
		}
		blocks, elements := scanPage(t, "account "+name+" page", document)
		if blocks != 0 {
			t.Errorf("the account %s page carries %d script data blocks, want none", name, blocks)
		}
		if elements < 8 {
			t.Errorf("the account %s page scan read %d elements; it is not reading the document", name, elements)
		}
	}
}

// TestArenaParticipationCarriesOnlyItsOwnExternalAssets scans the browser
// participation surface (P18-T06): the page a person acts on must render without
// a single inline style, inline script or event handler, with every dynamic
// value — the statement, the categories, the argument contents, the labels of
// the counted choices — escaped by html/template.
//
// It is the second page of the product that carries a CSRF token and a person's
// own state, so it is the second place where a policy widened for convenience
// would weaken every other document too.
func TestArenaParticipationCarriesOnlyItsOwnExternalAssets(t *testing.T) {
	t.Parallel()

	templates, err := arenashtml.NewParticipationTemplates(pageManifest())
	if err != nil {
		t.Fatalf("NewParticipationTemplates() error = %v", err)
	}

	choice := arenashtml.ChoiceData{Name: "position", Value: "agree", Label: hostileValue, ControlID: "position-agree"}

	var page bytes.Buffer
	err = templates.RenderPage(&page, arenashtml.ParticipationPageData{
		ParticipationChrome: arenashtml.ParticipationChrome{
			Lang:      "pt-BR",
			PageTitle: hostileValue,
			Brand:     hostileValue,
			NavLabel:  hostileValue,
			Nav:       []arenashtml.ParticipationLink{{Label: hostileValue, Href: "/login"}},
			SignOut:   &arenashtml.SignOutData{Action: "/logout", Field: "csrf_token", Token: "token.signature", Label: hostileValue},
		},
		ArenaID:          "00000000-0000-0000-0000-0000000000a1",
		Statement:        hostileValue,
		Context:          hostileValue,
		StatusLabel:      hostileValue,
		CategoryLabel:    hostileValue,
		LanguageLabel:    hostileValue,
		PublishedAt:      "2026-09-18T12:00:00Z",
		PublishedLabel:   hostileValue,
		Notices:          []string{hostileValue},
		AggregateHeading: hostileValue,
		RevealLabel:      hostileValue,
		RevealHref:       "/arenas/exemplo?reveal=1",
		Aggregate: &arenashtml.AggregateData{
			Heading:        hostileValue,
			TotalLabel:     hostileValue,
			CurrentHeading: hostileValue,
			InitialHeading: hostileValue,
			CheckedLabel:   hostileValue,
			SuppressedNote: hostileValue,
			Current:        []arenashtml.DistributionRow{{Label: hostileValue, Count: 3}},
			Initial:        []arenashtml.DistributionRow{{Label: hostileValue, Count: 2}},
		},
		PositionHeading: hostileValue,
		Anonymous: &arenashtml.AnonymousPositionData{
			Text:    hostileValue,
			Heading: hostileValue,
			Hint:    hostileValue,
			Choices: []arenashtml.ChoiceData{choice},
			Links:   []arenashtml.ParticipationLink{{Label: hostileValue, Href: "/register"}},
		},
		State: &arenashtml.PositionStateData{Heading: hostileValue, Initial: hostileValue, Current: hostileValue},
		Confirm: &arenashtml.PositionFormData{
			FormData: arenashtml.FormData{
				Action:       "/arenas/exemplo/position",
				Heading:      hostileValue,
				Intro:        hostileValue,
				Field:        "csrf_token",
				Token:        "token.signature",
				SummaryTitle: hostileValue,
				Summary:      []arenashtml.SummaryData{{Target: "position-agree", Message: hostileValue}},
				Error:        hostileValue,
				ErrorID:      "position-form-error",
				SubmitLabel:  hostileValue,
				BusyLabel:    hostileValue,
			},
			Group: arenashtml.FieldGroupData{Name: "position", Legend: hostileValue, Hint: hostileValue, Error: hostileValue, ControlID: "position", HintID: "position-hint", ErrorID: "position-error", Choices: []arenashtml.ChoiceData{choice}},
		},
		Change: &arenashtml.PositionFormData{
			FormData: arenashtml.FormData{Action: "/arenas/exemplo/position/change", Heading: hostileValue, Intro: hostileValue, Field: "csrf_token", Token: "token.signature", SubmitLabel: hostileValue, BusyLabel: hostileValue},
			Group:    arenashtml.FieldGroupData{Name: "position", Legend: hostileValue, ControlID: "position"},
		},
		Arguments: []arenashtml.RelationGroupData{{
			Heading:   hostileValue,
			Empty:     hostileValue,
			Arguments: []arenashtml.ArgumentData{{ID: "argument-1", RelationLabel: hostileValue, Content: hostileValue, CreatedAt: "2026-09-20T07:00:00Z", RepliesLabel: hostileValue, OptionLabel: hostileValue}},
		}},
		Publish: &arenashtml.PublishFormData{
			FormData:      arenashtml.FormData{Action: "/arenas/exemplo/arguments", Heading: hostileValue, Intro: hostileValue, Field: "csrf_token", Token: "token.signature", Hidden: []arenashtml.HiddenData{{Name: "attempt", Value: "attempt-1"}}, SubmitLabel: hostileValue, BusyLabel: hostileValue},
			Relations:     arenashtml.FieldGroupData{Name: "relation", Legend: hostileValue, ControlID: "relation"},
			ContentName:   "content",
			ContentLabel:  hostileValue,
			ContentHint:   hostileValue,
			ContentValue:  hostileValue,
			ContentID:     "content-control",
			ContentHintID: "content-hint",
		},
		Attribution: &arenashtml.AttributionFormData{
			FormData: arenashtml.FormData{Action: "/arenas/exemplo/attributions", Heading: hostileValue, Intro: hostileValue, Field: "csrf_token", Token: "token.signature", Hidden: []arenashtml.HiddenData{{Name: "change_id", Value: "change-1"}}, SubmitLabel: hostileValue, BusyLabel: hostileValue},
			Options:  arenashtml.FieldGroupData{Name: "argument_ids", Legend: hostileValue, Hint: hostileValue, ControlID: "argument_ids", HintID: "argument_ids-hint", ErrorID: "argument_ids-error", Limit: 3, Choices: []arenashtml.ChoiceData{{Name: "argument_ids", Value: "argument-1", Label: hostileValue, ControlID: "argument_ids-argument-1"}}},
		},
	})
	if err != nil {
		t.Fatalf("RenderPage() error = %v", err)
	}

	var notice bytes.Buffer
	err = templates.RenderNotice(&notice, arenashtml.NoticeData{
		ParticipationChrome: arenashtml.ParticipationChrome{Lang: "en-US", PageTitle: hostileValue, Brand: hostileValue, NavLabel: hostileValue},
		Heading:             hostileValue,
		Detail:              hostileValue,
		Actions:             []arenashtml.ParticipationLink{{Label: hostileValue, Href: "/arenas/exemplo"}},
	})
	if err != nil {
		t.Fatalf("RenderNotice() error = %v", err)
	}

	for name, document := range map[string]string{"participation": page.String(), "notice": notice.String()} {
		if !strings.Contains(document, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Fatalf("the Arena %s page did not render the hostile value escaped, so the scan is not reading the real document:\n%s", name, document)
		}
		blocks, elements := scanPage(t, "arena "+name+" page", document)
		if blocks != 0 {
			t.Errorf("the Arena %s page carries %d script data blocks, want none", name, blocks)
		}
		if elements < 10 {
			t.Errorf("the Arena %s page scan read %d elements; it is not reading the document", name, elements)
		}
	}
}

// TestTransparencyDocumentCarriesNoScriptAtAll scans the public transparency
// document, the other server-rendered surface, and proves the scan reads it.
func TestTransparencyDocumentCarriesNoScriptAtAll(t *testing.T) {
	t.Parallel()

	templates, err := transparencyhttp.NewTemplates()
	if err != nil {
		t.Fatalf("NewTemplates() error = %v", err)
	}

	var document bytes.Buffer
	err = templates.RenderDocument(&document, transparencyhttp.TransparencyDocument{
		Lang:         "pt-BR",
		PageTitle:    hostileValue,
		Heading:      hostileValue,
		Period:       hostileValue,
		Updated:      hostileValue,
		Methodology:  hostileValue,
		MetricHeader: hostileValue,
		ValueHeader:  hostileValue,
		Rows: []transparencyhttp.TransparencyRow{
			{Code: "arenas_published", Value: 6},
			{Code: "ink_free_granted", Value: 120},
		},
	})
	if err != nil {
		t.Fatalf("RenderDocument() error = %v", err)
	}

	rendered := document.String()
	if !strings.Contains(rendered, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("the hostile value was not rendered escaped, so the scan is not reading the real document:\n%s", rendered)
	}
	if !strings.Contains(rendered, "<td>arenas_published</td>") {
		t.Fatalf("the document lost its metric rows:\n%s", rendered)
	}

	blocks, elements := scanPage(t, "transparency document", rendered)
	if blocks != 0 {
		t.Errorf("transparency document carries %d script data blocks, want none", blocks)
	}
	if elements < 8 {
		t.Errorf("transparency document scan read %d elements; it is not reading the document", elements)
	}
}
