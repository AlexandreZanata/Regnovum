# Inventário de escopo da versão (P45-T01)

**Status: INVENTÁRIO, NÃO CERTIFICADO.** Este documento cataloga o
que entra no candidato de release; não aprova nada. Qualquer fase ou
PR pendente, regra não ratificada ou flag econômica ativa abaixo
**bloqueia o avanço** para T02. Proposta não ratificada não vira
regra por inferência.

**Referências:** [DECISOES_VIGENTES.md](../reino/DECISOES_VIGENTES.md) · [TEMPORADAS_SUCESSAO.md](../reino/TEMPORADAS_SUCESSAO.md) · [ECONOMY_CERTIFICATION_READINESS.md](ECONOMY_CERTIFICATION_READINESS.md) · [CI.md](../CI.md)

---

## 1. Fases mergeadas (verificado em `main`)

| Fase | PR | Merge |
|---|---|---|
| P23 qualidade de IA | #92 | `e5584f8` |
| P24 validação de negócio | #114 | `909a198` |
| P25 integração/contrato | #125 | `9091ee8` |
| P26 segurança/privacidade | #138 | `87a8ac5` |
| P27 confiabilidade | #149 | `44f632b` |
| P28 performance | #158 | `06e5c18` |
| P29 operações internacionais | #167 | `6d4dc34` |
| P30 certificação autônoma | #176 | `684aff2` |
| P31 contrato constitucional | #185 | `5972193` |
| P32 ledger de oferta fixa | #196 | `06e23b3` |
| P33 transição legada | #205 | `a28aeb2` |
| P34 tesouro/custódia | #214 | `beadb7a` |
| P35 cotações/pagamentos | #224 | `483f20d` |
| P36 medição/tempo do INK | #234 | `1bde3b8` |
| P37 comércio/dízimo | #244 | `a2f67d3` |
| P38 migalhas/refluxo | #254 | `e7a0372` |
| P39 carta/disputas | #263 | `e30611f` |
| P46 ciclo de vida sazonal | #277 | `d336f88` |
| P40 decretos/cargos reais | #286 | `84a7422` |
| P41 inquisição/morte de conta | #297 | `b75e8e2` |
| P42 reputação/registros/i18n | #306 | `b73c02f` |
| P43 patentes/produtos diferidos | #314 | `8a6261d` |
| P47 riqueza/sucessão da Coroa | #325 | `6941768` |
| P44 certificação econômica | #339 | `c876378` |
| P45 certificação da release | #344 | `3f4522b` |
| P48 inventário de rotas | #403 | `4d411ed` |
| P49 superfícies no servidor | #404 | `7ee634c` |
| P50 shell e autenticação | #405 | `16a950a` |
| P51 conta e privacidade | #406 | `e99c042` |
| P52 descoberta das Arenas | #407 | `7f26bc4` |
| P53 debate e persuasão | #408 | `21e78d1` |
| P54 carteira e cobrança | #409 | `a5b171b` |
| P55 moderação e operação | #410 | `50b5dd6` |
| P56 staged e harness | #412 | `1a90261` |
| P57 temporadas e contratos privados | #413 | `5774244` |
| P58 aceite do frontend | #414 | `9c3b49a` |

Zero PRs abertos (conferido via `gh pr list`). Issues abertas de
acompanhamento do plano: #397 (P58-T05) e #398–#401 (P59-T01–T04),
todas `needs-ai-review` — são as próprias tarefas de preparação, não
defeitos do produto. Branches laterais sem PR (`chore/fix-main-ci`,
`codex/*`, `docs/wiki-history`, `frontend-b-mvp`) são restos de
trabalhos já mergeados (#64, #82), fora do escopo da versão; nenhum
deles integra este candidato.

## 2. Módulos e migrações

- 38 pacotes em `internal/` (arenas → wallet, incluindo
  `economy`, `seasons`, `crown`, `commerce`, `crumbs`, `metering`,
  `patents`, `inquisition`, `disputes`, `charter`, `reputation`).
- 62 migrations (`00001`–`00062`, última
  `00062_dispute_case_records.sql`); manifesto de schema
  julgado por `schema_manifest_test.go`.
- Toolchains pinadas em `quality/toolchain.json` (go 1.27.1,
  postgres 18.4, typescript 7.0.2, staticcheck v0.8.1).

## 3. Regras ratificadas

- **Q01–Q36: nenhuma `APROVADA`** em
  [DECISOES_VIGENTES.md](../reino/DECISOES_VIGENTES.md) (só o
  vocabulário cita o termo). Q19–Q31 seguem `PENDENTE`; sem tabela
  de preço aprovada.
- **TEMP-01–04: INTENÇÃO APROVADA** (temporada de 90 dias, reinício
  econômico, memória, sucessão por riqueza) via
  [TEMPORADAS_SUCESSAO.md](../reino/TEMPORADAS_SUCESSAO.md); sem
  ratificar parâmetros, valores ou direitos.

## 4. Seasons, riqueza e reinado

- Season: 7.776.000s em UTC, máquina
  PREPARED→ACTIVE→CLOSING→SEALED→ARCHIVED, um Genesis por
  `season_key`, S por livro sem carry-over (`quality/season-trace.json`
  TEMP-01–12).
- Riqueza: patrimônio líquido confirmado, sem terceiro/recebível;
  sucessão com P>C, elegibilidade/termos/MFA e versão de reinado;
  Rei sazonal não é dono do Tesouro (P47; detalhes operacionais
  pendentes de aceite).

## 5. Flags e ativação: tudo desligado

- Nenhuma flag `ARENA_*_ENABLE`/`FEATURE_*` em `.env.example` ou
  `internal/platform/config`; nenhum wiring econômico em
  `internal/bootstrap` ou `cmd` (prova contínua:
  `internal/economy/noactivation_test.go`).
- Gate de coorte autoriza só `canary-synthetic`; kill switch congela
  novas mutações preservando leitura/export/recurso (P44-T11).
- S = 2,1B INK por temporada é especificação pendente (Q20), não
  valor vigente.

## 6. Riscos conhecidos (bloqueiam certificado, não inventário)

Parecer independente final ausente; aprovação jurídica/tributária
por país ausente; matriz DAST completa, carga de milhões,
PITR-WAL integral e suíte financeira integral ficam para a matriz
pós-merge desta fase; `economy-decisions-check` falha por desenho.

## 7. Frontend (P48–P58, medido em `9c3b49a`)

- 100 rotas no inventário (`quality/frontend-routes.json`): 85 publicadas
  com client+página+prova browser (postura complete segura),
  15 staged sob `planned-staged-harness` sem ativação, probes excluídos.
- Superfícies P50–P57 montadas no servidor com SSR-first e progressively
enhanced; jornadas reais por família em `tools/e2e/specs/` (22/22,
  pt-BR + en-US), documento de aceite em
  [FRONTEND_COMPLETION_READINESS.md](FRONTEND_COMPLETION_READINESS.md).
- Orçamentos docs/FRONTEND.md §11: JS inicial por página ≤50 KB (arena
  47.739 B), CSS total 6.290/40.000 B (`make audit-web` verde).

**Veredito do inventário: COMPLETO PARA PREPARAR, NÃO CERTIFICADO.**
Nenhuma fase/PR pendente além das tarefas P59 (issues #398–#401, a
própria preparação); nenhuma regra não ratificada escondida;
nenhuma flag ativa. Próximo: integração dos gates ao manifesto (P59-T02).
