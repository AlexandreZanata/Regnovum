/**
 * Native HTTP core (P18-T03; lifecycle P50-T03).
 *
 * One implementation of everything docs/FRONTEND.md section 6 requires:
 * base URL and API version, common headers and request ID, CSRF, timeout and
 * cancellation, Problem Details parsing, retry restricted to safe requests or
 * to mutations carrying an explicit idempotency key for a backend-proven
 * idempotent contract, uniform session-expired handling and observation that
 * never carries a private payload.
 *
 * The core never invents idempotence: a header alone does not make a replay
 * safe, so an unsafe request without an explicit `idempotencyKey` is never
 * retried, even when `retry` is requested. Callers cancel a stale request
 * with the `signal` they passed; an aborted request never replaces newer
 * state and is never retried. Domain clients (auth, arenas, positions,
 * arguments) compose this core and expose typed contracts; components never
 * call `fetch` themselves.
 */
import type { Problem } from "../contracts/generated.js";
import {
  ABORTED_ERROR_CODE,
  ApiError,
  INVALID_RESPONSE_CODE,
  NETWORK_ERROR_CODE,
  PROBLEM_MEDIA_TYPE,
  TIMEOUT_ERROR_CODE,
  UNEXPECTED_RESPONSE_CODE,
  errorKindForStatus,
  parseProblemBody,
  parseRetryAfter,
} from "./problem.js";

/** HTTP methods the clients use; anything else is a programming error. */
export type HttpMethod = "GET" | "HEAD" | "POST" | "PUT" | "PATCH" | "DELETE";

/** Scalar query values; `null`, `undefined` and `""` omit the parameter. */
export type QueryValue = string | number | boolean | null | undefined;

/** Tuning of the retry policy; every field is optional. */
export interface RetryOptions {
  /** Total attempts including the first one (1..5, default 3). */
  readonly maxAttempts?: number;
  /** First backoff delay in milliseconds (default 200). */
  readonly baseDelayMs?: number;
  /** Ceiling for a single backoff delay in milliseconds (default 2000). */
  readonly maxDelayMs?: number;
}

/** One request, described by data only: no closures, no ambient state. */
export interface RequestSpec {
  readonly method: HttpMethod;
  readonly path: string;
  readonly query?: Readonly<Record<string, QueryValue>>;
  readonly body?: unknown;
  readonly signal?: AbortSignal;
  /**
   * `false` disables retries; an object tunes them. Safe methods may retry;
   * an unsafe method retries only when `idempotencyKey` names the
   * backend-proven idempotent contract of that operation. Requesting retries
   * without a key never enables a mutation retry and never sends a header.
   */
  readonly retry?: RetryOptions | false;
  /**
   * Explicit `Idempotency-Key`, for replays that must survive a reload.
   * Required for any retry of an unsafe method; the core never invents one.
   */
  readonly idempotencyKey?: string;
  /** Endpoints where 401 is a normal answer (login) never expire the session. */
  readonly tolerateUnauthorized?: boolean;
}

/** Telemetry of one attempt: no query values, no bodies, no credentials. */
export interface RequestObservation {
  readonly method: HttpMethod;
  readonly path: string;
  readonly attempt: number;
  readonly status: number | null;
  readonly durationMs: number;
  readonly requestId: string;
  readonly code: string | null;
  readonly idempotencyReplayed: boolean;
}

/** Injected effects; tests replace them to stay deterministic. */
export interface HttpDependencies {
  readonly fetch: typeof fetch;
  readonly now: () => number;
  readonly sleep: (delayMs: number, signal?: AbortSignal) => Promise<void>;
  readonly random: () => number;
  readonly newRequestId: () => string;
  readonly readCookie: (name: string) => string | null;
}

/** Composition inputs of the core. */
export interface HttpCoreOptions {
  /** Origin prefix; empty means same origin (the default). */
  readonly baseUrl?: string;
  /** Per-attempt timeout in milliseconds (default 10000). */
  readonly timeoutMs?: number;
  /** Default retry policy, overridable per request. */
  readonly retry?: RetryOptions;
  readonly csrfCookieName?: string;
  readonly csrfHeaderName?: string;
  readonly requestIdHeader?: string;
  readonly idempotencyHeader?: string;
  readonly idempotencyReplayHeader?: string;
  /** Called once per request that answers 401 outside `tolerateUnauthorized`. */
  readonly onUnauthorized?: (spec: RequestSpec) => void;
  /** Called once per attempt. */
  readonly observe?: (event: RequestObservation) => void;
  readonly dependencies?: Partial<HttpDependencies>;
}

