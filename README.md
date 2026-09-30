# Regnovum

Regnovum é uma plataforma pública de debates estruturados que mede mudanças de
opinião e os argumentos que contribuíram para elas.

**Nome público:** Regnovum (decisão de 2026-09-23). Identificadores técnicos já
implantados — caminho local do repositório, URL remota, módulo Go, binário `arena`,
variáveis `ARENA_*`, tags de imagem e nomes de serviços — permanecem como estão.
A interface e os emails ainda podem exibir a marca anterior; esta mudança é
documental, não uma migração do runtime. A [direção constitucional do Reino](docs/reino/README.md)
continua separada do comportamento atualmente entregue.

O backend está implementado: o binário `arena`, a plataforma de browser em
TypeScript nativo, o PostgreSQL 18 com o ledger de INK e os gates de verificação
que descrevem, medem e recusam. O que ainda **impede o beta público** são duas
decisões do titular — `terms-of-use` e `security-channel` —, e `make release-gate`
continua vermelho nomeando cada uma enquanto elas existirem. A execução completa
que mede isso está em [docs/RELEASE_CHECKLIST.md](docs/RELEASE_CHECKLIST.md), e o
registro das decisões em [docs/GOVERNANCE.md](docs/GOVERNANCE.md).

## Índice

- [Índice da documentação](docs/README.md) — a ordem de precedência e todos os documentos.
- [Visão do produto](docs/PRODUCT.md), [Constituição](docs/CONSTITUTION.md) e [Regras de negócio](docs/BUSINESS_RULES.md).
- [Diretrizes do Reino](docs/reino/README.md) e [Carta Econômica](docs/reino/CARTA_ECONOMICA.md) — direção futura ainda não implantada.
- [Arquitetura](docs/ARCHITECTURE.md), [backend](docs/BACKEND.md) e [frontend](docs/FRONTEND.md).
- [Stack](docs/STACK.md), [CI](docs/CI.md) e [rastreabilidade dos requisitos](docs/REQUIREMENTS.md).
- [Deploy](docs/DEPLOYMENT.md), [runbooks](docs/RUNBOOKS.md) e [backup](docs/DEPLOYMENT.md).
- [Como contribuir](CONTRIBUTING.md) e [convenção de commits](docs/COMMITS.md).

## Arquitetura

Monólito modular com Ports and Adapters: as dependências apontam para dentro
(`adapters → application → domain`) e `internal/bootstrap` é quem compõe as
instâncias. Cada módulo vive em `internal/<módulo>/` — `arenas`, `arguments`,
`audit`, `billing`, `identity`, `jobs`, `moderation`, `notifications`,
`persuasion`, `positions`, `profiles`, `search`, `transparency` e `wallet` —, com
`internal/platform` para o que não é de produto (configuração, HTTP, banco,
observabilidade) e `internal/ports` para as fronteiras que o produto declara. Os
limites e as consequências de cada decisão estão em
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) e [docs/BACKEND.md](docs/BACKEND.md).

**Um binário só.** `arena` serve, consome a fila, migra e administra; nada mais é
instalado num host:

| Subcomando | O que faz |
|---|---|
| `arena server` | sobe o servidor HTTP e as jornadas compostas |
| `arena worker` | consome os jobs duráveis até receber `SIGTERM`/`SIGINT` |
| `arena migrate status\|up` | aplica ou inspeciona as migrations embutidas |
| `arena admin bootstrap\|revoke --email <address>` | administra do host, nunca por HTTP |
| `arena projections rebuild` | reconstrói as projeções públicas derivadas |
| `arena version [--json]` | identifica a build |
| `arena help` | lista os subcomandos |

