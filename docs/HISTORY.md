# História do projeto

Registro público e curado da construção do Goyim Arena: o que cada fase do
plano entregou, quando, com que evidência e em qual pull request. A fonte de
verdade continua sendo o repositório — o histórico do Git, os documentos que
cada fase escreveu e os gates que os julgam —; esta página é o índice que
costura os dois. Ela é espelhada na wiki do projeto e atualizada ao fim de cada
fase ([Como registrar](#como-registrar)).

## Como o trabalho acontece

O desenvolvimento segue um contrato fixo, publicado em [AGENTS.md](../AGENTS.md)
e [COMMITS.md](COMMITS.md):

- **Uma microtarefa por vez.** Cada microtarefa termina em **um commit
  atômico**, no formato Conventional Commits, e nunca em um commit que mistura
  assuntos.
- **Uma branch por fase, um PR por fase, merge só com o CI verde.** O fluxo é
  `phase-NN-<slug>` → PR → `verify` verde → merge. Nunca há push direto em
  `main`, `--force`, `--admin` ou `--no-verify`; correção de defeito é um
  commit novo, nunca um limiar reduzido, um teste pulado ou um snapshot aceito.
- **O CI chama os mesmos gates que um operador roda.** Cada job chama um alvo
  do `Makefile` — nunca uma cópia do comando —, e [CI.md](CI.md) é a tabela dos
  gates, dos jobs e do orçamento do pipeline.
- **O plano privado não é publicado.** O caderno de execução (`.local/`) guarda
  a ordem das fases e o progresso local; o que vira história pública é o que já
  foi medido e mergeado, escrito aqui em linguagem de entrega.

## Marcos

- **2026-09-16** — fundação documental e técnica: produto, constituição, regras
  de negócio, MVP, arquitetura, modelo de ameaças, stack e o esqueleto
  Go/TypeScript.
- **2026-09-18** — publicação autorizada pelo titular: branch de fase, PR e CI
  passam a fazer parte do protocolo; o trabalho das fases anteriores é
  publicado no mesmo movimento (`chore/git-workflow`, PR #1).
- **2026-09-22** — backend completo: as fases P00–P20 estão em `main`, com a
  verificação de release passando; o portão de release segue vermelho apenas
  pelas duas decisões humanas em aberto ([GOVERNANCE.md](GOVERNANCE.md)).

## Fundação e backend antes da publicação (P00–P14)

Construídas de 2026-09-16 a 2026-09-18 em commits locais, cada uma encerrada com
o próprio gate verde e publicadas em 2026-09-18. O módulo citado é o que ficou
no repositório.

| Fase | Tema | O que entrou |
| --- | --- | --- |
| P00 | Contrato de execução | Regras de execução do repositório, baseline e proteção do processo (`AGENTS.md`, `CONTRIBUTING.md`). |
| P01 | Fundação do projeto | Módulos Go, toolchain TypeScript nativo, `Makefile` e build reproduzível. |
| P02 | Núcleo da plataforma | Configuração, HTTP, Problem Details, observabilidade e o gerador de i18n (`internal/platform`, `internal/i18n`). |
| P03 | Fundação de banco | Migrations embutidas, roles, pool e harness PostgreSQL descartável (`internal/platform/dbmigrate`, `db/queries`). |
| P04 | Identidade e autenticação | Conta, senha Argon2id, email, sessão opaca, CSRF e MFA (`internal/identity`). |
| P05 | Perfis e privacidade | Username, preferências e controles do titular (`internal/profiles`). |
| P06 | Carteira e ledger | INK append-only, grants e concorrência (`internal/wallet`). |
| P07 | Entitlements | Arena Pass e benefícios Member (`internal/arenas/adapters/billingpass`). |
| P08 | Arenas | Rascunho, publicação, fechamento e feed (`internal/arenas`). |
| P09 | Posições | Posição inicial, mudanças e agregados privados (`internal/positions`). |
| P10 | Argumentos | Argumentos, respostas, fontes e débito atômico (`internal/arguments`). |
| P11 | Persuasão | Atribuição, reputação e antiabuso (`internal/persuasion`). |
| P12 | Cobrança | Stripe Checkout/Billing, compras, assinatura e reconciliação (`internal/billing`). |
| P13 | Moderação | Denúncia, decisão, recurso e sanções (`internal/moderation`). |
| P14 | Transparência e privacidade | Exportações, métricas e exclusão (`internal/transparency`, `internal/statprojections`). |

## Fases publicadas por PR (P15–P20)

Cada fase abaixo está mergeada em `main`; a coluna do PR aponta o merge e a
evidência que a fase produziu.

| Fase | Tema | O que entrou | Evidência | PR |
| --- | --- | --- | --- | --- |
| P15 | Jobs e notificações | Worker com lease/claim, retry, outbox, email e schedules (`internal/jobs`, `internal/notifications`). | Código e testes dos módulos | [#9](https://github.com/AlexandreZanata/Goyim-Arena/pull/9) (2026-09-18) |
| P16 | Endurecimento de segurança | Modelo de abuso, fronteiras de confiança e gates ofensivos ([THREAT_MODEL.md](THREAT_MODEL.md), [SECURITY.md](SECURITY.md)). | `make test-security` | [#19](https://github.com/AlexandreZanata/Goyim-Arena/pull/19) (2026-09-19) |
| P17 | Performance e escala | Cache, backpressure, orçamento de consultas e baseline de carga ([SCALABILITY.md](SCALABILITY.md), [QUERY_BUDGETS.md](QUERY_BUDGETS.md)). | `internal/platform/httpcache`, `internal/platform/backpressure`, `tests/load` | [#28](https://github.com/AlexandreZanata/Goyim-Arena/pull/28) (2026-09-19) |
| P18 | UI nativa mínima e harness E2E | Web Components nativos, contratos gerados e jornadas de navegador isoladas (`web/`, `tools/e2e`, [FRONTEND.md](FRONTEND.md)). | `make test-web`, `make test-e2e` | [#39](https://github.com/AlexandreZanata/Goyim-Arena/pull/39) (2026-09-21) |
| P19 | Operação de produção | Imagem distroless, topologia Compose, deploy/rollback, backup/PITR, ingress Caddy e o workflow de verificação (`Dockerfile`, `compose.production.yaml`, `deploy/`, [CI.md](CI.md)). | `make image-verify`, `make compose-verify`, `make deploy-verify`, `make backup-verify`, `make caddy-verify` | [#51](https://github.com/AlexandreZanata/Goyim-Arena/pull/51) (2026-09-22) |
| P20 | Prontidão de release | Auditorias finais e o release candidate local: migrations, segurança, desastre/carga, privacidade, i18n, verificação reproduzível e handoff. | [MIGRATION_AUDIT.md](MIGRATION_AUDIT.md), [SECURITY_AUDIT.md](SECURITY_AUDIT.md), [DISASTER_DRILL.md](DISASTER_DRILL.md), [PRIVACY_AUDIT.md](PRIVACY_AUDIT.md), [I18N_AUDIT.md](I18N_AUDIT.md), [RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md) | [#62](https://github.com/AlexandreZanata/Goyim-Arena/pull/62) (2026-09-22) |

## Fases de qualidade, economia e frontend (P21–P58)

Cada fase abaixo está mergeada em `main`; a coluna do PR aponta o merge (a
prova é o próprio merge: hash + PR). Em 2026-09-23 o repositório foi renomeado
de `Goyim-Arena` para `Regnovum` ([PR #81](https://github.com/AlexandreZanata/Regnovum/pull/81));
links antigos nesta página ainda apontam o nome anterior, os novos usam o atual.

| Fase | Tema | O que entrou | Evidência | PR |
| --- | --- | --- | --- | --- |
| P21 | Governança e regras executáveis de qualidade | Portões de qualidade com dono e próxima ação | Gates do `Makefile` | [#71](https://github.com/AlexandreZanata/Regnovum/pull/71) (2026-09-23) |
| P22 | Plataforma hermética de testes | Harness PostgreSQL descartável e plataforma de testes | `internal/platform/dbmigrate`, harness | [#80](https://github.com/AlexandreZanata/Regnovum/pull/80) (2026-09-23) |
| P23 | Código gerado por IA | Detecção de código ruim ou desalinhado (`audit-tests`, `audit-diff`) | Gates de qualidade dos testes | [#92](https://github.com/AlexandreZanata/Regnovum/pull/92) (2026-09-24) |
| P24 | Validação das regras de negócio | Validação profunda das regras por módulo | Testes de domínio | [#114](https://github.com/AlexandreZanata/Regnovum/pull/114) (2026-09-25) |
| P25 | Integração, persistência e contratos | Contrato de integração e cobertura financeira | `internal/contract` | [#125](https://github.com/AlexandreZanata/Regnovum/pull/125) (2026-09-25) |
| P26 | Segurança, privacidade e abuso adversarial | Endurecimento adversarial e privacidade | `make test-security` | [#138](https://github.com/AlexandreZanata/Regnovum/pull/138) (2026-09-25) |
| P27 | Regressão, confiabilidade e caos | Regressão e confiabilidade | Suíte de regressão | [#149](https://github.com/AlexandreZanata/Regnovum/pull/149) (2026-09-28) |
| P28 | Performance, capacidade e estabilidade | Capacidade e estabilidade | `tests/load`, `QUERY_BUDGETS.md` | [#158](https://github.com/AlexandreZanata/Regnovum/pull/158) (2026-09-28) |
| P29 | Qualidade internacional e operacional | i18n operacional e qualidade | `I18N_AUDIT.md` | [#167](https://github.com/AlexandreZanata/Regnovum/pull/167) (2026-09-28) |
| P30 | Certificação autônoma e qualidade contínua | Infraestrutura de certificação | Gates de certificação | [#176](https://github.com/AlexandreZanata/Regnovum/pull/176) (2026-09-28) |
| P31 | Contrato da nova economia | Contrato de negócio, tempo e risco | Módulos de economia | [#185](https://github.com/AlexandreZanata/Regnovum/pull/185) (2026-09-28) |
| P32 | Oferta fixa e custódia | Ledger de oferta fixa e custódia | Ledger append-only | [#196](https://github.com/AlexandreZanata/Regnovum/pull/196) (2026-09-29) |
| P33 | Direitos legados | Coexistência e transição | Migração de direitos | [#205](https://github.com/AlexandreZanata/Regnovum/pull/205) (2026-09-29) |
| P34 | Tesouro e reserva | Tesouro, reserva e obrigações | Custódia | [#214](https://github.com/AlexandreZanata/Regnovum/pull/214) (2026-09-29) |
| P35 | Cotação e compra fiat | Cotação, compra e estornos | Billing | [#224](https://github.com/AlexandreZanata/Regnovum/pull/224) (2026-09-29) |
| P36 | Medição de INK | Medição de uso e instante da cobrança | Metering | [#234](https://github.com/AlexandreZanata/Regnovum/pull/234) (2026-09-29) |
| P37 | Comércio e Dízimo | Transferências, comércio e Dízimo | Commerce | [#244](https://github.com/AlexandreZanata/Regnovum/pull/244) (2026-09-30) |
| P38 | Migalhas e métricas | Migalhas, R4 e métricas econômicas | Métricas | [#254](https://github.com/AlexandreZanata/Regnovum/pull/254) (2026-09-30) |
| P39 | Carta e disputas privadas | Carta versionada e disputas voluntárias | Disputes | [#263](https://github.com/AlexandreZanata/Regnovum/pull/263) (2026-09-30) |
| P40 | Coroa e decretos | Coroa, decretos e separação institucional | Coroa | [#286](https://github.com/AlexandreZanata/Regnovum/pull/286) (2026-10-01) |
| P41 | Inquisição e morte de conta | Contenção, morte de conta e liquidação | Inquisição | [#297](https://github.com/AlexandreZanata/Regnovum/pull/297) (2026-10-02) |
| P42 | Reputação e livros | Reputação, livros, privacidade e i18n | Reputação | [#306](https://github.com/AlexandreZanata/Regnovum/pull/306) (2026-10-02) |
| P43 | Patentes desativadas | Produtos financeiros deliberadamente desativados | Patents | [#314](https://github.com/AlexandreZanata/Regnovum/pull/314) (2026-10-05) |
| P44 | Certificação financeira | Preparação da certificação e operação controlada | Certificação | [#339](https://github.com/AlexandreZanata/Regnovum/pull/339) (2026-10-05) |
| P45 | Certificação da release | Preparação final e certificação | Gate final | [#344](https://github.com/AlexandreZanata/Regnovum/pull/344) (2026-10-05) |
| P46 | Economia sazonal | Adaptação sazonal e fechamento de 90 dias | Temporadas | [#277](https://github.com/AlexandreZanata/Regnovum/pull/277) (2026-10-01) |
| P47 | Sucessão e campeões | Patrimônio elegível, sucessão e campeões | Sucessão | [#325](https://github.com/AlexandreZanata/Regnovum/pull/325) (2026-10-05) |
| P48 | Inventário de rotas | Inventário executável de 100 rotas + gate de 16 regras + mapa mounted + contrato de aceite | `quality/frontend-routes.json`, `make frontend-coverage` | [#403](https://github.com/AlexandreZanata/Regnovum/pull/403) (2026-10-06) |
| P49 | Superfícies no servidor | Composição real das superfícies MVP, jobs no contrato (85/15/0) | `make quick-verify` + `Quick verification` | [#404](https://github.com/AlexandreZanata/Regnovum/pull/404) (2026-10-06) |
| P50 | Shell e autenticação | Shell SSR-first, skip-link, navegação e jornada de conta no browser | `tools/e2e/specs/account.spec.js` | [#405](https://github.com/AlexandreZanata/Regnovum/pull/405) (2026-10-07) |
| P51 | Conta e privacidade | Perfil, exportações, no-store, CSRF e segurança do browser | `web/tests/core/{http,downloads,exports}.test.ts` | [#406](https://github.com/AlexandreZanata/Regnovum/pull/406) (2026-10-07) |
| P52 | Descoberta das Arenas | Busca, autoria, documento público e agregado | `tools/e2e/specs/participation.spec.js` | [#407](https://github.com/AlexandreZanata/Regnovum/pull/407) (2026-10-07) |
| P53 | Debate e persuasão | Posições, respostas, fontes e influência | Jornadas de participação | [#408](https://github.com/AlexandreZanata/Regnovum/pull/408) (2026-10-07) |
| P54 | Carteira e cobrança | Passes, Member, checkout com redirect guard e portal | `web/tests/pages/{checkout,subscription}.test.ts` | [#409](https://github.com/AlexandreZanata/Regnovum/pull/409) (2026-10-07) |
| P55 | Moderação e operação | Painel do operador, jobs, transparência e 3 rotas no contrato | `web/tests/pages/operator.test.ts` | [#410](https://github.com/AlexandreZanata/Regnovum/pull/410) (2026-10-07) |
| P56 | Staged e harness | 15 contratos staged com harness real, sem ativação | `tools/e2e/specs/staged.spec.js` | [#412](https://github.com/AlexandreZanata/Regnovum/pull/412) (2026-10-08) |
| P57 | Temporadas e contratos privados | Seasons, metering, commerce e disputes staged com client+UI+teste | 15/15 staged com prova | [#413](https://github.com/AlexandreZanata/Regnovum/pull/413) (2026-10-08) |
| P58 | Aceite do frontend | Cobertura complete, a11y, i18n/XSS, budgets e este aceite | [FRONTEND_COMPLETION_READINESS.md](quality/FRONTEND_COMPLETION_READINESS.md) | [#414](https://github.com/AlexandreZanata/Regnovum/pull/414) (2026-10-08) |

## Estado atual e pendências

- O backend está completo e mergeado; `make verify` passa em `main`.
- O portão de release está **vermelho de propósito** enquanto as duas decisões
  do titular não forem tomadas — `terms-of-use` e `security-channel` —, e
  [GOVERNANCE.md](GOVERNANCE.md) registra cada uma com a aplicação.
- Pendências técnicas achadas nas auditorias estão registradas com dono e
  próxima ação, não omitidas: o que falta antes de promoção automática está em
  [DEPLOYMENT.md](DEPLOYMENT.md) §5, e os achados de internacionalização
  (`I18N-02`, `I18N-05`) estão em [I18N_AUDIT.md](I18N_AUDIT.md).
- O job `backup` do CI ficou vermelho nos PRs #51 e #62 e foi mergeado assim: a
  causa raiz era dupla — a imagem MinIO saiu do Docker Hub e o job não
  construía a imagem da aplicação que as migrations usam — e a auditoria do
  checklist de release ainda era derrubada por um clone raso no `foundation`.
  A correção está no PR #63, verificada localmente com o gate de backup inteiro
  e a suíte `make verify`, e entra aqui quando for mergeada.
- As fases seguintes (qualidade e operação contínua) só começam depois da
  P20 fechada; cada uma entra nesta página quando for mergeada.
- **2026-10-08 (P58)** — frontend implementado, release não certificada: as
  famílias P50–P57 têm jornada real ou negativa com prova, a cobertura
  complete segura na árvore real e os orçamentos §11 do [FRONTEND.md](FRONTEND.md)
  passam; ver [FRONTEND_COMPLETION_READINESS.md](quality/FRONTEND_COMPLETION_READINESS.md).
  Gaps conhecidos e com dono: `security-audit` vermelho nos threats THR-ECON
  (trilha de economia), `TestDeliveredTreeMeasuresWhatTheRulesJudge` (5ª
  família `staged-contracts` sem o par, P56), 2 testes PG deletion em profiles
  e a revisão humana do en-US — todos pré-release, nenhum escondido. A
  certificação volta na P59 com o gate final pós-merge que reutiliza a P45.

## Como registrar

1. Ao fim de cada fase, acrescente a linha na tabela acima com a data do merge
   e o link do PR. A evidência é o documento que o próprio gate produz — nunca
   uma paráfrase: se a ferramenta escreve o registro, o link aponta para ele.
2. Decisão nova de produto ou de arquitetura entra também em
   [DECISIONS.md](DECISIONS.md) ou em um [ADR](adr/README.md), no mesmo conjunto
   de mudanças.
3. Publique o espelho com `./.local/git-flow.sh wiki`; ele copia `docs/` para a
   wiki do projeto. O plano local (`.local/`) nunca é publicado.
4. A história não é reescrita: uma correção entra como nota datada no mesmo
   espírito do histórico do Git — o que mudou, quando e por quê.
