package domain

// CrumbMetricsLabels carries every human string the public
// crumb-metrics document may hold: the section titles in one locale.
// Amounts travel as integer millis either way; the locale only
// selects the titles and the window caption, never the numbers, so a
// pt document and an en document carry identical integers.
type CrumbMetricsLabels struct {
	Title       string
	Supply      string
	Treasury    string
	Circulation string
	R4          string
	Crumbs      string
	IRR         string
	Window      string
	Suppressed  string
}

// CrumbMetricsLabelsFor renders the closed title dictionary in one
// locale. The keys are stable across locales; only the titles
// translate.
func CrumbMetricsLabelsFor(locale TotalsLocale) CrumbMetricsLabels {
	if locale == TotalsLocaleEnglish {
		return CrumbMetricsLabels{
			Title:       "Crumbs and reflux — weekly aggregate",
			Supply:      "Total supply (S)",
			Treasury:    "Total treasury",
			Circulation: "In circulation",
			R4:          "R4 reference",
			Crumbs:      "Crumbs granted",
			IRR:         "Real reflux index",
			Window:      "Sealed window (UTC)",
			Suppressed:  "Low count withheld",
		}
	}
	return CrumbMetricsLabels{
		Title:       "Migalhas e refluxo — agregado semanal",
		Supply:      "Oferta total (S)",
		Treasury:    "Tesouro total",
		Circulation: "Em circulação",
		R4:          "Referência R4",
		Crumbs:      "Migalhas concedidas",
		IRR:         "Índice de refluxo real",
		Window:      "Janela selada (UTC)",
		Suppressed:  "Contagem baixa omitida",
	}
}
