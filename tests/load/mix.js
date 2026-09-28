import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter } from 'k6/metrics';

// P28-T04 usage-model mix over the surfaces the delivery binary mounts:
// the HTML account and participation journeys plus health. The JSON API
// (/api/v1/*) is not mounted on `arena server` — its adapters are proven
// by contract tests — so webhook and moderation workloads guard that
// adversarial-shaped traffic stays handled (never 5xx) and document where
// each family is really proven. Jobs have no public surface by design;
// the jobs workload covers the enqueue paths (signup mail, reset mail),
// while queue depth and lag belong to T06/T08.
//
// One ramping scenario drives a realistic international rotation with
// Accept-Language pt-BR/en-US and deterministic
// load-<seed>-<vu>-<serial>@example.invalid identities. CSRF tokens are
// single-use: every POST is preceded by its GET, exactly like a browser
// without JavaScript. The dataset (published arenas) is seeded out of band
// with tools/e2e/seed and passed via K6_ARENA_SLUGS.
http.setResponseCallback(http.expectedStatuses({ min: 200, max: 499 }));

const baseURL = (__ENV.K6_BASE_URL || 'http://127.0.0.1:8080').replace(/\/$/, '');
const datasetSeed = __ENV.K6_DATASET_SEED || 'synthetic-p28-t04';
const profile = __ENV.K6_MIX_PROFILE || 'full';
const accountPassword = __ENV.K6_ACCOUNT_PASSWORD || 'load-correct-horse-1';

const rateLimited = new Counter('mix_rate_limited_total');
const failed = new Counter('mix_failed_total');

const locales = ['pt-BR', 'en-US'];

function localeOf(iteration) {
  return locales[iteration % locales.length];
}

// Counts 429 apart: expected throttling is backpressure working, never a
// product failure. Returns true when the caller should skip its checks.
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

// Sessions travel as an explicit Cookie header, never in the VU jar: the
// jar does not survive across iterations, while an explicit header is
// deterministic and visible in every request. Returns null when the
// response issued no session.
function sessionCookie(response) {
  const raw = response.headers['Set-Cookie'] || '';
  const session = /arena_session=([^;]+)/.exec(raw);
  const csrf = /arena_csrf=([^;]+)/.exec(raw);
  if (!session || !csrf) {
    return null;
  }
  return `arena_session=${session[1]}; arena_csrf=${csrf[1]}`;
}

function authedHeaders(iteration, state, extra) {
  return Object.assign(
    { 'Accept-Language': localeOf(iteration), Cookie: state.session },
    extra || {},
  );
}

// Weighted rotation: public reads dominate, writes interleave. Deterministic
// per VU and iteration, so a seed replays the same mix.
const rotation = [
  'visitors', 'visitors', 'viral', 'visitors',
  'signup', 'login', 'position', 'argument',
  'webhook', 'moderation', 'jobs-enqueue', 'viral',
];

function workloadFor(vu, iteration) {
  return rotation[(vu + iteration) % rotation.length];
}

function stagesFor() {
  if (profile === 'sample') {
    // Two stages so the single VU starts at once instead of ramping in
    // only as the window closes.
    return [
      { duration: '1s', target: 1 },
      { duration: '9s', target: 1 },
    ];
  }
  return [
    { duration: '20s', target: 4 },
    { duration: '30s', target: 4 },
    { duration: '10s', target: 0 },
  ];
}

export const options = {
  // Never follow redirects: 303 is the login/position/argument invariant,
  // and following it would assert on the landing page instead.
  maxRedirects: 0,
  scenarios: {
    mix: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: stagesFor(),
      exec: 'mixed',
    },
  },
  thresholds: {
    mix_failed_total: ['count==0'],
    "checks{kind:invariant}": ['rate==1'],
    'http_req_duration{workload:visitors}': ['p(95)<500'],
    'http_req_duration{workload:viral}': ['p(95)<500'],
    'http_req_duration{workload:signup}': ['p(95)<1500'],
    'http_req_duration{workload:login}': ['p(95)<1500'],
    'http_req_duration{workload:position}': ['p(95)<1000'],
    'http_req_duration{workload:argument}': ['p(95)<1000'],
    'http_req_duration{workload:webhook}': ['p(95)<1000'],
    'http_req_duration{workload:moderation}': ['p(95)<1000'],
    'http_req_duration{workload:jobs-enqueue}': ['p(95)<1000'],
  },
};

