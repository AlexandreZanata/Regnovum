import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter } from 'k6/metrics';

// P28-T06 hot-key contention over the surfaces the delivery binary mounts
// (HTML journeys; see mix.js for why the JSON API is out of scope here).
// Every VU hammers the same hot spots: one shared wallet (owner publishes
// with distinct attempt keys), one fixed attempt key raced by all VUs (only
// the winner may ever exist), one arena for position confirms from distinct
// accounts, duplicate webhooks, and enqueue idempotency. Job leases have no
// HTTP surface and stay covered by Go tests (P25-T05 lease + cancellation
// schedule, P27-T06 reclaim and idempotent redelivery); k6 covers the
// enqueue side. Per-relation listings show the newest 20, so reconciliation
// is immediate and window-safe: every verified write is re-read in the same
// iteration, while it is still the newest.
//
// Sessions travel as an explicit Cookie header (the VU jar does not persist
// across iterations); CSRF tokens are single-use, one GET per POST. The
// dataset (funded owner, verified per-VU accounts, positioned owner,
// published arenas) is seeded out of band with tools/e2e/seed.
http.setResponseCallback(http.expectedStatuses({ min: 200, max: 499 }));

const baseURL = (__ENV.K6_BASE_URL || 'http://127.0.0.1:8080').replace(/\/$/, '');
const datasetSeed = __ENV.K6_DATASET_SEED || 'synthetic-p28-t06';
const profile = __ENV.K6_HOTKEYS_PROFILE || 'full';
const accountPassword = __ENV.K6_ACCOUNT_PASSWORD || 'load-correct-horse-1';
const accountCount = parseInt(__ENV.K6_HOTKEY_ACCOUNTS || '10', 10);

const rateLimited = new Counter('hotkey_rate_limited_total');
const failed = new Counter('hotkey_failed_total');

const HOT_ARENA = 0;

// The race contents form a closed set: one fixed attempt key can only ever
// materialize one of them, so teardown reconciles exactly without knowing
// which VU won.
function raceSet() {
  return [
    `hotkey race ${datasetSeed} alpha`,
    `hotkey race ${datasetSeed} beta`,
    `hotkey race ${datasetSeed} gamma`,
  ];
}

function raceKey() {
  return `hot-${datasetSeed}-race`;
}

function ownerEmail() {
  return `load-${datasetSeed}-owner@example.invalid`;
}

function accountEmail(vu) {
  return `load-${datasetSeed}-v${((vu - 1) % accountCount) + 1}@example.invalid`;
}

// Counts 429 apart: shedding under contention is the product working,
// never a failure. Returns true when the caller should skip its checks.
function separated(response, workload) {
  if (response && response.status === 429) {
    rateLimited.add(1, { workload });
    return true;
  }
  return false;
}

function transportFailed(response, workload, what) {
  if (!response || response.status === 0) {
    failed.add(1, { workload });
    check(null, { [`${workload} ${what} reached the server`]: () => false }, { kind: 'invariant', workload });
    return true;
  }
  return false;
}

function serverFailed(response, workload) {
  if (response.status >= 500) {
    failed.add(1, { workload });
    return true;
  }
  return false;
}

function csrfToken(document, workload, what) {
  const match = /name="csrf_token" value="([^"]+)"/.exec(document);
  if (!match) {
    failed.add(1, { workload });
    check(null, { [`${workload} ${what} page carries csrf_token`]: () => false }, { kind: 'invariant', workload });
    return null;
  }
  return match[1];
}

function hiddenValue(document, name) {
  const match = new RegExp(`name="${name}" value="([^"]+)"`).exec(document);
  return match ? match[1] : null;
}

function hasSessionCookie(response) {
  const cookies = response.headers['Set-Cookie'] || '';
  return cookies.includes('arena_session');
}

function sessionCookie(response) {
  const raw = response.headers['Set-Cookie'] || '';
  const session = /arena_session=([^;]+)/.exec(raw);
  const csrf = /arena_csrf=([^;]+)/.exec(raw);
  if (!session || !csrf) {
    return null;
  }
  return `arena_session=${session[1]}; arena_csrf=${csrf[1]}`;
}

