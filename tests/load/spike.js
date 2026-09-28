import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter } from 'k6/metrics';
import { scenario } from 'k6/execution';

// P28-T05 spike, stress and saturation point over the surfaces the delivery
// binary mounts (HTML journeys; see mix.js for why the JSON API is out of
// scope here). One ramping scenario walks four stages — baseline, sudden
// spike, gradual stress to saturation, recovery — with the same
// international rotation as the mix. Latency thresholds bind baseline and
// recovery only: spike and stress exist to characterize (which workload
// degrades first, where 429 shedding engages), never to pass SLOs while
// saturated. Two properties hold in every stage: zero 5xx / transport
// failures, and invariant shapes on every non-429 answer. teardown() runs
// one final write pass after the load drops: a system that did not recover
// fails there, not in a log nobody reads.
//
// Sessions travel as an explicit Cookie header (the VU jar does not persist
// across iterations); CSRF tokens are single-use, one GET per POST; the
// owner account is seeded, verified and funded out of band.
http.setResponseCallback(http.expectedStatuses({ min: 200, max: 499 }));

const baseURL = (__ENV.K6_BASE_URL || 'http://127.0.0.1:8080').replace(/\/$/, '');
const datasetSeed = __ENV.K6_DATASET_SEED || 'synthetic-p28-t05';
const profile = __ENV.K6_SPIKE_PROFILE || 'full';
const accountPassword = __ENV.K6_ACCOUNT_PASSWORD || 'load-correct-horse-1';

const rateLimited = new Counter('spike_rate_limited_total');
const failed = new Counter('spike_failed_total');

const locales = ['pt-BR', 'en-US'];

// Stages and k6 stage definitions come from one table so progress fractions
// never drift from the schedule the thresholds judge.
const STAGES = profile === 'sample'
  ? [
    // Two entries for baseline so its VU starts at once instead of
    // ramping in only as the window closes (same trap as mix.js sample).
    { name: 'baseline', dur: 1, target: 1 },
    { name: 'baseline', dur: 3, target: 1 },
    { name: 'spike', dur: 4, target: 5 },
    { name: 'stress', dur: 4, target: 8 },
    { name: 'recovery', dur: 4, target: 1 },
  ]
  : [
    { name: 'baseline', dur: 10, target: 2 },
    { name: 'spike', dur: 20, target: 20 },
    { name: 'stress', dur: 30, target: 40 },
    { name: 'recovery', dur: 20, target: 2 },
  ];
const TOTAL_SECS = STAGES.reduce((acc, stage) => acc + stage.dur, 0);

function stageNow() {
  const progress = scenario.progress;
  let elapsed = 0;
  for (const stage of STAGES) {
    elapsed += stage.dur / TOTAL_SECS;
    if (progress < elapsed) {
      return stage.name;
    }
  }
  return 'recovery';
}

function localeOf(iteration) {
  return locales[iteration % locales.length];
}

// Counts 429 apart: shedding under pressure is the product working, never
// a failure. Returns true when the caller should skip its checks.
function separated(response, workload, stage) {
  if (response && response.status === 429) {
    rateLimited.add(1, { workload, stage });
    return true;
  }
  return false;
}

function transportFailed(response, workload, stage, what) {
  if (!response || response.status === 0) {
    failed.add(1, { workload, stage });
    check(null, { [`${workload} ${what} reached the server`]: () => false }, { kind: 'invariant', workload, stage });
    return true;
  }
  return false;
}

function serverFailed(response, workload, stage) {
  if (response.status >= 500) {
    failed.add(1, { workload, stage });
    return true;
  }
  return false;
}

