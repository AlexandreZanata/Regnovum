# Política de dependências

**Status:** padrão obrigatório

**Referência arquitetural:** [ADR-011](adr/ADR-011-dependency-admission-policy.md) · [STACK.md](STACK.md) · [ARCHITECTURE.md](ARCHITECTURE.md)

**Última revisão:** 2026-09-24

---

## 1. Princípio fundamental: Standard Library First

O Regnovum adota o princípio de prioridade máxima às capacidades nativas das plataformas oficiais:
1. **No backend:** a biblioteca padrão da linguagem Go (`net/http`, `crypto`, `database/sql`, `html/template`, `log/slog`, etc.) é a primeira e principal escolha para resolução de problemas técnicos e arquiteturais.
2. **No frontend:** as APIs e padrões nativos da plataforma web (HTML semântico, Custom Elements/Web Components, CSS moderno com custom properties e `@layer`, `fetch`, `AbortController`) constituem o alicerce exclusivo de execução.

Dependências de terceiros não são conveniências para evitar código simples; são passivos de longo prazo que introduzem riscos de segurança, manutenção e obsolescência. Nenhuma dependência externa é admitida sem que a alternativa nativa tenha sido avaliada e sua insuficiência comprovada.

---

## 2. Política para o frontend

### 2.1 Zero runtime externo no browser
- O navegador do usuário **não executa nenhuma biblioteca de terceiros**. É estritamente proibido o uso de frameworks (como React, Vue, Angular, Svelte), microframeworks (como HTMX, Alpine.js), bibliotecas utilitárias (como jQuery, Lodash, Axios) ou bibliotecas de componentes.
- Componentes de interface são construídos exclusivamente como Custom Elements nativos com o prefixo `ga-` (conforme documentado em [FRONTEND.md](FRONTEND.md)).
- Estilização utiliza CSS nativo com `@layer`, container queries e custom properties. Proibido Tailwind, Bootstrap, Sass ou soluções de CSS-in-JS.
- Proibida a importação de scripts ou estilos a partir de CDNs externas em tempo de execução.

### 2.2 Dependências de build do frontend
- A única dependência autorizada para o frontend é o compilador oficial `typescript`, fixado e executado exclusivamente durante o processo de build para emissão de JavaScript ESM nativo.
- Proibido o uso de bundlers complexos (Vite, Webpack, Rollup, Babel). A compilação é realizada diretamente pelo `tsc`.
- Ferramentas de teste E2E (como Playwright) são confinadas ao pipeline de testes e nunca incluídas no pacote de produção. O pacote isolado é `tools/e2e` (P18-T07): ele depende de `web/`, nunca o contrário, e `tools/e2e/isolation-check.sh` recusa o build se o runner aparecer no pacote do frontend, no build referenciado pelas páginas ou no binário.

---

## 3. Política para o backend

### 3.1 Go puro no domínio e na aplicação
- As camadas centrais (`domain` e `application` em `internal/<modulo>/`) utilizam exclusivamente a biblioteca padrão do Go.
- É estritamente proibido importar drivers de banco de dados, bibliotecas de serialização de terceiros, frameworks HTTP, SDKs de fornecedores ou utilitários externos no domínio e nos casos de uso.

### 3.2 Isolamento estrito por Adapter
- Toda dependência externa homologada deve ser confinada a um **Adapter Outbound** ou **Adapter Inbound** específico.
- Tipos concretos de fornecedores (como structs do SDK Stripe, tipos do driver `pgx` ou handlers do Sentry) **nunca** ultrapassam a fronteira do adapter.
- A camada de aplicação define portas (ports / interfaces) pequenas e coesas; o adapter implementa essas interfaces traduzindo tipos externos para entidades ou value objects do domínio.

---

## 4. Catálogo de dependências homologadas

Cada dependência admitida no projeto possui uma classe clara, um owner responsável e um escopo restrito de atuação:

