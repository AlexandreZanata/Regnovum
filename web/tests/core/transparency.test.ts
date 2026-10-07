/**
 * Tests of the public transparency client (P55-T04) against a
 * fake transport: the metrics read the versioned suppressed
 * counts of one window — anonymous, cacheable, with no email
 * and no account-level position anywhere. The browser cache
 * with its ETag does the revalidation; no validator is
 * assembled by hand. An incoherent window fails as
 * validation.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createTransparencyClient } from "../../src/core/clients/transparency.js";
import { captureApiError, createTestContext, jsonResponse, problemResponse } from "../support/harness.js";

const METRICS = {
  methodology_version: 2,
  period_start: "2026-09-05T00:00:00Z",
  period_end: "2026-10-05T00:00:00Z",
  timezone: "UTC",
  updated_at: "2026-10-05T10:00:00Z",
  metrics: {
    eligible_accounts: 120,
    arenas_published: 9,
    arenas_closed: 1,
    arenas_restricted: 0,
    arenas_removed: 0,
    arguments_published: 40,
    arguments_withdrawn: 2,
    position_changes: 15,
    attributions_valid: 30,
    attributions_invalidated: 1,
    influenced_authors: 11,
    ink_free_granted: 100000,
    ink_free_expired: 5000,
    ink_free_consumed: 20000,
    ink_purchased_granted: 50000,
    ink_purchased_consumed: 8000,
    ink_refunded: 0,
    ink_admin_adjusted: 0,
    passes_purchase_granted: 12,
    passes_member_granted: 3,
    passes_consumed: 9,
    reports_filed: 4,
    actions_recorded: 1,
    appeals_filed: 1,
    appeals_reversed: 0,
  },
} as const;

test("the metrics read anonymously through the browser cache", async () => {
  const context = createTestContext({ responder: () => jsonResponse(METRICS) });

  const document = await createTransparencyClient(context.core).metrics();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/public/transparency");
  assert.equal(document.methodology_version, 2);
  assert.equal(document.metrics.reports_filed, 4);
  assert.equal(context.lastCall().init.cache, "default");
  assert.equal(context.unauthorized.length, 0, "a public read never expires a session");
  const serialized = JSON.stringify(document).toLowerCase();
  for (const marker of ["email", "stripe", "\"ip\"", "account_id"]) {
    assert.ok(!serialized.includes(marker), `private marker leaked: ${marker}`);
  }
});

test("the window travels verbatim with the echoed timezone", async () => {
  const context = createTestContext({ responder: () => jsonResponse(METRICS) });

  await createTransparencyClient(context.core).metrics({
    period_start: "2026-09-05T00:00:00Z",
    period_end: "2026-10-05T00:00:00Z",
    timezone: "America/Sao_Paulo",
  });

  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/public/transparency?period_start=2026-09-05T00%3A00%3A00Z&period_end=2026-10-05T00%3A00%3A00Z&timezone=America%2FSao_Paulo",
  );
});

test("an incoherent window fails as validation without a document", async () => {
  const context = createTestContext({ responder: () => problemResponse(400, "invalid_window") });

  const failure = await captureApiError(() =>
    createTransparencyClient(context.core).metrics({ period_start: "not-an-instant" }),
  );

  assert.equal(failure.code, "invalid_window");
  assert.equal(failure.kind, "validation");
  assert.equal(context.calls.length, 1);
});
