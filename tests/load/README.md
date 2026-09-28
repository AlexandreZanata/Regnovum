# P17-T06 load smoke

`smoke.js` is the versioned, synthetic workload for the first capacity baseline. It
runs these workloads in separate windows so the report can identify their
latencies without mixing budgets:

- public Arena cache cold/hot reads;
- login;
- position read;
- argument plus wallet read;
- Stripe webhook replay handling;
- viral Arena read fan-out.

The dataset is selected by `K6_DATASET_SEED` and resource identifiers are passed
explicitly. No production account, token, webhook secret, or fixture is stored
in this repository.

Run against a local instance prepared with synthetic data only:

```bash
K6_BASE_URL=http://127.0.0.1:8080 \
K6_DATASET_SEED=synthetic-p17-t06 \
K6_ARENA_ID=<synthetic-arena-id> \
K6_ACCOUNT_EMAIL=load-test@example.invalid \
K6_ACCOUNT_PASSWORD='<synthetic-password>' \
make test-load-smoke
```

The command records the current commit, host/kernel/CPU information, the
configured dataset seed, and k6's JSON summary. Thresholds are intentionally
modest first-stage baselines; they are not a capacity promise.

# P28-T04 usage-model mix

`mix.js` drives the HTML journeys the delivery binary mounts (the JSON API
is contract-tested, not served) in one ramping international mix: visitors,
signup/login, viral Arena, position, argument, webhook, moderation and
jobs-enqueue, with `Accept-Language: pt-BR/en-US` rotation, deterministic
`load-<seed>-<vu>-<serial>@example.invalid` identities and fresh CSRF tokens
per mutation (single-use, like a browser). Thresholds apply the SLO
commitments from `docs/SLO.md`.

Responses validate invariants, not just status: the arena page answers its
slug, owner login issues `arena_session` with 303 (wrong password is 401
with no session), position confirm is 303 or immutable 409 (the owner is
shared), argument publish carries the server-issued `attempt` key and
answers 303 (or funded-refusal 402), duplicate signup stays a uniform 200,
password-reset answers ghost and real addresses identically. Sessions come
from the seeded verified owner: fresh signups stay pending until email
verification (which travels by mail, not HTTP), so they cannot sign in —
the signup workload covers the register path, not sessions. HTTP 429 is
counted apart in `mix_rate_limited_total`: expected throttling is
backpressure working, never a failure; `mix_failed_total` must stay zero
and every invariant check must pass. Auth writes are throttled per IP by
design (register 5/hour, login 10/minute, reset 3/hour), so probes stay
rare and a fresh server process means fresh windows, exactly like Go tests
with their per-test servers.

Jobs have no public HTTP surface by design, so the jobs workload covers the
enqueue paths; queue depth and lag belong to T06/T08. The delivery surface
mounts no Stripe webhook route and no moderation route (both proven by
contract tests and the drill), so those workloads guard adversarial-shaped
traffic staying handled, never 5xx.

Profiles select duration: `K6_MIX_PROFILE=sample` runs one VU for seconds
(functional sample for the creating task); the default `full` ramps 0→4 VUs
over 20s, holds 30s and ramps down in 10s. Prolonged load stays a P30
version gate.

```bash
go run ./tools/e2e/database create \
  --dsn "postgres://arena:arena-local-dev@127.0.0.1:54329/arena?sslmode=disable" \
  --name arena_e2e_load_t04
export ARENA_DATABASE_URL="postgres://arena:arena-local-dev@127.0.0.1:54329/arena_e2e_load_t04?sslmode=disable"
go run ./tools/e2e/seed account --email "load-<seed>-owner@example.invalid" \
  --password '<synthetic-password>' --ink 100000
go run ./tools/e2e/seed arena --slug <slug> --creator-email "load-<seed>-owner@example.invalid" \
  --statement 'Synthetic statement'
# Confirm one position for the owner through the HTML journey once (argument
# publish requires it); then start a fresh server process so throttle
# windows start clean, exactly like Go tests with per-test servers.
K6_BASE_URL=http://127.0.0.1:8099 \
K6_DATASET_SEED=<seed> \
K6_ARENA_SLUGS=<slug>[,<slug2>] \
K6_MIX_PROFILE=sample \
k6 run tests/load/mix.js
go run ./tools/e2e/database drop \
  --dsn "postgres://arena:arena-local-dev@127.0.0.1:54329/arena?sslmode=disable" \
  --name arena_e2e_load_t04
```