function csrfToken(document, workload, stage, what) {
  const match = /name="csrf_token" value="([^"]+)"/.exec(document);
  if (!match) {
    failed.add(1, { workload, stage });
    check(null, { [`${workload} ${what} page carries csrf_token`]: () => false }, { kind: 'invariant', workload, stage });
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

function authedHeaders(stage, iteration, state, extra) {
  return Object.assign(
    { 'Accept-Language': localeOf(iteration), Cookie: state.session },
    extra || {},
  );
}

// Same weighted rotation as the mix: public reads dominate, writes
// interleave. Deterministic per VU and iteration.
const rotation = [
  'visitors', 'visitors', 'viral', 'visitors',
  'signup', 'login', 'position', 'argument',
  'webhook', 'moderation', 'jobs-enqueue', 'viral',
];

function workloadFor(vu, iteration) {
  return rotation[(vu + iteration) % rotation.length];
}

function signupEmail(vu, serial) {
  return `load-${datasetSeed}-${vu}-${serial}@example.invalid`;
}

function ownerEmail() {
  return `load-${datasetSeed}-owner@example.invalid`;
}

function postForm(url, fields, workload, stage) {
  return http.post(url, fields, {
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    tags: { workload, stage },
  });
}

export const options = {
  maxRedirects: 0,
  scenarios: {
    spike: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: STAGES.map((stage) => ({ duration: `${stage.dur}s`, target: stage.target })),
      exec: 'saturated',
    },
  },
  thresholds: {
    spike_failed_total: ['count==0'],
    "checks{kind:invariant}": ['rate==1'],
    'http_req_duration{workload:visitors,stage:baseline}': ['p(95)<500'],
    'http_req_duration{workload:visitors,stage:recovery}': ['p(95)<500'],
    'http_req_duration{workload:viral,stage:baseline}': ['p(95)<500'],
    'http_req_duration{workload:viral,stage:recovery}': ['p(95)<500'],
    'http_req_duration{workload:signup,stage:baseline}': ['p(95)<1500'],
    'http_req_duration{workload:signup,stage:recovery}': ['p(95)<1500'],
    'http_req_duration{workload:login,stage:baseline}': ['p(95)<1500'],
    'http_req_duration{workload:login,stage:recovery}': ['p(95)<1500'],
    'http_req_duration{workload:position,stage:baseline}': ['p(95)<1000'],
    'http_req_duration{workload:position,stage:recovery}': ['p(95)<1000'],
    'http_req_duration{workload:argument,stage:baseline}': ['p(95)<1000'],
    'http_req_duration{workload:argument,stage:recovery}': ['p(95)<1000'],
    'http_req_duration{workload:webhook,stage:baseline}': ['p(95)<1000'],
    'http_req_duration{workload:webhook,stage:recovery}': ['p(95)<1000'],
    'http_req_duration{workload:moderation,stage:baseline}': ['p(95)<1000'],
    'http_req_duration{workload:moderation,stage:recovery}': ['p(95)<1000'],
    'http_req_duration{workload:jobs-enqueue,stage:baseline}': ['p(95)<1000'],
    'http_req_duration{workload:jobs-enqueue,stage:recovery}': ['p(95)<1000'],
  },
};

