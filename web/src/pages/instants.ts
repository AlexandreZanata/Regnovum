/**
 * Instants of the Arena participation page (P18-T06 journey, "Visitante
 * entende uma Arena" and "Participante publica argumento").
 *
 * The server renders the machine value of an instant — the RFC 3339 string the
 * API sent — inside `<time datetime="…">`, and the sentence around it comes
 * from the catalog with that same raw string interpolated. A document without
 * scripts has to show something, and showing the machine value is the honest
 * fallback; what the module adds is the locale rendering: the text a person
 * reads becomes the `Intl` form of the instant while the attribute keeps the
 * machine value assistive technology and crawlers consume.
 *
 * The decision is DOM-free so the Node runner can verify it: the entry module
 * only reads the attribute and the visible text, applies what is returned here
 * and leaves the element untouched when there is nothing safe to apply.
 */
import { formatInstant } from "../i18n/formats.js";
import type { Locale } from "../i18n/locale.js";

/** The rendering of one instant: a readable date and time, reader's zone. */
const INSTANT_OPTIONS = { dateStyle: "medium", timeStyle: "short" } as const;

/**
 * timeText returns the visible text one `<time>` element should carry, or null
 * when the element must stay exactly as the server rendered it.
 *
 * The three cases, in order:
 *
 *   - the element shows the machine value alone (the argument list), so the
 *     whole text is replaced by the formatted instant;
 *   - the element shows a catalog sentence with the machine value interpolated
 *     (the publication line), so only that substring is replaced — the words
 *     around it were translated by the server and are never rewritten here;
 *   - the element shows something else, or the attribute is not an instant, so
 *     nothing is applied. An unparseable value is a defect of the page, and
 *     echoing it back or guessing a date would be worse than the raw string.
 */
export function timeText(locale: Locale, datetime: string, visible: string): string | null {
  const machine = datetime.trim();
  if (machine === "") {
    return null;
  }
  let rendered: string;
  try {
    rendered = formatInstant(locale, machine, INSTANT_OPTIONS);
  } catch {
    return null;
  }

  const current = visible.trim();
  if (current === "" || current === machine) {
    return rendered;
  }
  if (!current.includes(machine)) {
    return null;
  }
  return current.replace(machine, rendered);
}
