# Diretrizes para Agentes

Este documento estabelece as regras mandatórias de execução técnica, arquitetura e controle de alterações para qualquer agente de IA ou desenvolvedor atuando neste repositório.

## 1. Regras de Execução e Git

- **Uma microtarefa por vez:** execute estritamente uma única tarefa por ciclo. É proibido acumular tarefas, pular etapas ou ampliar escopo não solicitado.
- **Commits atômicos:** cada tarefa concluída deve gerar exatamente um commit atômico após todos os gates passarem.
- **Padrão de commit:** utilize o formato Conventional Commits (`type(scope): descrição`), conforme documentado em `docs/COMMITS.md`.
- **Publicação autorizada (branch de fase + PR + merge):** uma branch por microfase (`phase-NN-<slug>`), um commit por microtarefa e um PR por fase. Após os testes direcionados e o exit gate, o merge exige `make quick-verify` local e o check remoto `Quick verification` verde. A suíte completa é gate de release somente depois do merge de todas as fases atuais até P44, na futura P45; P30/P44 apenas preparam os gates. Ferramenta canônica: `.local/git-flow.sh`. Nunca empurre direto em `main`; nunca use `--force`, `--admin` ou `--no-verify`.
- **Proibição de operações destrutivas:** é proibido usar `git reset --hard`, `git clean -fd`, `--force` ou `--no-verify`.
- **Limpeza do repositório:** antes de iniciar qualquer alteração, confirme que o repositório está limpo (`git status --short`). Ao finalizar, o repositório deve permanecer limpo.
- **Segredos e dados privados:** é estritamente proibido inserir dados reais, segredos, credenciais, endereços de email pessoais ou dumps de produção.
- **Critério de interrupção:** se qualquer teste, compilação ou validação falhar, ou se houver dúvida sobre requisitos, pare imediatamente e não faça commit.
- **Registro histórico e wiki:** ao encerrar cada fase, registre o que foi entregue, a evidência e o PR mergeado em `docs/HISTORY.md` e publique o espelho com `./.local/git-flow.sh wiki`. A wiki é a vitrine pública do histórico; o plano local (`.local/`) nunca é publicado.

## 2. Arquitetura Obrigatória

### Backend (Go)
- **Estilo arquitetural:** monólito modular com Ports and Adapters (Arquitetura Hexagonal).
- **Regra de dependências:** dependências apontam para dentro:
  `adapters → application → domain`; `bootstrap` compõe as instâncias.
- **Domain (`internal/<modulo>/domain`):** Go puro (apenas biblioteca padrão). É estritamente proibido importar HTTP, SQL, tags de serialização, frameworks ou drivers no domínio.
- **Application (`internal/<modulo>/application`):** casos de uso, orquestração de transações e ports (interfaces) orientados ao consumidor. Evite repositórios genéricos como `Repository[T]`.
- **Adapters Inbound:** HTTP baseado em `net/http` e `http.ServeMux` da biblioteca padrão (sem roteadores externos), CLI e workers. Rotas sob `/api/v1`. Erros mapeados para o padrão RFC 9457 Problem Details (`application/problem+json`).
- **Adapters Outbound:** PostgreSQL 18 (`pgx/v5`, `sqlc`), Stripe, Resend e Sentry/PostHog. Tipos de fornecedores e drivers nunca ultrapassam a fronteira dos adapters.
- **Persistência e dados:** PostgreSQL 18 (schema `app`, timestamps em UTC com `timestamptz`, identificadores ordenáveis). Ledger de INK é append-only (`bigint`). Valores monetários utilizam inteiros em minor units com código de moeda ISO (nunca `float`). Transações explícitas para operações financeiras e mudanças de estado crítico.

### Frontend (Plataforma Nativa do Browser)
- **Zero runtime externo:** nenhuma biblioteca ou framework de terceiros no browser (proibidos React, Vue, Angular, Svelte, HTMX, Alpine, Tailwind, etc.).
- **Componentes:** Custom Elements / Web Components nativos com prefixo `ga-` (Light DOM como padrão).
- **Emissão e Toolchain:** TypeScript 7 estrito compilado diretamente pelo `tsc` oficial para módulos ESM nativos. Sem bundlers (sem Vite, Webpack, Rollup ou Babel).
- **CSS:** CSS nativo com custom properties (tokens), `@layer` e container queries.
- **Contratos:** gerados em `web/src/contracts/generated.ts` a partir de `api/openapi.json` por gerador Go interno. O arquivo gerado nunca deve ser editado manualmente.
- **Segurança de renderização:** dados dinâmicos de usuário devem ser injetados exclusivamente via `textContent` ou text nodes. O uso de `innerHTML`, `outerHTML` ou `eval` é proibido.
- **Progressive Enhancement:** HTML semântico servido pelo backend via `html/template` para SEO e resiliência quando aplicável.

## 3. Política de Dependências

- **Standard Library First:** priorize sempre as capacidades nativas do Go e as APIs nativas da plataforma web.
- **Dependências aprovadas:**
  - Frontend runtime: zero dependências de terceiros.
  - Frontend build: exclusivamente o pacote oficial `typescript`.
  - Backend: apenas pacotes homologados e isolados nos respectivos adapters (`pgx/v5`, `sqlc`, `goose`, `golang.org/x/crypto`, Stripe SDK, Sentry SDK).
- **Novas dependências:** nenhuma dependência externa adicional pode ser adicionada sem justificativa técnica documentada, avaliação de alternativas nativas e aprovação formal via ADR.

## 4. Validação e Gates de Qualidade

Toda alteração deve ser validada antes de qualquer commit. Execute os testes diretamente pertinentes e a validação mínima da tarefa; falha conhecida não é adiada para release. Em Q0, teste casos positivos, negativos, autorização, idempotência, concorrência e falha parcial quando aplicáveis, com PostgreSQL real para regras transacionais. `make quick-verify` e o check remoto curto são o gate de integração; `make verify` e os gates caros são de release na P45, após todas as fases atuais até P44. Execute as verificações pertinentes:

- **Integridade Git:**
  ```bash
  git diff --check
  git status --short
  ```
- **Go:**
  - Código formatado com `gofmt`.
  - Checagem estática com linters e testes (`go test ./...`, detector de corrida `go test -race ./...`).
- **Frontend:**
  - Checagem estrita de tipos (`tsc --noEmit`).
- **Gates automatizados (via Makefile quando disponível):**
  - `make fmt-check`
  - `make lint`
  - `make test-unit`
  - `make test-integration`
  - `make test-contract`
  - `make test-security`
  - `make verify`