export function setup() {
  const live = http.get(`${baseURL}/health/live`);
  if (!live || live.status !== 200) {
    throw new Error('setup GET /health/live: unreachable');
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

function ensureSession(vu, iteration, state, stage) {
  if (state.authed) {
    return true;
  }
  void vu;
  const page = http.get(`${baseURL}/login`, { tags: { workload: 'login', stage } });
  if (transportFailed(page, 'login', stage, 'login page')) return false;
  if (separated(page, 'login', stage)) return false;
  const token = csrfToken(page.body, 'login', stage, 'login');
  if (!token) return false;
  const response = postForm(`${baseURL}/login`, { email: ownerEmail(), password: accountPassword, csrf_token: token }, 'login', stage);
  if (transportFailed(response, 'login', stage, 'login')) return false;
  if (separated(response, 'login', stage)) return false;
  const session = sessionCookie(response);
  const ok = check(response, {
    'login issues a session with 303': (r) => r.status === 303 && hasSessionCookie(r) && session !== null,
  }, { kind: 'invariant', workload: 'login', stage });
  if (serverFailed(response, 'login', stage)) return false;
  state.authed = ok && response.status === 303;
  if (state.authed) {
    state.session = session;
  }
  return state.authed;
}

function visitors(data, iteration, stage) {
  const workload = 'visitors';
  const slug = data.slugs[iteration % data.slugs.length];
  const response = http.get(`${baseURL}/arenas/${slug}`, {
    headers: { 'Accept-Language': localeOf(iteration) },
    tags: { workload, stage },
  });
  if (transportFailed(response, workload, stage, 'arena')) return;
  if (separated(response, workload, stage)) return;
  check(response, {
    [`${workload} arena page is 200 with its slug`]: (r) => r.status === 200 && r.body.includes(slug),
  }, { kind: 'invariant', workload, stage });
  serverFailed(response, workload, stage);
  sleep(0.1);
}

function viral(data, iteration, stage) {
  const workload = 'viral';
  const slug = data.slugs[(iteration + 1) % data.slugs.length];
  const response = http.get(`${baseURL}/arenas/${slug}`, {
    headers: { 'Accept-Language': localeOf(iteration) },
    tags: { workload, stage },
  });
  if (transportFailed(response, workload, stage, 'arena')) return;
  if (separated(response, workload, stage)) return;
  check(response, {
    [`${workload} fan-out answers its slug`]: (r) => r.status === 200 && r.body.includes(slug),
  }, { kind: 'invariant', workload, stage });
  serverFailed(response, workload, stage);
  sleep(0.05);
}

function signup(vu, iteration, state, stage) {
  const workload = 'signup';
  const email = iteration % 10 === 0 ? ownerEmail() : signupEmail(vu, state.signupSerial);
  state.signupSerial += 1;
  const page = http.get(`${baseURL}/register`, { tags: { workload, stage } });
  if (transportFailed(page, workload, stage, 'register page')) return;
  if (separated(page, workload, stage)) return;
  const token = csrfToken(page.body, workload, stage, 'register');
  if (!token) return;
  const response = postForm(`${baseURL}/register`, { email, password: accountPassword, csrf_token: token }, workload, stage);
  if (transportFailed(response, workload, stage, 'register')) return;
  if (separated(response, workload, stage)) return;
  check(response, {
    [`${workload} register is uniform 200`]: (r) => r.status === 200,
  }, { kind: 'invariant', workload, stage });
  serverFailed(response, workload, stage);
  sleep(0.1);
}

function login(vu, iteration, state, stage) {
  const workload = 'login';
  if (!state.authed) {
    ensureSession(vu, iteration, state, stage);
    sleep(0.05);
    return;
  }
  if (!state.loginProbed) {
    state.loginProbed = true;
    const page = http.get(`${baseURL}/login`, { tags: { workload, stage } });
    if (transportFailed(page, workload, stage, 'login page')) return;
    if (separated(page, workload, stage)) return;
    const token = csrfToken(page.body, workload, stage, 'login');
    if (!token) return;
    const response = postForm(`${baseURL}/login`, { email: signupEmail(vu, 0), password: 'wrong-password-9', csrf_token: token }, workload, stage);
    if (transportFailed(response, workload, stage, 'login')) return;
    if (separated(response, workload, stage)) return;
    check(response, {
      [`${workload} wrong password is 401 without session`]: (r) => r.status === 401 && !hasSessionCookie(r),
    }, { kind: 'invariant', workload, stage });
    serverFailed(response, workload, stage);
    return;
  }
  sleep(0.05);
}

function position(data, vu, iteration, state, stage) {
  const workload = 'position';
  if (!ensureSession(vu, iteration, state, stage)) {
    return;
  }
  const slug = data.slugs[iteration % data.slugs.length];
  const page = http.get(`${baseURL}/arenas/${slug}`, {
    headers: authedHeaders(stage, iteration, state),
    tags: { workload, stage },
  });
  if (transportFailed(page, workload, stage, 'arena page')) return;
  if (separated(page, workload, stage)) return;
  const token = csrfToken(page.body, workload, stage, 'arena page');
  if (!token) return;
  const response = http.post(`${baseURL}/arenas/${slug}/position`, { position: 'agree', csrf_token: token }, {
    headers: authedHeaders(stage, iteration, state, { 'Content-Type': 'application/x-www-form-urlencoded' }),
    tags: { workload, stage },
  });
  if (transportFailed(response, workload, stage, 'position')) return;
  if (separated(response, workload, stage)) return;
  // The owner account is shared across VUs, and the initial position is
  // immutable: whoever confirms first gets 303, everyone after gets 409.
  check(response, {
    [`${workload} confirm is 303 or immutable 409`]: (r) => r.status === 303 || r.status === 409,
  }, { kind: 'invariant', workload, stage });
  serverFailed(response, workload, stage);
  sleep(0.1);
}

function argument(data, vu, iteration, state, stage) {
  const workload = 'argument';
  if (!ensureSession(vu, iteration, state, stage)) {
    return;
  }
  const slug = data.slugs[iteration % data.slugs.length];
  const page = http.get(`${baseURL}/arenas/${slug}`, {
    headers: authedHeaders(stage, iteration, state),
    tags: { workload, stage },
  });
  if (transportFailed(page, workload, stage, 'arena page')) return;
  if (separated(page, workload, stage)) return;
  const token = csrfToken(page.body, workload, stage, 'arena page');
  if (!token) return;
  const attempt = hiddenValue(page.body, 'attempt');
  if (!attempt) {
    failed.add(1, { workload, stage });
    check(null, { [`${workload} page carries attempt key`]: () => false }, { kind: 'invariant', workload, stage });
    return;
  }
  const response = http.post(`${baseURL}/arenas/${slug}/arguments`, {
    relation: ['support', 'oppose', 'context'][iteration % 3],
    content: `Conteúdo sintético ${datasetSeed} ${vu} ${iteration} para medir publicação sob carga.`,
    attempt,
    csrf_token: token,
  }, {
    headers: authedHeaders(stage, iteration, state, { 'Content-Type': 'application/x-www-form-urlencoded' }),
    tags: { workload, stage },
  });
  if (transportFailed(response, workload, stage, 'argument')) return;
  if (separated(response, workload, stage)) return;
  check(response, {
    [`${workload} publish is 303 or funded-refusal 402`]: (r) => r.status === 303 || r.status === 402,
  }, { kind: 'invariant', workload, stage });
  serverFailed(response, workload, stage);
  sleep(0.1);
}

function webhook(iteration, stage) {
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
    tags: { workload, stage },
  });
  if (transportFailed(response, workload, stage, 'webhook')) return;
  if (separated(response, workload, stage)) return;
  check(response, {
    [`${workload} adversarial traffic is handled`]: (r) => r.status >= 200 && r.status < 500,
  }, { kind: 'invariant', workload, stage });
  serverFailed(response, workload, stage);
  sleep(0.1);
}

