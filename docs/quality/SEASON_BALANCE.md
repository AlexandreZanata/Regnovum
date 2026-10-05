# Balanço de temporadas da Coroa (P47-T10)

**Estado:** registro test-only. Não é aprovação, probabilidade,
parâmetro vigente nem autorização de lançamento. A oferta S
ratificada, o Dízimo vigente, o teto M de Migalhas e os budgets de
lançamento continuam pendentes de ratificação própria; nada aqui
os substitui. Proposta não ratificada não vira regra por
inferência. Consumo: P44 prepara o gate e P45 executa a matriz
integral (TEMP-08–11).

## 1. Parâmetros do oráculo (sintéticos)

| Parâmetro | Valor no oráculo | Origem |
|---|---|---|
| Oferta por livro (`S`) | 2.100.000.000 milliINK | fixture sintética, nunca o S ratificado |
| Dízimo comercial | `floor(preço × 10 / 100)` | §7 do adendo, recalculado pelo modelo |
| Teto de Migalhas por lote | 1.000.000 milliINK | fixture sintética, nunca o M vigente |
| Elegibilidade de Migalhas | 1 por pessoa por temporada | proposta recomendada, sem ativação |
| Política de riqueza | `wealth-v1` | única versão implementada |
| Livros | `temporada-coroa-a/b/tie/mut/boa-fe` + orçamentos | nomes sintéticos |

## 2. Seeds e reprodução

- Seed padrão: `20260923` (`internal/platform/testsource`).
- Reprodução: `ARENA_TEST_SEED=20260923 go test ...`.
- Cada execução registra o seed no log (`seed=20260923 ...`).

## 3. Cenários e resultados (2026-10-05)

| Cenário | Resultado observado |
|---|---|
| Livro A: vendas 900M/700M, Dízimo, custo 60M, empréstimo 50M, terceiro 30M, Migalhas 3 cabeças | Σ = S após cada mutação; Dízimo = 160M exato; repetição de cabeça paga 0 |
| Coroação legítima rara (ana P > C) | 1 conquista na revisão do cruzamento; antes, igualdade não deu trono; depois, incumbente retida; custo 200M ana→bruno trocou o trono 1 vez |
| Empate exato em C (700M = C) | 0 tronos; incumbente retida sem mudança de versão |
| Custódia de terceiro 600M | 0 tronos por ela; W exclui o bolso nos dois lados |
| Fraude pós-corte + correção vinculada | original preservado; revisão tardia tratada como pós-corte, sem riqueza nova |
| Reset (livro B) | Tesouro = S, participantes = 0, 0 revisões herdadas; transferência cruzada recusada; reinado inicial v1 com riqueza 0 |
| Concentração extrema (vitória B) | trono muda por transferência lícita; Tesouro nunca negativo; nenhuma reserva baixada às escondidas |
| Conta sancionada | perde elegibilidade e vendas, conserva custódia |
| 4 mutantes (ignore-debt, count-third-party, carry-over, king-self-grant) + obsolete-reign | todos detectados; `ErrStaleReign` no reinado obsoleto |

## 4. Custos medidos (PostgreSQL local, 2026-10-05)

| Medida | Amostra | Resultado |
|---|---|---|
| Top-1 pelo índice em 200 contas | 50 leituras | média 141,6µs/leitura |
| Update incremental por outbox | 100 commits | média 892,4µs/commit |
| Drenagem da outbox (24 revisões) | 1 drenagem | 38,9ms total; lag final 0 |
| Contenção de lease (4 workers) | 1 revisão | 1 vence, 3 recuam, 1 avaliação |

Testes que fixam os tetos de regressão (generosos, anti-patologia):

- `internal/crown/adapters/postgres/season_balance_budget_test.go::TestCrownBudgetTopIndexCost` (500ms/leitura)
- `...::TestCrownBudgetIncrementalUpdateCost` (500ms/commit)
- `...::TestCrownBudgetOutboxDrainLag` (lag 0; 30s/drenagem)
- `...::TestCrownBudgetLeaseContention` (exatamente 1 vencedor)

Os tetos acima são regressão de teste, não budgets ratificados:
o lançamento exige budgets ratificados antes, com hardware de
referência publicado.

## 5. Findings para decisão (não mudam a economia sozinhos)

1. Vitória lícita é possível em ao menos um cenário (ana por
   vendas + custo recebido), sem baixar reserva escondido.
2. Empate, custódia alheia e transferência cruzada dão zero
   trono em todos os cenários.
3. Uma autoridade por revisão em todas as drenagens; contenção
   serializa sem duplicar.
4. Hipótese incapaz (vitória impossível) ou frequência
   indesejada seria finding de balanceamento para decisão do
   titular, nunca motivo para alterar a economia neste ciclo.

## 6. Verificadores entregues (TEMP-08–11)

- `internal/seasons/domain/crown_succession_oracle_test.go` (4 testes)
- `internal/crown/adapters/postgres/season_balance_budget_test.go` (4 testes)
- `quality/season-trace.json` TEMP-08–11 apontando estes verificadores
- `quality/catalog.json` `QUAL-THR-CROWN-02`
