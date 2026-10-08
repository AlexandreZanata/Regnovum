/**
 * Tests of the seasons staged presentation (P57-T01).
 *
 * They run the real generated catalogs in both locales: one season renders
 * UTC dates with its verbatim state, history renders the server order with
 * no balance, champions reuse the redacted exhibit with pseudonyms only,
 * the countdown expires without extending the cutoff, stale answers are
 * discarded, and failures name only the server codes the backend really
 * emits. A disabled capability renders unavailability and sends nothing.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import type {
  SeasonChampionsDocument,
  SeasonDocument,
  SeasonHistoryDocument,
} from "../../src/contracts/staged/seasons.js";
import { noStagedCapabilities, stagedCapabilities } from "../../src/core/staged.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { Locale } from "../../src/i18n/locale.js";
import {
  isSeasonLive,
  isStaleSeasonsResponse,
  requireSeasonsEnabled,
  seasonChampionsView,
  seasonDetailView,
  seasonFailure,
  seasonHistoryView,
  seasonsGate,
} from "../../src/pages/seasons.js";
import { StagedUnavailableError } from "../../src/pages/staged.js";

/** A translator holding exactly the namespace the page renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["seasons"] });
}

const SEASON: SeasonDocument = {
  ends_at: "2026-11-05T00:00:00Z",
  ordinal: 312,
  season_key: "temporada-harness-b",
  starts_at: "2026-10-05T00:00:00Z",
  state: "active",
  title: "Temporada Harness B",
};

const ARCHIVED: SeasonDocument = {
  ends_at: "2026-10-05T00:00:00Z",
  ordinal: 311,
  season_key: "temporada-harness-a",
  starts_at: "2026-07-05T00:00:00Z",
  state: "archived",
  title: "Temporada Harness A",
};

const CHAMPIONS: SeasonChampionsDocument = {
  cutoff_revision: 7,
  hash: "hash-harness",
  last_king: "pseudonimo-rei",
  last_king_title: "Último Rei",
  leaders: [{ display: "pseudonimo-lider", subject: "sujeito-1" }],
  richest_title: "Mais rico no corte",
  season_key: "temporada-harness-b",
  title: "Campeões da temporada",
};

const NOW_BEFORE_END = new Date("2026-10-20T00:00:00Z");
const NOW_AFTER_END = new Date("2026-12-01T00:00:00Z");

test("one season renders UTC dates with its verbatim state", () => {
  for (const locale of ["pt-BR", "en-US"] as const) {
    const view = seasonDetailView(translatorOf(locale), locale, SEASON, NOW_BEFORE_END);

    assert.equal(view.key, "temporada-harness-b");
    assert.ok(!view.period.includes("2026-10-05T00:00:00Z"), "raw instant leaked");
    assert.ok(!view.period.includes("2026-11-05T00:00:00Z"), "raw instant leaked");
    assert.ok(view.state.includes("active"), "verbatim state missing");
    assert.equal(view.expired, false);
    assert.equal(view.expiryNote, null);
    assert.ok(!JSON.stringify(view).toLowerCase().includes("balance"), "history must carry no balance");
  }
});

test("the countdown expires without extending the cutoff", () => {
  assert.equal(isSeasonLive(SEASON.ends_at, NOW_BEFORE_END), true);
  assert.equal(isSeasonLive(SEASON.ends_at, NOW_AFTER_END), false);
  assert.equal(isSeasonLive(SEASON.ends_at, new Date(SEASON.ends_at)), false, "reaching the cutoff ends it");

  const live = seasonDetailView(translatorOf("pt-BR"), "pt-BR", SEASON, NOW_BEFORE_END);
  assert.equal(live.expired, false);

  const expired = seasonDetailView(translatorOf("pt-BR"), "pt-BR", SEASON, NOW_AFTER_END);
  assert.equal(expired.expired, true);
  assert.ok((expired.expiryNote ?? "").includes("corte"), "expiry names the server cutoff");
  assert.equal(expired.period, live.period, "expiry never rewrites the server window");

  const en = seasonDetailView(translatorOf("en-US"), "en-US", SEASON, NOW_AFTER_END);
  assert.ok((en.expiryNote ?? "").includes("cutoff"), "expiry names the server cutoff");
});

test("history keeps the server order with no spendable balance", () => {
  const history: SeasonHistoryDocument = { seasons: [ARCHIVED, SEASON], title: "Histórico" };
  const view = seasonHistoryView(translatorOf("pt-BR"), "pt-BR", history, NOW_BEFORE_END);

  assert.equal(view.state, "ready");
  if (view.state === "ready") {
    assert.deepEqual(
      view.rows.map((row) => row.key),
      ["temporada-harness-a", "temporada-harness-b"],
      "history must keep the server order",
    );
    assert.ok(view.count.includes("2"), "count missing");
    const serialized = JSON.stringify(view).toLowerCase();
    for (const marker of ["balance", "wealth", "holder", "ledger", "spend", "saldo"]) {
      assert.ok(!serialized.includes(marker), `history leaked ${marker}`);
    }
  }

  const empty = seasonHistoryView(translatorOf("en-US"), "en-US", { seasons: [], title: "History" }, NOW_BEFORE_END);
  assert.equal(empty.state, "empty");
});

test("champions reuse the redacted exhibit with pseudonyms only", () => {
  const view = seasonChampionsView(translatorOf("pt-BR"), "pt-BR", CHAMPIONS);

  assert.deepEqual(
    view.exhibit.lines.map((line) => line.text),
    ["Campeões da temporada", "Mais rico no corte", "Último Rei: pseudonimo-rei", "pseudonimo-lider"],
  );
  assert.equal(view.exhibit.role, "status");
  assert.ok(view.cutoff.includes("7"), "cutoff revision missing");
  assert.equal(view.hash, "hash-harness");
  assert.equal(view.empty, null);
  const serialized = JSON.stringify(view).toLowerCase();
  assert.ok(!serialized.includes("wealth"), "exact wealth must never render");

  const none = seasonChampionsView(translatorOf("en-US"), "en-US", { ...CHAMPIONS, leaders: [] });
  assert.ok((none.empty ?? "").includes("No champions"), "empty champions missing");
});

test("cross-season reads keep their own keys without leaking the other book", () => {
  const current = seasonDetailView(translatorOf("pt-BR"), "pt-BR", SEASON, NOW_BEFORE_END);
  const archived = seasonDetailView(translatorOf("pt-BR"), "pt-BR", ARCHIVED, NOW_AFTER_END);

  assert.equal(current.key, "temporada-harness-b");
  assert.equal(archived.key, "temporada-harness-a");
  assert.ok(!JSON.stringify(current).includes("temporada-harness-a"), "cross-season leak");
  assert.ok(!JSON.stringify(archived).includes("temporada-harness-b"), "cross-season leak");
  assert.equal(archived.expired, true);
});

test("a stale answer is discarded, never merged", () => {
  assert.equal(isStaleSeasonsResponse(1, 2), true);
  assert.equal(isStaleSeasonsResponse(2, 2), false);
  assert.equal(isStaleSeasonsResponse(3, 2), false);
});

test("season denials name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(seasonFailure(translator, "unauthorized").includes("Sign in"));
  assert.ok(seasonFailure(translator, "season_mismatch").includes("namespace"));
  assert.ok(seasonFailure(translator, "season_unknown").includes("does not exist"));
  assert.ok(seasonFailure(translator, "season_closed").includes("No active season"));
  assert.ok(seasonFailure(translator, "season_archived").includes("not open yet"));
  assert.equal(
    seasonFailure(translator, "something-the-backend-never-emits"),
    seasonFailure(translator, "unknown-code"),
    "an unknown code must fall back to the generic sentence",
  );
});

test("the game office is a game cargo, never an admin grant", () => {
  const view = seasonChampionsView(translatorOf("en-US"), "en-US", CHAMPIONS);
  const serialized = JSON.stringify(view).toLowerCase();
  assert.ok(!serialized.includes("admin"), "game cargo must never read as admin");
});

test("a disabled capability renders unavailability and guards the read", () => {
  const translator = translatorOf("pt-BR");

  const off = seasonsGate(noStagedCapabilities(), translator);
  assert.equal(off.enabled, false);
  assert.ok((off.view?.heading ?? "").includes("indisponível"), "unavailability heading missing");

  const on = seasonsGate(stagedCapabilities(["seasons"]), translator);
  assert.equal(on.enabled, true);
  assert.equal(on.view, null);

  assert.throws(() => requireSeasonsEnabled(noStagedCapabilities()), StagedUnavailableError);
  requireSeasonsEnabled(stagedCapabilities(["seasons"]));
});
