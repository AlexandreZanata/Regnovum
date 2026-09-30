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
