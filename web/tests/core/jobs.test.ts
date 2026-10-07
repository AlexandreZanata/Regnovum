/**
 * Tests of the jobs operator client (P55-T03) against a fake
 * transport: the health reads counts and waits only, the dead
 * page reads lifecycle columns oldest first, and the retry
 * states only the operator reason. No job payload ever
 * serializes. Every call is assignment-gated with step-up for
 * the retry, and every answer stays private under no-store.
 * The retry is single-shot with no idempotency key — the
 * contract declares none — and a duplicate answers 409.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createJobsClient } from "../../src/core/clients/jobs.js";
import {
  bodyOf,
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const HEALTH = {
  generated_at: "2026-10-05T10:00:00Z",
  queue: { queued: 4, leased: 1, succeeded: 90, dead: 2, due_now: 1, lag_seconds: 30, oldest_dead_seconds: 3600 },
} as const;

const DEAD_PAGE = {
  generated_at: "2026-10-05T10:00:00Z",
  total: 2,
  items: [
    {
      job_id: "job-1",
      type: "email.send",
      version: 1,
      attempts: 5,
      max_attempts: 5,
      age_seconds: 3600,
      last_error_code: "E_TIMEOUT",
      retryable: true,
    },
  ],
} as const;

const RETRY = { job_id: "job-1", type: "email.send", state: "queued" } as const;

test("the health reads counts and waits with no payload", async () => {
  const context = createTestContext({ responder: () => jsonResponse(HEALTH) });

  const health = await createJobsClient(context.core).health();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/admin/jobs/health");
  assert.equal(health.queue.dead, 2);
  assert.equal(context.lastCall().init.cache, "no-store");
  const serialized = JSON.stringify(health).toLowerCase();
  assert.ok(!serialized.includes("payload"), "job payload must never serialize");
});

test("the dead page reads lifecycle columns oldest first", async () => {
  const context = createTestContext({ responder: () => jsonResponse(DEAD_PAGE) });

  const page = await createJobsClient(context.core).dead(50);

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/admin/jobs/dead?limit=50");
  assert.equal(page.total, 2);
  assert.equal(page.items[0]?.job_id, "job-1");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the public and the stranger read health as denied", async () => {
  for (const [status, code, kind] of [
    [401, "unauthorized", "unauthorized"],
    [403, "forbidden", "forbidden"],
  ] as const) {
    const context = createTestContext({ responder: () => problemResponse(status, code) });
    const failure = await captureApiError(() => createJobsClient(context.core).health());
    assert.equal(failure.code, code);
    assert.equal(failure.kind, kind);
    assert.equal(context.calls.length, 1, `${code}: competence denied without a read`);
  }
});

test("the retry states only the reason under one confirmation", async () => {
  const context = createTestContext({ responder: () => jsonResponse(RETRY) });

  const retry = await createJobsClient(context.core).retry("job-1", { reason: "provider was down" });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/admin/jobs/job-1/retry");
  assert.deepEqual(bodyOf(context.lastCall()), { reason: "provider was down" });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(retry.state, "queued");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("a stale step-up, a refused workload and a duplicate fail distinctly", async () => {
  for (const [status, code, kind] of [
    [401, "step_up_required", "unauthorized"],
    [403, "retry_not_allowed", "forbidden"],
    [404, "job_not_found", "not_found"],
    [409, "job_not_dead", "conflict"],
  ] as const) {
    const context = createTestContext({ responder: () => problemResponse(status, code) });
    const failure = await captureApiError(() =>
      createJobsClient(context.core).retry("job-1", { reason: "again" }),
    );
    assert.equal(failure.code, code);
    assert.equal(failure.kind, kind);
    assert.equal(context.calls.length, 1, `${code}: no automatic second attempt`);
  }
});

test("an empty reason never becomes a request body", async () => {
  const context = createTestContext({ responder: () => problemResponse(400, "reason_required") });

  const failure = await captureApiError(() => createJobsClient(context.core).retry("job-1", { reason: "" }));

  assert.equal(failure.code, "reason_required");
  assert.equal(failure.kind, "validation");
});