Seed and run against a disposable database only, dropped after. No
production account, token, webhook secret, or fixture is stored in this
repository; signup emails are synthetic and deterministic per seed.
Funding is generous on purpose (re-seeding an account never double-credits:
the seed idempotency key resolves the first grant), so publishes stay on
the 303 path while 402/409 remain accepted refusal shapes.

# P28-T05 spike, stress and saturation point

`spike.js` walks four stages in one ramping scenario — baseline, sudden
spike, gradual stress to saturation, recovery — with the mix rotation,
per-stage request tags and `teardown()` verification. Latency thresholds
bind baseline and recovery only (same SLO numbers as the mix); spike and
stress exist to characterize, never to pass SLOs while saturated. Two
properties hold in every stage: `spike_failed_total` stays zero (no 5xx,
no transport failure) and every invariant check passes. `teardown()` logs
in fresh, reads the arena and publishes one argument expecting 303: a
system that did not recover fails there.

Read the report for the bottleneck by evidence: per-workload p95 in
spike/stress plus `spike_rate_limited_total` show where shedding engages
(expected: auth writes throttle per IP first — register 5/hour, login
10/minute, reset 3/hour — while reads saturate on pool/CPU). Auth writes
beyond those budgets 429 by design and stay separated from failures;
late-ramp VUs may never authenticate and run unauthenticated, which is
what a visitor surge looks like.

`K6_SPIKE_PROFILE=sample` runs ~16s up to 8 VUs (functional sample for
the creating task); the default `full` runs 80s up to 40 VUs. Prolonged
saturation stays a P30 version gate; single-IP runs measure per-IP
throttle behavior plus read capacity, never distributed capacity.

# P28-T06 hot-key contention

`hotkeys.js` hammers shared hot spots under constant concurrency: one
wallet (owner publishes with distinct attempt keys), one fixed attempt key
raced by every VU (only the winner may ever exist), one arena for position
confirms from distinct seeded accounts, duplicate webhooks, enqueue
idempotency, and aggregate reads with `?reveal=1`. Job leases have no HTTP
surface and stay covered by Go tests (P25-T05 lease + cancellation,
P27-T06 reclaim and idempotent redelivery); k6 covers the enqueue side.

Reconciliation is immediate and window-safe (per-relation listings show the
newest 20, so a later re-read could miss evicted rows): every verified
write is re-read in the same iteration — 303-published contents must appear
exactly once, refused contents zero times. `teardown()` reconciles without
shared VU state: no hotkey content may ever appear twice; the closed
3-content race set shows at most one content (exactly one while the window
is not full); positions are spot-checked per account (owner + v1..v3 must
render `Posição atual:`); a revealed aggregate must sum at least those four
(a suppressed one carries the note instead); one final fresh-key publish
expects 303 (402 names an exhausted owner pot). Thresholds: SLO p95 per
workload, `hotkey_failed_total` zero, every invariant check green.

Dataset: funded owner, `K6_HOTKEY_ACCOUNTS` verified accounts (default 10;
aggregates reveal at 10 participants), positioned owner, published arenas —
all via `tools/e2e/seed`. `K6_HOTKEYS_PROFILE=sample` runs ~12s with 2 VUs;
default `full` ramps 0→10 VUs, holds 40s and ramps down. Sessions travel as
an explicit `Cookie` header (the VU jar does not persist across
iterations); CSRF tokens are single-use, one GET per POST. Reruns demand a
fresh disposable database: contents are deterministic per seed, so rows
from a previous run would read as duplicates. Teardown retries throttled
logins and publishes with backoff (bounded, fresh CSRF per attempt —
the middleware consumes the token before the throttle refuses).
