# Prontidão da certificação do backend

**Status: NÃO CERTIFICADO.** Este documento declara prontidão do
*mecanismo*, não certificação do produto. Nenhuma tag, release ou
execução integral foi realizada; `BACKEND_CERTIFICATION.md` não existe e
só passa a existir na P45, após todas as fases atuais. Ler este arquivo
como certificado é ler errado de propósito.

**Referências:** [QUALITY_CHARTER.md](QUALITY_CHARTER.md) · [OPERATIONS.md](OPERATIONS.md) · [tiers.json](../../quality/tiers.json) · [gate-controls.json](../../quality/gate-controls.json) · [RUNBOOKS.md](../RUNBOOKS.md) · [CI.md](../CI.md)

---

## 1. Gates previstos (Q0/Q1 com owner, comando e evidência)

Todo gate abaixo existe, roda e tem dono por papel. `Owner` nunca é nome
de pessoa ([OPERATIONS.md](OPERATIONS.md) §3).

| # | Gate | Risco | Comando | Owner | Evidência esperada | Custo (tier) |
|---|---|---|---|---|---|---|
| 1 | formatting | Q1 | `make fmt-check` | setor de qualidade | saída do job | fast |
| 2 | static analysis | Q0/Q1 | `make lint` | setor de qualidade | `quality/lint-baseline.json` | main |
| 3 | complexity, duplication and size | Q1 | `make audit-complexity` | setor de qualidade | `quality/complexity-baseline.json` | main |
| 4 | dead code, placeholders | Q1 | `make audit-deadcode` | setor de qualidade | saída + fixtures | main |
| 5 | errors, contexts, resources | Q0 | `make audit-errors` | setor de qualidade | saída + fixtures | main |
| 6 | generated-artifact provenance | Q1 | `make audit-provenance` | setor de qualidade | `quality/provenance.json` | main |
| 7 | test quality | Q0/Q1 | `make audit-tests` | setor de qualidade | `quality/test-waivers.json` | main |
| 8 | production change evidence | Q0 | `make audit-diff` | setor de qualidade | `quality/diff-policy.json` | main |
| 9 | dependency provenance | Q0 | `make audit-deps` | setor de qualidade | `quality/dependencies.json`, `quality/sbom.json` | main |
| 10 | mutation testing | Q0 | `make audit-mutations` | setor de qualidade | `quality/mutations.json` | weekly |
| 11 | coverage floors | Q0/Q1 | `make audit-coverage` | setor de qualidade | `quality/coverage.json`, `quality/coverage-floors.json` | weekly |
| 12 | generated-artifact drift | Q1 | `make generate-check` | setor de qualidade | sqlc, contracts, i18n regenerados | main |
| 13 | unit tests | Q0/Q1 | `make test-unit` | mantenedor do módulo + setor | bundle de evidências | nightly |
| 14 | PostgreSQL integration | Q0 | `make test-integration` | mantenedor do módulo + setor | bundle de evidências (PG real) | nightly |
| 15 | race detector | Q0 | `make test-race` | mantenedor do módulo + setor | bundle (`-race` limpo) | weekly |
| 16 | migrations | Q0 | `make test-migration` | plataforma + setor | `testdata/schema-manifest.json` | weekly |
| 17 | OpenAPI contract | Q0/Q1 | `make test-contract` | contrato + setor | `testdata/openapi.baseline.json` | nightly |
| 18 | security regressions | Q0 | `make test-security` | segurança + setor | bundle (25 pacotes da matriz) | nightly |
| 19 | frontend build | Q1 | `make test-web` | web + setor | `web/dist` testado | weekly |
| 20 | frontend typecheck | Q1 | `make typecheck` | web + setor | `tsc --noEmit` | weekly |
| 21 | frontend measurement | Q1 | `make audit-web` | web + setor | `web/dist` medido | weekly |
| 22 | interface language | Q1 | `make audit-i18n` | i18n + setor | catálogos pt/en | main |
| 23 | browser journeys | Q0/Q1 | `make test-e2e` | jornadas + setor | jornadas verdes | release |
| 24 | dependency vulnerabilities | Q0 | `make vuln` | supply chain + setor | scan sem critical/high | release |
| 25 | production image | Q0 | `make image-verify` | operações + setor | imagem distroless auditada | release |
| 26 | production image scan | Q0 | `make image-scan` | supply chain + setor | scan da imagem | release |
| 27 | ingress configuration | Q0 | `make caddy-verify` | operações + setor | Caddy verificado | release |
| 28 | production topology | Q0 | `make compose-verify` | operações + setor | compose verificado | release |
| 29 | backup and PITR | Q0 | `make backup-verify` | operações + setor | restore + PITR provados | release |
| 30 | deploy and rollback | Q0 | `make deploy-verify` | operações + setor | deploy/rollback provados | release |
| 31 | aggregate verification | Q0/Q1 | `make verify` | setor de qualidade | todos os acima | release |