function moderation(data, vu, iteration, state, stage) {
  const workload = 'moderation';
  if (!ensureSession(vu, iteration, state, stage)) {
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
    tags: { workload, stage },
  });
  if (transportFailed(response, workload, stage, 'report')) return;
  if (separated(response, workload, stage)) return;
  check(response, {
    [`${workload} unmounted report stays handled`]: (r) => r.status >= 200 && r.status < 500,
  }, { kind: 'invariant', workload, stage });
  serverFailed(response, workload, stage);
  sleep(0.1);
}

function jobsEnqueue(iteration, state, stage) {
  const workload = 'jobs-enqueue';
  // Password reset enqueues the email job. Ghost and real addresses share
  // the uniform answer: no enumeration, and the enqueue path is exercised
  // either way. The ghost/real pair runs once per VU (reset mail is
  // throttled at 3/hour per IP); later executions alternate singles.
  const pair = !state.resetPaired;
  if (pair) {
    state.resetPaired = true;
  }
  const emails = pair
    ? [ownerEmail(), `ghost-${datasetSeed}-${iteration}@example.invalid`]
    : [(iteration % 2 === 0 ? ownerEmail() : `ghost-${datasetSeed}-${iteration}@example.invalid`)];
  const statuses = [];
  emails.forEach((email) => {
    const page = http.get(`${baseURL}/reset`, { tags: { workload, stage } });
    if (transportFailed(page, workload, stage, 'reset page')) {
      statuses.push(0);
      return;
    }
    if (separated(page, workload, stage)) {
      statuses.push(429);
      return;
    }
    const token = csrfToken(page.body, workload, stage, 'reset page');
    if (!token) {
      statuses.push(0);
      return;
    }
    const response = postForm(`${baseURL}/reset`, { email, csrf_token: token }, workload, stage);
    if (transportFailed(response, workload, stage, 'reset')) {
      statuses.push(0);
      return;
    }
    if (separated(response, workload, stage)) {
      statuses.push(429);
      return;
    }
    statuses.push(response.status);
    serverFailed(response, workload, stage);
  });
  if (pair && statuses.indexOf(0) === -1 && statuses.indexOf(429) === -1) {
    check({ first: statuses[0], second: statuses[1] }, {
      [`${workload} reset answer is uniform`]: (answers) => answers.first === answers.second && answers.first >= 200 && answers.first < 500,
    }, { kind: 'invariant', workload, stage });
  }
  sleep(0.1);
}