O frontend é a plataforma nativa do browser: TypeScript 7 estrito compilado pelo
`tsc` para ESM nativo, Web Components com prefixo `ga-` em Light DOM, CSS nativo
com `@layer` e container queries, e **zero dependência de terceiros no runtime**.
O contrato consumido pelo browser é gerado de `api/openapi.json` para
`web/src/contracts/generated.ts` (`make generate`), e o arquivo gerado nunca é
editado à mão. Os orçamentos e as regras de dependência são medidos por
`make audit-web` e `make audit-i18n`.

Os dados vivem num PostgreSQL 18, schema `app`, timestamps em UTC e valores
monetários em inteiros de unidade mínima. O ledger de INK é append-only. As 32
migrations são **embutidas no binário** (`internal/platform/dbmigrate/migrations`)
e aplicadas por `arena migrate up`, então uma release não carrega uma ferramenta de
migração separada.

**O que o processo entregue serve hoje**, medido contra o binário em
desenvolvimento: `GET /health/live` e `GET /health/ready`; a jornada da conta
(`/register`, `/verify`, `/login`, `/logout`, `/reset`, `/reset/confirm`); a
jornada de participação (`/arenas/{slug}` e as quatro transições que se submetem a
partir dela); o documento público cacheável de uma arena (`/d/{slug}`); e o
frontend construído em `/assets`. A **API JSON do contrato ainda não é composta**
no processo entregue — as rotas `/api/v1/...` respondem 404 —, e isso é uma
pendência registrada, com dono e trabalho seguinte, na evidência da
[docs/RELEASE_CHECKLIST.md](docs/RELEASE_CHECKLIST.md) e de
[docs/DISASTER_DRILL.md](docs/DISASTER_DRILL.md).

## Configuração

Toda a configuração vem do ambiente, com o prefixo `ARENA_`. A lista versionada
completa, comentada variável por variável, é o [`.env.example`](.env.example):
copie para `.env` (que é ignorado pelo Git) e preencha lá. Uma variável `ARENA_*`
que o processo não conhece **recusa o boot** com uma mensagem que a nomeia, em vez
de ser ignorada em silêncio.

O essencial para desenvolver:

| Variável | Padrão | Para que serve |
|---|---|---|
| `ARENA_ENV` | `development` | `development`, `test` ou `production`; produção liga as regras de segurança abaixo |
| `ARENA_DATABASE_URL` | — | DSN do PostgreSQL; obrigatória em produção, e no desenvolvimento aponta para o serviço do `compose.yaml` |
| `ARENA_CURSOR_SECRET` | — | assina os cursores da jornada de participação; 32 bytes ou mais, estável entre reinícios. Sem ela a jornada não é montada em desenvolvimento e o boot é recusado em produção |
| `ARENA_ASSETS_DIR` | `web/dist` | diretório do build do frontend que o servidor serve |
| `ARENA_ADDR` | `127.0.0.1:8080` | endereço HTTP (host:port) |
| `ARENA_LOG_LEVEL` | `info` | `debug`, `info`, `warn` ou `error` |
| `ARENA_ADMIN_ADDR` | desligado | listener administrativo privado, só em loopback: métricas em `/metrics` no formato Prometheus e `/debug/pprof/` |

O resto é **opcional em desenvolvimento e obrigatório em produção**, cada um com a
regra de recusa escrita no `.env.example`: o provedor de email
(`ARENA_RESEND_API_KEY`, `ARENA_EMAIL_FROM`), o pagamento
(`ARENA_STRIPE_SECRET_KEY`, `ARENA_BILLING_PRICE_IDS`, `ARENA_BILLING_MARKETS`,
`ARENA_BILLING_SUCCESS_URL`, `ARENA_BILLING_CANCEL_URL`), o ajuste do pool
(`ARENA_DB_*`) e a telemetria (`ARENA_SENTRY_DSN`, `ARENA_POSTHOG_API_KEY`,
`ARENA_POSTHOG_HOST`, `ARENA_ANALYTICS_SAMPLE_RATE`). Em `development`, o sink
local de email (`ARENA_EMAIL_SINK_DIR`) grava as mensagens em disco para que uma
jornada seja completável sem provedor — e é **recusado** em produção, porque um
diretório de códigos de conta não é entrega.