Portões de decisão humana (`release-gate`, `security-audit`,
`privacy-audit`, `release-verify`) não estão na tabela porque não são
automatizáveis: eles acontecem na P45 com gente, não com comando.

## 2. Comandos canônicos

As camadas de [tiers.json](../../quality/tiers.json), espelhadas por
`quality-fast`, `quality-main`, `quality-nightly`, `quality-weekly` e
`quality-certify`: `fast` ⊂ `main` ⊂ `nightly` ⊂ `weekly` ⊂ `certify` por
construção (teste de fecho em `internal/contract/quality_tiers_test.go`),
de modo que execução rápida nunca substitui certificação. Pipelines em
`.github/workflows/{quick,nightly,weekly,release,verify,supply-chain}.yml`;
`nightly`, `weekly` e `release` disparam somente por despacho manual até a
P45, e `release` ainda exige `vars.QUALITY_FULL_ENABLED` (regra
`full-matrix-cadence` do `tools/ciaudit` bloqueia matriz completa em PR).

## 3. Custos

Custo é tier mais teto de CI (30 min por job), não promessa de duração:
`fast` em ~1 min, `main` (`quick-verify`) medido na saída desta fase,
`nightly`/`weekly`/`release` medidos somente na P45 — até lá, qualquer
número seria inventado. A matriz integral nunca rodou nesta fase por
desenho (P30 prepara, P45 executa).

## 4. Pré-condições da P45

1. Todas as fases atuais até P44 mergeadas, mais os PRs paralelos da versão.
2. `nightly`, `weekly` e `release` agendados/habilitados com a variável de ativação.
3. Bundle montado por `make quality-manifest` a partir da execução real.
4. Decisão `PASS` de `make quality-decide` sobre o bundle, sem reasons.
5. Revisão independente, aprovação jurídica e decisão de release (humanos).

## 5. Lacunas para a P45 (com correção dirigida)

1. ~~`TestTheInternalTreeMatchesItsDeclaredShape` falhava em `main`
   (`internal/performance` e `internal/regression` não declarados)~~ —
   **corrigido nesta tarefa**: ambos são suítes só de testes e foram
   declarados como pacotes standalone no portão, que agora passa limpo.
   Severidade original: alta para certificação (quebraria `test-unit`),
   nenhuma para produto ou merge.
2. `TestFallbackMetricCountsOnlyTrueFallbacks` usa contador global e falha
   com `-count>1` (padrão `-count=1` estável). Higiene de P45, severidade
   normal: reescrever com contador isolado ou documentar a restrição.
3. Assinatura externa do bundle pendente de decisão de infraestrutura
   (escopo explícito da P30-T03): até lá, o selo `manifest.sha256` é a
   evidência de tamper.
4. `next` vazio em `quality/toolchain.json`: nenhuma próxima patch
   aprovada. Promover exige tarefa, ADR e as suites Q0; o job informativo
   só reporta.

Nenhum finding crítico ou alto de produto é conhecido. Se um aparecer,
ele bloqueia o merge até correção dirigida — e esta frase estaria
acompanhada do link da issue, não deste parágrafo.

## 6. Decisor e fixtures

`tools/qualitydecide` decide somente sobre bundle verificado (selo,
hashes, tiers do bundle, vereditos com o commit do manifesto, waivers
válidos), com reasons estáveis e sem override. Fixtures herméticas
provam PASS reproduzível e FAIL estável sem confundir certificado real;
`internal/contract/gate_failclosed_test.go` prova que os 11 controles de
portão recusam defeito artificial. Metade da prontidão é o decisor
existir; a outra metade é ele nunca ter dito PASS — e ele nunca disse.
