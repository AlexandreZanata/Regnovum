/**
 * Tests of the argument reads (P53-T02) against a fake transport:
 * the list speaks one relation newest-first through the public
 * cache, the single read resolves a withdrawn argument as a
 * retracted placeholder while a removed one reads as not found, and
 * the full-text search carries the query verbatim for the server to
 * judge. Every read takes a caller signal so a page turn or a new
 * search cancels the request it replaces; an aborted answer is
 * discarded, never rendered.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createArgumentsClient } from "../../src/core/clients/arguments.js";
import {
  captureApiError,
  createTestContext,
  hangingResponse,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const ITEM = {
  arena_id: "arena-1",
  content: "Because responsibility presupposes consciousness.",
  created_at: "2026-10-02T11:00:00Z",
  id: "arg-1",
  parent_id: null,
  relation: "support",
  reply_count: 2,
  status: "published",
} as const;

test("the list speaks one relation newest-first through the public cache", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ items: [ITEM], next_cursor: "c1" }) });

  const page = await createArgumentsClient(context.core).list("arena-1", { relation: "support" });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/arenas/arena-1/arguments?relation=support");
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(context.lastCall().init.cache, "default");
  assert.equal(page.items.length, 1);
  assert.equal(page.next_cursor, "c1");
});

test("a relation outside the vocabulary fails as validation", async () => {
  const context = createTestContext({ responder: () => problemResponse(400, "argument_invalid_relation") });

  const failure = await captureApiError(() =>
    createArgumentsClient(context.core).list("arena-1", { relation: "talvez" }),
  );

  assert.equal(failure.code, "argument_invalid_relation");
  assert.equal(failure.kind, "validation");
  assert.equal(context.calls.length, 1);
});

test("a forged cursor fails as validation", async () => {
  const context = createTestContext({ responder: () => problemResponse(400, "invalid_cursor") });

  const failure = await captureApiError(() =>
    createArgumentsClient(context.core).list("arena-1", { relation: "support", cursor: "forged" }),
  );

  assert.equal(failure.code, "invalid_cursor");
  assert.equal(failure.kind, "validation");
});

test("the single read resolves a withdrawn argument as a placeholder", async () => {
  const withdrawn = { ...ITEM, content: null, status: "withdrawn" };
  const context = createTestContext({ responder: () => jsonResponse(withdrawn) });

  const argument = await createArgumentsClient(context.core).get("arg-1");

  assert.equal(context.lastCall().url, "https://arena.test/api/v1/arguments/arg-1");
  assert.equal(argument.status, "withdrawn");
  assert.equal(argument.content, null, "the content is withheld while the identity stays visible");
});

test("a removed argument reads as not found", async () => {
  const context = createTestContext({ responder: () => problemResponse(404, "argument_not_found") });

  const failure = await captureApiError(() => createArgumentsClient(context.core).get("removed"));

  assert.equal(failure.code, "argument_not_found");
  assert.equal(failure.kind, "not_found");
});

test("the search carries the query verbatim for the server to judge", async () => {
  const context = createTestContext({
    responder: () =>
      jsonResponse({
        items: [
          {
            arena_id: "arena-1",
            content: "Because responsibility presupposes consciência.",
            created_at: "2026-10-02T11:00:00Z",
            id: "arg-1",
            language: "pt-BR",
            relation: "support",
            score: 0.9,
          },
        ],
        next_cursor: null,
      }),
  });

  const page = await createArgumentsClient(context.core).search({ q: "consciência", language: "pt-BR" });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/search/arguments?q=consci%C3%AAncia&language=pt-BR",
  );
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(context.lastCall().init.cache, "default");
  assert.equal(page.items.length, 1);
});

test("an invalid search fails without shaping anything", async () => {
  const context = createTestContext({ responder: () => problemResponse(400, "invalid_query") });

  const failure = await captureApiError(() => createArgumentsClient(context.core).search({ q: "x" }));

  assert.equal(failure.code, "invalid_query");
  assert.equal(failure.kind, "validation");
  assert.equal(context.calls.length, 1);
});

test("a cancelled page turn aborts instead of rendering late", async () => {
  const controller = new AbortController();
  const context = createTestContext({ responder: (call) => hangingResponse(call) });

  const pending = captureApiError(() =>
    createArgumentsClient(context.core).list("arena-1", { relation: "support" }, controller.signal),
  );
  controller.abort();
  const failure = await pending;

  assert.equal(failure.code, "aborted");
  assert.equal(failure.aborted, true, "an aborted answer is discarded, never rendered");
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
});

test("reads send no body and no key", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ items: [], next_cursor: null }) });

  await createArgumentsClient(context.core).list("arena-1", { relation: "oppose", limit: 10 });

  assert.equal(context.lastCall().init.body, undefined);
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/arenas/arena-1/arguments?relation=oppose&limit=10",
  );
});
