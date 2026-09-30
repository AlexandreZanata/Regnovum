# ADR-011 — Política de admissão de dependências

**Status:** aceito; detalha ADR-001, ADR-009 e ADR-010

**Data:** 2026-09-16

## Contexto

A inclusão descontrolada de dependências de terceiros é uma das principais fontes de falhas de segurança na cadeia de suprimentos (supply chain), obsolescência prematura, incompatibilidades em atualizações de versão e acoplamento a modelos conceituais de terceiros.

Para garantir estabilidade a longo prazo, reprodutibilidade de builds e facilidade de auditoria, o Regnovum necessita de um critério estrito e auditável para governança de bibliotecas e ferramentas.

## Decisão

Instituir o princípio **Standard Library First** e a política formal documentada em [docs/DEPENDENCIES.md](../DEPENDENCIES.md):

1. **Frontend nativo:** o browser executa zero bibliotecas de terceiros em tempo de execução. A única dependência permitida no desenvolvimento do frontend é o compilador oficial `typescript`, para emissão de ESM nativo.
2. **Backend isolado:** as camadas de domínio e aplicação (`internal/<modulo>/domain` e `application`) utilizam exclusivamente a biblioteca padrão do Go. Dependências de infraestrutura e fornecedores (`pgx`, `sqlc`, `goose`, `golang.org/x/crypto`, Stripe, Resend, Sentry, PostHog) são confinadas a adapters outbound específicos. Tipos de fornecedores nunca atravessam as portas da aplicação.
3. **Pinning e reprodutibilidade:** todas as versões são estritamente fixadas por checksums (`go.sum`, `package-lock.json`) e digests imutáveis de contêiner.
4. **Governança de licenças e vulnerabilidades:** apenas licenças abertas permissivas são aceitas no runtime. O scanner `govulncheck` é obrigatório na validação de qualidade.
5. **Admissão estrita:** nenhuma nova biblioteca de terceiros pode ser aprovada de forma implícita. Qualquer adição requer avaliação de alternativa nativa, justificativa técnica comprovada, owner atribuído, plano de saída e aprovação formal via ADR.

## Alternativas

- **Admissão livre orientada à conveniência:** acelera prototipação inicial, mas resulta em explosão de dependências transitivas, vulnerabilidades não monitoradas e dependência de mantenedores externos.
- **Uso de frameworks full-stack e bibliotecas abrangentes:** transfere decisões estruturais para fornecedores externos e dificulta evolução independente de módulos e do transporte.
- **Dependências compartilhadas pelo domínio:** reduz a quantidade de adaptadores e interfaces, mas contamina as regras de negócio com detalhes de bancos de dados ou APIs de terceiros.

## Consequências

- **Positivas:**
  - Superfície de ataque e risco de cadeia de suprimentos drasticamente reduzidos.
  - Regras de negócio protegidas e independentes de fornecedores.
  - Builds confiáveis, rápidos e sem dependências ocultas.
  - Frontend leve, seguro contra XSS por dependências e compatível com a plataforma nativa do browser.
  - Facilidade de substituir qualquer fornecedor externo reescrevendo apenas seu adapter.
- **Negativas / Desafios:**
  - Exige escrita explícita de DTOs e mapeamentos entre tipos de fornecedores e entidades do domínio.
  - Demanda implementação própria de pequenos utilitários em vez de recorrer a pacotes externos genéricos.
  - Requer disciplina rigorosa dos desenvolvedores e agentes de IA para não burlar fronteiras de pacotes.

## Regra de qualidade

Nenhuma nova dependência externa pode ser incluída nos manifestos de dependência sem um ADR aprovado e a atualização prévia da tabela em [docs/DEPENDENCIES.md](../DEPENDENCIES.md). Linters de arquitetura e checagens estáticas devem barrar imports de pacotes não autorizados fora de seus adapters.

## Revisão

Reavaliar a política anualmente ou quando novas capacidades da biblioteca padrão do Go e das APIs nativas do browser tornarem obsoletas dependências atualmente homologadas.
