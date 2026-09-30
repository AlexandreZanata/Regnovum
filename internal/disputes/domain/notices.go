package domain

// DisputeEvent is the closed vocabulary of lifecycle events one
// private case notifies: proposal, acceptance, defense, ruling and
// appeal. Every event renders a localized notice title; the safety
// rite never emits these notices.
type DisputeEvent string

const (
	// EventProposal names the sealed bilateral proposal notice.
	EventProposal DisputeEvent = "proposal"
	// EventAccept names the acceptance notice.
	EventAccept DisputeEvent = "accept"
	// EventDefense names the filed defense notice.
	EventDefense DisputeEvent = "defense"
	// EventRuling names the reasoned ruling notice.
	EventRuling DisputeEvent = "ruling"
	// EventAppeal names the appeal notice.
	EventAppeal DisputeEvent = "appeal"
)

// AllDisputeEvents returns the closed vocabulary in lifecycle order.
func AllDisputeEvents() []DisputeEvent {
	return []DisputeEvent{EventProposal, EventAccept, EventDefense, EventRuling, EventAppeal}
}

// ParseDisputeEvent validates an event token. Matching is exact: no
// trimming, no case folding.
func ParseDisputeEvent(raw string) (DisputeEvent, error) {
	event := DisputeEvent(raw)
	switch event {
	case EventProposal, EventAccept, EventDefense, EventRuling, EventAppeal:
		return event, nil
	default:
		return "", ErrInvalidCase
	}
}

// String returns the stored event value.
func (e DisputeEvent) String() string { return string(e) }

// NoticeLocale is the closed vocabulary of interface locales the
// private case display renders in: Portuguese and English. Versions,
// amounts and instants travel canonical either way; the locale only
// selects the titles, never the values.
type NoticeLocale string

const (
	// NoticeLocalePortuguese renders the case titles in Portuguese.
	NoticeLocalePortuguese NoticeLocale = "pt"
	// NoticeLocaleEnglish renders the case titles in English.
	NoticeLocaleEnglish NoticeLocale = "en"
)

// AllNoticeLocales returns the closed vocabulary in canonical order.
func AllNoticeLocales() []NoticeLocale {
	return []NoticeLocale{NoticeLocalePortuguese, NoticeLocaleEnglish}
}

// ParseNoticeLocale validates a locale against the closed
// vocabulary. Matching is exact: surrounding whitespace is not
// trimmed and case is not folded.
func ParseNoticeLocale(raw string) (NoticeLocale, error) {
	locale := NoticeLocale(raw)
	switch locale {
	case NoticeLocalePortuguese, NoticeLocaleEnglish:
		return locale, nil
	default:
		return "", ErrInvalidCase
	}
}

// String returns the stored locale value.
func (l NoticeLocale) String() string { return string(l) }

// NoticeTitles carries every human string the private case display
// may hold in one locale: the case title, one notice per lifecycle
// event, the ruling title and the failure title. No amount,
// identifier, digest or secret ever enters this shape — only these
// dictionary values reach the reader.
type NoticeTitles struct {
	Case     string
	Proposal string
	Accept   string
	Defense  string
	Ruling   string
	Appeal   string
	Failure  string
}

// NoticeTitlesFor renders the closed title dictionary in one locale.
// The values translate; versions, amounts and instants never do.
func NoticeTitlesFor(locale NoticeLocale) NoticeTitles {
	if locale == NoticeLocaleEnglish {
		return NoticeTitles{
			Case:     "Private case",
			Proposal: "Sealed proposal",
			Accept:   "Acceptance recorded",
			Defense:  "Defense filed",
			Ruling:   "Reasoned ruling",
			Appeal:   "Appeal opened",
			Failure:  "Case unavailable",
		}
	}
	return NoticeTitles{
		Case:     "Caso privado",
		Proposal: "Proposta selada",
		Accept:   "Aceite registrado",
		Defense:  "Defesa apresentada",
		Ruling:   "Sentença fundamentada",
		Appeal:   "Recurso aberto",
		Failure:  "Caso indisponível",
	}
}

// TitleForEvent renders the notice title of one lifecycle event in
// one locale.
func TitleForEvent(event DisputeEvent, locale NoticeLocale) (string, error) {
	titles := NoticeTitlesFor(locale)
	switch event {
	case EventProposal:
		return titles.Proposal, nil
	case EventAccept:
		return titles.Accept, nil
	case EventDefense:
		return titles.Defense, nil
	case EventRuling:
		return titles.Ruling, nil
	case EventAppeal:
		return titles.Appeal, nil
	default:
		return "", ErrInvalidCase
	}
}