## Desenvolvimento

Pré-requisitos: Go 1.27.1 (o que o `go.mod` declara), Node com npm, Docker com
daemon alcançável, e `sqlc` na versão fixada apenas para regenerar o código do
banco (`go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.29.0`).
O PostgreSQL de desenvolvimento é o serviço `db` do [compose.yaml](compose.yaml),
que publica uma porta só em loopback com credenciais de desenvolvimento — nunca
reutilizáveis em produção.

Do zero, num checkout limpo, na ordem:

<!-- quickstart:begin -->
```bash
docker compose up -d db
export ARENA_DATABASE_URL='postgres://arena:arena-local-dev@127.0.0.1:54329/arena?sslmode=disable'
export ARENA_CURSOR_SECRET='dev-cursor-secret-0123456789abcdef'
go run ./cmd/arena migrate up
make test-unit
make build-web
make image-verify
```
<!-- quickstart:end -->

Cada passo é uma coisa: o banco sobe com dados em volume nomeado (`docker compose
down` preserva; `down -v` apaga); as migrations são aplicadas pelo próprio binário;
`make test-unit` compila e testa a árvore (incluindo o catálogo pseudolocale);
`make build-web` compila o frontend e gera os assets com hash em `web/dist`, que é
o que `ARENA_ASSETS_DIR` aponta por padrão; e `make image-verify` constrói a imagem
de produção e a exercita contra um PostgreSQL descartável, provando que ela aplica
as próprias migrations e serve uma página e o asset que ela referencia.

Para ver a aplicação respondendo, com o build do frontend já feito:

<!-- serve:begin -->
```bash
ARENA_ENV=development ARENA_ASSETS_DIR=web/dist ARENA_ADDR=127.0.0.1:8080 go run ./cmd/arena server
```
<!-- serve:end -->

`/health/live` responde 200 quando o processo está de pé, `/health/ready` responde
200 quando o banco responde, e `/login` é a porta da jornada da conta. `arena
worker` é o outro processo: ele consome os jobs duráveis (entrega de email,
notificações) e precisa das mesmas variáveis de configuração.

O código gerado é versionado e conferido: `make generate` regenera o código do
`sqlc`, o contrato TypeScript e os catálogos de i18n, e `make generate-check`
recusa a árvore em que o artefato entregue divergiu da fonte. Nunca edite
`web/src/contracts/generated.ts`, `web/src/i18n/generated.ts`,
`internal/i18n/generated.go` nem os arquivos `*.sql.go` à mão.

## Teste

`make verify` é o gate de merge e é a mesma coisa que o CI roda no job
`foundation`: formatação, gerados em dia, testes unitários, integração,
corridas, migrations, contrato, segurança, frontend, auditorias de build e de
i18n, e a rastreabilidade dos requisitos. Ele exige um PostgreSQL alcançável em
`ARENA_DATABASE_URL` (os harnesses criam bancos descartáveis ao lado dele).

Os gates que exigem ambiente próprio têm alvo próprio, e nenhum deles está
escondido dentro de `verify`:

| Gate | Exige | O que cobre |
|---|---|---|
| `make test-e2e` | navegador | as jornadas de browser, nos dois idiomas |
| `make test-load-smoke` | `k6` | o smoke de carga versionado |
| `make image-verify` | Docker | a imagem de produção aplicando migrations, servindo página e asset |
| `make image-scan` | Docker, `trivy` | vulnerabilidades da imagem |
| `make vuln` | `govulncheck` | vulnerabilidades conhecidas das dependências |
| `make caddy-verify`, `make compose-verify`, `make deploy-verify`, `make backup-verify` | Docker | borda, topologia, deploy/rollback e backup/restore |
| `make migration-audit`, `make disaster-drill` | Docker (o drill: `k6` e navegador) | ciclo de vida das migrations e o exercício de desastre |
| `make release-gate` | — | as decisões humanas do lançamento |
| `make security-audit`, `make privacy-audit`, `make release-verify` | (a última: Docker) | auditoria de segurança, revisão de privacidade e a verificação reproduzível |
| `make handoff-check` | — | as afirmações deste README contra a árvore |
| `make handoff-walkthrough` | Docker | segue este README num checkout limpo e executa o smoke |

