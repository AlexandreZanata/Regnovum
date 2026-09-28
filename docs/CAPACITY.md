# Backend capacity — baseline v1 (P28-T08)

Versioned report of what the backend can sustain, on which hardware, judged
by which thresholds. The machine-readable twin is
`quality/capacity-baseline.json`; the gate `tools/capacityratchet` refuses
any tree whose live thresholds, budgets, ceilings or product bounds drift
from it. Evidence template: measured numbers, pinned commit, hardware,
dataset, seed, toolchain, sample window and the comparable baseline.

Provenance: thresholds, budgets, ceilings and bounds read from the tree
at commit `687458c` on hardware class `dev-i7-13620H-16cpu-31GB`; this
commit only adds the baseline, the report and the gate without touching
any judged source. No earlier capacity baseline exists, so this report
founds the series instead of comparing against one.

## Environment

| Axis       | Value                                                        |
| ---------- | ------------------------------------------------------------ |
| Hardware   | `dev-i7-13620H-16cpu-31GB` — 16 vCPU i7-13620H, 31 GiB RAM   |
| OS / arch  | `linux` / `amd64`                                            |
| Go         | `go1.27.1`                                                   |
| k6         | `k6 v1.8.1`                                                  |
| PostgreSQL | `18.4` (`compose.production.yaml` image)                     |
| Go dataset | Deterministic in-repo fixtures (`budget`/`payload` tests)    |
| k6 seeds   | `synthetic-p28-t04` (mix), `synthetic-p28-t05` (spike), `synthetic-p28-t06` (hotkeys) |

## Load scenarios (k6, `tests/load/`)

All scenarios seed deterministically per VU and iteration, so a seed
replays the same mix; every run judges `*_failed_total: ['count==0']` and
`"checks{kind:invariant}": ['rate==1']` as globals.

| Scenario | File | Exec | Full profile |
| -------- | ---- | ---- | ------------ |
| Production mix | `mix.js` | `mixed` | 4 VUs: 20 s ramp/hold, 30 s hold, 10 s down; 12-slot rotation weighted to public reads |
| Saturation and recovery | `spike.js` | `saturated` | baseline 2 VUs/10 s, spike 20 VUs/20 s, stress 40 VUs/30 s, recovery 2 VUs/20 s |
| Hot-key contention | `hotkeys.js` | `contended` | 10 VUs: 15 s ramp, 40 s hold, 5 s down; every VU hammers the same wallet and arena |

Sample profiles (`K6_*_PROFILE=sample`) run one VU for seconds and judge
the same thresholds; they prove wiring, not capacity.

### p95 budgets (ms)

Mix (`mix.js`, untagged workloads):

| Workload | p95 |
| -------- | --- |
| visitors, viral | 500 |
| position, argument, webhook, moderation, jobs-enqueue | 1000 |
| signup, login | 1500 |

Spike (`spike.js`): the same 9 workloads (`visitors`, `viral`, `signup`,
`login`, `position`, `argument`, `webhook`, `moderation`, `jobs-enqueue`)
judged twice — `stage:baseline` and `stage:recovery` — with the same bounds
(500 / 1000 / 1500 ms). Recovery must answer like baseline after the
40-VU stress; `teardown()` additionally runs a fresh-session write pass
that must answer 303, which is what "the system recovered" means here.

Hot keys (`hotkeys.js`, `hot-` workloads):

| Workload | p95 |
| -------- | --- |
| hot-visitors | 500 |
| hot-wallet, hot-attempt, hot-arena, hot-aggregate, hot-webhook, hot-jobs | 1000 |
| hot-login | 1500 |

Contention converges by construction: the race publishes into one fixed
key with a closed content set (`alpha`, `beta`, `gamma`), so exactly one
of them may exist; `teardown()` reconciles without shared VU state and
spot-checks per-account positions.

## Native budgets (Go, `internal/performance/budget_test.go`)

| Operation | Median | Allocs |
| --------- | ------ | ------ |
| argon2-verify | 150 ms | 70 |
| grapheme-count | 5 µs | 0 |
| export-json-encode | 15 µs | 8 |
| ratelimit-policy-for | 200 ns | 2 |
| httpcache-validator | 1.5 µs | 6 |
| wallet-allocate-debit | 150 ns | 2 |
| i18n-format | 800 ns | 4 |
| derive-current-position | 800 ns | 5 |

The write path is dominated by `argon2-verify` by design: password
hashing costs ~150 ms per verification while every other hot operation
stays in the microsecond range. Capacity planning must count concurrent
verifications, not average handler time.

## Payload ceilings (Go, `internal/performance/payload_test.go`)

| Constant | Ceiling |
| -------- | ------- |
| `maxFeedPageBytes` | 512 KiB |
| `maxArgumentsPageBytes` | 512 KiB |
| `maxStatementPageBytes` | 128 KiB |
| `maxExportDocBytes` | 2 MiB |

## Product bounds (page limits)

| Constant | Bound |
| -------- | ----- |
| `internal/arenas/application/feed.go:MaxFeedLimit` | 100 |
| `internal/arguments/application/public_arguments.go:MaxArgumentPageLimit` | 100 |
| `internal/wallet/application/queries.go:MaxStatementLimit` | 100 |

## Saturation, headroom and recommendations

- Saturation schedule: the spike profile climbs 2 → 20 → 40 VUs and
  returns to 2; the baseline/recovery threshold pairs above are the
  verdict on whether the climb left damage. Full-profile k6 runs are a
  release gate (P30/P45); the short `sample` runs in CI prove wiring
  only.
- Headroom: every p95 bound above matches the threshold the scenario
  judges — headroom is therefore defined, not observed: any future
  full-profile run reproduces the comparison frame pinned here, and the
  ratchet below fails the build on any silent loosening.
- Recommendations: (1) keep password verifications off the hot read
  path — at ~150 ms each they are the first resource to saturate under
  login bursts; (2) signup stays gated at 3/hour per IP, which caps the
  most expensive write mix; (3) re-baseline on any hardware change
  instead of comparing across machines.

## Reproduce

```bash
K6_MIX_PROFILE=sample go run github.com/grafana/k6/cmd/k6 run tests/load/mix.js
K6_SPIKE_PROFILE=sample go run github.com/grafana/k6/cmd/k6 run tests/load/spike.js
K6_HOTKEYS_PROFILE=sample go run github.com/grafana/k6/cmd/k6 run tests/load/hotkeys.js
go test ./internal/performance/ -run 'TestHot|TestPayload' -count=1
go run ./tools/capacityratchet -root .
```

## Ratchet

`go run ./tools/capacityratchet -root .` re-reads every threshold,
budget, ceiling and product bound from the tree and compares them with
`quality/capacity-baseline.json` (schema
`quality/capacity-baseline.schema.json`, tolerance `0`):

- `k6-drift`, `budget-drift`, `bytes-drift`, `product-drift`: any
  loosened, tightened, vanished or unlisted bound.
- `bad-baseline`: unreadable/missing source, unknown envelope or
  negative tolerance.
- `bad-schema-file`: the schema file stopped describing the loader.
- `hardware-mismatch`: baseline measured on another OS/arch — re-baseline
  instead of comparing.
- `stale-report`: `docs/CAPACITY.md` cites neither the pinned commit nor
  the hardware class.

Exit `0` holds the baseline, `1` lists every violation, `2` is a usage
error. The command never rewrites the baseline or the report; moving a
bound requires a human commit that moves both files together.
