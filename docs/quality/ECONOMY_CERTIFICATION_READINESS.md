# Prontidão da certificação financeira (P44-T13)

**Status: NÃO CERTIFICADO.** Este documento declara prontidão do
*mecanismo*, não certificação do produto. Nenhum certificado foi
emitido, nenhuma ativação foi ligada e `make verify` com a matriz
integral continua gate exclusivo da P45. Ler este arquivo como
certificado é ler errado de propósito. CI rápido (`make
quick-verify`) não é CI de release (`make verify` + matriz completa
no PR pronto): o primeiro dá feedback de mudança, o segundo decide
release.

**Referências:** [ECONOMY_INDEPENDENT_REVIEW.md](ECONOMY_INDEPENDENT_REVIEW.md) · [CERTIFICATION_READINESS.md](CERTIFICATION_READINESS.md) · [RUNBOOKS.md](../RUNBOOKS.md) · [CI.md](../CI.md) · [THREAT_MODEL.md](../THREAT_MODEL.md) · [DECISOES_VIGENTES.md](../reino/DECISOES_VIGENTES.md) · [TEMPORADAS_SUCESSAO.md](../reino/TEMPORADAS_SUCESSAO.md) · [Makefile](../../Makefile) · [financial-coverage.json](../../quality/financial-coverage.json) · [season-trace.json](../../quality/season-trace.json) · [toolchain.json](../../quality/toolchain.json)

---

## 1. Wiring: o produto segue desativado

A superfície econômica expõe ports, nunca portas: nenhum routeador,
comando, flag, decreto ou wiring de bootstrap move custódia ou
contorna o freeze (`internal/economy/noactivation_test.go`: registro
de rotas, contrato OpenAPI, varredura de fontes e mux vivo com 404
nas sondas econômicas e diário vazio). O gate de coorte
(`internal/economy/domain/activation.go`) só autoriza a coorte
sintética `canary-synthetic`; piloto e público são recusados por
controle próprio. O kill switch congela novas mutações e preserva
leitura, export, replay de intenção liquidada e recurso por
compensação (`internal/economy/adapters/postgres/activation_canary_test.go`).
Pré-condição da execução dupla em P45: repetir a prova de wiring
antes de cada rodada e recusar qualquer entrada econômica nova.

## 2. Fixtures e hashes íntegros

| Artefato | Estado |
|---|---|
| `tools/economycertify/testdata/green.json` | fixture verde versionada; `make economy-certify` (padrão) emite PASS com razões vazias |
| Decisor `tools/economycertify` | 5 testes verdes: verde, 14 eixos vermelhos, SHA segurado, ilegível, CLI 0/1/2 |
| `quality/financial-coverage.json` | 11 eixos mapeados (T01); pendentes aparecem como pendentes, nunca como parâmetro vigente |
| `quality/season-trace.json` | TEMP-01–12 com estados honestos (T01 da P46); adendo nunca ratifica Q pendente por inferência |
| `quality/toolchain.json` | pinos julgados por `make audit-toolchain`, fora do `quick-verify` |

Qualquer fixture ausente, SHA alterado, finding aberto, país
pendente, drift de 1 milliINK, árvore suja ou produto proibido ativo
gera FAIL no decisor — provado pelas fixtures vermelhas, não por
promessa.

## 3. Runbooks e observabilidade

Desvio de 1 milliINK em qualquer livro ativo congela antes da
próxima mutação; arquivo divergido bloqueia a sucessora; lag/stale
bloqueia atos reais com alerta ([RUNBOOKS.md](../RUNBOOKS.md) R1, R3–R6;
owners por papel, nunca por pessoa). O monitor registra owner,
runbook e ação por achado, com log privado sem PII
(`internal/economy/domain/monitor.go`). RTO físico medido: 1,6s
(< 2m); kill switch medido: ~7ms (< 5s). Em P45, cada rodada da
execução dupla reabre pelos runbooks antes de julgar.

## 4. Pré-condições da execução dupla em P45

1. P46 e P47 mergeadas (evidência exigida pelo decisor:
   `evidence-merge-missing` reprova sem elas).
2. `economy-certify` executado **duas vezes** sobre bundles
   construídos da árvore limpa no SHA candidato, como
   `tools/releaseverify/verify.sh` executa cada comando duas vezes:
   a segunda rodada detecta flake e mutação entre rodadas.
3. `make verify` verde + matriz integral + parecer independente
   final (formato IREV-NN) com zero critical/high aberto.
4. Autorização separada do titular para qualquer ativação; sem ela,
   o canary sintético continua sendo o único autorizado.

## 5. Pendências explícitas (não-certificação)

- Q19–Q31 `PENDENTE` em [DECISOES_VIGENTES.md](../reino/DECISOES_VIGENTES.md);
  sem tabela de preço aprovada (`economy-decisions-check` falha por
  desenho, fora do `finish`).
- Parecer independente final ausente (pacote em
  [ECONOMY_INDEPENDENT_REVIEW.md](ECONOMY_INDEPENDENT_REVIEW.md), piso zero
  critical/high aberto).
- Matriz DAST completa, carga de milhões, Migalhas/cotação/k6,
  PITR-WAL integral e suíte financeira integral: gate de release
  na P45, após o merge de todas as fases atuais até P44.
- S = 2,1B INK por temporada é especificação pendente de
  ratificação (Q20), não valor vigente.

**Veredito: NÃO CERTIFICADO.** Harnesses (oracle, crash, red team,
restore/PITR, capacidade, migração), decisor e kill switch estão
prontos e provados em cenários direcionados; direitos e oferta
reconciliados nesses cenários; zero critical/high conhecido no
escopo. Certificação, auditoria independente final e ativação
dependem de P45 e autorização separada.
