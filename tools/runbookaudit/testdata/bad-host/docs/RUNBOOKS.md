# Runbooks (fixture)

## R1 — Fixture alert

Safe commands only:

```bash
curl --silent "http://9.9.9.9/metrics" | grep '^http_requests_total'
deploy/ok.sh
make ok-target
docker compose -f compose.fixture.yaml ps
```

See [OTHER.md](OTHER.md) and [the alert](#r1--fixture-alert).
