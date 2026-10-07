/**
 * Tests of the arena export read (P52-T05) against a fake transport:
 * one page of the versioned public document by stable identifier,
 * cursor and page size travelling as asked with the public cache
 * policy. Unknown, draft and removed Arenas read as not found; a
 * forged cursor fails as validation. Only public data ever
 * serializes, so the test fixture carries no individual position,
 * change history or attributor identity — and asserts none appears.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createArenasClient } from "../../src/core/clients/arenas.js";
import type { ArenaExport } from "../../src/contracts/generated.js";
import {
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const EXPORT: ArenaExport = {
  schema_version: 1,
  arena: {
    id: "018f6b2a-0000-7000-8000-000000000001",
    slug: "maquinas-podem-ser-responsaveis-018f6b2a",
    statement: "Máquinas podem ser responsáveis?",
    context: null,
    category: "philosophy",
    language: "pt-BR",
    status: "closed",
    published_at: "2026-10-02T10:00:00Z",
    closes_at: "2026-10-03T10:00:00Z",
  },
  positions: {
    participants_total: 22,
    position_changes: 4,
    initial: { agree: 10, disagree: 8, undecided: 4 },
    current: { agree: 12, disagree: 7, undecided: 3 },
    suppressed: false,
  },
  influence: { valid_attributions: 5, influenced_authors: 3 },
  arguments: {
    items: [
      {
        id: "arg-1",
        relation: "support",
        content: "Porque a responsabilidade pressupõe consciência.",
        created_at: "2026-10-02T11:00:00Z",
        influence: { distinct_people: 2, valid_attributions: 2 },
        parent_id: null,
        sources: [{ url: "https://example.test/ethics" }],
        status: "published",
        withdrawn_at: null,
      },
    ],
    next_cursor: null,
  },
};

test("the export speaks the contract path and keeps the public cache", async () => {
  const context = createTestContext({ responder: () => jsonResponse(EXPORT) });

  const doc = await createArenasClient(context.core).exportById("018f6b2a-0000-7000-8000-000000000001");

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/arenas/018f6b2a-0000-7000-8000-000000000001/export",
  );
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(context.lastCall().init.cache, "default", "the public export revalidates through the HTTP cache");
  assert.equal(doc.schema_version, 1);
  assert.equal(doc.arena.status, "closed");
});

test("the export pages with the cursor and size exactly as asked", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ ...EXPORT, arguments: { items: [], next_cursor: null } }) });

  await createArenasClient(context.core).exportById("arena-1", { cursor: "opaque-cursor", limit: 20 });

  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/arenas/arena-1/export?cursor=opaque-cursor&limit=20",
  );
});

test("an unknown, draft or removed arena reads as not found", async () => {
  const context = createTestContext({ responder: () => problemResponse(404, "arena_not_found") });

  const failure = await captureApiError(() => createArenasClient(context.core).exportById("gone-or-draft"));

  assert.equal(failure.code, "arena_not_found");
  assert.equal(failure.kind, "not_found");
});

test("a forged cursor fails as validation without shaping anything", async () => {
  const context = createTestContext({ responder: () => problemResponse(400, "invalid_cursor") });

  const failure = await captureApiError(() => createArenasClient(context.core).exportById("arena-1", { cursor: "forged" }));

  assert.equal(failure.code, "invalid_cursor");
  assert.equal(failure.kind, "validation");
  assert.equal(context.calls.length, 1);
});

test("the export carries no private data to render", async () => {
  const context = createTestContext({ responder: () => jsonResponse(EXPORT) });

  const doc = await createArenasClient(context.core).exportById("arena-1");

  const serialized = JSON.stringify(doc).toLowerCase();
  for (const marker of ["email", "account_id", "debit", "\"ink\""]) {
    assert.ok(!serialized.includes(marker), `private marker leaked: ${marker}`);
  }
});
