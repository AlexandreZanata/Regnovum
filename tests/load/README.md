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
