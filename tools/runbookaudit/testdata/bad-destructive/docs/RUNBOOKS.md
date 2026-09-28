# Runbooks (fixture)

## R1 — Fixture alert

Safe commands only:

```bash
curl --silent "http://127.0.0.1:9090/metrics" | grep '^http_requests_total'
deploy/ok.sh
rm -rf /data/probe
make ok-target
docker compose -f compose.fixture.yaml ps
```

See [OTHER.md](OTHER.md) and [the alert](#r1--fixture-alert).