function countOccurrences(body, needle) {
  if (!needle || !body) {
    return 0;
  }
  return body.split(needle).length - 1;
}

function postForm(url, fields, workload, session) {
  const headers = { 'Content-Type': 'application/x-www-form-urlencoded' };
  if (session) {
    headers.Cookie = session;
  }
  return http.post(url, fields, { headers, tags: { workload } });
}

export const options = {
  maxRedirects: 0,
  scenarios: {
    hotkeys: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: profile === 'sample'
        ? [
          { duration: '1s', target: 2 },
          { duration: '11s', target: 2 },
        ]
        : [
          { duration: '15s', target: 10 },
          { duration: '40s', target: 10 },
          { duration: '5s', target: 0 },
        ],
      exec: 'contended',
    },
  },
  thresholds: {
    hotkey_failed_total: ['count==0'],
    "checks{kind:invariant}": ['rate==1'],
    'http_req_duration{workload:hot-wallet}': ['p(95)<1000'],
    'http_req_duration{workload:hot-attempt}': ['p(95)<1000'],
    'http_req_duration{workload:hot-arena}': ['p(95)<1000'],
    'http_req_duration{workload:hot-aggregate}': ['p(95)<1000'],
    'http_req_duration{workload:hot-webhook}': ['p(95)<1000'],
    'http_req_duration{workload:hot-jobs}': ['p(95)<1000'],
    'http_req_duration{workload:hot-login}': ['p(95)<1500'],
    'http_req_duration{workload:hot-visitors}': ['p(95)<500'],
  },
};

// Weighted rotation across the hot spots. Deterministic per VU and
// iteration, so a seed replays the same contention.
const rotation = [
  'hot-wallet', 'hot-attempt', 'hot-arena', 'hot-aggregate',
  'hot-login', 'hot-webhook', 'hot-jobs', 'hot-visitors',
  'hot-wallet',
];

function workloadFor(vu, iteration) {
  return rotation[(vu + iteration) % rotation.length];
}

export function setup() {
  const live = http.get(`${baseURL}/health/live`);
  if (!live || live.status !== 200) {
    throw new Error('setup GET /health/live: unreachable');
  }
  const slugs = (__ENV.K6_ARENA_SLUGS || '').split(',').map((s) => s.trim()).filter((s) => s.length > 0);
  if (slugs.length === 0) {
    throw new Error('setup: K6_ARENA_SLUGS is required (comma-separated slugs seeded with tools/e2e/seed arena)');
  }
  const probe = http.get(`${baseURL}/arenas/${slugs[HOT_ARENA]}`);
  if (!probe || probe.status !== 200 || !probe.body.includes(slugs[HOT_ARENA])) {
    throw new Error(`setup arena ${slugs[HOT_ARENA]}: status ${probe && probe.status}`);
  }
  // One owner session shared by every VU for the shared-wallet workloads.
  // Sessions do not cross the setup/VU boundary in the jar, so the cookie
  // travels as data, like every other deterministic input.
  const page = http.get(`${baseURL}/login`);
  const token = csrfToken(page.body, 'hot-login', 'login');
  if (!token) {
    throw new Error('setup /login carries no csrf_token');
  }
  const login = postForm(`${baseURL}/login`, { email: ownerEmail(), password: accountPassword, csrf_token: token }, 'hot-login');
  if (!login || login.status !== 303) {
    throw new Error(`setup owner login: status ${login && login.status}`);
  }
  const session = sessionCookie(login);
  if (!session) {
    throw new Error('setup owner login issued no session');
  }
  return { slugs, ownerSession: session };
}

