package domain

import (
	"errors"
	"testing"
)

func TestNoticeTitlesParity(t *testing.T) {
	pt := NoticeTitlesFor(NoticeLocalePortuguese)
	en := NoticeTitlesFor(NoticeLocaleEnglish)
	fields := map[string][2]string{
		"case":     {pt.Case, en.Case},
		"proposal": {pt.Proposal, en.Proposal},
		"accept":   {pt.Accept, en.Accept},
		"defense":  {pt.Defense, en.Defense},
		"ruling":   {pt.Ruling, en.Ruling},
		"appeal":   {pt.Appeal, en.Appeal},
		"failure":  {pt.Failure, en.Failure},
	}
	for name, pair := range fields {
		if pair[0] == "" || pair[1] == "" {
			t.Fatalf("titles %q has a blank side: pt=%q en=%q", name, pair[0], pair[1])
		}
		if pair[0] == pair[1] {
			t.Fatalf("titles %q is not translated: %q", name, pair[0])
		}
	}
	for _, event := range AllDisputeEvents() {
		ptTitle, err := TitleForEvent(event, NoticeLocalePortuguese)
		if err != nil || ptTitle == "" {
			t.Fatalf("TitleForEvent(%q, pt) = %q/%v", string(event), ptTitle, err)
		}
		enTitle, err := TitleForEvent(event, NoticeLocaleEnglish)
		if err != nil || enTitle == "" || enTitle == ptTitle {
			t.Fatalf("TitleForEvent(%q, en) = %q/%v, want a translated title", string(event), enTitle, err)
		}
	}
	if _, err := TitleForEvent("sentenca", NoticeLocalePortuguese); !errors.Is(err, ErrInvalidCase) {
		t.Fatalf("TitleForEvent(unknown) = %v, want ErrInvalidCase", err)
	}
	for _, raw := range []string{"", "Proposal", " proposta", "veredito"} {
		if _, err := ParseDisputeEvent(raw); !errors.Is(err, ErrInvalidCase) {
			t.Fatalf("ParseDisputeEvent(%q) = %v, want ErrInvalidCase", raw, err)
		}
	}
	for _, raw := range []string{"", "PT", " pt", "português"} {
		if _, err := ParseNoticeLocale(raw); !errors.Is(err, ErrInvalidCase) {
			t.Fatalf("ParseNoticeLocale(%q) = %v, want ErrInvalidCase", raw, err)
		}
	}
}