/** The only service the domain clients depend on. */
export interface HttpCore {
  request<T>(spec: RequestSpec): Promise<T>;
}

const DEFAULT_TIMEOUT_MS = 10_000;
const DEFAULT_RETRY: Required<RetryOptions> = { maxAttempts: 3, baseDelayMs: 200, maxDelayMs: 2_000 };
const MAX_ATTEMPTS_LIMIT = 5;
const SAFE_METHODS: readonly HttpMethod[] = ["GET", "HEAD"];
const KNOWN_METHODS: readonly HttpMethod[] = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"];
const RETRYABLE_STATUSES: readonly number[] = [429, 502, 503, 504];
const SESSION_PATH_PREFIX = "/api/v1/me";
const AUTH_PATH_PREFIX = "/api/v1/auth";
const MODERATION_PATH_PREFIX = "/api/v1/moderation";
const NO_CONTENT_STATUSES: readonly number[] = [204, 205];

/** Resolved retry policy of one request. */
interface ResolvedRetry {
  readonly maxAttempts: number;
  readonly baseDelayMs: number;
  readonly maxDelayMs: number;
  readonly allowed: boolean;
}

/** One attempt either produced a value or a failure. */
type AttemptOutcome<T> =
  | { readonly ok: true; readonly value: T }
  | { readonly ok: false; readonly failure: ApiError };

/** A timeout/abort pair wrapped around the caller signal. */
interface Deadline {
  readonly signal: AbortSignal;
  readonly timedOut: () => boolean;
  readonly aborted: () => boolean;
  readonly dispose: () => void;
}

/** Everything one attempt needs, gathered once per request. */
interface AttemptInput {
  readonly spec: RequestSpec;
  readonly method: HttpMethod;
  readonly url: string;
  readonly retry: ResolvedRetry;
  readonly idempotencyKey: string | null;
  readonly attempt: number;
}

/** Context of a non-2xx response, used to build the failure. */
interface ResponseContext {
  readonly attempt: number;
  readonly retryAllowed: boolean;
  readonly fallbackRequestId: string;
  readonly startedAt: number;
}

/** Context of a rejected `fetch`, used to build the failure. */
interface TransportContext {
  readonly attempt: number;
  readonly retryAllowed: boolean;
  readonly requestId: string;
  readonly deadline: Deadline;
}

/**
 * createHttpCore binds configuration and effects once; every domain client
 * shares the same instance.
 */
