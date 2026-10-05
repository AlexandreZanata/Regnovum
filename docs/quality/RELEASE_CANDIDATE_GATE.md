# Portão do candidato imutável (P45-T04)

**Status: AINDA NÃO CERTIFICADO.** Este documento fecha a
preparação e define o roteiro exato da execução pós-merge; ele não
executa a matriz, não emite certificado e não cria tag. Nenhum
portão obrigatório ficou sem execução definida; nenhuma pendência
Q0/Q1 conhecida ficou sem dono; os checks rápidos estão verdes e a
economia segue desligada.

**Referências:** [RELEASE_SCOPE.md](RELEASE_SCOPE.md) · [ECONOMY_CERTIFICATION_READINESS.md](ECONOMY_CERTIFICATION_READINESS.md) · [CERTIFICATION_READINESS.md](CERTIFICATION_READINESS.md) · [RUNBOOKS.md](../RUNBOOKS.md) · [CI.md](../CI.md) · [Makefile](../../Makefile) · [release-matrix.json](../../quality/release-matrix.json) · [tiers.json](../../quality/tiers.json)

---

## 1. Ligação P30/P44 → pipeline final

| Origem | Artefato | Ligação |
|---|---|---|
| P30-T03 | `tools/qualitymanifest` + `make quality-manifest` | monta o bundle determinístico lido pelo decisor |
| P30-T05 | `tools/qualitydecide` + `make quality-decide` | julga o bundle (PASS/FAIL); portões P45-G02 |
| P30-T01 | `quality/tiers.json` | camadas fast⊂main⊂nightly⊂weekly⊂certify; `make quality-certify` |
| P44-T01 | `quality/financial-coverage.json` | 11 eixos; insumo do decisor econômico |
| P44-T06/T11 | monitor + kill switch (`economy_mode`, `economy_incidents`) | congela novas mutações (~7ms medidos), preserva leitura/export/recurso |
| P44-T12 | `tools/economycertify` + `make economy-certify` | 16 razões, fixtures verde/vermelhas |
| P45-T03 | decisão final + `final-template.json` | +10 razões (oracle, resets, webhook, empatadas, ex-Rei, expiração, arquivo, IREV, legal) |
| P45-T02 | `quality/release-matrix.json` + `tools/releasematrix` | 22 portões × 18 áreas, artefato por SHA, SKIPPED é FAIL |

Todo portão da matriz resolve contra um alvo real do
[Makefile](../../Makefile) (prova mecânica: `TestDeliveredManifestPasses`);
comando sem alvo reprova (`command-missing`).

## 2. Runbooks e kill switch

Resposta a incidentes em [RUNBOOKS.md](../RUNBOOKS.md) R1–R9, com o
recorte monetário em R1/R3–R6 (owner por papel, alerta com ação,
log privado sem PII). RTO físico medido 1,6s (< 2m); kill switch
medido ~7ms (< 5s); RPO provado por marker. Pré-condição de cada
rodada pós-merge: reabrir pelos runbooks antes de julgar; desvio
de 1 milliINK congela antes da próxima mutação.

## 3. Pendências registradas (bloqueiam certificado, não o merge)

- Q19–Q31 `PENDENTE`, sem tabela de preço (`economy-decisions-check`
  falha por desenho).
- Parecer independente final (IREV) e aprovação jurídica/tributária
  por país ausentes — exigidos com zero critical/high aberto.
- Matriz integral, DAST completa, carga de milhões, PITR-WAL e
  suíte financeira integral: só no SHA final pós-merge, duas
  execuções independentes, artifacts imutáveis por SHA.

## 4. Roteiro exato pós-merge (gate da fase, não microtarefa)

1. Confirmar P23–P45, P46/P47 e paralelos mergeados; congelar o SHA
   final de `main` com árvore limpa. Faltando algo, parar sem tag.
2. Nesse SHA, em dois ambientes limpos: `make verify`, `make
   quality-certify`, `make economy-certify` e todos os portões da
   matriz (E2E/i18n, DAST/scans, mutação, flake, race, carga,
   restore/PITR, upgrade, privacidade, contratos). Exigir parecer
   real, decisão jurídica por país e zero critical/high ou desvio.
   Ausência/`SKIPPED` obrigatório é FAIL.
3. Só com tudo verde, tag candidata inédita `vX.Y.Z-rc.N` no mesmo
   SHA; aguardar `verify.yml` e `supply-chain.yml` verdes na tag.
   Falha exige novo PR/merge, novo SHA e reexecução integral.
4. Certificados vinculados ao SHA; tag estável só com autorização
   explícita do titular. Ativação econômica exige autorização
   separada por país/produto; nunca automática por merge ou tag.

**Veredito: AINDA NÃO CERTIFICADO.** Preparação fechada, checks
rápidos verdes, economia desligada, nenhuma tag criada nesta
tarefa.
