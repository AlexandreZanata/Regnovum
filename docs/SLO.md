# SLOs e budgets por jornada

**Status:** baseline medido onde há medição; compromisso inicial onde ainda não há. Nenhum número abaixo é inventado: cada célula diz se é **medido** (com dataset, ambiente, janela e método) ou **compromisso a validar** (T04–T08, ratchet na T08, carga prolongada em P30).

**Regra:** baseline e compromisso nunca se misturam. Baseline muda só com nova medição registrada (commit, hardware, dataset, janela, método). Compromisso muda só com evidência de carga. Violar compromisso bloqueia; violar baseline exige explicação ou novo baseline.

## 1. Método, ambiente e dataset das medições T01

As duas amostras curtas desta tarefa (P28-T01) rodaram nesta máquina:

- Hardware/SO: Linux 7.1.5-76070105-generic x86_64, 13th Gen Intel i7-13620H, 16 CPUs, 31 GB RAM.
- Toolchain: Go 1.27.1 linux/amd64.
- Banco: PostgreSQL descartável via `internal/platform/dbtest` (migrations aplicadas, 1 conta sintética + 1 profile + 1 wallet + 1 Arena publicada `query-budget-arena`); nada de produção.

Amostra A — queries dos hot paths (dataset acima, janela de 1 execução, método `go test ./internal/platform/dbbudget/ -run TestHotPathBudgetsCountRealPostgreSQLQueries -count=1`):

| Hot path | Queries medidas | Tempo de DB medido |
| --- | ---: | --- |
| feed público | 1 | <1ms |
| Arena pública | 1 | <1ms |
| profile público | 1 | <1ms |
| wallet (leitura do titular) | 1 | <1ms |

Amostra B — custo de hashing do login (método `go test ./internal/identity/adapters/argon2id/ -run XXX -bench 'BenchmarkVerifyPassword_Default|BenchmarkHashPassword_Default' -benchtime 10x -count=1`, 10 amostras):

| Operação | Medido |
| --- | --- |
| Argon2id hash (parâmetros Default) | ~66ms/op |
| Argon2id verify (parâmetros Default) | ~69ms/op |

Piso honesto do login: cada tentativa (existente ou não — o dummy hash mantém uniformidade) custa ~69ms de CPU só em hashing.

Baselines pré-existentes reutilizados (não remedidos aqui, com a fonte):

- `tests/load/smoke.js` (P17-T06): thresholds de primeira etapa em janelas de 3s a 1 VU por workload — `http_req_failed` rate<0.05; p95 cache-cold<1000ms, cache-hot<300ms, login<1500ms, position<1000ms, argument-wallet<1000ms, webhook-replay<1000ms, arena-viral<500ms.
- Transporte (`internal/platform/httpserver/httpserver.go`): read header 10s, read 20s, write 30s, idle 120s — teto duro de qualquer latência servida.
- Pool (`internal/platform/dbpool/dbpool.go`): `MaxConns` padrão 10 por instância.
- Recuperação (`docs/DISASTER_DRILL.md` §3): RPO observado 5s (bound `archive_timeout` 300s, meta de release 900s); RTO 2s até escrita e 3s até a aplicação responder (meta 14400s); job de email sobreviveu ao provedor inalcançável e entregou 99s após o retorno.
- Abuso (P26-T01): login trava em 429 no 10º erro consecutivo — erro esperado, não falha.

## 2. SLOs por jornada

Convenção das tabelas: **Baseline** = medido (fonte entre parênteses); **Compromisso** = alvo inicial a validar em T04–T08; **—** = sem baseline, medir em T04 (não é número, é tarefa). Janela padrão dos compromissos: janelas do cenário k6 que os valida (T04 define rampas; até lá, vale a janela do smoke: 3s por workload).

### 2.1 Leitura pública (feed, Arena, profile)

| Métrica | Baseline | Compromisso |
| --- | --- | --- |
| p50 feed/Arena/profile | — | <150ms |
| p95 cache-hot | <300ms (smoke) | <300ms |
| p95 cache-cold | <1000ms (smoke) | <1000ms |
| p99 | — (teto = write 30s) | a medir em T04 |
| erro (5xx) | — | 0; 4xx de parâmetro inválido não contam como erro |
| timeout | teto 30s (transporte) | cliente desiste em 10s; servidor nunca estoura o write |
| query count | 1 por hot path (amostra A) | ≤1; 2ª query falha o gate (`QUERY_BUDGETS.md`) |
| pool | padrão 10/instância | p95 de espera por conexão <50ms |
| freshness | — | réplica só com política explícita de frescor (SCALABILITY §4); sem réplica, leitura é do primário |

### 2.2 Login