function signupEmail(vu, serial) {
  return `load-${datasetSeed}-${vu}-${serial}@example.invalid`;
}

export function setup() {
  const live = http.get(`${baseURL}/health/live`);
  if (!live || live.status !== 200) {
    throw new Error(`setup GET /health/live: unreachable`);
  }
  const slugs = (__ENV.K6_ARENA_SLUGS || '').split(',').map((s) => s.trim()).filter((s) => s.length > 0);
  if (slugs.length === 0) {
    throw new Error('setup: K6_ARENA_SLUGS is required (comma-separated slugs seeded with tools/e2e/seed arena)');
  }
  const probe = http.get(`${baseURL}/arenas/${slugs[0]}`);
  if (!probe || probe.status !== 200 || !probe.body.includes(slugs[0])) {
    throw new Error(`setup arena ${slugs[0]}: status ${probe && probe.status}`);
  }
  return { slugs };
}

function visitors(data, iteration) {
  const workload = 'visitors';
  const slug = data.slugs[iteration % data.slugs.length];
  const response = http.get(`${baseURL}/arenas/${slug}`, {
    headers: { 'Accept-Language': localeOf(iteration) },
    tags: { workload },
  });
  if (transportFailed(response, workload, 'arena')) return;
  if (separated(response, workload)) return;
  check(response, {
    [`${workload} arena page is 200 with its slug`]: (r) => r.status === 200 && r.body.includes(slug),
  }, { kind: 'invariant', workload });
  serverFailed(response, workload);
  sleep(0.1);
}

function viral(data, iteration) {
  const workload = 'viral';
  const slug = data.slugs[(iteration + 1) % data.slugs.length];
  const response = http.get(`${baseURL}/arenas/${slug}`, {
    headers: { 'Accept-Language': localeOf(iteration) },
    tags: { workload },
  });
  if (transportFailed(response, workload, 'arena')) return;
  if (separated(response, workload)) return;
  check(response, {
    [`${workload} fan-out answers its slug`]: (r) => r.status === 200 && r.body.includes(slug),
  }, { kind: 'invariant', workload });
  serverFailed(response, workload);
  sleep(0.05);
}

function postForm(url, fields, workload) {
  return http.post(url, fields, {
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    tags: { workload },
  });
}

function ownerEmail() {
  return `load-${datasetSeed}-owner@example.invalid`;
}

// Sessions come from the seeded, email-verified owner account: fresh
// signups stay pending until verification (which travels by mail, not
// HTTP), so they cannot sign in. The signup workload still covers the
// register path and its uniformity invariant.
function ensureSession(vu, iteration, state) {
  if (state.authed) {
    return true;
  }
  void vu;
  const page = http.get(`${baseURL}/login`, { tags: { workload: 'login' } });
  if (transportFailed(page, 'login', 'login page')) return false;
  if (separated(page, 'login')) return false;
  const token = csrfToken(page.body, 'login', 'login');
  if (!token) return false;
  const response = postForm(`${baseURL}/login`, { email: ownerEmail(), password: accountPassword, csrf_token: token }, 'login');
  if (transportFailed(response, 'login', 'login')) return false;
  if (separated(response, 'login')) return false;
  const session = sessionCookie(response);
  const ok = check(response, {
    'login issues a session with 303': (r) => r.status === 303 && hasSessionCookie(r) && session !== null,
  }, { kind: 'invariant', workload: 'login' });
  if (serverFailed(response, 'login')) return false;
  state.authed = ok && response.status === 303;
  if (state.authed) {
    state.session = session;
  }
  return state.authed;
}

