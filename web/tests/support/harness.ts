/**
 * Test support of the frontend HTTP core (P18-T03).
 *
 * No third-party mocking library: a fake `fetch` records the calls it saw and
 * answers from a responder, while the injected clock, sleep and random source
 * keep retries and timeouts deterministic.
 */
import { ApiError } from "../../src/core/problem.js";
import { createHttpCore } from "../../src/core/http.js";
import type {
  HttpCore,
  HttpCoreOptions,
  RequestObservation,
  RequestSpec,
  RetryOptions,
} from "../../src/core/http.js";
import type { Problem } from "../../src/contracts/generated.js";

/** One call the core made, exactly as it was handed to `fetch`. */
export interface RecordedCall {
  readonly url: string;
  readonly init: RequestInit;
}

/** Answers one call; the responder decides the response or rejection. */
export type Responder = (call: RecordedCall, index: number) => Response | Promise<Response>;

/** Fake transport that records every call. */
export interface FakeTransport {
  readonly calls: RecordedCall[];
  readonly fetchImpl: typeof fetch;
}

/** Builds a fake `fetch` over a responder. */
export function createFakeTransport(responder: Responder): FakeTransport {
  const calls: RecordedCall[] = [];
  const fetchImpl: typeof fetch = async (input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
    const call: RecordedCall = { url, init: init ?? {} };
    calls.push(call);
    return responder(call, calls.length - 1);
  };
  return { calls, fetchImpl };
}

/** Options of a test context; every effect can be overridden. */
export interface TestContextOptions {
  readonly responder: Responder;
  readonly baseUrl?: string;
  readonly timeoutMs?: number;
  readonly retry?: RetryOptions;
  readonly onUnauthorized?: (spec: RequestSpec) => void;
  readonly cookies?: Readonly<Record<string, string>>;
  readonly now?: () => number;
  readonly random?: () => number;
  readonly sleep?: (delayMs: number, signal?: AbortSignal) => Promise<void>;
}

/** The core plus every observation the assertions need. */
export interface TestContext {
  readonly core: HttpCore;
  readonly calls: readonly RecordedCall[];
  readonly delays: number[];
  readonly observations: RequestObservation[];
  readonly unauthorized: RequestSpec[];
  readonly lastCall: () => RecordedCall;
}

/** createTestContext wires a core with deterministic effects. */
export function createTestContext(options: TestContextOptions): TestContext {
  const transport = createFakeTransport(options.responder);
  const delays: number[] = [];
  const observations: RequestObservation[] = [];
  const unauthorized: RequestSpec[] = [];
  let requestIds = 0;
  const cookies = options.cookies ?? {};

  const coreOptions: HttpCoreOptions = {
    baseUrl: options.baseUrl ?? "https://arena.test",
    timeoutMs: options.timeoutMs ?? 1_000,
    ...(options.retry === undefined ? {} : { retry: options.retry }),
    observe: (event: RequestObservation): void => {
      observations.push(event);
    },
    onUnauthorized: (spec: RequestSpec): void => {
      unauthorized.push(spec);
      options.onUnauthorized?.(spec);
    },
    dependencies: {
      fetch: transport.fetchImpl,
      now: options.now ?? (() => 0),
      random: options.random ?? (() => 1),
      sleep:
        options.sleep ??
        ((delayMs: number): Promise<void> => {
          delays.push(delayMs);
          return Promise.resolve();
        }),
      newRequestId: () => {
        requestIds += 1;
        return `req-${requestIds}`;
      },
      readCookie: (name: string) => cookies[name] ?? null,
    },
  };

  const calls: RecordedCall[] = transport.calls;
  return {
    core: createHttpCore(coreOptions),
    calls,
    delays,
    observations,
    unauthorized,
    lastCall: (): RecordedCall => {
      const call = calls.at(-1);
      if (call === undefined) {
        throw new Error("no request was recorded");
      }
      return call;
    },
  };
}

/** Reads one header of a recorded call. */
export function headerOf(call: RecordedCall, name: string): string | null {
  return new Headers(call.init.headers).get(name);
}

/** Decodes the JSON body of a recorded call. */
export function bodyOf(call: RecordedCall): unknown {
  const { body } = call.init;
  if (typeof body !== "string") {
    throw new Error(`recorded call has no JSON body: ${String(body)}`);
  }
  return JSON.parse(body);
}

/** A JSON response, as the API answers success. */
export function jsonResponse(value: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "content-type": "application/json", ...headers },
  });
}

/** A Problem Details response, as the API answers failure. */
export function problemResponse(status: number, code: string, headers: Record<string, string> = {}): Response {
  const problem: Problem = { type: "about:blank", title: code, status, code };
  return new Response(JSON.stringify(problem), {
    status,
    headers: { "content-type": "application/problem+json", ...headers },
  });
}

/** A transport that never answers until its signal aborts. */
export function hangingResponse(call: RecordedCall): Promise<Response> {
  return new Promise<Response>((_resolve, reject) => {
    const signal = call.init.signal;
    if (signal === null || signal === undefined) {
      return;
    }
    signal.addEventListener("abort", () => {
      reject(signal.reason ?? new DOMException("Aborted", "AbortError"));
    });
  });
}

/** Runs a request and returns the ApiError it must raise. */
export async function captureApiError(action: () => Promise<unknown>): Promise<ApiError> {
  try {
    await action();
  } catch (error) {
    if (error instanceof ApiError) {
      return error;
    }
    throw error;
  }
  throw new Error("expected the request to fail");
}

/** Runs a request and returns whatever it threw, for non-ApiError guards. */
export async function captureThrown(action: () => Promise<unknown>): Promise<unknown> {
  try {
    await action();
  } catch (error) {
    return error;
  }
  throw new Error("expected the request to throw");
}
