# Aceite do frontend — prontidão de implementação (P58)

Registro de aceite do frontend nativo, escrito na P58-T05 sobre o que foi
medido e mergeado. O documento declara **FRONTEND IMPLEMENTADO, RELEASE NÃO
CERTIFICADA**: cada família abaixo tem jornada real ou negativa com prova no
SHA citado, e a certificação de release (P45 reutilizada no novo SHA, P59 e o
gate final) continua pendente e fora deste documento.

Fontes de verdade: o histórico do Git, `quality/frontend-routes.json` e os
gates que os julgam (`frontendcoverage`, `webaudit`, `audit-web`,
`audit-i18n`, `privacy-audit`, `test-e2e`). Este arquivo é o índice que os
costura; nada aqui substitui a execução deles.

## Postura por família (medida em `b1b6b4e`, ambas as locales salvo nota)

- **Conta e autenticação (P50).** Registro, confirmação pelo email entregue
  fora do processo, sessão opaca httpOnly e sign-out: `tools/e2e/specs/account.spec.js`
  (pt-BR, en-US). Teclado via skip-link, foco no error-summary após recusa e
  nomes acessíveis: `tools/e2e/specs/accessibility.spec.js` (14 testes).
  Suíte E2E integral na árvore: **22/22** (`/tmp/t05-e2e.log` da execução;
  o harness descarta o banco ao sair).
- **Conta, perfil, privacidade e segurança (P51).** Exportação pelo port de
  downloads (URL de objeto criada e revogada uma vez), `cache: no-store` em
  `/api/v1/me/*`, `/api/v1/auth/*`, `/api/v1/moderation/*` e
  `/api/v1/admin/*` com `WritePrivate` nos handlers, CSRF double-submit e
  storage sem segredo: testes dirigidos `web/tests/core/{http,downloads,exports}.test.ts`
  + `make privacy-audit` verde.
- **Arenas, descoberta e autoria (P52).** Confirmação de posição, publicação,
  mudança e atribuição: `tools/e2e/specs/participation.spec.js` (×2 locales,
  arenas separadas por locale). Idioma da interface × idioma do conteúdo:
  teste cruzado `web/tests/pages/document.test.ts` + `expectContentLanguage`
  no browser. Revelação do agregado move o foco ao heading; 360 px e 640 px
  sem rolagem lateral: `accessibility.spec.js`.
- **Debate e persuasão (P53).** Posições, respostas, fontes e influência com
  limites do servidor: `web/tests/pages/{participation,arguments,attribution}.test.ts`
  e jornadas do `participation.spec.js`; hostile strings não executam
  (`<img onerror>`, `<script>` recusados nos modelos).
- **Carteira, passes e cobrança (P54).** Ofertas MVP allowlisted, redirect
  `https:` sem credenciais nem fragmento (`isSafeRedirectUrl`, `javascript:`
  recusado) e portal pelo mesmo guarda: `web/tests/pages/{checkout,subscription}.test.ts`;
  preços em inteiros com ISO de moeda, sem `float`.
- **Moderação e operação restrita (P55).** Painel do operador (saúde, dead,
  retry com motivo 1..200 e 409 lido em vez de reenviado) e denúncias:
  `web/tests/{pages/operator,core/jobs}.test.ts` + contrato 85/15/0.
- **Contratos staged e harness (P56).** 15/15 staged com client+UI+teste sob
  `planned-staged-harness`, nunca ativados pela direção:
  `tools/e2e/specs/staged.spec.js` + `frontendcoverage` modo planning.
- **Temporadas e contratos privados (P57).** Seasons, metering, commerce e
  disputes staged com recibo verbatim, política terminal explicada e recurso
  único: testes `web/tests/{seasons,metering,commerce,disputes}/` + pages.
- **Transversal (P58).** Cobertura complete: `FRONTEND_COVERAGE_MODE=complete`
  segura na árvore real (85 published VERIFIED com client+página+prova
  browser, 15 staged harness-only, probes excluídos). Orçamentos
  docs/FRONTEND.md §11: arena 47.739/50.000 B, demais páginas ≤8.505 B, CSS
  6.290/40.000 B (`make audit-web` verde). i18n: `make audit-i18n` verde,
  catálogos pt/en paritários, pseudo-locale fora do build entregue. Sem
  `innerHTML`/`eval` no fonte (architecture + `webaudit` csp).

## Gaps conhecidos (não escondidos)

- `security-audit` vermelho pré-existente e fora do frontend: threats
  THR-ECON sem matriz/registro (dono: trilha de economia, rumo a P59).
- `TestDeliveredTreeMeasuresWhatTheRulesJudge` falha desde antes da P58: o
  registro tem 5 famílias e a fase do portão nomeia 4 (`staged-contracts` da
  P56 sem o par). O gate `make audit-provenance` está verde.
- 2 testes PG deletion em `profiles/adapters/postgres` falham na árvore
  limpa (pré-existente, causa fora do frontend).
- Revisão humana do en-US continua pré-release (P58-T03): paridade
  automática não é certificado de idioma.

## Veredito

**FRONTEND IMPLEMENTADO, RELEASE NÃO CERTIFICADA.** O aceite por família
acima fecha a P58; a certificação volta na P59 com o gate final pós-merge
que reutiliza a P45 no novo SHA. Nenhuma tag, nenhuma release, nenhuma
matriz integral neste documento.