// ensureSelfSession signs the VU into its own seeded account exactly once,
// for the multi-account contention on one arena. Returns false on throttle.
function ensureSelfSession(vu, state) {
  if (state.selfAuthed) {
    return true;
  }
  const workload = 'hot-login';
  const page = http.get(`${baseURL}/login`, { tags: { workload } });
  if (transportFailed(page, workload, 'login page')) return false;
  if (separated(page, workload)) return false;
  const token = csrfToken(page.body, workload, 'login page');
  if (!token) return false;
  const response = postForm(`${baseURL}/login`, { email: accountEmail(vu), password: accountPassword, csrf_token: token }, workload);
  if (transportFailed(response, workload, 'login')) return false;
  if (separated(response, workload)) return false;
  const session = sessionCookie(response);
  const ok = check(response, {
    'hot-login self session issues with 303': (r) => r.status === 303 && hasSessionCookie(r) && session !== null,
  }, { kind: 'invariant', workload });
  if (serverFailed(response, workload)) return false;
  state.selfAuthed = ok && response.status === 303;
  if (state.selfAuthed) {
    state.selfSession = session;
  }
  return state.selfAuthed;
}

// verifyOwnWrite re-reads the listing immediately, while a just-written row
// is still the newest: a 303-published content must appear exactly once, a
// refused content must appear zero times. Immediate means truncation can
// never excuse a miss.
function verifyOwnWrite(slug, session, content, published, workload) {
  const page = http.get(`${baseURL}/arenas/${slug}`, {
    headers: { Cookie: session },
    tags: { workload },
  });
  if (transportFailed(page, workload, 'verify page')) return;
  if (separated(page, workload)) return;
  const count = countOccurrences(page.body, content);
  if (published) {
    check({ count }, {
      [`${workload} own write exists exactly once`]: (result) => result.count === 1,
    }, { kind: 'invariant', workload });
    if (count !== 1) {
      failed.add(1, { workload });
    }
    return;
  }
  check({ count }, {
    [`${workload} refused write exists zero times`]: (result) => result.count === 0,
  }, { kind: 'invariant', workload });
  if (count !== 0) {
    failed.add(1, { workload });
  }
}

function hotWallet(data, vu, iteration, state) {
  const workload = 'hot-wallet';
  const slug = data.slugs[HOT_ARENA];
  const content = `hotkey wallet ${datasetSeed} ${vu} ${iteration} conteudo sintetico para contencao`;
  const page = http.get(`${baseURL}/arenas/${slug}`, {
    headers: { Cookie: data.ownerSession },
    tags: { workload },
  });
  if (transportFailed(page, workload, 'arena page')) return;
  if (separated(page, workload)) return;
  const token = csrfToken(page.body, workload, 'arena page');
  if (!token) return;
  const attempt = hiddenValue(page.body, 'attempt');
  if (!attempt) {
    failed.add(1, { workload });
    check(null, { [`${workload} page carries attempt key`]: () => false }, { kind: 'invariant', workload });
    return;
  }
  const response = http.post(`${baseURL}/arenas/${slug}/arguments`, {
    relation: ['support', 'oppose', 'context'][iteration % 3],
    content,
    attempt,
    csrf_token: token,
  }, {
    headers: { Cookie: data.ownerSession, 'Content-Type': 'application/x-www-form-urlencoded' },
    tags: { workload },
  });
  if (transportFailed(response, workload, 'publish')) return;
  if (separated(response, workload)) return;
  check(response, {
    [`${workload} publish is 303 or funded-refusal`]: (r) => r.status === 303 || r.status === 402 || r.status === 409,
  }, { kind: 'invariant', workload });
  if (serverFailed(response, workload)) return;
  // Every third publish is verified immediately: 303 means the row must
  // exist exactly once, any refusal means it must not exist at all.
  state.walletCount = (state.walletCount || 0) + 1;
  if (state.walletCount % 3 === 0) {
    verifyOwnWrite(slug, data.ownerSession, content, response.status === 303, workload);
  }
  sleep(0.05);
}