Nenhum desses alvos reduz limiar para passar: um vermelho nomeia a regra que
falhou e a correção é na causa. O `make verify` e os gates nomeados acima estão
descritos com as suas razões em [docs/CI.md](docs/CI.md).

## Operação

- **Migrations:** `arena migrate status` inspeciona, `arena migrate up` aplica. As
  fontes são embutidas no binário e a estratégia (janela, lock observado,
  compatibilidade para trás) está em [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) §6.
- **Processos:** `arena server` e `arena worker` compartilham a configuração; a
  fila é durável na tabela `app.jobs` e sobrevive a reinícios.
- **Saúde e telemetria:** `/health/live` e `/health/ready` são os sinais de vida e
  prontidão; `ARENA_ADMIN_ADDR` abre o listener administrativo (loopback) com
  `/metrics` e `/debug/pprof/`. Ele é privado por desenho, e num contêiner
  distroless isso significa que ele não é alcançável de fora — pendência
  registrada em [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) §5.
- **Administração:** `arena admin bootstrap --email <address>` promove o primeiro
  administrador e `arena admin revoke` desfaz. Os dois exigem um endereço cuja
  conta existe, foi verificada e já tem segundo fator confirmado; a confirmação é
  pedida no terminal a menos que `--yes` a dispense; cada transição grava o evento
  de auditoria na mesma transação.
- **Backup e recuperação:** [deploy/backup/base-backup.sh](deploy/backup/base-backup.sh)
  e [deploy/backup/restore.sh](deploy/backup/restore.sh) fazem o backup e a
  recuperação ponto-no-tempo, e `deploy/backup/verify.sh` exercita o ciclo. O RPO e
  o RTO medidos estão em [docs/DISASTER_DRILL.md](docs/DISASTER_DRILL.md).
- **Deploy:** [deploy/deploy.sh](deploy/deploy.sh) promove uma release por digest
  com o [compose.production.yaml](compose.production.yaml), e devolve a release
  anterior quando a prontidão não responde. O pipeline, os ambientes e a topologia
  estão em [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) §1–§5; os alertas e o que
  fazer em cada incidente, em [docs/RUNBOOKS.md](docs/RUNBOOKS.md).
- **Antes de publicar:** `make release-gate` (as decisões humanas),
  `make security-audit`, `make privacy-audit` e `make release-verify`, que reexecuta
  a verificação de ponta a ponta num checkout limpo e escreve
  [docs/RELEASE_CHECKLIST.md](docs/RELEASE_CHECKLIST.md). Enquanto as duas decisões
  do titular estiverem abertas, o portão recusa: uma verificação técnica verde não
  é uma release autorizada.
- **Segurança:** o canal de reporte está em [SECURITY.md](SECURITY.md) e o modelo
  de ameaças em [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md).

## Como contribuir

Leia [CONTRIBUTING.md](CONTRIBUTING.md) e [AGENTS.md](AGENTS.md). Em resumo: um
commit por mudança, na convenção de [docs/COMMITS.md](docs/COMMITS.md); o gate
`make verify` verde antes de qualquer commit; o que o código promete tem de estar
testado, e o que o documento promete tem de ser verificável; decisões de produto
no [log de decisões](docs/DECISIONS.md) e decisões técnicas nos
[ADRs](docs/adr/README.md). Nenhum segredo, dado real ou fixture de produção entra
no repositório.