function signup(vu, iteration, state) {
  // Duplicate registration of a known address is the idempotency
  // invariant: new or replayed, the answer stays a uniform 200.
  const workload = 'signup';
  const email = iteration % 10 === 0 ? ownerEmail() : signupEmail(vu, state.signupSerial);
  state.signupSerial += 1;
  const page = http.get(`${baseURL}/register`, { tags: { workload } });
  if (transportFailed(page, workload, 'register page')) return;
  if (separated(page, workload)) return;
  const token = csrfToken(page.body, workload, 'register');
  if (!token) return;
  const response = postForm(`${baseURL}/register`, { email, password: accountPassword, csrf_token: token }, workload);
  if (transportFailed(response, workload, 'register')) return;
  if (separated(response, workload)) return;
  check(response, {
    [`${workload} register is uniform 200`]: (r) => r.status === 200,
  }, { kind: 'invariant', workload });
  serverFailed(response, workload);
  sleep(0.1);
}

function login(vu, iteration, state) {
  const workload = 'login';
  if (!state.authed) {
    ensureSession(vu, iteration, state);
    sleep(0.05);
    return;
  }
  if (!state.loginProbed) {
    // One wrong-password probe per VU exercises the refusal path: 401
    // with no session issued, never enumeration, never 5xx. Auth writes
    // are throttled per IP by design (register 5/hour, login 10/minute),
    // so probes stay rare and 429s stay separated, never failures.
    state.loginProbed = true;
    const page = http.get(`${baseURL}/login`, { tags: { workload } });
    if (transportFailed(page, workload, 'login page')) return;
    if (separated(page, workload)) return;
    const token = csrfToken(page.body, workload, 'login');
    if (!token) return;
    const response = postForm(`${baseURL}/login`, { email: signupEmail(vu, 0), password: 'wrong-password-9', csrf_token: token }, workload);
    if (transportFailed(response, workload, 'login')) return;
    if (separated(response, workload)) return;
    check(response, {
      [`${workload} wrong password is 401 without session`]: (r) => r.status === 401 && !hasSessionCookie(r),
    }, { kind: 'invariant', workload });
    serverFailed(response, workload);
    return;
  }
  sleep(0.05);
}

function position(data, vu, iteration, state) {
  const workload = 'position';
  if (!ensureSession(vu, iteration, state)) {
    return;
  }
  const slug = data.slugs[iteration % data.slugs.length];
  const page = http.get(`${baseURL}/arenas/${slug}`, {
    headers: authedHeaders(iteration, state),
    tags: { workload },
  });
  if (transportFailed(page, workload, 'arena page')) return;
  if (separated(page, workload)) return;
  const token = csrfToken(page.body, workload, 'arena page');
  if (!token) return;
  const response = http.post(`${baseURL}/arenas/${slug}/position`, { position: 'agree', csrf_token: token }, {
    headers: authedHeaders(iteration, state, { 'Content-Type': 'application/x-www-form-urlencoded' }),
    tags: { workload },
  });
  if (transportFailed(response, workload, 'position')) return;
  if (separated(response, workload)) return;
  // The owner account is shared across VUs, and the initial position is
  // immutable: whoever confirms first gets 303, everyone after gets 409.
  // Both are correct handled outcomes; a 5xx would be the failure.
  check(response, {
    [`${workload} confirm is 303 or immutable 409`]: (r) => r.status === 303 || r.status === 409,
  }, { kind: 'invariant', workload });
  serverFailed(response, workload);
  sleep(0.1);
}

function argument(data, vu, iteration, state) {
  const workload = 'argument';
  if (!ensureSession(vu, iteration, state)) {
    return;
  }
  const slug = data.slugs[iteration % data.slugs.length];
  const page = http.get(`${baseURL}/arenas/${slug}`, {
    headers: authedHeaders(iteration, state),
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
    content: `Conteúdo sintético ${datasetSeed} ${vu} ${iteration} para medir publicação sob carga.`,
    attempt,
    csrf_token: token,
  }, {
    headers: authedHeaders(iteration, state, { 'Content-Type': 'application/x-www-form-urlencoded' }),
    tags: { workload },
  });
  if (transportFailed(response, workload, 'argument')) return;
  if (separated(response, workload)) return;
  check(response, {
    [`${workload} publish is 303 or funded-refusal 402`]: (r) => r.status === 303 || r.status === 402,
  }, { kind: 'invariant', workload });
  serverFailed(response, workload);
  sleep(0.1);
}