| Métrica | Baseline | Compromisso |
| --- | --- | --- |
| p50 | ≥69ms de hashing (amostra B) | <300ms |
| p95 | <1500ms (smoke) | <1500ms |
| p99 | — (teto = write 30s) | a medir em T04 |
| erro | — | 5xx = 0; 401/429 são respostas corretas, não erro |
| trava anti-força-bruta | 429 no 10º erro (P26-T01) | mantido; 429 tem `Retry-After` |
| timeout | teto 30s (transporte) | <5s fim a fim no cenário |
| query count | — | a medir em T03 (alvo ≤3: conta, credencial, sessão) |
| uniformidade | dummy hash (amostra B: mesma ordem de grandeza) | tempo de existente ≈ inexistente (sem enumeração) |

### 2.3 Posição (leitura e confirmação)

| Métrica | Baseline | Compromisso |
| --- | --- | --- |
| p95 leitura | <1000ms (smoke) | <1000ms |
| p95 confirmação (mutação) | — | <1000ms |
| p99 | — | a medir em T04 |
| erro | — | 5xx = 0; 409 de versão/401/403 são corretos |
| timeout | teto 30s | <5s fim a fim |
| query count | — | a medir em T03 (leitura ≤2, confirmação em 1 transação) |

### 2.4 Argumento + wallet (Q0)

| Métrica | Baseline | Compromisso |
| --- | --- | --- |
| p95 | <1000ms (smoke, leitura) | <1000ms leitura e publicação |
| p99 | — | a medir em T04 |
| erro | — | 5xx = 0 |
| timeout | teto 30s | <5s fim a fim |
| query count leitura wallet | 1 (amostra A) | ≤1 |
| publicação | transação explícita única | débito atômico argumento+ledger ou nada (sem parcial) |

Budgets Q0 de consistência/erro (além de latência): conservação do ledger (soma = projeção), idempotência por chave (replay resolve sem duplicar), sem saldo negativo, sem escrita parcial — todos já cobertos por testes de concorrência (P24-T04, P25-T05) e revalidados sob contenção na T06.

### 2.5 Webhook Stripe (Q0)

| Métrica | Baseline | Compromisso |
| --- | --- | --- |
| p95 replay | <1000ms (smoke) | <1000ms |
| p99 | — | a medir em T04 |
| erro | — | 5xx = 0; assinatura inválida → 4xx (nunca 5xx, nunca benefício) |
| timeout | teto 30s | <5s fim a fim |
| efeito | exatamente 1 crédito por evento (P26-T06: corrida de 8 vira 1×200 + 7×429) | idempotência por `idempotency_key`; reordenação liquida 1 vez |
| fila | job sobreviveu a outage e entregou 99s após retorno (drill) | lag de webhook <5min com provedor são; atraso maior é incidente |

### 2.6 Moderação (Q0)

| Métrica | Baseline | Compromisso |
| --- | --- | --- |
| p95 | — | <1000ms leitura e decisão |
| p99 | — | a medir em T04 |
| erro | — | 5xx = 0; 403 sem papel é correto |
| timeout | teto 30s | <5s fim a fim |
| trilha | congelada por trigger (P26-T08) | 100% das decisões com evento de auditoria na mesma transação |

### 2.7 Export pessoal (Q0)

| Métrica | Baseline | Compromisso |
| --- | --- | --- |
| p95 | — | <2000ms (documento admite geração; T04 mede) |
| erro | — | 5xx = 0; link expirado → 4xx |
| timeout | teto 30s | <10s fim a fim |
| isolamento | allowlist por titular (P26-T09) | zero mistura entre titulares, inclusive sob carga |
| payload | limites de `test-httplimits` | corpo acima do limite falha cedo, sem OOM |

### 2.8 Jobs e fila

| Métrica | Baseline | Compromisso |
| --- | --- | --- |
| lag (atraso do mais antigo devido) | observável (`QueueHealth.LagSeconds`) | <60s em operação normal |
| throughput do worker | — | a medir em T04 (concorrência configurável) |
| erro | — | poison vai a dead-letter/estado terminal; retry nunca vira loop quente (P27-T06) |
| efeito | 1x por job apesar de at-least-once (P27-T06) | deduplicação preservada sob carga |
| fila | backlog observável | profundidade e idade com alerta antes de saturar |

## 3. Throughput

O smoke a 1 VU não mede throughput: nenhum compromisso de throughput tem baseline. Compromissos de throughput por jornada serão propostos na T04 com o mix internacional e viram números só após a primeira execução. Até lá, o único compromisso quantitativo de contenção é o pool: 10 conexões/instância com p95 de espera <50ms.

## 4. O que T04–T08 e P30 fazem com este documento

- T02–T03 preenchem os custos unitários (benchmarks) e os planos SQL com datasets pequeno/representativo.
- T04–T06 executam os cenários, preenchem cada "—" com baseline medido e promovem compromissos a SLO ou os corrigem.
- T07 preenche payloads/recursos por jornada; T08 congela o relatório e o ratchet (baseline não regravável pelo comando de teste; troca de hardware invalida comparação).
- Carga prolongada e certificação ficam em P30/P45. Este documento nunca promete capacidade de produção (SCALABILITY §1).