function hotAttempt(data, vu, iteration, state) {
  const workload = 'hot-attempt';
  const slug = data.slugs[HOT_ARENA];
  const set = raceSet();
  const content = set[(vu + iteration) % set.length];
  const page = http.get(`${baseURL}/arenas/${slug}`, {
    headers: { Cookie: data.ownerSession },
    tags: { workload },
  });
  if (transportFailed(page, workload, 'arena page')) return;
  if (separated(page, workload)) return;
  const token = csrfToken(page.body, workload, 'arena page');
  if (!token) return;
  const response = http.post(`${baseURL}/arenas/${slug}/arguments`, {
    relation: 'support',
    content,
    attempt: raceKey(),
    csrf_token: token,
  }, {
    headers: { Cookie: data.ownerSession, 'Content-Type': 'application/x-www-form-urlencoded' },
    tags: { workload },
  });
  if (transportFailed(response, workload, 'race publish')) return;
  if (separated(response, workload)) return;
  // Winner and replays share one answer: replay resolves the recorded
  // argument instead of debiting twice. Teardown proves the exactly-once
  // effect against the closed content set.
  check(response, {
    [`${workload} race resolves 303`]: (r) => r.status === 303,
  }, { kind: 'invariant', workload });
  serverFailed(response, workload);
  sleep(0.05);
}

function hotArena(data, vu, iteration, state) {
  const workload = 'hot-arena';
  if (!ensureSelfSession(vu, state)) {
    return;
  }
  const slug = data.slugs[HOT_ARENA];
  const page = http.get(`${baseURL}/arenas/${slug}`, {
    headers: { Cookie: state.selfSession },
    tags: { workload },
  });
  if (transportFailed(page, workload, 'arena page')) return;
  if (separated(page, workload)) return;
  const token = csrfToken(page.body, workload, 'arena page');
  if (!token) return;
  // Distinct accounts confirm distinct values where possible, so the
  // aggregate distribution is non-trivial to reconcile. Every seventh
  // execution posts the opposite value as a conflict probe: immutability
  // refuses it 409 without touching the held value.
  const own = vu % 2 === 0 ? 'agree' : 'disagree';
  const opposite = own === 'agree' ? 'disagree' : 'agree';
  state.arenaCount = (state.arenaCount || 0) + 1;
  const posted = state.arenaCount % 7 === 0 ? opposite : own;
  const response = http.post(`${baseURL}/arenas/${slug}/position`, { position: posted, csrf_token: token }, {
    headers: { Cookie: state.selfSession, 'Content-Type': 'application/x-www-form-urlencoded' },
    tags: { workload },
  });
  if (transportFailed(response, workload, 'confirm')) return;
  if (separated(response, workload)) return;
  // The initial position is immutable per account: the held value replays
  // 303, anything else is refused 409. Unknown held state (sibling VU or
  // previous run confirmed first) accepts either once, then follows.
  const held = state.heldPosition || null;
  // Unknown held state (sibling VU or previous run confirmed first)
  // accepts either once, then follows; a known held value replays 303 and
  // refuses anything else 409, which is also what the conflict probe
  // (every seventh execution posts the opposite value) must draw.
  let want = [303, 409];
  if (held !== null && held !== 'conflict') {
    want = posted === held ? [303] : [409];
  }
  if (response.status === 303 && held === null) {
    state.heldPosition = posted;
  } else if (response.status === 409 && held === null) {
    state.heldPosition = 'conflict';
  }
  check(response, {
    [`${workload} confirm replays 303, conflicts 409`]: (r) => want.indexOf(r.status) !== -1,
  }, { kind: 'invariant', workload });
  serverFailed(response, workload);
  sleep(0.05);
}

function hotAggregate(data, iteration) {
  const workload = 'hot-aggregate';
  const slug = data.slugs[HOT_ARENA];
  const response = http.get(`${baseURL}/arenas/${slug}?reveal=1`, {
    headers: { 'Accept-Language': ['pt-BR', 'en-US'][iteration % 2] },
    tags: { workload },
  });
  if (transportFailed(response, workload, 'aggregate')) return;
  if (separated(response, workload)) return;
  check(response, {
    [`${workload} aggregate renders or stays suppressed`]: (r) => r.status === 200 && (r.body.includes('Resultado agregado') || r.body.includes('Aggregate result')),
  }, { kind: 'invariant', workload });
  serverFailed(response, workload);
  sleep(0.1);
}

