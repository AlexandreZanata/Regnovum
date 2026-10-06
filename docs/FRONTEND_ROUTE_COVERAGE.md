# Contrato de aceite do frontend — cobertura de rotas

**Status:** padrão de aceite em planejamento (P48-T04). Não certifica
nada: a cobertura completa só fecha na P58 e a release só se decide no
gate pós-merge que reutiliza a P45.

**Fontes normativas:** [FRONTEND.md](FRONTEND.md) (arquitetura e
orçamentos), `api/openapi.json` + 4 fragmentos staged (contrato),
`internal/*/adapters/{http,html}/routes.go` + probes da plataforma
(declaração), [frontend-routes](../quality/frontend-routes.json)
(inventário reconciliado, P48-T01),
[frontend-surfaces](../quality/frontend-surfaces.json) (alcance medido,
P48-T03) e [frontendcoverage](../tools/frontendcoverage) (portão,
P48-T02). A tabela de evidência abaixo é gerada desses arquivos; em
divergência, os arquivos vencem este documento.

## 1. O que este documento é (e não é)

É o contrato que cada microtarefa P49–P59 assina: os critérios que uma
jornada precisa cumprir, as páginas e roles planejadas, os estados
honestos que uma rota pode ter e a evidência exigida por operationId.
"Implementado/validado tecnicamente" continua diferente de "release
certificada/ativada": aprovações, parecer independente, jurídico por
país e matriz integral seguem gates separados, e produto econômico
segue desligado.

## 2. Critérios comuns de aceite por jornada

Toda operação aplicável ao browser precisa, na fase que a liga:

- client tipado compondo `web/src/core/http.ts`, binding de
  página/componente/form e teste de jornada de sucesso **mais**
  negativa de autorização/erro pertinente. Wrapper sozinho ou teste
  com mock não conta como integração;
- estados cobertos: loading, empty, success, error e disabled;
  códigos 401/403/404/409/422/429 quando aplicáveis; resposta
  abortada/stale sem vazar estado; duplo envio sem duplicar efeito;
  teclado/foco operáveis; confirmação explícita antes de ação
  irreversível;
- operação destrutiva ou financeira só exibe sucesso confirmado pelo
  servidor; chave de idempotência no HTTP sozinha não prova
  idempotência — a prova é o replay sem duplicar efeito;
- HTML é navegação/form realmente servido pelo backend, não fetch
  JSON inventado; documento funciona antes do aprimoramento quando a
  jornada permitir;
- textos em `pt-BR`/`en-US` vindos dos catálogos (`locales/`,
  `web/src/i18n/generated.ts`); `lang`, idioma do conteúdo e timezone
  separados; CSS lógico; links acessíveis; dados privados sempre
  `no-store`;
- renderização só via `textContent`/text nodes; `innerHTML`,
  `outerHTML`, `insertAdjacentHTML` e `eval` proibidos para dados
  dinâmicos; `fetch` somente sob `web/src/core/`; dinheiro em minor
  units inteiras + currency ISO, nunca `float`.

## 3. Orçamentos fixos (de [FRONTEND.md](FRONTEND.md) §11)

Fixados, sem renegociação por fase: JavaScript inicial por página
pública de **50 KB** comprimidos (módulos `web/dist/pages/*.js` e seu
fecho de imports, somados por resposta); CSS inicial de **40 KB**
comprimidos; nenhuma dependência remota bloqueando render; zero
layout shift sem reserva de espaço. "KB" lê-se 1.000 bytes e a
compressão é gzip. O guardião é `make audit-web`
(`tools/webaudit`); exceção exige medida, justificativa e revisão.

## 4. Entregas preservadas (não reescrever)