const vuState = {};

export function saturated(data) {
  const vu = __VU;
  if (!vuState[vu]) {
    vuState[vu] = { authed: false, signupSerial: vu * 100000, iteration: 0 };
  }
  const state = vuState[vu];
  const iteration = state.iteration;
  state.iteration = iteration + 1;
  const stage = stageNow();
  const name = workloadFor(vu, iteration);
  switch (name) {
    case 'visitors':
      visitors(data, iteration, stage);
      break;
    case 'viral':
      viral(data, iteration, stage);
      break;
    case 'signup':
      signup(vu, iteration, state, stage);
      break;
    case 'login':
      login(vu, iteration, state, stage);
      break;
    case 'position':
      position(data, vu, iteration, state, stage);
      break;
    case 'argument':
      argument(data, vu, iteration, state, stage);
      break;
    case 'webhook':
      webhook(iteration, stage);
      break;
    case 'moderation':
      moderation(data, vu, iteration, state, stage);
      break;
    case 'jobs-enqueue':
      jobsEnqueue(iteration, state, stage);
      break;
    default:
      visitors(data, iteration, stage);
      break;
  }
}

export function teardown(data) {
  // Post-load verification with a fresh session: the write path answers
  // 303 after the storm, which is what "the system recovered" means here.
  // Any failure aborts teardown and fails the run.
  const page = http.get(`${baseURL}/login`, { tags: { workload: 'teardown' } });
  if (!page || page.status !== 200) {
    throw new Error(`teardown login page: status ${page && page.status}`);
  }
  // The storm trips the login throttle (10/minute per IP) by design, so
  // teardown retries with backoff instead of demanding the first attempt:
  // bounded patience, never an unbounded loop, and 5xx still aborts at
  // once through the checks below.
  let login = null;
  for (let attempt = 0; attempt < 6; attempt += 1) {
    const candidatePage = attempt === 0 ? page : http.get(`${baseURL}/login`, { tags: { workload: 'teardown' } });
    if (!candidatePage || candidatePage.status === 0) {
      throw new Error('teardown login page unreachable');
    }
    const candidateToken = csrfToken(candidatePage.body, 'teardown', 'teardown', 'login');
    if (!candidateToken) {
      throw new Error('teardown login page carries no csrf_token');
    }
    const candidate = postForm(`${baseURL}/login`, { email: ownerEmail(), password: accountPassword, csrf_token: candidateToken }, 'teardown', 'teardown');
    if (!candidate || candidate.status === 0 || candidate.status >= 500) {
      throw new Error(`teardown login: status ${candidate && candidate.status}`);
    }
    if (candidate.status === 303) {
      login = candidate;
      break;
    }
    if (candidate.status !== 429) {
      throw new Error(`teardown login: status ${candidate.status}, want 303 or throttled 429`);
    }
    sleep(5);
  }
  if (!login) {
    throw new Error('teardown login still throttled after bounded retries');
  }
  const session = sessionCookie(login);
  if (!session) {
    throw new Error('teardown login issued no session');
  }
  const slug = data.slugs[0];
  const arena = http.get(`${baseURL}/arenas/${slug}`, {
    headers: { Cookie: session },
    tags: { workload: 'teardown' },
  });
  if (!arena || arena.status !== 200 || !arena.body.includes(slug)) {
    throw new Error(`teardown arena page: status ${arena && arena.status}`);
  }
  const attempt = hiddenValue(arena.body, 'attempt');
  const formToken = csrfToken(arena.body, 'teardown', 'teardown', 'arena page');
  if (!attempt || !formToken) {
    throw new Error('teardown arena page carries no attempt key');
  }
  const published = http.post(`${baseURL}/arenas/${slug}/arguments`, {
    relation: 'support',
    content: `Teardown sintético ${datasetSeed}: a escrita sobreviveu à saturação.`,
    attempt,
    csrf_token: formToken,
  }, {
    headers: { Cookie: session, 'Content-Type': 'application/x-www-form-urlencoded' },
    tags: { workload: 'teardown' },
  });
  if (!published || published.status !== 303) {
    throw new Error(`teardown publish: status ${published && published.status}`);
  }
}
