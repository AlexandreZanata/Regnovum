/**
 * Tests of the metering staged client (P57-T02) against a fake transport.
 *
 * The four metering operations speak the paths, methods and bodies their
 * fragment declares, and nothing else: the preview prices without charging,
 * the confirmation settles with the exact charge under the intention key
 * the fragment declares in the body, and the receipt and the statement read
 * back what the server stored. Mutations are single-shot with no
 * `Idempotency-Key` header — no metering POST contract declares one — and
 * every call leaves with the account no-store policy the core already owns
 * for `/api/v1/me/*`. A repeated key replays the original settlement; the
 * same key over different terms answers 409.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createMeteringClient } from "../../src/core/clients/metering.js";
import {
  bodyOf,
  captureApiError,
  createTestContext,
  hangingResponse,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const QUOTE = {
  accepted_at: "2026-10-05T10:00:00Z",
  content_hash: "hash-conteudo",
  expires_at: "2026-10-05T10:05:00Z",
  price_milli: 250,
  quote_hash: "hash-cotacao",
  title: "Preço de medição de INK",
  total_milli: 2750,
  units: 11,
  version: 2,
} as const;

const PUBLICATION = {
  posted_at: "2026-10-05T10:01:00Z",
  publication_id: "00000000-0000-4000-8000-000000000001",
  replayed: false,
  title: "Recibo de INK",
  total_milli: 2750,
  transfer_id: "00000000-0000-4000-8000-000000000002",
} as const;

const RECEIPT = {
  content_hash: "hash-conteudo",
  legs: [],
  posted_at: "2026-10-05T10:01:00Z",
  publication_id: "00000000-0000-4000-8000-000000000001",
  service: "argument-publish",
  title: "Recibo de INK",
  total_milli: 2750,
  transfer_id: "00000000-0000-4000-8000-000000000002",
  units: 11,
  version: 2,
} as const;

const STATEMENT = {
  balance_milli: 997250,
  entries: [],
  title: "Extrato de INK",
} as const;

test("the preview prices without charging under one shot", async () => {
  const context = createTestContext({ responder: () => jsonResponse(QUOTE) });

  const quote = await createMeteringClient(context.core).previewMeteringQuote({
    content: "texto final",
    service: "argument-publish",
  });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/metering/quotes");
  assert.deepEqual(bodyOf(context.lastCall()), { content: "texto final", service: "argument-publish" });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(context.lastCall().init.cache, "no-store");
  assert.equal(quote.total_milli, 2750);
});

test("the confirmation settles the exact charge under the body key", async () => {
  const context = createTestContext({ responder: () => jsonResponse(PUBLICATION) });

  const publication = await createMeteringClient(context.core).confirmMeteringPublication({
    intention_key: "intencao-1",
    content: "texto final",
    service: "argument-publish",
  });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/metering/publications");
  assert.deepEqual(bodyOf(context.lastCall()), {
    intention_key: "intencao-1",
    content: "texto final",
    service: "argument-publish",
  });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(context.lastCall().init.cache, "no-store");
  assert.equal(publication.replayed, false);
});

test("a repeated key replays the original settlement without a second charge", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ ...PUBLICATION, replayed: true }) });

  const publication = await createMeteringClient(context.core).confirmMeteringPublication({
    intention_key: "intencao-1",
    content: "texto final",
    service: "argument-publish",
  });

  assert.equal(publication.replayed, true);
  assert.equal(publication.total_milli, 2750);
  assert.equal(context.calls.length, 1, "the replay is the server's answer, never a second attempt");
});

test("one receipt reads back with its legs", async () => {
  const context = createTestContext({ responder: () => jsonResponse(RECEIPT) });

  const receipt = await createMeteringClient(context.core).readMeteringReceipt(
    "00000000-0000-4000-8000-000000000001",
  );

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/me/metering/publications/00000000-0000-4000-8000-000000000001",
  );
  assert.equal(receipt.content_hash, "hash-conteudo");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the statement reads the journal-derived balance", async () => {
  const context = createTestContext({ responder: () => jsonResponse(STATEMENT) });

  const statement = await createMeteringClient(context.core).readMeteringStatement();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/metering/statement");
  assert.equal(statement.balance_milli, 997250);
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("anonymous, mismatched, lapsed, divergent and foreign reads fail distinctly", async () => {
  const failing = [
    { run: "preview", status: 401, code: "unauthorized", kind: "unauthorized" },
    { run: "preview", status: 400, code: "price_unavailable", kind: "validation" },
    { run: "confirm", status: 403, code: "quote_mismatch", kind: "forbidden" },
    { run: "confirm", status: 400, code: "quote_expired", kind: "validation" },
    { run: "confirm", status: 409, code: "publish_conflict", kind: "conflict" },
    { run: "receipt", status: 404, code: "publication_unknown", kind: "not_found" },
  ] as const;
  for (const target of failing) {
    const context = createTestContext({ responder: () => problemResponse(target.status, target.code) });
    const client = createMeteringClient(context.core);
    const failure = await captureApiError(() => {
      switch (target.run) {
        case "preview":
          return client.previewMeteringQuote({ content: "x", service: "argument-publish" });
        case "confirm":
          return client.confirmMeteringPublication({
            intention_key: "k",
            content: "x",
            service: "argument-publish",
          });
        case "receipt":
          return client.readMeteringReceipt("00000000-0000-4000-8000-000000000001");
      }
    });
    assert.equal(failure.code, target.code);
    assert.equal(failure.kind, target.kind);
    assert.equal(context.calls.length, 1, `${target.code}: no automatic second attempt`);
  }
});

test("a cancelled receipt read aborts instead of rendering late", async () => {
  const controller = new AbortController();
  const context = createTestContext({ responder: (call) => hangingResponse(call) });

  const pending = captureApiError(() =>
    createMeteringClient(context.core).readMeteringReceipt(
      "00000000-0000-4000-8000-000000000001",
      controller.signal,
    ),
  );
  controller.abort();
  const failure = await pending;

  assert.equal(failure.code, "aborted");
  assert.equal(failure.aborted, true, "an aborted answer is discarded, never rendered");
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
});