O que já serve o browser permanece e é reutilizado, nunca
reinventado: núcleo `web/src/core/http.ts` (+ `problem.ts`),
clients `web/src/core/clients/` (`auth.ts`, `arenas.ts`,
`positions.ts`, `arguments.ts`), páginas `web/src/pages/`
(`auth.ts`, `arena.ts`, `participation.ts`, `submission.ts`,
`aggregate.ts`, `instants.ts`), primitivas
`web/src/components/primitives/`, componentes de domínio
(`arenas/`, `commerce/`, `disputes/`, `metering/`,
`position-aggregate/`, `seasons/`, `transparency/`), runtime i18n
(`locale.ts`, `formats.ts`, `translator.ts`, `localization.ts`) e
jornadas servidas `AccountSurface`/`ParticipationSurface`
(P18-T05/T07). Componente recebe dados e emite intenção; página
orquestra; backend decide saldo, custo, direitos, role, prazo e
estado. Sem fetch direto no componente e sem token em
localStorage.

## 5. Páginas planejadas × roles

| Ator | Superfícies e páginas | Notas de autorização |
|---|---|---|
| Visitante (sem sessão) | `auth` (register/login/reset/verify), documento `/d/{slug}`, feed e busca (P52), leitura da página de participação (P52) | Leitura pública; escrita exige conta; sem sessão expirada por 401 de login |
| Titular | + drafts e publish/close (P52), posição/argumentos/respostas/retirada (P53), atribuições e reputação própria (P53), carteira/passes/assinatura — leitura (P54–P55), perfil/export/exclusão/sessões/MFA (P50–P51) | Servidor revalida dono em cada mutação; outra conta é a contraparte negativa (IDOR) em todo teste de dono |
| Outra conta | Mesmas páginas do titular, como contraparte adversária | Nunca enxerga nem muta recurso alheio; erro não expõe existência |
| Moderador (competência + MFA) | Console restrito: casos/claim/decisões (P55), sinais de atribuição (P55), recursos | Sem auto-promoção; promoção só via host (`ComposeAdministration`), nunca pelo browser |
| Operador (restrito) | Painel de jobs após P49-T09 (contrato operador/role/MFA), CLI `arena`, primeira administração via host | Sem retry público; sem DSN/payload secreto exposto |

## 6. Estados (vocabulário fechado)

Por rota: `declaration` (declarada, placeholder 404 no processo),
`mounted` (servida pelo processo no ambiente),
`validated` (jornada completa provada — **zero rotas neste estado
até P49+**). Por evidência: `NOT_VERIFIED` (pendência declarada) e
`MISSING_PUBLISHED_CONTRACT` (gap explícito dos 3 jobs). Por
postura do portão: `planning` (aceita pendência, não certifica) e
`complete` (zero lacuna browser e zero falta de contrato obrigatória;
só fecha na P58). Medição atual: 100 declaradas, 19 montadas, 81 só
declaração, 0 validadas — `make frontend-coverage` (planning) verde
e `FRONTEND_COVERAGE_MODE=complete` recusando 95 lacunas honestas.

## 7. Escopo excluído (sem botão público para completar percentual)

- Sondas `GET /health/live` e `GET /health/ready`: sem pessoa,
  sem client/página; motivo técnico registrado no inventário;
- webhook Stripe (`internal/billing/adapters/stripe/webhook.go`):
  ingresso servidor-servidor com assinatura verificada; contrato
  provider-only, nunca client UI — retorno de browser jamais concede
  benefício;
- CLI `arena server|worker|migrate` (+ administração via host):
  poder de operador fora do browser;
- métricas/pprof e rotas internas: sem superfície de browser;
- produtos deliberadamente desligados (Genesis sazonal, cobrança por
  medição, transferências comerciais, dízimo produtivo, decretos da
  Coroa, tribunal privado): consumo real só em harness sintético
  separado (P56/P57), nunca montados no servidor entregue;
- perfil GET não ganha editor fictício; dado faltante é gap
  explícito; UI indisponível nunca simula saldo, pagamento, decisão,
  campeão ou sucesso.

## 8. Fixtures sintéticas por ator (sem conceder roles pelo browser)

