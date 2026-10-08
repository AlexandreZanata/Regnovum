/**
 * Tests of the staged capabilities and the staged module clients
 * (P56-T04) against a fake transport.
 *
 * The fifteen staged operations speak the paths, methods and bodies
 * their fragments declare, and nothing else: reads are GETs, the five
 * mutations are single-shot POSTs with no `Idempotency-Key` (no staged
 * POST contract declares the header; the publication intention key
 * travels in the body the fragment declares), and every call leaves
 * with the account no-store policy the core already owns for
 * `/api/v1/me/*`. Capabilities are a closed allowlist handed in by
 * composition: unknown names are dropped, the production empty set
 * disables everything, and no DOM attribute, URL or storage value
 * ever enables the backend — the sources below are scanned to prove
 * the capability module never reads them.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createStagedClient } from "../../src/core/clients/staged.js";
import {
  STAGED_FEATURES,
  isStagedEnabled,
  noStagedCapabilities,
  stagedCapabilities,
} from "../../src/core/staged.js";
import {
  bodyOf,
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";
import { readPackageFile } from "../support/paths.js";

const SEASON = {
  ends_at: "2026-11-05T00:00:00Z",
  ordinal: 2,
  season_key: "temporada-harness-b",
  starts_at: "2026-10-05T00:00:00Z",
  state: "active",
  title: "Temporada Harness B",
} as const;

const HISTORY = { seasons: [SEASON] } as const;

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

const QUOTE = {
  accepted_at: "2026-10-05T10:00:00Z",
  content_hash: "hash-conteudo",
  expires_at: "2026-10-05T10:05:00Z",
  price_milli: 120,
  quote_hash: "hash-cotacao",
  title: "Preço de medição de INK",
  total_milli: 120,
  units: 400,
  version: 3,
} as const;

const PUBLICATION = {
  posted_at: "2026-10-05T10:01:00Z",
  publication_id: "00000000-0000-4000-8000-000000000001",
  replayed: false,
  title: "Recibo de INK",
  total_milli: 120,
  transfer_id: "transfer-1",
} as const;

const RECEIPT = {
  content_hash: "hash-conteudo",
  legs: [],
  posted_at: "2026-10-05T10:01:00Z",
  publication_id: "00000000-0000-4000-8000-000000000001",
  service: "argument-publish",
  title: "Recibo de INK",
  total_milli: 120,
  transfer_id: "transfer-1",
  units: 400,
  version: 3,
} as const;

const METERING_STATEMENT = { balance_milli: 880, entries: [], title: "Extrato de INK" } as const;

const TRADE_RECEIPT = {
  contract_id: "00000000-0000-4000-8000-000000000002",
  contract_key: "contrato-1",
  escrow_transfer_id: "escrow-1",
  expires_at: "2026-11-05T00:00:00Z",
  gross_milli: 1000,
  net_milli: 900,
  object: "arena:1",
  posted_at: "2026-10-05T10:01:00Z",
  refunded_milli: 0,
  refunds: [],
  role: "buyer",
  status: "pending",
  tithe_milli: 100,
  title: "Recibo de comércio",
} as const;

const TRADE_STATEMENT = { entries: [], title: "Extrato de comércio" } as const;

const CASE_FILE = {
  accepts: 1,
  appealed: false,
  defenses: 0,
  escrow_ref: "escrow-caso-1",
  expires_at: "2026-11-05T00:00:00Z",
  key: "caso-1",
  kind: "private",
  notices: [],
  object: "arena:1",
  role: "claimant",
  status: "proposed",
  title: "Caso privado",
  value_milli: 500,
  version: 1,
} as const;

const RULING = {
  appeal_due_at: "2026-11-12T00:00:00Z",
  appealed: false,
  award_milli: 0,
  decided_at: "2026-10-06T10:00:00Z",
  decision_code: "claimant-prevails",
  key: "caso-1",
  terms_hash: "hash-termos",
  title: "Sentença do rito",
  verdict: "procedente",
} as const;

test("the production composition enables nothing", () => {
  const capabilities = noStagedCapabilities();

  assert.deepEqual(STAGED_FEATURES, ["seasons", "metering", "commerce", "disputes"]);
  for (const feature of STAGED_FEATURES) {
    assert.equal(isStagedEnabled(capabilities, feature), false);
  }
});

test("the allowlist keeps known features and drops everything else", () => {
  const capabilities = stagedCapabilities([
    "seasons",
    "metering",
    "commerce",
    "disputes",
    "genesis",
    "tribunal",
    "__proto__",
    "seasons",
    "https://arena.test/api/v1/me/seasons/current",
  ]);

  for (const feature of STAGED_FEATURES) {
    assert.equal(isStagedEnabled(capabilities, feature), true);
  }
  assert.equal(capabilities.enabled.size, 4);
});

test("a later mutation of the input changes nothing", () => {
  const input = ["seasons"];
  const capabilities = stagedCapabilities(input);
  input.push("metering", "commerce", "disputes");

  assert.equal(isStagedEnabled(capabilities, "seasons"), true);
  assert.equal(isStagedEnabled(capabilities, "metering"), false);

  const again = stagedCapabilities(["seasons"]);
  assert.notEqual(capabilities.enabled, again.enabled);
  assert.equal(again.enabled.size, 1);
});

test("the capability module never reads the DOM, the URL or storage", () => {
  const source = readPackageFile("src/core/staged.ts")
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/(^|\s)\/\/.*$/gm, "$1");

  for (const ambient of [
    "window",
    "document",
    "location",
    "localStorage",
    "sessionStorage",
    "URLSearchParams",
  ]) {
    assert.ok(!source.includes(ambient), `staged.ts must never reach ${ambient}`);
  }
});

test("the four season reads speak their fragment paths", async () => {
  const context = createTestContext({ responder: () => jsonResponse(SEASON) });
  const client = createStagedClient(context.core);

  await client.readCurrentSeason();
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/seasons/current");
  assert.equal(context.lastCall().init.method, "GET");

  const historyContext = createTestContext({ responder: () => jsonResponse(HISTORY) });
  await createStagedClient(historyContext.core).readSeasonHistory();
  assert.equal(historyContext.lastCall().url, "https://arena.test/api/v1/me/seasons/history");

  const oneContext = createTestContext({ responder: () => jsonResponse(SEASON) });
  await createStagedClient(oneContext.core).readSeason("temporada harness/b");
  assert.equal(
    oneContext.lastCall().url,
    "https://arena.test/api/v1/me/seasons/temporada%20harness%2Fb",
  );

  const championsContext = createTestContext({ responder: () => jsonResponse(CHAMPIONS) });
  const champions = await createStagedClient(championsContext.core).readSeasonChampions("temporada-harness-b");
  assert.equal(
    championsContext.lastCall().url,
    "https://arena.test/api/v1/me/seasons/temporada-harness-b/champions",
  );
  assert.equal(champions.last_king, "pseudonimo-rei");
  assert.equal(championsContext.lastCall().init.cache, "no-store");
});

test("the four metering operations speak paths, methods and bodies", async () => {
  const quoteContext = createTestContext({ responder: () => jsonResponse(QUOTE) });
  const quote = await createStagedClient(quoteContext.core).previewMeteringQuote({
    content: "texto final",
    service: "argument-publish",
  });
  assert.equal(quoteContext.lastCall().url, "https://arena.test/api/v1/me/metering/quotes");
  assert.equal(quoteContext.lastCall().init.method, "POST");
  assert.deepEqual(bodyOf(quoteContext.lastCall()), { content: "texto final", service: "argument-publish" });
  assert.equal(headerOf(quoteContext.lastCall(), "idempotency-key"), null);
  assert.equal(quote.total_milli, 120);

  const publicationContext = createTestContext({ responder: () => jsonResponse(PUBLICATION) });
  await createStagedClient(publicationContext.core).confirmMeteringPublication({
    intention_key: "intencao-1",
    content: "texto final",
    service: "argument-publish",
  });
  assert.equal(publicationContext.lastCall().url, "https://arena.test/api/v1/me/metering/publications");
  assert.deepEqual(bodyOf(publicationContext.lastCall()), {
    intention_key: "intencao-1",
    content: "texto final",
    service: "argument-publish",
  });
  assert.equal(headerOf(publicationContext.lastCall(), "idempotency-key"), null);

  const receiptContext = createTestContext({ responder: () => jsonResponse(RECEIPT) });
  await createStagedClient(receiptContext.core).readMeteringReceipt("00000000-0000-4000-8000-000000000001");
  assert.equal(
    receiptContext.lastCall().url,
    "https://arena.test/api/v1/me/metering/publications/00000000-0000-4000-8000-000000000001",
  );

  const statementContext = createTestContext({ responder: () => jsonResponse(METERING_STATEMENT) });
  await createStagedClient(statementContext.core).readMeteringStatement();
  assert.equal(statementContext.lastCall().url, "https://arena.test/api/v1/me/metering/statement");
  assert.equal(statementContext.lastCall().init.cache, "no-store");
});

test("a lost metering mutation is a failure the harness answers by asking again", async () => {
  const context = createTestContext({ responder: () => problemResponse(503, "unavailable") });

  const failure = await captureApiError(() =>
    createStagedClient(context.core).previewMeteringQuote({ content: "texto final", service: "argument-publish" }),
  );

  assert.equal(context.calls.length, 1, "the core must not replay a mutation without a contract key");
  assert.equal(failure.retryable, false);
});

test("the two commerce reads speak their fragment paths", async () => {
  const receiptContext = createTestContext({ responder: () => jsonResponse(TRADE_RECEIPT) });
  const receipt = await createStagedClient(receiptContext.core).readTradeReceipt(
    "00000000-0000-4000-8000-000000000002",
  );
  assert.equal(
    receiptContext.lastCall().url,
    "https://arena.test/api/v1/me/commerce/contracts/00000000-0000-4000-8000-000000000002",
  );
  assert.equal(receipt.tithe_milli, 100);

  const statementContext = createTestContext({ responder: () => jsonResponse(TRADE_STATEMENT) });
  await createStagedClient(statementContext.core).readTradeStatement();
  assert.equal(statementContext.lastCall().url, "https://arena.test/api/v1/me/commerce/statement");
  assert.equal(statementContext.lastCall().init.cache, "no-store");
});

test("the five dispute operations speak paths, methods and bodies", async () => {
  const fileContext = createTestContext({ responder: () => jsonResponse(CASE_FILE) });
  await createStagedClient(fileContext.core).readPrivateCaseFile("caso-1");
  assert.equal(fileContext.lastCall().url, "https://arena.test/api/v1/me/disputes/cases/caso-1");
  assert.equal(fileContext.lastCall().init.method, "GET");

  const acceptContext = createTestContext({ responder: () => jsonResponse(CASE_FILE) });
  await createStagedClient(acceptContext.core).acceptPrivateCaseTerms("caso-1");
  assert.equal(acceptContext.lastCall().url, "https://arena.test/api/v1/me/disputes/cases/caso-1/accepts");
  assert.equal(acceptContext.lastCall().init.method, "POST");
  assert.equal(acceptContext.lastCall().init.body, undefined);
  assert.equal(headerOf(acceptContext.lastCall(), "idempotency-key"), null);

  const defenseContext = createTestContext({ responder: () => jsonResponse(CASE_FILE) });
  await createStagedClient(defenseContext.core).filePrivateCaseDefense("caso-1", { digest: "resumo-1" });
  assert.equal(defenseContext.lastCall().url, "https://arena.test/api/v1/me/disputes/cases/caso-1/defenses");
  assert.deepEqual(bodyOf(defenseContext.lastCall()), { digest: "resumo-1" });
  assert.equal(headerOf(defenseContext.lastCall(), "idempotency-key"), null);

  const rulingContext = createTestContext({ responder: () => jsonResponse(RULING) });
  const ruling = await createStagedClient(rulingContext.core).readPrivateCaseRuling("caso-1");
  assert.equal(rulingContext.lastCall().url, "https://arena.test/api/v1/me/disputes/cases/caso-1/ruling");
  assert.equal(ruling.verdict, "procedente");

  const appealContext = createTestContext({ responder: () => jsonResponse(CASE_FILE) });
  await createStagedClient(appealContext.core).appealPrivateCaseRuling("caso-1", { reason: "recurso honesto" });
  assert.equal(appealContext.lastCall().url, "https://arena.test/api/v1/me/disputes/cases/caso-1/appeals");
  assert.deepEqual(bodyOf(appealContext.lastCall()), { reason: "recurso honesto" });
  assert.equal(headerOf(appealContext.lastCall(), "idempotency-key"), null);
});

test("a stranger reads exactly like a missing case and a lost appeal never replays", async () => {
  const stranger = createTestContext({ responder: () => problemResponse(404, "unknown_case") });
  const strangerFailure = await captureApiError(() =>
    createStagedClient(stranger.core).readPrivateCaseFile("caso-alheio"),
  );
  assert.equal(strangerFailure.code, "unknown_case");
  assert.equal(strangerFailure.kind, "not_found");

  const lost = createTestContext({ responder: () => problemResponse(503, "unavailable") });
  const lostFailure = await captureApiError(() =>
    createStagedClient(lost.core).appealPrivateCaseRuling("caso-1", { reason: "recurso honesto" }),
  );
  assert.equal(lost.calls.length, 1);
  assert.equal(lostFailure.retryable, false);
});

test("the staged clients fail closed without a session and never touch fetch directly", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "unauthorized") });

  const failure = await captureApiError(() => createStagedClient(context.core).readTradeStatement());

  assert.equal(failure.code, "unauthorized");
  assert.equal(failure.kind, "unauthorized");
  assert.equal(context.unauthorized.length, 1);

  for (const file of ["src/core/clients/staged.ts", "src/core/staged.ts", "src/pages/staged.ts"]) {
    const source = readPackageFile(file)
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .replace(/(^|\s)\/\/.*$/gm, "$1");
    assert.ok(!/\bfetch\s*\(/.test(source), `${file} must go through the HTTP core`);
    assert.ok(!source.includes("localStorage"), `${file} must never reach localStorage`);
  }
});