export function createHttpCore(options: HttpCoreOptions = {}): HttpCore {
  const config = {
    baseUrl: options.baseUrl ?? "",
    timeoutMs: options.timeoutMs ?? DEFAULT_TIMEOUT_MS,
    csrfCookieName: options.csrfCookieName ?? "arena_csrf",
    csrfHeaderName: options.csrfHeaderName ?? "X-CSRF-Token",
    requestIdHeader: options.requestIdHeader ?? "X-Request-Id",
    idempotencyHeader: options.idempotencyHeader ?? "Idempotency-Key",
    idempotencyReplayHeader: options.idempotencyReplayHeader ?? "Idempotency-Replayed",
  };
  const defaults = resolveDefaults(options.retry);
  const observe = options.observe ?? ((): void => undefined);
  const deps: HttpDependencies = {
    fetch: options.dependencies?.fetch ?? ((input, init) => globalThis.fetch(input, init)),
    now: options.dependencies?.now ?? (() => Date.now()),
    sleep: options.dependencies?.sleep ?? defaultSleep,
    random: options.dependencies?.random ?? (() => Math.random()),
    newRequestId: options.dependencies?.newRequestId ?? (() => globalThis.crypto.randomUUID()),
    readCookie: options.dependencies?.readCookie ?? defaultReadCookie,
  };

  /** Performs one response-driven attempt; it never rejects. */
  async function performAttempt<T>(input: AttemptInput): Promise<AttemptOutcome<T>> {
    const { spec, method, url, retry, idempotencyKey, attempt } = input;
    const requestId = deps.newRequestId();
    const startedAt = deps.now();
    const deadline = createDeadline(config.timeoutMs, spec.signal);
    try {
      const headers = new Headers();
      headers.set("Accept", "application/json");
      headers.set(config.requestIdHeader, requestId);
      if (isUnsafeMethod(method)) {
        if (idempotencyKey !== null) {
          headers.set(config.idempotencyHeader, idempotencyKey);
        }
        // Double-submit CSRF: the token is read from a cookie that a
        // cross-site request cannot set. The backend stays the authority.
        const csrfToken = deps.readCookie(config.csrfCookieName);
        if (csrfToken !== null && csrfToken !== "") {
          headers.set(config.csrfHeaderName, csrfToken);
        }
      }
      const init: RequestInit = {
        method,
        headers,
        credentials: "same-origin",
        redirect: "error",
        cache: cacheModeFor(spec.path),
        signal: deadline.signal,
      };
      if (spec.body !== undefined) {
        headers.set("Content-Type", "application/json");
        init.body = JSON.stringify(spec.body);
      }

      const response = await deps.fetch(url, init);
      const serverRequestId = response.headers.get(config.requestIdHeader) ?? requestId;
      const replayed = response.headers.get(config.idempotencyReplayHeader) === "true";

      if (response.ok) {
        const body = await response.text();
        if (NO_CONTENT_STATUSES.includes(response.status) || body.trim() === "") {
          observe({ method, path: spec.path, attempt, status: response.status, durationMs: deps.now() - startedAt, requestId: serverRequestId, code: null, idempotencyReplayed: replayed });
          return { ok: true, value: undefined as unknown as T };
        }
        let value: unknown;
        try {
          value = JSON.parse(body);
        } catch {
          const failure = new ApiError({
            code: INVALID_RESPONSE_CODE,
            kind: null,
            status: response.status,
            problem: null,
            requestId: serverRequestId,
            retryAfterSeconds: null,
            retryable: false,
            aborted: false,
            attempts: attempt,
          });
          observe({ method, path: spec.path, attempt, status: response.status, durationMs: deps.now() - startedAt, requestId: serverRequestId, code: failure.code, idempotencyReplayed: replayed });
          return { ok: false, failure };
        }
        observe({ method, path: spec.path, attempt, status: response.status, durationMs: deps.now() - startedAt, requestId: serverRequestId, code: null, idempotencyReplayed: replayed });
        return { ok: true, value: value as T };
      }

      const failure = await failureFromResponse(response, {
        attempt,
        retryAllowed: retry.allowed,
        fallbackRequestId: requestId,
        startedAt,
      });
      observe({ method, path: spec.path, attempt, status: response.status, durationMs: deps.now() - startedAt, requestId: failure.requestId ?? serverRequestId, code: failure.code, idempotencyReplayed: replayed });
      if (response.status === 401 && spec.tolerateUnauthorized !== true) {
        options.onUnauthorized?.(spec);
      }
      return { ok: false, failure };
    } catch (cause) {
      const failure = transportFailure(cause, {
        attempt,
        retryAllowed: retry.allowed,
        requestId,
        deadline,
      });
      observe({ method, path: spec.path, attempt, status: null, durationMs: deps.now() - startedAt, requestId, code: failure.code, idempotencyReplayed: false });
      return { ok: false, failure };
    } finally {
      deadline.dispose();
    }
  }

  /** Turns a non-2xx response into the failure the caller branches on. */
  async function failureFromResponse(response: Response, context: ResponseContext): Promise<ApiError> {
    const body = await response.text();
    const contentType = response.headers.get("content-type") ?? "";
    const problemType = contentType.includes(PROBLEM_MEDIA_TYPE);
    const problem: Problem | null = problemType ? parseProblemBody(body) : null;
    const code = problem?.code ?? (problemType ? INVALID_RESPONSE_CODE : UNEXPECTED_RESPONSE_CODE);
    return new ApiError({
      code,
      kind: errorKindForStatus(response.status),
      status: response.status,
      problem,
      requestId: response.headers.get(config.requestIdHeader) ?? problem?.request_id ?? context.fallbackRequestId,
      retryAfterSeconds: parseRetryAfter(response.headers.get("retry-after"), context.startedAt),
      retryable: context.retryAllowed && RETRYABLE_STATUSES.includes(response.status),
      aborted: false,
      attempts: context.attempt,
    });
  }

  return {
    async request<T>(spec: RequestSpec): Promise<T> {
      const method = normalizeMethod(spec.method);
      const url = buildUrl(config.baseUrl, spec.path, spec.query);
      const retry = resolveRetryPolicy(spec, defaults);
      const idempotencyKey = resolveIdempotencyKey(spec);

      let lastFailure: ApiError | null = null;
      for (let attempt = 1; attempt <= retry.maxAttempts; attempt += 1) {
        const outcome = await performAttempt<T>({ spec, method, url, retry, idempotencyKey, attempt });
        if (outcome.ok) {
          return outcome.value;
        }
        lastFailure = outcome.failure;
        if (attempt >= retry.maxAttempts || !outcome.failure.retryable) {
          break;
        }
        const delayMs = retryDelayMs(outcome.failure, attempt, retry, deps.random);
        if (delayMs === null) {
          break;
        }
        try {
          await deps.sleep(delayMs, spec.signal);
        } catch (cause) {
          // The caller cancelled during the backoff: report the cancellation,
          // never the attempt that was about to be retried.
          throw abortFailure(attempt, cause);
        }
      }
      throw lastFailure ?? abortFailure(0, undefined);
    },
  };
}