Cinco atores sintéticos cobrem visitante, titular, outra conta,
moderador e operador: emails `@example.test`, slugs e cursores
sintéticos, `dbtest` descartável por teste, `fakeemail` como sink e
relógio/semente injetados. Papéis de moderador/operador nascem de
sementes de teste e da administração via host, jamais de chamada do
browser; segredos de produção nunca entram em fixture. O padrão
executável vive em `internal/bootstrap/*_test.go` (`browser`,
`signedIn`, `publishedArena`, `creditInk`): a mesma forma serve
P49+ sem duplicar harness.

## 9. Evidência por operationId (100 entradas, todas com dono/fase/destino)

Gerada de `quality/frontend-routes.json` (dono, fase, client/página
ou exclusão, provas de declaração) cruzada com
`quality/frontend-surfaces.json` (estado montado vs declaração).
Nenhum endpoint interno foi exposto para completar percentual; os 3
gaps sem operationId são os jobs do operador, com contrato fechado
em P49-T09.

| operationId | Rota | Dono (audience) | Fase | Destino | Estado | Evidência |
|---|---|---|---|---|---|---|
| `acceptPrivateCaseTerms` | `POST /api/v1/me/disputes/cases/{key}/accepts` | titular (dono do recurso) | P57 | staged-disputes via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/disputes/adapters/http/routes.go`<br>`internal/disputes/adapters/http/openapi.fragment.json`<br>`internal/disputes/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `appealPrivateCaseRuling` | `POST /api/v1/me/disputes/cases/{key}/appeals` | titular (dono do recurso) | P57 | staged-disputes via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/disputes/adapters/http/routes.go`<br>`internal/disputes/adapters/http/openapi.fragment.json`<br>`internal/disputes/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `authConfirmPasswordReset` | `POST /api/v1/auth/password-reset/confirm` | visitante (jornada de conta) | P50 | identity-api via `auth` + `auth` | declaration | `internal/identity/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `authLogin` | `POST /api/v1/auth/login` | visitante (jornada de conta) | P50 | identity-api via `auth` + `auth` | declaration | `internal/identity/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `authLogout` | `POST /api/v1/auth/logout` | visitante (jornada de conta) | P50 | identity-api via `auth` + `auth` | declaration | `internal/identity/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `authRegister` | `POST /api/v1/auth/register` | visitante (jornada de conta) | P50 | identity-api via `auth` + `auth` | declaration | `internal/identity/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `authRequestPasswordReset` | `POST /api/v1/auth/password-reset/request` | visitante (jornada de conta) | P50 | identity-api via `auth` + `auth` | declaration | `internal/identity/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `authVerify` | `GET /api/v1/auth/verify` | visitante (jornada de conta) | P50 | identity-api via `auth` + `auth` | declaration | `internal/identity/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `authViewPasswordReset` | `GET /api/v1/auth/password-reset` | visitante (jornada de conta) | P50 | identity-api via `auth` + `auth` | declaration | `internal/identity/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `beginMFAEnrollment` | `POST /api/v1/me/mfa/enrollment` | titular (dono do recurso) | P50 | identity-api via `auth` + `planned-account-security` | declaration | `internal/identity/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `cancelAccountDeletion` | `POST /api/v1/me/deletion/cancel` | titular (dono do recurso) | P51 | profiles-api via `planned-account` + `planned-account` | declaration | `internal/profiles/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `changePosition` | `POST /api/v1/me/arenas/{id}/position/changes` | titular (dono do recurso) | P53 | positions-api via `positions` + `planned-participation` | declaration | `internal/positions/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `claimModerationCase` | `POST /api/v1/moderation/cases/{id}/claim` | moderador (competência + MFA) | P55 | moderation-api via `planned-moderation` + `planned-moderation-console` | declaration | `internal/moderation/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `closeArena` | `POST /api/v1/me/arenas/{id}/close` | titular (dono do recurso) | P52 | arenas-api via `arenas` + `planned-arenas` | declaration | `internal/arenas/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `confirmMFAEnrollment` | `POST /api/v1/me/mfa/enrollment/confirm` | titular (dono do recurso) | P50 | identity-api via `auth` + `planned-account-security` | declaration | `internal/identity/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `confirmMeteringPublication` | `POST /api/v1/me/metering/publications` | titular (dono do recurso) | P57 | staged-metering via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/metering/adapters/http/routes.go`<br>`internal/metering/adapters/http/openapi.fragment.json`<br>`internal/metering/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `confirmPosition` | `POST /api/v1/me/arenas/{id}/position` | titular (dono do recurso) | P53 | positions-api via `positions` + `planned-participation` | declaration | `internal/positions/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `createArenaDraft` | `POST /api/v1/me/arena-drafts` | titular (dono do recurso) | P52 | arenas-api via `arenas` + `planned-arenas` | declaration | `internal/arenas/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `createBillingCheckout` | `POST /api/v1/me/billing/checkout` | titular (dono do recurso) | P54 | wallet-billing via `planned-wallet` + `planned-wallet` | declaration | `internal/billing/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `createBillingPortal` | `POST /api/v1/me/billing/portal` | titular (dono do recurso) | P54 | wallet-billing via `planned-wallet` + `planned-wallet` | declaration | `internal/billing/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `decideModerationCase` | `POST /api/v1/moderation/cases/{id}/decisions` | moderador (competência + MFA) | P55 | moderation-api via `planned-moderation` + `planned-moderation-console` | declaration | `internal/moderation/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `deleteArenaDraft` | `DELETE /api/v1/me/arena-drafts/{id}` | titular (dono do recurso) | P52 | arenas-api via `arenas` + `planned-arenas` | declaration | `internal/arenas/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `downloadPersonalExport` | `GET /api/v1/me/exports/{id}/download` | titular (dono do recurso) | P51 | profiles-api via `planned-account` + `planned-account` | declaration | `internal/profiles/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `exportPublicArena` | `GET /api/v1/arenas/{id}/export` | visitante ou titular (leitura pública) | P52 | transparency via `planned-transparency` + `planned-transparency` | declaration | `internal/transparency/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `fileModerationAppeal` | `POST /api/v1/me/moderation/appeals` | titular (dono do recurso) | P55 | moderation-api via `planned-moderation` + `planned-moderation-console` | declaration | `internal/moderation/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `fileModerationReport` | `POST /api/v1/me/moderation/reports` | titular (dono do recurso) | P55 | moderation-api via `planned-moderation` + `planned-moderation-console` | declaration | `internal/moderation/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `filePrivateCaseDefense` | `POST /api/v1/me/disputes/cases/{key}/defenses` | titular (dono do recurso) | P57 | staged-disputes via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/disputes/adapters/http/routes.go`<br>`internal/disputes/adapters/http/openapi.fragment.json`<br>`internal/disputes/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `getAccountDeletionStatus` | `GET /api/v1/me/deletion` | titular (dono do recurso) | P51 | profiles-api via `planned-account` + `planned-account` | declaration | `internal/profiles/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getArenaDocument` | `GET /d/{slug}` | pessoa (documento HTML) | P52 | arena-document via `server-document` + `planned-arenas` | declaration | `internal/arenas/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getArenaDraft` | `GET /api/v1/me/arena-drafts/{id}` | titular (dono do recurso) | P52 | arenas-api via `arenas` + `planned-arenas` | declaration | `internal/arenas/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getArenaFeed` | `GET /api/v1/arenas` | visitante ou titular (leitura pública) | P52 | arenas-api via `arenas` + `planned-arenas` | declaration | `internal/arenas/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getArenaPassHistory` | `GET /api/v1/me/passes/history` | titular (dono do recurso) | P54 | wallet-billing via `planned-wallet` + `planned-wallet` | declaration | `internal/billing/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getArenaPassSummary` | `GET /api/v1/me/passes` | titular (dono do recurso) | P54 | wallet-billing via `planned-wallet` + `planned-wallet` | declaration | `internal/billing/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getArgumentAttributions` | `GET /api/v1/arguments/{id}/attributions` | visitante ou titular (leitura pública) | P53 | persuasion-api via `arguments` + `planned-participation` | declaration | `internal/persuasion/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getAttributionSignals` | `GET /api/v1/moderation/attribution-signals/{authorID}` | moderador (competência + MFA) | P55 | moderation-api via `planned-persuasion` + `planned-participation` | declaration | `internal/persuasion/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getBillingSubscription` | `GET /api/v1/me/billing/subscription` | titular (dono do recurso) | P54 | wallet-billing via `planned-wallet` + `planned-wallet` | declaration | `internal/billing/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getHealthLive` | `GET /health/live` | sonda interna (sem pessoa) | P48 | health — internal probe; no browser surface | mounted | `internal/platform/httpserver/routes.go`<br>`api/openapi.json`<br>`internal/platform/httpserver/httpserver_test.go::TestHealthEndpoints` |
| `getHealthReady` | `GET /health/ready` | sonda interna (sem pessoa) | P48 | health — internal probe; no browser surface | mounted | `internal/platform/httpserver/routes.go`<br>`api/openapi.json`<br>`internal/platform/httpserver/httpserver_test.go::TestHealthEndpoints` |
| `getMyPosition` | `GET /api/v1/me/arenas/{id}/position` | titular (dono do recurso) | P53 | positions-api via `positions` + `planned-participation` | declaration | `internal/positions/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getPositionAggregate` | `GET /api/v1/arenas/{id}/positions` | visitante ou titular (leitura pública) | P53 | positions-api via `positions` + `planned-participation` | declaration | `internal/positions/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getPrivateProfile` | `GET /api/v1/me/profile` | titular (dono do recurso) | P51 | profiles-api via `planned-account` + `planned-account` | declaration | `internal/profiles/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getProfileReputation` | `GET /api/v1/profiles/{username}/reputation` | visitante ou titular (leitura pública) | P51 | persuasion-api via `planned-account` + `planned-account` | declaration | `internal/persuasion/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getPublicArena` | `GET /api/v1/arenas/{slug}` | visitante ou titular (leitura pública) | P52 | arenas-api via `arenas` + `planned-arenas` | declaration | `internal/arenas/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getPublicArgument` | `GET /api/v1/arguments/{id}` | visitante ou titular (leitura pública) | P53 | arguments-api via `arguments` + `planned-participation` | declaration | `internal/arguments/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getPublicProfile` | `GET /api/v1/profiles/{username}` | visitante ou titular (leitura pública) | P51 | profiles-api via `planned-account` + `planned-account` | declaration | `internal/profiles/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getTransparencyDocument` | `GET /transparency` | pessoa (documento HTML) | P55 | transparency via `server-document` + `planned-transparency` | declaration | `internal/transparency/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getTransparencyMetrics` | `GET /api/v1/public/transparency` | visitante ou titular (leitura pública) | P55 | transparency via `planned-transparency` + `planned-transparency` | declaration | `internal/transparency/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getWalletBalance` | `GET /api/v1/me/wallet` | titular (dono do recurso) | P54 | wallet-billing via `planned-wallet` + `planned-wallet` | declaration | `internal/wallet/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `getWalletStatement` | `GET /api/v1/me/wallet/transactions` | titular (dono do recurso) | P54 | wallet-billing via `planned-wallet` + `planned-wallet` | declaration | `internal/wallet/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `listArenaArguments` | `GET /api/v1/arenas/{id}/arguments` | visitante ou titular (leitura pública) | P53 | arguments-api via `arguments` + `planned-participation` | declaration | `internal/arguments/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `listArenaDrafts` | `GET /api/v1/me/arena-drafts` | titular (dono do recurso) | P52 | arenas-api via `arenas` + `planned-arenas` | declaration | `internal/arenas/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `listArgumentReplies` | `GET /api/v1/arguments/{id}/replies` | visitante ou titular (leitura pública) | P53 | arguments-api via `arguments` + `planned-participation` | declaration | `internal/arguments/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `listModerationCases` | `GET /api/v1/moderation/cases` | moderador (competência + MFA) | P55 | moderation-api via `planned-moderation` + `planned-moderation-console` | declaration | `internal/moderation/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `listMyPositionChanges` | `GET /api/v1/me/arenas/{id}/position/changes` | titular (dono do recurso) | P53 | positions-api via `positions` + `planned-participation` | declaration | `internal/positions/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `listSessions` | `GET /api/v1/me/sessions` | titular (dono do recurso) | P50 | identity-api via `auth` + `planned-account-security` | declaration | `internal/identity/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `previewMeteringQuote` | `POST /api/v1/me/metering/quotes` | titular (dono do recurso) | P57 | staged-metering via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/metering/adapters/http/routes.go`<br>`internal/metering/adapters/http/openapi.fragment.json`<br>`internal/metering/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `publishArenaDraft` | `POST /api/v1/me/arena-drafts/{id}/publish` | titular (dono do recurso) | P52 | arenas-api via `arenas` + `planned-arenas` | declaration | `internal/arenas/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `publishArgument` | `POST /api/v1/me/arenas/{id}/arguments` | titular (dono do recurso) | P53 | arguments-api via `arguments` + `planned-participation` | declaration | `internal/arguments/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `readCurrentSeason` | `GET /api/v1/me/seasons/current` | titular (dono do recurso) | P57 | staged-seasons via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/seasons/adapters/http/routes.go`<br>`internal/seasons/adapters/http/openapi.fragment.json`<br>`internal/seasons/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `readMeteringReceipt` | `GET /api/v1/me/metering/publications/{id}` | titular (dono do recurso) | P57 | staged-metering via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/metering/adapters/http/routes.go`<br>`internal/metering/adapters/http/openapi.fragment.json`<br>`internal/metering/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `readMeteringStatement` | `GET /api/v1/me/metering/statement` | titular (dono do recurso) | P57 | staged-metering via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/metering/adapters/http/routes.go`<br>`internal/metering/adapters/http/openapi.fragment.json`<br>`internal/metering/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `readPrivateCaseFile` | `GET /api/v1/me/disputes/cases/{key}` | titular (dono do recurso) | P57 | staged-disputes via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/disputes/adapters/http/routes.go`<br>`internal/disputes/adapters/http/openapi.fragment.json`<br>`internal/disputes/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `readPrivateCaseRuling` | `GET /api/v1/me/disputes/cases/{key}/ruling` | titular (dono do recurso) | P57 | staged-disputes via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/disputes/adapters/http/routes.go`<br>`internal/disputes/adapters/http/openapi.fragment.json`<br>`internal/disputes/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `readSeason` | `GET /api/v1/me/seasons/{season_key}` | titular (dono do recurso) | P57 | staged-seasons via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/seasons/adapters/http/routes.go`<br>`internal/seasons/adapters/http/openapi.fragment.json`<br>`internal/seasons/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `readSeasonChampions` | `GET /api/v1/me/seasons/{season_key}/champions` | titular (dono do recurso) | P57 | staged-seasons via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/seasons/adapters/http/routes.go`<br>`internal/seasons/adapters/http/openapi.fragment.json`<br>`internal/seasons/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `readSeasonHistory` | `GET /api/v1/me/seasons/history` | titular (dono do recurso) | P57 | staged-seasons via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/seasons/adapters/http/routes.go`<br>`internal/seasons/adapters/http/openapi.fragment.json`<br>`internal/seasons/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `readTradeReceipt` | `GET /api/v1/me/commerce/contracts/{id}` | titular (dono do recurso) | P57 | staged-commerce via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/commerce/adapters/http/routes.go`<br>`internal/commerce/adapters/http/openapi.fragment.json`<br>`internal/commerce/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `readTradeStatement` | `GET /api/v1/me/commerce/statement` | titular (dono do recurso) | P57 | staged-commerce via `planned-staged-harness` + `planned-staged-harness` | declaration | `internal/commerce/adapters/http/routes.go`<br>`internal/commerce/adapters/http/openapi.fragment.json`<br>`internal/commerce/adapters/http/contract_test.go::TestRoutesMatchFragment` |
| `recordAttributions` | `POST /api/v1/me/position-changes/{id}/attributions` | titular (dono do recurso) | P53 | positions-api via `positions` + `planned-participation` | declaration | `internal/persuasion/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `recoverMFA` | `POST /api/v1/me/mfa/recovery` | titular (dono do recurso) | P50 | identity-api via `auth` + `planned-account-security` | declaration | `internal/identity/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `replyToArgument` | `POST /api/v1/me/arenas/{id}/arguments/{argumentID}/replies` | titular (dono do recurso) | P53 | arguments-api via `arguments` + `planned-participation` | declaration | `internal/arguments/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `requestAccountDeletion` | `POST /api/v1/me/deletion` | titular (dono do recurso) | P51 | profiles-api via `planned-account` + `planned-account` | declaration | `internal/profiles/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `requestPersonalExport` | `POST /api/v1/me/exports` | titular (dono do recurso) | P51 | profiles-api via `planned-account` + `planned-account` | declaration | `internal/profiles/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `revokeSession` | `POST /api/v1/me/sessions/revocation` | titular (dono do recurso) | P50 | identity-api via `auth` + `planned-account-security` | declaration | `internal/identity/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `rotateSession` | `POST /api/v1/me/sessions/rotation` | titular (dono do recurso) | P50 | identity-api via `auth` + `planned-account-security` | declaration | `internal/identity/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `searchArenas` | `GET /api/v1/search/arenas` | visitante ou titular (leitura pública) | P52 | search-api via `planned-search` + `planned-arenas` | declaration | `internal/search/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `searchArguments` | `GET /api/v1/search/arguments` | visitante ou titular (leitura pública) | P53 | search-api via `arguments` + `planned-participation` | declaration | `internal/search/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `showArenaParticipationPage` | `GET /arenas/{slug}` | pessoa (documento HTML) | P52 | participation via `server-document` + `planned-participation` | mounted | `internal/arenas/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `showLoginPage` | `GET /login` | pessoa (documento HTML) | P50 | account via `server-document` + `auth` | mounted | `internal/identity/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `showLogoutPage` | `GET /logout` | pessoa (documento HTML) | P50 | account via `server-document` + `auth` | mounted | `internal/identity/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `showPasswordResetConfirmPage` | `GET /reset/confirm` | pessoa (documento HTML) | P50 | account via `server-document` + `auth` | mounted | `internal/identity/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `showPasswordResetRequestPage` | `GET /reset` | pessoa (documento HTML) | P50 | account via `server-document` + `auth` | mounted | `internal/identity/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `showRegisterPage` | `GET /register` | pessoa (documento HTML) | P50 | account via `server-document` + `auth` | mounted | `internal/identity/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `showVerifyPage` | `GET /verify` | pessoa (documento HTML) | P50 | account via `server-document` + `auth` | mounted | `internal/identity/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `stepUpMFA` | `POST /api/v1/me/mfa/step-up` | titular (dono do recurso) | P50 | identity-api via `auth` + `planned-account-security` | declaration | `internal/identity/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `submitArenaArgument` | `POST /arenas/{slug}/arguments` | pessoa (documento HTML) | P53 | participation via `server-document` + `planned-participation` | mounted | `internal/arenas/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `submitArenaAttributions` | `POST /arenas/{slug}/attributions` | pessoa (documento HTML) | P53 | participation via `server-document` + `planned-participation` | mounted | `internal/arenas/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `submitArenaInitialPosition` | `POST /arenas/{slug}/position` | pessoa (documento HTML) | P53 | participation via `server-document` + `planned-participation` | mounted | `internal/arenas/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `submitArenaPositionChange` | `POST /arenas/{slug}/position/change` | pessoa (documento HTML) | P53 | participation via `server-document` + `planned-participation` | mounted | `internal/arenas/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `submitLoginPage` | `POST /login` | pessoa (documento HTML) | P50 | account via `server-document` + `auth` | mounted | `internal/identity/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `submitLogoutPage` | `POST /logout` | pessoa (documento HTML) | P50 | account via `server-document` + `auth` | mounted | `internal/identity/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `submitPasswordResetConfirmPage` | `POST /reset/confirm` | pessoa (documento HTML) | P50 | account via `server-document` + `auth` | mounted | `internal/identity/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `submitPasswordResetRequestPage` | `POST /reset` | pessoa (documento HTML) | P50 | account via `server-document` + `auth` | mounted | `internal/identity/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `submitRegisterPage` | `POST /register` | pessoa (documento HTML) | P50 | account via `server-document` + `auth` | mounted | `internal/identity/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `submitVerifyPage` | `POST /verify` | pessoa (documento HTML) | P50 | account via `server-document` + `auth` | mounted | `internal/identity/adapters/html/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `updateArenaDraft` | `PATCH /api/v1/me/arena-drafts/{id}` | titular (dono do recurso) | P52 | arenas-api via `arenas` + `planned-arenas` | declaration | `internal/arenas/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| `withdrawArgument` | `POST /api/v1/me/arguments/{id}/withdraw` | titular (dono do recurso) | P53 | arguments-api via `arguments` + `planned-participation` | declaration | `internal/arguments/adapters/http/routes.go`<br>`api/openapi.json`<br>`internal/contract/contract_test.go::TestContractRoutesMatchRegisteredRoutes` |
| — *(gap explícito, sem contrato publicado)* | `GET /api/v1/admin/jobs/dead` | operador (painel restrito) | P55 | jobs-admin — restricted operator panel without published contract; browser binding planned in P55, not claimed here | declaration | `internal/jobs/adapters/http/routes.go`<br>`internal/jobs/adapters/http/handler_test.go::TestHealthAnswersCountsAndLag` |
| — *(gap explícito, sem contrato publicado)* | `GET /api/v1/admin/jobs/health` | operador (painel restrito) | P55 | jobs-admin — restricted operator panel without published contract; browser binding planned in P55, not claimed here | declaration | `internal/jobs/adapters/http/routes.go`<br>`internal/jobs/adapters/http/handler_test.go::TestHealthAnswersCountsAndLag` |
| — *(gap explícito, sem contrato publicado)* | `POST /api/v1/admin/jobs/{id}/retry` | operador (painel restrito) | P55 | jobs-admin — restricted operator panel without published contract; browser binding planned in P55, not claimed here | declaration | `internal/jobs/adapters/http/routes.go`<br>`internal/jobs/adapters/http/handler_test.go::TestHealthAnswersCountsAndLag` |

## 10. Portões e handoff

- Agora: `make frontend-coverage` (planning) verde; `FRONTEND_COVERAGE_MODE=complete`
  recusando 95 lacunas honestas; testes `tools/frontendcoverage` (24) e
  `internal/bootstrap` de frontend (4+4) verdes com casos reais;
  `quick-verify` local + `Quick verification` no PR a cada fase;
- P49 monta as famílias MVP (T01–T09) e atualiza cobertura/estado
  somente da família entregue, com prova no processo;
- P58 fecha `complete` (zero lacuna browser, zero falta de contrato
  obrigatória) com jornadas E2E, segurança, carga e restore;
- só então o gate pós-merge reutiliza a P45 no SHA novo — sem tag,
  matriz integral ou release nesta fase.
