/**
 * Tests of the seasons staged client (P57-T01) against a fake transport.
 *
 * The four season reads speak the paths their fragment declares — current,
 * history, one season and its champions — and nothing else: reads are GETs
 * with no `Idempotency-Key`, every call leaves with the account no-store
 * policy the core already owns for `/api/v1/me/*`, and a cancelled
 * navigation aborts instead of rendering late. Failures name only the
 * server codes the backend really emits.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createSeasonsClient } from "../../src/core/clients/seasons.js";
import {
  captureApiError,
  createTestContext,
  hangingResponse,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const SEASON = {
  ends_at: "2026-11-05T00:00:00Z",
  ordinal: 312,
  season_key: "temporada-harness-b",
  starts_at: "2026-10-05T00:00:00Z",
  state: "active",
  title: "Temporada Harness B",
} as const;

const HISTORY = { seasons: [SEASON], title: "Histórico de temporadas" } as const;

const CHAMPIONS = {
  cutoff_revision: 7,
  hash: "hash-harness",
  last_king: "pseudonimo-rei",
  last_king_title: "Último Rei",
  leaders: [{ display: "pseudonimo-lider", subject: "sujeito-1" }],
  richest_title: "Mais rico no corte",
  season_key: "temporada-harness-b",
  title: "Campeões da temporada",
} as const;

test("the current season reads dates and state with no key", async () => {
  const context = createTestContext({ responder: () => jsonResponse(SEASON) });

  const season = await createSeasonsClient(context.core).readCurrentSeason();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/seasons/current");
  assert.equal(season.season_key, "temporada-harness-b");
  assert.equal(season.state, "active");
  assert.equal(context.lastCall().init.cache, "no-store");
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
});

test("the history reads the allowlist without balances", async () => {
  const context = createTestContext({ responder: () => jsonResponse(HISTORY) });

  const history = await createSeasonsClient(context.core).readSeasonHistory();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/seasons/history");
  assert.equal(history.seasons.length, 1);
  assert.equal(context.lastCall().init.cache, "no-store");
  const serialized = JSON.stringify(history).toLowerCase();
  for (const marker of ["balance", "holder", "wealth", "ledger", "account_id"]) {
    assert.ok(!serialized.includes(marker), `allowlist leaked ${marker}`);
  }
});

test("one season travels encoded with its key", async () => {
  const context = createTestContext({ responder: () => jsonResponse(SEASON) });

  const season = await createSeasonsClient(context.core).readSeason("temporada-harness-b");

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/seasons/temporada-harness-b");
  assert.equal(season.ordinal, 312);
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the champions read pseudonyms with no exact wealth", async () => {
  const context = createTestContext({ responder: () => jsonResponse(CHAMPIONS) });

  const champions = await createSeasonsClient(context.core).readSeasonChampions("temporada-harness-b");

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/me/seasons/temporada-harness-b/champions",
  );
  assert.equal(champions.leaders[0]?.display, "pseudonimo-lider");
  assert.equal(context.lastCall().init.cache, "no-store");
  const serialized = JSON.stringify(champions).toLowerCase();
  assert.ok(!serialized.includes("wealth"), "exact wealth must never serialize");
});

test("a stranger key with a slash travels encoded", async () => {
  const context = createTestContext({ responder: () => jsonResponse(SEASON) });

  await createSeasonsClient(context.core).readSeason("a/b");

  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/seasons/a%2Fb");
});

test("anonymous, foreign-namespace, unknown and suspended reads fail distinctly", async () => {
  for (const [status, code, kind] of [
    [401, "unauthorized", "unauthorized"],
    [403, "season_mismatch", "forbidden"],
    [404, "season_unknown", "not_found"],
    [409, "season_closed", "conflict"],
    [409, "season_archived", "conflict"],
  ] as const) {
    const context = createTestContext({ responder: () => problemResponse(status, code) });
    const failure = await captureApiError(() => createSeasonsClient(context.core).readCurrentSeason());
    assert.equal(failure.code, code);
    assert.equal(failure.kind, kind);
    assert.equal(context.calls.length, 1, `${code}: no automatic second attempt`);
  }
});

test("reads send no body and no idempotency key", async () => {
  const context = createTestContext({ responder: () => jsonResponse(SEASON) });

  await createSeasonsClient(context.core).readSeason("temporada-harness-b");

  assert.equal(context.lastCall().init.body, undefined);
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
});

test("a cancelled season read aborts instead of rendering late", async () => {
  const controller = new AbortController();
  const context = createTestContext({ responder: (call) => hangingResponse(call) });

  const pending = captureApiError(() => createSeasonsClient(context.core).readCurrentSeason(controller.signal));
  controller.abort();
  const failure = await pending;

  assert.equal(failure.code, "aborted");
  assert.equal(failure.aborted, true, "an aborted answer is discarded, never rendered");
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
});