/** Resolves the default retry policy from options. */
function resolveDefaults(retry: RetryOptions | undefined): Required<RetryOptions> {
  return {
    maxAttempts: clampAttempts(retry?.maxAttempts ?? DEFAULT_RETRY.maxAttempts),
    baseDelayMs: nonNegative(retry?.baseDelayMs ?? DEFAULT_RETRY.baseDelayMs),
    maxDelayMs: nonNegative(retry?.maxDelayMs ?? DEFAULT_RETRY.maxDelayMs),
  };
}

/** Resolves whether the request may be retried at all, and with what tuning. */
function resolveRetryPolicy(spec: RequestSpec, defaults: Required<RetryOptions>): ResolvedRetry {
  const requested = spec.retry;
  const safe = SAFE_METHODS.includes(spec.method);
  // A mutation is retried only when the caller names the backend-proven
  // idempotent contract with an explicit key. A header alone never creates
  // idempotence, so the core never invents a key and never retries an unsafe
  // request that carries none — even when retries were requested.
  const allowed = requested !== false && (safe || spec.idempotencyKey !== undefined);
  if (requested === undefined || requested === false) {
    return { ...defaults, allowed };
  }
  return {
    maxAttempts: clampAttempts(requested.maxAttempts ?? defaults.maxAttempts),
    baseDelayMs: nonNegative(requested.baseDelayMs ?? defaults.baseDelayMs),
    maxDelayMs: nonNegative(requested.maxDelayMs ?? defaults.maxDelayMs),
    allowed,
  };
}

/** The `Idempotency-Key` value to send, or null when none was supplied. */
function resolveIdempotencyKey(spec: RequestSpec): string | null {
  return spec.idempotencyKey ?? null;
}

/** Backoff delay of the next attempt, or null when the retry must not happen. */
function retryDelayMs(failure: ApiError, attempt: number, retry: ResolvedRetry, random: () => number): number | null {
  if (failure.retryAfterSeconds !== null) {
    const serverDelay = failure.retryAfterSeconds * 1000;
    // Never retry earlier than the server asked; if that exceeds the ceiling,
    // failing is safer than hammering the endpoint.
    return serverDelay > retry.maxDelayMs ? null : serverDelay;
  }
  const exponential = retry.baseDelayMs * 2 ** (attempt - 1);
  const capped = Math.min(exponential, retry.maxDelayMs);
  const jitter = 0.5 + 0.5 * clampUnit(random());
  return Math.round(capped * jitter);
}

/** Classifies a rejection from `fetch` into the transport failure vocabulary. */
function transportFailure(cause: unknown, context: TransportContext): ApiError {
  if (context.deadline.timedOut()) {
    return new ApiError({
      code: TIMEOUT_ERROR_CODE,
      kind: null,
      status: null,
      problem: null,
      requestId: context.requestId,
      retryAfterSeconds: null,
      retryable: false,
      aborted: true,
      attempts: context.attempt,
      cause,
    });
  }
  if (context.deadline.aborted() || isAbortError(cause)) {
    return abortFailure(context.attempt, cause);
  }
  return new ApiError({
    code: NETWORK_ERROR_CODE,
    kind: null,
    status: null,
    problem: null,
    requestId: context.requestId,
    retryAfterSeconds: null,
    retryable: context.retryAllowed,
    aborted: false,
    attempts: context.attempt,
    cause,
  });
}

/** The single cancellation shape, shared by caller abort and backoff abort. */
function abortFailure(attempt: number, cause?: unknown): ApiError {
  const init = {
    code: ABORTED_ERROR_CODE,
    kind: null,
    status: null,
    problem: null,
    requestId: null,
    retryAfterSeconds: null,
    retryable: false,
    aborted: true,
    attempts: attempt,
  };
  return cause === undefined ? new ApiError(init) : new ApiError({ ...init, cause });
}