function hotWebhook(iteration) {
  const workload = 'hot-webhook';
  // Duplicate delivery of one event: the delivery surface mounts no Stripe
  // route, so every duplicate must stay handled exactly like the first.
  const body = JSON.stringify({
    id: `evt_${datasetSeed}_hotkey`,
    type: 'checkout.session.completed',
    livemode: false,
  });
  const first = http.post(`${baseURL}/api/v1/webhooks/stripe`, body, {
    headers: { 'Content-Type': 'application/json', 'Stripe-Signature': 'synthetic-invalid-signature' },
    tags: { workload },
  });
  if (transportFailed(first, workload, 'webhook')) return;
  if (separated(first, workload)) return;
  const second = http.post(`${baseURL}/api/v1/webhooks/stripe`, body, {
    headers: { 'Content-Type': 'application/json', 'Stripe-Signature': 'synthetic-invalid-signature' },
    tags: { workload },
  });
  if (transportFailed(second, workload, 'webhook')) return;
  if (separated(second, workload)) return;
  check({ first: first.status, second: second.status }, {
    [`${workload} duplicate delivery is deterministic`]: (pair) => pair.first === pair.second && pair.first >= 200 && pair.first < 500,
  }, { kind: 'invariant', workload });
  serverFailed(first, workload);
  serverFailed(second, workload);
  sleep(0.05);
}

function hotJobs(iteration, state) {
  const workload = 'hot-jobs';
  // Concurrent reset requests enqueue email jobs; ghost and real share the
  // uniform answer. The pair runs once per VU (reset mail is throttled at
  // 3/hour per IP); later executions alternate singles.
  const pair = !state.resetPaired;
  if (pair) {
    state.resetPaired = true;
  }
  const emails = pair
    ? [ownerEmail(), `ghost-${datasetSeed}-${iteration}@example.invalid`]
    : [(iteration % 2 === 0 ? ownerEmail() : `ghost-${datasetSeed}-${iteration}@example.invalid`)];
  const statuses = [];
  emails.forEach((email) => {
    const page = http.get(`${baseURL}/reset`, { tags: { workload } });
    if (transportFailed(page, workload, 'reset page')) {
      statuses.push(0);
      return;
    }
    if (separated(page, workload)) {
      statuses.push(429);
      return;
    }
    const token = csrfToken(page.body, workload, 'reset page');
    if (!token) {
      statuses.push(0);
      return;
    }
    const response = postForm(`${baseURL}/reset`, { email, csrf_token: token }, workload);
    if (transportFailed(response, workload, 'reset')) {
      statuses.push(0);
      return;
    }
    if (separated(response, workload)) {
      statuses.push(429);
      return;
    }
    statuses.push(response.status);
    serverFailed(response, workload);
  });
  if (pair && statuses.indexOf(0) === -1 && statuses.indexOf(429) === -1) {
    check({ first: statuses[0], second: statuses[1] }, {
      [`${workload} reset answer is uniform`]: (answers) => answers.first === answers.second && answers.first >= 200 && answers.first < 500,
    }, { kind: 'invariant', workload });
  }
  sleep(0.1);
}

function hotVisitors(data, iteration) {
  const workload = 'hot-visitors';
  const slug = data.slugs[iteration % data.slugs.length];
  const response = http.get(`${baseURL}/arenas/${slug}`, {
    headers: { 'Accept-Language': ['pt-BR', 'en-US'][iteration % 2] },
    tags: { workload },
  });
  if (transportFailed(response, workload, 'arena')) return;
  if (separated(response, workload)) return;
  check(response, {
    [`${workload} reads stay intact under contention`]: (r) => r.status === 200 && r.body.includes(slug),
  }, { kind: 'invariant', workload });
  serverFailed(response, workload);
  sleep(0.05);
}

function hotLogin(vu, iteration, state) {
  const workload = 'hot-login';
  if (!state.selfAuthed) {
    ensureSelfSession(vu, state);
    sleep(0.05);
    return;
  }
  sleep(0.05);
}

const vuState = {};