| Dependência / Tecnologia | Classe | Owner / Escopo | Finalidade | Licença |
|---|---|---|---|---|
| `typescript` (oficial) | Dev / Build Tooling | `web/` | Compilação estrita de TypeScript para módulos ESM nativos | Apache-2.0 |
| `github.com/jackc/pgx/v5` | Runtime Backend (Adapter) | `internal/platform/adapters/out/postgres` | Driver PostgreSQL de alta performance e conexão com banco | MIT |
| `sqlc` | Dev / Codegen Tooling | `db/queries` e `adapters/out/postgres` | Compilação de consultas SQL tipadas para Go sem reflexão | MIT |
| `goose` (`github.com/pressly/goose/v3`) | Dev / Migration Tooling | `db/migrations` | Gerenciamento e execução de migrations versionadas em SQL | MIT |
| `golang.org/x/crypto` | Runtime Backend (Adapter / Platform) | `internal/platform/crypto` | Hashing seguro de senhas com algoritmo Argon2id | BSD-3-Clause |
| `github.com/stripe/stripe-go` | Runtime Backend (Adapter) | `internal/billing/adapters/out/stripe` | Integração de Checkout, Billing e validação de webhooks | MIT |
| `Resend (API HTTP)` | Runtime Backend (Adapter) | `internal/notifications/adapters/email` | Envio de emails transacionais pela API HTTP do fornecedor, sem SDK | Proprietária (SaaS) |
| `github.com/rivo/uniseg` v0.4.7 | Runtime Backend (Platform) | `internal/platform/text` | Segmentação e contagem de grapheme clusters (UAX #29) para o limite de 3.000 clusters e a tarifação de 1 INK por cluster (ADR-013) | MIT |
| `staticcheck` | Dev / Quality Tooling | `tools/staticaudit` e o `Makefile` | Analisador fixado de `make lint`: correctness, nilness, contexto e tratamento de erro (ADR-016, que substituiu a CLI agregada) | MIT |
| `govulncheck` | Dev / Security Tooling | Pipeline de CI e `Makefile` | Verificação oficial de vulnerabilidades conhecidas em Go | BSD-3-Clause |
| `k6` | Test Tooling | `tests/load` | Testes de carga, estresse e validação de SLO de performance | AGPL-3.0 (CLI externa) |
| `playwright` | Test Tooling | `tools/e2e` | Testes end-to-end em navegadores reais (isolado da web; `make test-e2e` prova por gate que nada do runner é entregue) | Apache-2.0 |
| `gitleaks` | Dev / Security Tooling | Pipeline de CI e pre-commit | Varredura de credenciais e segredos no histórico Git | MIT |
| `dependabot` | Dev / Security Tooling | Repositório / GitHub | Monitoramento automatizado de novas versões e CVEs | Serviço GitHub |
| `PostgreSQL 18.x` | Infraestrutura / Dados | `infra/postgres` | Sistema relacional primário de registro e persistência ACID | PostgreSQL License |
| `Caddy 2` | Infraestrutura / Proxy | `infra/caddy` | Proxy reverso, terminação TLS automática e compressão | Apache-2.0 |
| `Cloudflare` (DNS/CDN/WAF) | Infraestrutura / Borda | `infra/cloudflare` | CDN de ativos públicos, mitigação DDoS e Turnstile | Proprietária (SaaS) |
| `Cloudflare R2` (futuro) | Infraestrutura / Storage | `internal/platform/adapters/out/storage` | Armazenamento de arquivos estáticos quando necessário | Proprietária (SaaS) |
| `Docker Compose` | Infraestrutura / Deploy | `infra/compose` | Orquestração local e de deploy do monólito na VPS | Apache-2.0 |
| `GitHub Actions` | Infraestrutura / CI | `.github/workflows` | Execução automatizada de testes e checagens no CI | Proprietária (SaaS) |

### 4.1 Dependências transitivas do build

Estas não foram escolhidas: chegam com as diretas acima e o `go.mod` as fixa. Elas entram no catálogo pelo mesmo motivo que as diretas — alguém tem de responder por elas — e o `make audit-deps` recusa a transitiva que ninguém aprovou, além de exigir a versão que a árvore declara e não a versão que alguém lembra.

| Dependência / Tecnologia | Classe | Owner / Escopo | Finalidade | Licença |
|---|---|---|---|---|
| `github.com/jackc/pgpassfile` | Runtime Backend (Transitiva) | trazida por `github.com/jackc/pgx/v5` | Leitura do arquivo de senha do PostgreSQL usada pelo driver | MIT |
| `github.com/jackc/pgservicefile` | Runtime Backend (Transitiva) | trazida por `github.com/jackc/pgx/v5` | Leitura do `pg_service.conf` pelo driver | MIT |
| `github.com/jackc/puddle/v2` | Runtime Backend (Transitiva) | trazida por `github.com/jackc/pgx/v5` | Pool genérico de recursos sobre o qual o pool do driver é construído | MIT |
| `github.com/mfridman/interpolate` | Runtime Backend (Transitiva) | trazida por `github.com/pressly/goose/v3` | Interpolação das variáveis de ambiente usadas nas migrations | MIT |
| `github.com/sethvargo/go-retry` | Runtime Backend (Transitiva) | trazida por `github.com/pressly/goose/v3` | Retentativa com backoff usada pelo goose ao falar com o banco | Apache-2.0 |
| `go.uber.org/multierr` | Runtime Backend (Transitiva) | trazida por `github.com/pressly/goose/v3` | Agregação de erros do goose | MIT |
| `golang.org/x/sync` | Runtime Backend (Transitiva) | trazida por `github.com/jackc/pgx/v5` | Primitivas de sincronização usadas pelo driver e pelo goose | BSD-3-Clause |
| `golang.org/x/sys` | Runtime Backend (Transitiva) | trazida por `github.com/jackc/pgx/v5` | Chamadas de sistema usadas pelo driver | BSD-3-Clause |
| `golang.org/x/text` | Runtime Backend (Transitiva) | trazida por `github.com/jackc/pgx/v5` | Normalização e codificação de texto usadas pelo driver | BSD-3-Clause |

### 4.2 Imagens de contêiner e actions da esteira

As imagens abaixo são as que entram no build e no deploy; as de produção são referenciadas por `@sha256:`, e o `make audit-deps` recusa a tag no arquivo que roda. As actions são presas a um commit revisado, com a tag no comentário como documentação, e o portão recusa a action que volta a apontar para uma tag.

| Dependência / Tecnologia | Classe | Owner / Escopo | Finalidade | Licença |
|---|---|---|---|---|
| `node:24-bookworm-slim` | Imagem de Build | `Dockerfile` (estágio `web`) | Compila o frontend com o `tsc`; não entra na imagem final | MIT |
| `golang:1.27.1-bookworm` | Imagem de Build | `Dockerfile` (estágio `build`) | Compila os binários Go do repositório; não entra na imagem final | BSD-3-Clause |
| `gcr.io/distroless/static-debian12:nonroot` | Imagem de Runtime | `Dockerfile` (estágio `runtime`) | Base da imagem final: sem shell, sem gerenciador de pacotes e sem root | Apache-2.0 |
| `trivy` | Dev / Security Tooling | `Makefile` e o job de supply chain | Varredura de vulnerabilidades da imagem construída | Apache-2.0 |
| `actions/checkout` | Dev / CI Tooling | `.github/workflows` | Clonagem do repositório no runner | MIT |
| `actions/setup-go` | Dev / CI Tooling | `.github/workflows` | Toolchain Go fixada pela versão do `go.mod`, com cache do `go.sum` | MIT |
| `actions/setup-node` | Dev / CI Tooling | `.github/workflows` | Node 24 com cache do `web/package-lock.json` | MIT |
| `aquasecurity/trivy-action` | Dev / CI Tooling | `.github/workflows` | O scanner da esteira, na versão presa por commit | Apache-2.0 |

### 4.3 Serviços alcançados por HTTP, sem SDK

Dois provedores de telemetria e o de email são alcançados pelas próprias APIs HTTP, em `internal/platform/observability` e `internal/notifications/adapters/email`: nenhum pacote `getsentry/sentry-go`, `posthog-go` ou `resend-go` entra no `go.mod`. Sob `Standard Library First`, um SDK é uma dependência nova que exige justificativa e ADR, e as APIs HTTP de envelope (Sentry), de lote (PostHog) e de envio (Resend) são suficientes.

| Dependência / Tecnologia | Classe | Owner / Escopo | Finalidade | Licença |
|---|---|---|---|---|
| `Sentry (API HTTP)` | Telemetria / Erro | `internal/platform/observability` | Recebimento dos relatórios de erro pelo envelope HTTP | Proprietária (SaaS) |
| `PostHog (API HTTP)` | Telemetria / Produto | `internal/platform/observability` | Recebimento dos eventos de produto pelo endpoint de lote | Proprietária (SaaS) |

> **Sentry e PostHog, sem SDK (P19-T05).** Os dois provedores de telemetria
> são alcançados pelas **próprias APIs HTTP**, em `internal/platform/observability`,
> do mesmo modo que o adapter de email chama a API do Resend sem adicionar o
> SDK dele. Nenhum pacote `getsentry/sentry-go` ou `posthog-go` entra no
> `go.mod`: sob `Standard Library First`, um SDK é uma dependência nova que
> exige justificativa e ADR, e a API HTTP de envelope (Sentry) e de lote
> (PostHog) é suficiente e estável. O que cruza a fronteira do adapter são
> valores planos (`ErrorReport`, `Event`), nunca tipos do fornecedor.

---

## 5. Política de versões e pinning

1. **Backend (Go modules):**
   - O arquivo `go.mod` deve fixar versões semânticas exatas (ou patches específicos).
   - O arquivo `go.sum` é versionado obrigatoriamente e valida os checksums criptográficos de cada dependência e suas dependências transitivas.
   - Atualizações utilizam `go get -u=patch` para patches de segurança ou atualização controlada por tarefa.
2. **Frontend (Node/TypeScript):**
   - O arquivo `package.json` define a versão exata do compilador `typescript` (sem prefixos `^` ou `~`).
   - O arquivo `package-lock.json` é mantido estritamente consistente e versionado no Git.
3. **Infraestrutura e contêineres:**
   - Imagens de contêiner em arquivos `Dockerfile` e `docker-compose.yml` utilizam tags específicas e devem apontar para digests imutáveis (`image@sha256:...`) em produção.
   - Proibido o uso de tags voláteis como `:latest`.

---

## 6. Política de licenças

O código do projeto não é uma dependência dele. A licença do **código deste repositório** é decisão do proprietário, registrada em [GOVERNANCE.md](GOVERNANCE.md) (`repository-license`): AGPL-3.0, identificador SPDX `AGPL-3.0-only`. Esta seção governa o outro lado — as licenças dos terceiros que o projeto incorpora ou executa:

- **Licenças homologadas para runtime e bibliotecas:** MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC e PostgreSQL License.
- **Licenças restritas/proibidas no runtime:** Licenças copyleft com cláusulas fortes de reciprocidade (como AGPL, GPL, SSPL, EUPL) são expressamente proibidas no código fonte compilado da aplicação. A reciprocidade da licença do projeto é uma obrigação de quem opera o serviço pela rede, **não** uma permissão para incorporar dependência copyleft: as duas listas não se contaminam.
- **Ferramentas de desenvolvimento e teste isoladas:** Softwares e executáveis de suporte executados externamente ao binário (ex.: `k6`, o `trivy` do operador) podem utilizar licenças como GPL ou AGPL, visto que não são linkados nem distribuídos junto à aplicação. É a classe do componente, em `quality/dependencies.json`, que diz onde fica essa fronteira e o que cada classe homologa — e o `make audit-deps` recusa a licença copyleft forte na classe de runtime, que é a que entra no binário.

---

## 7. Gestão de vulnerabilidades (CVEs)

A segurança da cadeia de suprimentos segue as diretrizes de [SECURITY.md](SECURITY.md):
1. **Varredura obrigatória:** o comando `govulncheck ./...` é executado antes de releases e em pipelines automatizados.
2. **Monitoramento contínuo:** alertas de segurança disparados pelo Dependabot ou relatórios externos são triados conforme a matriz de impacto do Threat Model.
3. **Prazos de remediação:**
   - Vulnerabilidades com severidade **Crítica** ou **Alta** com vetor de exploração direto devem ter plano de contenção em até 24 horas e atualização em até 72 horas.
   - Vulnerabilidades **Médias** ou **Baixas** são tratadas no ciclo regular de manutenção.
4. **Isolamento como barreira:** vulnerabilidades em dependências de adapters externos não devem comprometer o núcleo de domínio caso o adapter mantenha validação estrita de fronteira.

---

## 8. Processo de atualização

1. **Atualizações de rotina:**
   - Dependências são atualizadas preferencialmente em ciclos planejados e commits isolados do tipo `build(deps): ...`.
   - É proibido atualizar dependências como efeito colateral de tarefas de regras de negócio.
2. **Critérios de aprovação de atualização:**
   - Leitura e validação prévia do changelog do fornecedor.
   - Execução bem-sucedida de todos os testes de unidade, integração, contrato e corrida (`make verify`).
   - Confirmação de ausência de novas dependências transitivas indesejadas.

---

## 9. Política de descontinuação e remoção

Toda dependência externa é adotada sob a premissa de que poderá ser removida ou substituída:
1. **Critérios de descontinuação:**
   - Biblioteca declarada como sem manutenção ou abandonada pelo mantenedor.
   - Surgimento de vulnerabilidade crítica sem previsão de correção pelo mantenedor.
   - Disponibilização de suporte nativo equivalente na biblioteca padrão do Go ou plataforma web.
   - Violação de princípios arquiteturais do projeto.
2. **Facilidade de substituição:**
   - Como toda dependência externa é confinada a um adapter que implementa uma porta da aplicação, a remoção da dependência exige apenas a reescrita ou substituição do adapter correspondente, mantendo o domínio e os casos de uso intactos.

---

## 10. Regra de admissão de novas dependências

> **Gate mandatário:** nenhuma nova biblioteca de terceiros pode ser adicionada ao projeto de forma implícita.

Qualquer proposta de adição de nova dependência externa requer:
1. **Necessidade mensurada:** problema concreto que não pode ser resolvido com código razoável utilizando a biblioteca padrão ou a plataforma web nativa.
2. **Avaliação técnica documentada:** relatório curto demonstrando por que a implementação própria não é viável ou recomendada.
3. **Análise de fornecedor:** reputação, histórico de releases, licença compatível e impacto de dependências transitivas.
4. **Isolamento arquitetural:** definição prévia de qual adapter será o único owner da dependência.
5. **Plano de contingência e remoção:** documentação de como o sistema operará caso a biblioteca precise ser removida.
6. **Aprovação formal:** criação de um Architecture Decision Record (ADR) dedicado em `docs/adr/`, aprovado formalmente antes da inclusão do pacote nos manifestos de dependência (`go.mod` ou `package.json`).