function webhook(iteration) {
  const workload = 'webhook';
  // The delivery surface mounts no Stripe route (settlement is proven by
  // contract tests and the drill): adversarial-shaped traffic must still
  // be handled, never a 5xx.
  const response = http.post(`${baseURL}/api/v1/webhooks/stripe`, JSON.stringify({
    id: `evt_${datasetSeed}_${iteration}`,
    type: 'checkout.session.completed',
    livemode: false,
  }), {
    headers: { 'Accept-Language': localeOf(iteration), 'Content-Type': 'application/json', 'Stripe-Signature': 'synthetic-invalid-signature' },
    tags: { workload },
  });
  if (transportFailed(response, workload, 'webhook')) return;
  if (separated(response, workload)) return;
  check(response, {
    [`${workload} adversarial traffic is handled`]: (r) => r.status >= 200 && r.status < 500,
  }, { kind: 'invariant', workload });
  serverFailed(response, workload);
  sleep(0.1);
}

function moderation(data, vu, iteration, state) {
  const workload = 'moderation';
  if (!ensureSession(vu, iteration, state)) {
    return;
  }
  // No moderation route is mounted on the delivery surface (moderation is
  // proven by contract tests): the workload guards the refusal staying
  // handled instead of degrading into a 5xx.
  const response = http.post(`${baseURL}/api/v1/me/moderation/reports`, JSON.stringify({
    target_type: 'arena',
    target_id: data.slugs[iteration % data.slugs.length],
    reason: 'spam',
  }), {
    headers: { 'Accept-Language': localeOf(iteration), 'Content-Type': 'application/json' },
    tags: { workload },
  });
  if (transportFailed(response, workload, 'report')) return;
  if (separated(response, workload)) return;
  check(response, {
    [`${workload} unmounted report stays handled`]: (r) => r.status >= 200 && r.status < 500,
  }, { kind: 'invariant', workload });
  serverFailed(response, workload);
  sleep(0.1);
}

function jobsEnqueue(iteration, state) {
  const workload = 'jobs-enqueue';
  // Password reset enqueues the email job. Ghost and real addresses share
  // the uniform answer: no enumeration, and the enqueue path is exercised
  // either way.
  // The ghost/real pair runs once per VU (reset mail is throttled at
  // 3/hour per IP); later executions alternate single requests.
  const pair = !state.resetPaired;
  if (pair) {
    state.resetPaired = true;
  }
  const emails = pair
    ? [ownerEmail(), `ghost-${datasetSeed}-${iteration}@example.invalid`]
    : [(iteration % 2 === 0 ? ownerEmail() : `ghost-${datasetSeed}-${iteration}@example.invalid`)];
  const statuses = [];
  emails.forEach((email) => {
    // Fresh token per POST: CSRF tokens are single-use, like a browser.
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

const vuState = {};

export function mixed(data) {
  const vu = __VU;
  if (!vuState[vu]) {
    vuState[vu] = { authed: false, signupSerial: vu * 100000, iteration: 0 };
  }
  const state = vuState[vu];
  const iteration = state.iteration;
  state.iteration = iteration + 1;
  const name = workloadFor(vu, iteration);
  switch (name) {
    case 'visitors':
      visitors(data, iteration);
      break;
    case 'viral':
      viral(data, iteration);
      break;
    case 'signup':
      signup(vu, iteration, state);
      break;
    case 'login':
      login(vu, iteration, state);
      break;
    case 'position':
      position(data, vu, iteration, state);
      break;
    case 'argument':
      argument(data, vu, iteration, state);
      break;
    case 'webhook':
      webhook(iteration);
      break;
    case 'moderation':
      moderation(data, vu, iteration, state);
      break;
    case 'jobs-enqueue':
      jobsEnqueue(iteration, state);
      break;
    default:
      visitors(data, iteration);
      break;
  }
}