export function contended(data) {
  const vu = __VU;
  if (!vuState[vu]) {
    vuState[vu] = {
      selfAuthed: false,
      signupSerial: vu * 100000,
      iteration: 0,
      resetPaired: false,
      arenaConfirmed: false,
      walletCount: 0,
    };
  }
  const state = vuState[vu];
  const iteration = state.iteration;
  state.iteration = iteration + 1;
  const name = workloadFor(vu, iteration);
  switch (name) {
    case 'hot-wallet':
      hotWallet(data, vu, iteration, state);
      break;
    case 'hot-attempt':
      hotAttempt(data, vu, iteration, state);
      break;
    case 'hot-arena':
      hotArena(data, vu, iteration, state);
      break;
    case 'hot-aggregate':
      hotAggregate(data, iteration);
      break;
    case 'hot-login':
      hotLogin(vu, iteration, state);
      break;
    case 'hot-webhook':
      hotWebhook(iteration);
      break;
    case 'hot-jobs':
      hotJobs(iteration, state);
      break;
    case 'hot-visitors':
      hotVisitors(data, iteration);
      break;
    default:
      hotVisitors(data, iteration);
      break;
  }
}

export function teardown(data) {
  // Independent reconciliation after contention, with no shared VU state:
  // the race publishes into one fixed key with a closed content set, so
  // exactly one of them may exist; per-account positions are spot-checked
  // for persistence; one final write proves the path healthy. Any
  // deviation aborts teardown and fails the run.
  const workload = 'teardown';
  const slug = data.slugs[HOT_ARENA];
  const set = raceSet();
  const raceContent = set[0];
  // The run trips the login throttle by design, so teardown retries with
  // backoff instead of demanding the first attempt: bounded patience,
  // never an unbounded loop, and 5xx still aborts at once.
  let session = null;
  for (let attempt = 0; attempt < 6; attempt += 1) {
    const loginPage = http.get(`${baseURL}/login`, { tags: { workload } });
    if (!loginPage || loginPage.status !== 200) {
      throw new Error(`teardown login page: status ${loginPage && loginPage.status}`);
    }
    const loginToken = csrfToken(loginPage.body, workload, 'login');
    if (!loginToken) {
      throw new Error('teardown login page carries no csrf_token');
    }
    const login = postForm(`${baseURL}/login`, { email: ownerEmail(), password: accountPassword, csrf_token: loginToken }, workload);
    if (!login || login.status === 0 || login.status >= 500) {
      throw new Error(`teardown login: status ${login && login.status}`);
    }
    if (login.status === 303) {
      session = sessionCookie(login);
      if (!session) {
        throw new Error('teardown login issued no session');
      }
      break;
    }
    if (login.status !== 429) {
      throw new Error(`teardown login: status ${login.status}, want 303 or throttled 429`);
    }
    sleep(5);
  }
  if (!session) {
    throw new Error('teardown login still throttled after bounded retries');
  }
  const arena = http.get(`${baseURL}/arenas/${slug}`, {
    headers: { Cookie: session },
    tags: { workload },
  });
  if (!arena || arena.status !== 200) {
    throw new Error(`teardown arena page: status ${arena && arena.status}`);
  }
  const formToken = csrfToken(arena.body, workload, 'arena page');
  if (!formToken) {
    throw new Error('teardown arena page carries no csrf_token');
  }
  // Same key and content across retries: a refused attempt never
  // executed, so retrying it cannot duplicate; the first 303 wins.
  let raceResponse = null;
  for (let attempt = 0; attempt < 6; attempt += 1) {
    const retryPage = attempt === 0 ? arena : http.get(`${baseURL}/arenas/${slug}`, {
      headers: { Cookie: session },
      tags: { workload },
    });
    if (!retryPage || retryPage.status !== 200) {
      throw new Error(`teardown race page: status ${retryPage && retryPage.status}`);
    }
    const retryToken = csrfToken(retryPage.body, workload, 'race page');
    if (!retryToken) {
      throw new Error('teardown race page carries no csrf_token');
    }
    const candidate = http.post(`${baseURL}/arenas/${slug}/arguments`, {
      relation: 'support',
      content: raceContent,
      attempt: raceKey(),
      csrf_token: retryToken,
    }, {
      headers: { Cookie: session, 'Content-Type': 'application/x-www-form-urlencoded' },
      tags: { workload },
    });
    if (!candidate || candidate.status === 0 || candidate.status >= 500) {
      throw new Error(`teardown race publish: status ${candidate && candidate.status}`);
    }
    if (candidate.status === 303) {
      raceResponse = candidate;
      break;
    }
    if (candidate.status !== 429) {
      throw new Error(`teardown race publish: status ${candidate.status}, want 303 or throttled 429`);
    }
    sleep(5);
  }
  if (!raceResponse) {
    throw new Error('teardown race publish still throttled after bounded retries');
  }
  const listing = http.get(`${baseURL}/arenas/${slug}?reveal=1`, { tags: { workload } });
  if (!listing || listing.status !== 200) {
    throw new Error(`teardown listing: status ${listing && listing.status}`);
  }
  // Duplication is impossible to hide behind truncation: a visible
  // duplicate is a real double-create whatever the window holds.
  let visible = 0;
  set.forEach((content) => {
    const count = countOccurrences(listing.body, content);
    if (count > 1) {
      throw new Error(`teardown race content duplicated ${count}x: ${content.slice(0, 40)}`);
    }
    visible += count;
  });
  // The listing shows the newest 20 per relation: a full window may have
  // evicted the winner, so absence only fails on a partial window.
  const windowRows = countOccurrences(listing.body, '<p>hotkey ');
  if (visible !== 1 && windowRows < 60) {
    throw new Error(`teardown race set shows ${visible} contents, want exactly 1`);
  }
  if (visible !== 1) {
    console.log(`teardown race winner outside the full ${windowRows}-row window; duplication still refused above`);
  }
  // Spot-check position persistence for the owner and the first three
  // seeded accounts: a confirmed position that did not survive is a lost
  // update, and the page says so by rendering the confirm form instead.
  // Logins retry on 429 with backoff: the run trips the login throttle by
  // design, and teardown patience is bounded, never infinite.
  const accounts = [ownerEmail()];
  for (let index = 1; index <= 3; index += 1) {
    accounts.push(`load-${datasetSeed}-v${index}@example.invalid`);
  }
  accounts.forEach((email) => {
    let accountSession = null;
    for (let attempt = 0; attempt < 4; attempt += 1) {
      const loginPage = http.get(`${baseURL}/login`, { tags: { workload } });
      if (!loginPage || loginPage.status !== 200) {
        throw new Error(`teardown login page for ${email}: status ${loginPage && loginPage.status}`);
      }
      const loginToken = csrfToken(loginPage.body, workload, 'login');
      if (!loginToken) {
        throw new Error(`teardown login page for ${email} carries no csrf_token`);
      }
      const auth = postForm(`${baseURL}/login`, { email, password: accountPassword, csrf_token: loginToken }, workload);
      if (!auth || auth.status === 0 || auth.status >= 500) {
        throw new Error(`teardown login ${email}: status ${auth && auth.status}`);
      }
      if (auth.status === 429) {
        sleep(5);
        continue;
      }
      if (auth.status !== 303) {
        throw new Error(`teardown login ${email}: status ${auth.status}, want 303`);
      }
      accountSession = sessionCookie(auth);
      if (!accountSession) {
        throw new Error(`teardown login ${email} issued no session`);
      }
      break;
    }
    if (!accountSession) {
      throw new Error(`teardown login ${email} still throttled after bounded retries`);
    }
    // Confirm-then-verify makes the check independent of run history: a
    // 303 creates or replays, a 409 keeps the earlier value, and the page
    // that follows must render the held position in every case. A missing
    // position afterwards is a lost update, not an ordering artifact.
    const confirmPage = http.get(`${baseURL}/arenas/${slug}`, {
      headers: { Cookie: accountSession },
      tags: { workload },
    });
    if (!confirmPage || confirmPage.status !== 200) {
      throw new Error(`teardown confirm page for ${email}: status ${confirmPage && confirmPage.status}`);
    }
    const confirmToken = csrfToken(confirmPage.body, workload, 'confirm page');
    if (!confirmToken) {
      throw new Error(`teardown confirm page for ${email} carries no csrf_token`);
    }
    const confirmed = http.post(`${baseURL}/arenas/${slug}/position`, { position: 'agree', csrf_token: confirmToken }, {
      headers: { Cookie: accountSession, 'Content-Type': 'application/x-www-form-urlencoded' },
      tags: { workload },
    });
    if (!confirmed || (confirmed.status !== 303 && confirmed.status !== 409)) {
      throw new Error(`teardown confirm ${email}: status ${confirmed && confirmed.status}, want 303 or 409`);
    }
    const page = http.get(`${baseURL}/arenas/${slug}`, {
      headers: { Cookie: accountSession },
      tags: { workload },
    });
    if (!page || page.status !== 200) {
      throw new Error(`teardown arena page for ${email}: status ${page && page.status}`);
    }
    if (!/Posição atual: /.test(page.body)) {
      throw new Error(`teardown position of ${email} did not persist`);
    }
  });
  // Aggregate lower bound: every spot-checked account above confirmed, so
  // a revealed aggregate must sum at least those four. A suppressed
  // aggregate carries the note instead of numbers.
  const revealed = http.get(`${baseURL}/arenas/${slug}?reveal=1`, {
    headers: { Cookie: session },
    tags: { workload },
  });
  if (!revealed || revealed.status !== 200) {
    throw new Error(`teardown aggregate page: status ${revealed && revealed.status}`);
  }
  if (/amostra é pequena demais|sample is too small/i.test(revealed.body)) {
    console.log('teardown aggregate suppressed by the privacy quorum; spot checks above carry the persistence proof');
  } else {
    let total = 0;
    let rows = 0;
    const pattern = /<li>([^<>]+): (\d+)<\/li>/g;
    let match = pattern.exec(revealed.body);
    while (match !== null) {
      total += parseInt(match[2], 10);
      rows += 1;
      match = pattern.exec(revealed.body);
    }
    if (rows === 0 || total < 4) {
      throw new Error(`teardown aggregate sums to ${total} over ${rows} rows, want at least the 4 spot-checked positions`);
    }
    console.log(`teardown aggregate sums to ${total} over ${rows} rows`);
  }
  // Final write with a fresh key: the path is healthy after contention.
  // Same fixed key across retries: a refused attempt never executed.
  // Fresh CSRF per attempt: the middleware consumes the token before the
  // throttle refuses, so a reused token would 403 instead of retrying.
  let finale = null;
  for (let attempt = 0; attempt < 6; attempt += 1) {
    const retryPage = http.get(`${baseURL}/arenas/${slug}`, {
      headers: { Cookie: session },
      tags: { workload },
    });
    if (!retryPage || retryPage.status !== 200) {
      throw new Error(`teardown finale page: status ${retryPage && retryPage.status}`);
    }
    const retryToken = csrfToken(retryPage.body, workload, 'finale page');
    if (!retryToken) {
      throw new Error('teardown finale page carries no csrf_token');
    }
    const candidate = http.post(`${baseURL}/arenas/${slug}/arguments`, {
      relation: 'context',
      content: `hotkey finale ${datasetSeed} conteudo de fechamento`,
      attempt: `hot-${datasetSeed}-finale`,
      csrf_token: retryToken,
    }, {
      headers: { Cookie: session, 'Content-Type': 'application/x-www-form-urlencoded' },
      tags: { workload },
    });
    if (!candidate || candidate.status === 0 || candidate.status >= 500) {
      throw new Error(`teardown finale publish: status ${candidate && candidate.status}`);
    }
    if (candidate.status === 429) {
      sleep(5);
      continue;
    }
    finale = candidate;
    break;
  }
  if (!finale) {
    throw new Error('teardown finale publish still throttled after bounded retries');
  }
  // A replay across runs resolves 303 without duplicating; 402 names an
  // exhausted owner pot, which seeds afresh instead of failing silently.
  if (finale.status !== 303 && finale.status !== 402) {
    throw new Error(`teardown finale publish: status ${finale.status}`);
  }
}