/** Combines the caller signal with a per-attempt deadline. */
function createDeadline(timeoutMs: number, callerSignal?: AbortSignal): Deadline {
  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);
  const onAbort = (): void => {
    controller.abort();
  };
  if (callerSignal !== undefined) {
    if (callerSignal.aborted) {
      controller.abort();
    } else {
      callerSignal.addEventListener("abort", onAbort, { once: true });
    }
  }
  return {
    signal: controller.signal,
    timedOut: () => timedOut,
    aborted: () => controller.signal.aborted,
    dispose: () => {
      clearTimeout(timer);
      callerSignal?.removeEventListener("abort", onAbort);
    },
  };
}

/** Default backoff sleep that stays interruptible by the caller signal. */
function defaultSleep(delayMs: number, signal?: AbortSignal): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    if (signal?.aborted === true) {
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }
    const onAbort = (): void => {
      clearTimeout(timer);
      reject(new DOMException("Aborted", "AbortError"));
    };
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, delayMs);
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

/** Reads a cookie without assuming a DOM is present. */
function defaultReadCookie(name: string): string | null {
  if (typeof document === "undefined") {
    return null;
  }
  const prefix = `${name}=`;
  for (const part of document.cookie.split(";")) {
    const trimmed = part.trim();
    if (trimmed.startsWith(prefix)) {
      const value = trimmed.slice(prefix.length);
      try {
        return decodeURIComponent(value);
      } catch {
        return value;
      }
    }
  }
  return null;
}

/** Builds the absolute request URL from the base, path and query values. */
function buildUrl(baseUrl: string, path: string, query?: Readonly<Record<string, QueryValue>>): string {
  if (!path.startsWith("/")) {
    throw new TypeError(`http: request path must start with "/", got ${JSON.stringify(path)}`);
  }
  const base = baseUrl.endsWith("/") ? baseUrl.slice(0, -1) : baseUrl;
  const search = new URLSearchParams();
  if (query !== undefined) {
    for (const key of Object.keys(query)) {
      const value = query[key];
      if (value === undefined || value === null || value === "") {
        continue;
      }
      search.append(key, String(value));
    }
  }
  const suffix = search.toString();
  return suffix === "" ? `${base}${path}` : `${base}${path}?${suffix}`;
}

/**
 * Account-scoped responses are never cached (the browser must not replay a
 * former account's data); public reads keep the HTTP cache and its ETags.
 * The session family (`/api/v1/me/*`), the authentication family
 * (`/api/v1/auth/*`: register, login, verify, password reset) and the
 * restricted moderation console (`/api/v1/moderation/*`: triage queue,
 * claims, decisions and abuse signals, all role-gated with a mandatory
 * private cache policy) are private.
 */
function cacheModeFor(path: string): RequestCache {
  if (
    path.startsWith(SESSION_PATH_PREFIX) ||
    path.startsWith(AUTH_PATH_PREFIX) ||
    path.startsWith(MODERATION_PATH_PREFIX)
  ) {
    return "no-store";
  }
  return "default";
}

/** Rejects method spellings outside the vocabulary instead of guessing. */
function normalizeMethod(method: HttpMethod): HttpMethod {
  if (!KNOWN_METHODS.includes(method)) {
    throw new TypeError(`http: unsupported method ${JSON.stringify(method)}`);
  }
  return method;
}

/** Unsafe methods need CSRF and can only be retried when idempotent. */
function isUnsafeMethod(method: HttpMethod): boolean {
  return !SAFE_METHODS.includes(method);
}

/** Narrowing helper for the abort error name used by DOM and Node. */
function isAbortError(cause: unknown): boolean {
  return cause instanceof Error && cause.name === "AbortError";
}

/** Attempts stay within 1..5: fewer hides the policy, more is hammering. */
function clampAttempts(value: number): number {
  if (!Number.isFinite(value)) {
    return DEFAULT_RETRY.maxAttempts;
  }
  return Math.min(MAX_ATTEMPTS_LIMIT, Math.max(1, Math.trunc(value)));
}

/** Delays must be finite and non-negative. */
function nonNegative(value: number): number {
  if (!Number.isFinite(value) || value < 0) {
    return 0;
  }
  return Math.trunc(value);
}

/** Random sources are clamped to [0, 1] before jittering. */
function clampUnit(value: number): number {
  if (!Number.isFinite(value)) {
    return 0;
  }
  return Math.min(1, Math.max(0, value));
}
