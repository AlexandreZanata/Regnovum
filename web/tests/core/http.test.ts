/**
 * Tests of the native HTTP core (P18-T03; lifecycle P50-T03) against a fake
 * transport: deterministic 401/403/409/422/429/5xx, empty/illegible bodies,
 * timeout/abort, out-of-order navigation, Problem Details, deadlines and the
 * strict mutation rule — an unsafe request retries only with an explicit
 * idempotency key for a backend-proven contract; the core never invents one.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import type { HttpMethod } from "../../src/core/http.js";
import {
  bodyOf,
  captureApiError,
  captureThrown,
  createTestContext,
  headerOf,
  hangingResponse,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

test("composes the absolute URL from base, path and defined query values", async () => {
  const context = createTestContext({
    responder: () => jsonResponse({ ok: true }),
    baseUrl: "https://arena.test/",
  });

  await context.core.request({
    method: "GET",
    path: "/api/v1/arenas",
    query: { language: "pt-BR", limit: 20, cursor: undefined, status: null, empty: "" },
  });

  assert.equal(context.lastCall().url, "https://arena.test/api/v1/arenas?language=pt-BR&limit=20");
});

test("rejects relative paths and unknown methods instead of guessing", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ ok: true }) });

  const relative = await captureThrown(() => context.core.request({ method: "GET", path: "api/v1/arenas" }));
  assert.equal(relative instanceof TypeError, true);

  const unknown = await captureThrown(() =>
    context.core.request({ method: "TRACE" as unknown as HttpMethod, path: "/api/v1/arenas" }),
  );
  assert.equal(unknown instanceof TypeError, true);
  assert.equal(context.calls.length, 0);
});

test("sends Accept, request id and CSRF only where the contract requires them", async () => {
  const context = createTestContext({
    responder: () => jsonResponse({ ok: true }),
    cookies: { arena_csrf: "csrf-token" },
  });

  await context.core.request({ method: "GET", path: "/api/v1/arenas" });
  const read = context.lastCall();
  assert.equal(headerOf(read, "accept"), "application/json");
  assert.equal(headerOf(read, "x-request-id"), "req-1");
  assert.equal(headerOf(read, "x-csrf-token"), null);
  assert.equal(headerOf(read, "idempotency-key"), null);

  await context.core.request({ method: "POST", path: "/api/v1/auth/login", body: { email: "a@b.test", password: "x" } });
  const write = context.lastCall();
  assert.equal(headerOf(write, "x-request-id"), "req-2");
  assert.equal(headerOf(write, "x-csrf-token"), "csrf-token");
  assert.equal(headerOf(write, "content-type"), "application/json");
  assert.equal(headerOf(write, "idempotency-key"), null, "login is not retried, so no key is invented");
  assert.deepEqual(bodyOf(write), { email: "a@b.test", password: "x" });
});

test("keeps the browser HTTP cache for public reads and no-store for account data", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ ok: true }) });

  await context.core.request({ method: "GET", path: "/api/v1/arenas" });
  assert.equal(context.lastCall().init.cache, "default");

  await context.core.request({ method: "GET", path: "/api/v1/me/profile" });
  assert.equal(context.lastCall().init.cache, "no-store");
  await context.core.request({ method: "GET", path: "/api/v1/me/sessions" });
  assert.equal(context.lastCall().init.cache, "no-store");
  await context.core.request({ method: "POST", path: "/api/v1/auth/login", body: {} });
  assert.equal(context.lastCall().init.cache, "no-store");
  await context.core.request({ method: "GET", path: "/api/v1/auth/verify", query: { token: "t" } });
  assert.equal(context.lastCall().init.cache, "no-store");
  assert.equal(context.lastCall().init.credentials, "same-origin");
  assert.equal(context.lastCall().init.redirect, "error");
});

test("returns the decoded body and treats empty responses as no content", async () => {
  const context = createTestContext({
    responder: (_call, index) => (index === 0 ? jsonResponse({ id: "arena-1" }) : new Response(null, { status: 204 })),
  });

  const payload = await context.core.request<{ readonly id: string }>({ method: "GET", path: "/api/v1/arenas/a" });
  assert.deepEqual(payload, { id: "arena-1" });

  const empty = await context.core.request<undefined>({ method: "POST", path: "/api/v1/auth/logout", retry: false });
  assert.equal(empty, undefined);

  const blank = createTestContext({ responder: () => new Response("   ", { status: 200 }) });
  assert.equal(await blank.core.request<undefined>({ method: "GET", path: "/api/v1/arenas" }), undefined);
});

test("reports a success body that is not JSON as invalid_response", async () => {
  const context = createTestContext({
    responder: () => new Response("<html>ok</html>", { status: 200, headers: { "content-type": "application/json" } }),
  });

  const failure = await captureApiError(() => context.core.request({ method: "GET", path: "/api/v1/arenas" }));
  assert.equal(failure.code, "invalid_response");
  assert.equal(failure.kind, null);
  assert.equal(failure.status, 200);
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
});

test("decodes Problem Details, prefers the server request id and flags the session", async () => {
  const context = createTestContext({
    responder: () =>
      new Response(JSON.stringify({ type: "about:blank", title: "unauthorized", status: 401, code: "session_expired", request_id: "req-from-problem" }), {
        status: 401,
        headers: { "content-type": "application/problem+json" },
      }),
  });

  const failure = await captureApiError(() => context.core.request({ method: "GET", path: "/api/v1/me/profile" }));
  assert.equal(failure.code, "session_expired");
  assert.equal(failure.kind, "unauthorized");
  assert.equal(failure.status, 401);
  assert.equal(failure.requestId, "req-from-problem");
  assert.equal(failure.problem?.title, "unauthorized");
  assert.equal(failure.retryable, false);
  assert.equal(failure.attempts, 1);
  assert.equal(context.calls.length, 1);
  assert.equal(context.unauthorized.length, 1);
  assert.equal(context.unauthorized[0]?.path, "/api/v1/me/profile");
});

test("keeps wrong-credential 401 out of the session-expiry path", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "invalid_credentials") });

  const failure = await captureApiError(() =>
    context.core.request({ method: "POST", path: "/api/v1/auth/login", body: {}, tolerateUnauthorized: true }),
  );
  assert.equal(failure.code, "invalid_credentials");
  assert.equal(failure.kind, "unauthorized");
  assert.equal(context.unauthorized.length, 0);
});

test("maps 409 to conflict and never retries it", async () => {
  const context = createTestContext({
    responder: () => problemResponse(409, "position_already_confirmed"),
  });

  const failure = await captureApiError(() =>
    context.core.request({ method: "POST", path: "/api/v1/me/arenas/a/position", body: { position: "agree" }, retry: { maxAttempts: 3 } }),
  );
  assert.equal(failure.code, "position_already_confirmed");
  assert.equal(failure.kind, "conflict");
  assert.equal(failure.status, 409);
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
  assert.deepEqual(context.delays, []);
});

test("retries a 429 after Retry-After for a safe request", async () => {
  const context = createTestContext({
    retry: { maxAttempts: 3, baseDelayMs: 100, maxDelayMs: 2_000 },
    responder: (_call, index) =>
      index === 0 ? problemResponse(429, "rate_limited", { "retry-after": "1" }) : jsonResponse({ items: [] }),
  });

  const payload = await context.core.request<{ readonly items: readonly unknown[] }>({ method: "GET", path: "/api/v1/arenas" });
  assert.deepEqual(payload, { items: [] });
  assert.equal(context.calls.length, 2);
  assert.deepEqual(context.delays, [1_000]);
});

test("does not retry earlier than the server asked when that exceeds the ceiling", async () => {
  const context = createTestContext({
    retry: { maxAttempts: 3, baseDelayMs: 10, maxDelayMs: 2_000 },
    responder: () => problemResponse(429, "rate_limited", { "retry-after": "60" }),
  });

  const failure = await captureApiError(() => context.core.request({ method: "GET", path: "/api/v1/arenas" }));
  assert.equal(failure.code, "rate_limited");
  assert.equal(failure.kind, "rate_limited");
  assert.equal(failure.retryAfterSeconds, 60);
  assert.equal(context.calls.length, 1);
  assert.deepEqual(context.delays, []);
});

test("uses exponential backoff with the injected jitter for transient failures", async () => {
  const context = createTestContext({
    retry: { maxAttempts: 4, baseDelayMs: 100, maxDelayMs: 2_000 },
    random: () => 0,
    responder: (_call, index) => (index < 3 ? problemResponse(503, "unavailable") : jsonResponse({ ok: true })),
  });

  await context.core.request({ method: "GET", path: "/api/v1/arenas" });
  assert.equal(context.calls.length, 4);
  // random() === 0 keeps the lower half of the jitter window: 50/100/200.
  assert.deepEqual(context.delays, [50, 100, 200]);
});

test("retries a network failure only when the request is safe", async () => {
  const safe = createTestContext({
    retry: { maxAttempts: 2, baseDelayMs: 100, maxDelayMs: 1_000 },
    responder: (_call, index) => {
      if (index === 0) {
        throw new TypeError("network down");
      }
      return jsonResponse({ ok: true });
    },
  });
  await safe.core.request({ method: "GET", path: "/api/v1/arenas" });
  assert.equal(safe.calls.length, 2);
  assert.deepEqual(safe.delays, [100]);

  const unsafe = createTestContext({
    retry: { maxAttempts: 2, baseDelayMs: 100, maxDelayMs: 1_000 },
    responder: () => {
      throw new TypeError("network down");
    },
  });
  const failure = await captureApiError(() => unsafe.core.request({ method: "POST", path: "/api/v1/auth/login", body: {} }));
  assert.equal(failure.code, "network_error");
  assert.equal(failure.status, null);
  assert.equal(failure.retryable, false);
  assert.equal(unsafe.calls.length, 1);
  assert.deepEqual(unsafe.delays, []);
});

test("never retries a mutation without an explicit key and never invents one", async () => {
  const context = createTestContext({
    responder: () => problemResponse(503, "unavailable"),
  });

  const failure = await captureApiError(() =>
    context.core.request({ method: "POST", path: "/api/v1/me/arenas/x/arguments", body: { content: "c" }, retry: { maxAttempts: 3 } }),
  );
  assert.equal(failure.code, "unavailable");
  assert.equal(failure.retryable, false);
  assert.equal(failure.attempts, 1);
  assert.equal(context.calls.length, 1);
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.deepEqual(context.delays, []);
});

test("retries a mutation only with an explicit key and replays the same key", async () => {
  const context = createTestContext({
    responder: (_call, index) => (index === 0 ? problemResponse(503, "unavailable") : jsonResponse({ argument: { id: "a1" } })),
  });

  await context.core.request({
    method: "POST",
    path: "/api/v1/me/arenas/x/arguments",
    body: { content: "c" },
    retry: { maxAttempts: 2 },
    idempotencyKey: "arg-1",
  });
  assert.equal(context.calls.length, 2);
  assert.equal(headerOf(context.calls[0]!, "idempotency-key"), "arg-1");
  assert.equal(headerOf(context.calls[1]!, "idempotency-key"), "arg-1", "a retry must replay the same key");
});

test("maps 403/422 to their kinds and never retries them", async () => {
  for (const [status, code, kind] of [[403, "forbidden_action", "forbidden"], [422, "invalid_content", "validation"]] as const) {
    const context = createTestContext({ responder: () => problemResponse(status, code) });
    const failure = await captureApiError(() =>
      context.core.request({ method: "POST", path: "/api/v1/me/arenas/x/arguments", body: {}, retry: { maxAttempts: 3 }, idempotencyKey: "k" }),
    );
    assert.equal(failure.code, code);
    assert.equal(failure.kind, kind);
    assert.equal(failure.status, status);
    assert.equal(failure.retryable, false);
    assert.equal(context.calls.length, 1);
    assert.deepEqual(context.delays, []);
  }
});

test("never retries a 500 and keeps retrying only 502/503/504/429", async () => {
  const fatal = createTestContext({ responder: () => problemResponse(500, "broken") });
  const fatalFailure = await captureApiError(() => fatal.core.request({ method: "GET", path: "/api/v1/arenas" }));
  assert.equal(fatalFailure.code, "broken");
  assert.equal(fatalFailure.kind, "internal");
  assert.equal(fatalFailure.retryable, false);
  assert.equal(fatal.calls.length, 1);

  const transient = createTestContext({
    responder: (_call, index) => (index === 0 ? problemResponse(502, "bad_gateway") : jsonResponse({ ok: true })),
  });
  await transient.core.request({ method: "GET", path: "/api/v1/arenas" });
  assert.equal(transient.calls.length, 2);
});

test("a cancelled navigation never replaces the newer answer", async () => {
  const previous = new AbortController();
  const context = createTestContext({
    responder: (call, index) => {
      // The core wraps the caller signal in a per-attempt deadline, so the
      // stale call is recognized by order, not by signal identity.
      if (index === 0) {
        return hangingResponse(call);
      }
      return jsonResponse({ id: "newest" });
    },
  });

  const stale = captureApiError(() => context.core.request({ method: "GET", path: "/api/v1/arenas", signal: previous.signal }));
  const newest = await context.core.request<{ readonly id: string }>({ method: "GET", path: "/api/v1/arenas" });
  previous.abort();
  const staleFailure = await stale;

  assert.equal(newest.id, "newest");
  assert.equal(staleFailure.code, "aborted");
  assert.equal(staleFailure.retryable, false);
  assert.equal(context.calls.length, 2);
});

test("honours an explicit idempotency key and surfaces the replay header", async () => {
  const context = createTestContext({
    responder: () =>
      jsonResponse({ argument: { id: "a1" } }, 200, { "idempotency-replayed": "true", "x-request-id": "server-7" }),
  });

  await context.core.request({
    method: "POST",
    path: "/api/v1/me/arenas/x/arguments",
    body: { content: "c" },
    idempotencyKey: "visit-key",
  });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), "visit-key");
  assert.equal(context.observations.length, 1);
  assert.equal(context.observations[0]?.idempotencyReplayed, true);
  assert.equal(context.observations[0]?.requestId, "server-7");
});

test("classifies the per-attempt timeout without retrying", async () => {
  const context = createTestContext({
    timeoutMs: 5,
    retry: { maxAttempts: 3 },
    responder: (call) => hangingResponse(call),
  });

  const failure = await captureApiError(() => context.core.request({ method: "GET", path: "/api/v1/arenas" }));
  assert.equal(failure.code, "timeout");
  assert.equal(failure.aborted, true);
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
  assert.deepEqual(context.delays, []);
});

test("classifies a caller abort as aborted and stops the request", async () => {
  const controller = new AbortController();
  const context = createTestContext({
    retry: { maxAttempts: 3 },
    responder: (call) => hangingResponse(call),
  });

  const pending = captureApiError(() => context.core.request({ method: "GET", path: "/api/v1/arenas", signal: controller.signal }));
  controller.abort();
  const failure = await pending;

  assert.equal(failure.code, "aborted");
  assert.equal(failure.aborted, true);
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
});

test("reports cancellation that happens during the backoff", async () => {
  const controller = new AbortController();
  const context = createTestContext({
    retry: { maxAttempts: 3, baseDelayMs: 50, maxDelayMs: 500 },
    responder: () => problemResponse(503, "unavailable"),
    sleep: () => {
      controller.abort();
      return Promise.reject(new DOMException("Aborted", "AbortError"));
    },
  });

  const failure = await captureApiError(() => context.core.request({ method: "GET", path: "/api/v1/arenas", signal: controller.signal }));
  assert.equal(failure.code, "aborted");
  assert.equal(failure.attempts, 1);
  assert.equal(context.calls.length, 1);
});

test("observes every attempt without query values, bodies or credentials", async () => {
  const context = createTestContext({
    retry: { maxAttempts: 2, baseDelayMs: 10, maxDelayMs: 100 },
    responder: (_call, index) => (index === 0 ? problemResponse(502, "bad_gateway") : jsonResponse({ ok: true })),
  });

  await context.core.request({ method: "GET", path: "/api/v1/search/arenas", query: { q: "private words" } });

  assert.equal(context.observations.length, 2);
  const [first, second] = context.observations;
  assert.deepEqual(Object.keys(first!).sort(), [
    "attempt",
    "code",
    "durationMs",
    "idempotencyReplayed",
    "method",
    "path",
    "requestId",
    "status",
  ]);
  assert.equal(first?.status, 502);
  assert.equal(first?.code, "bad_gateway");
  assert.equal(first?.attempt, 1);
  assert.equal(second?.status, 200);
  assert.equal(second?.code, null);
  assert.equal(second?.attempt, 2);
  for (const event of context.observations) {
    assert.equal(event.path, "/api/v1/search/arenas", "the query string never reaches the observer");
    assert.equal(JSON.stringify(event).includes("private words"), false);
  }
});

test("reports a non-Problem error body as unexpected_response and keeps retrying 5xx", async () => {
  const context = createTestContext({
    retry: { maxAttempts: 2, baseDelayMs: 5, maxDelayMs: 50 },
    responder: () => new Response("<html>gateway</html>", { status: 502, headers: { "content-type": "text/html" } }),
  });

  const failure = await captureApiError(() => context.core.request({ method: "GET", path: "/api/v1/arenas" }));
  assert.equal(failure.code, "unexpected_response");
  assert.equal(failure.kind, "internal");
  assert.equal(failure.retryable, true);
  assert.equal(failure.attempts, 2);
  assert.equal(context.calls.length, 2);
});

test("reports a problem+json body that is not a Problem as invalid_response", async () => {
  const context = createTestContext({
    responder: () => new Response("not json", { status: 400, headers: { "content-type": "application/problem+json" } }),
  });

  const failure = await captureApiError(() => context.core.request({ method: "GET", path: "/api/v1/arenas" }));
  assert.equal(failure.code, "invalid_response");
  assert.equal(failure.kind, "validation");
  assert.equal(failure.problem, null);
});

test("clamps hostile retry tuning instead of trusting it", async () => {
  const context = createTestContext({
    retry: { maxAttempts: 99, baseDelayMs: -5, maxDelayMs: -1 },
    responder: () => problemResponse(503, "unavailable"),
  });

  const failure = await captureApiError(() => context.core.request({ method: "GET", path: "/api/v1/arenas" }));
  assert.equal(failure.attempts, 5, "attempts stay within the documented ceiling");
  assert.equal(context.calls.length, 5);
  assert.deepEqual(context.delays, [0, 0, 0, 0]);
});
