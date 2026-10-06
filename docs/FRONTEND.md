# Arquitetura do frontend

**Ampliação de escopo (2026-10-06):** o frontend passa do harness inicial à cobertura completa das rotas aplicáveis; ver [direção e critérios de conclusão](FRONTEND_COMPLETION.md). P48–P59 cobrem composição real do servidor, clientes, páginas, painéis restritos, interfaces staged isoladas e aceite, sem mudar a stack nem autorizar lançamento econômico. A matriz integral aguarda o SHA final após essas fases.

**Status:** padrão obrigatório

**Runtime de terceiros no browser:** nenhum

## 1. Princípios

1. A plataforma do browser é o framework.
2. HTML funciona antes do aprimoramento quando a jornada permitir.
3. Um componente conhece seu contrato, não a página inteira.
4. Estado remoto pertence ao servidor; não criar cópia global sem necessidade.
5. Acessibilidade e segurança são parte da API do componente.
6. Performance é controlada por orçamento, não por impressão.

## 2. Toolchain

TypeScript 7 é compilado pelo `tsc` oficial para módulos ESM. O frontend não possui bundler, JSX, transpilador adicional, preprocessor CSS ou runtime de framework.

Imports relativos usam a extensão `.js` que existirá após emissão. Bare specifiers de pacotes são proibidos. O compilador preserva uma saída previsível de módulos; CSS é carregado por `<link>` ou pela composição documentada da página, não por plugin de bundler.

O `tsconfig.json` futuro deve habilitar, no mínimo:

- `strict`;
- `noUncheckedIndexedAccess`;
- `exactOptionalPropertyTypes`;
- `useUnknownInCatchVariables`;
- `noImplicitOverride`;
- `noFallthroughCasesInSwitch`;
- `verbatimModuleSyntax`;
- `isolatedDeclarations` quando a estrutura permitir.

`any` exige comentário com motivo e issue de remoção quando não for boundary inevitável. Preferir `unknown` e narrowing.

## 3. Tipos de componentes

- **Primitive:** botão, dialog, tabs e feedback; não conhece domínio.
- **Domain component:** apresenta ou coleta um conceito como argumento ou posição.
- **Page controller:** compõe componentes, URL, carregamento e navegação.
- **Service:** client HTTP, i18n, sessão visível e telemetria; expõe interface pequena.

Componentes não buscam service locator global. Dependências entram pelo construtor/factory, propriedade ou contexto explícito da página.

## 4. Contrato de componente

Todo componente documenta:

- tag e responsabilidade;
- atributos e propriedades;
- eventos emitidos e seus payloads;
- estados visual, vazio, carregando, erro e desabilitado;
- comportamento de teclado e foco;
- requisitos de CSS e tokens;
- efeitos externos e cancelamento;
- exemplos de uso.

Custom Elements são registrados uma vez. O componente trata reconexão ao DOM sem duplicar listeners. `disconnectedCallback` cancela requests e timers próprios.

## 5. Comunicação

- pai passa dados; filho emite intenção;
- eventos sobem com `CustomEvent` tipado;
- componentes irmãos não se importam nem se procuram no DOM;
- page controller coordena jornadas;
- alterações de URL usam APIs nativas e permanecem compartilháveis;
- nenhum event bus global genérico.

## 6. Client HTTP

Um único núcleo implementa:

- base URL e versão da API;
- headers comuns e request ID;
- CSRF;
- timeout e cancelamento;
- parsing de Problem Details;
- política de retry apenas para operações seguras ou idempotentes;
- tratamento uniforme de sessão expirada;
- observabilidade sem payload privado.

Clients por domínio compõem esse núcleo e retornam contratos tipados. Componentes nunca chamam `fetch` diretamente.

Tipos de transporte gerados ficam em `web/src/contracts/generated.ts`. O arquivo é sobrescrito pelo gerador interno e nunca recebe edição manual ou comportamento.

## 7. Estado

- estado efêmero permanece no componente;
- estado de página permanece no page controller;
- URL representa filtros e paginação compartilháveis;
- servidor é autoridade para conta, wallet, posição e permissões;
- optimistic UI só é usada quando rollback é claro;
- nenhuma mutação crítica é considerada concluída antes da resposta do servidor.

## 8. CSS nativo

Tokens cobrem cor, tipografia, espaçamento, raio, sombra, duração e z-index. Tokens semânticos substituem valores de marca dentro dos componentes.

Cada componente possui folha local importada pelo entrypoint da página. `@layer` define precedência. Container queries substituem breakpoints baseados na tela quando o componente depende de seu contêiner.

CSS deve funcionar em forced colors, zoom de 200%, reduced motion e navegação por teclado. Não esconder foco sem alternativa visível.

## 9. Segurança de renderização

- texto de usuário é atribuído por `textContent` ou criado como text node;
- `innerHTML`, `outerHTML`, `insertAdjacentHTML` e `eval` são proibidos para dados dinâmicos;
- JSON inicial é serializado com escaping seguro pelo servidor;
- URLs passam por parser e allowlist de protocolo;
- atributos de evento inline são proibidos;
- CSP bloqueia script não autorizado.

## 10. i18n

Textos não ficam espalhados em componentes. Catálogos tipados por locale vivem em módulo próprio gerado (`web/src/i18n/generated.ts`, produzido por `cmd/i18ngen` a partir de `locales/`). Arena mantém idioma independente do locale da interface. Formatação usa `Intl` nativo.

### 10.1 O runtime do frontend

O browser consome os catálogos por um runtime próprio, sem dependência de terceiros, em quatro módulos:

- `web/src/i18n/locale.ts` — identidade do locale: allowlist (`pt-BR`, `en-US`), canonicalização por `Intl.getCanonicalLocales` e resolução que devolve **apenas** um locale que o produto publica. Um valor próximo (`pt-PT`), malformado (`pt_BR`, uma tag com markup) ou de outro tipo nunca é refletido; sem candidato válido, cai no default do produto. É a contraparte no browser da precedência do servidor: o documento já chega renderizado no locale resolvido.
- `web/src/i18n/formats.ts` — helpers sobre `Intl`: `formatNumber`, `formatCurrencyMinor`, `formatInstant`, `formatRelativeTime`, `formatList` e `pluralCategory`. O locale é sempre explícito. Dinheiro usa minor units inteiros + currency ISO e é montado por aritmética inteira, sem passar por ponto flutuante, com a escala lida do próprio `Intl` (2 para BRL e USD, 0 para JPY, 3 para BHD) e o restante da formatação (símbolo, posição, separadores, sinal) tomado da renderização do ICU; um valor fracionário ou fora do intervalo seguro é recusado, não arredondado.
- `web/src/i18n/translator.ts` — `createTranslator(locale, { catalog?, namespaces?, fallbackLocale? })`: carrega o catálogo do locale restrito aos namespaces pedidos, interpola placeholders nomeados (valor numérico é formatado para o locale da página) e recusa o que não consegue renderizar. A substituição é uma passada única, então um valor que contenha `{outro}` permanece chaves literais e nunca é reexpandido; o catálogo é injetável, que é como os testes provam o mecanismo de plural e como o pseudo-locale da T10 lê um catálogo derivado.
- `web/src/i18n/localization.ts` — `createLocalization(locale)`: o controller **de uma página**. Trocar de idioma troca o translator e notifica quem assinou, sem recarregar o documento. A instância é criada pela página, nunca um singleton de módulo, então duas páginas do mesmo processo jamais herdam o idioma uma da outra.

Contrato e chave ausente:

- páginas e componentes recebem o texto pelo próprio contrato (propriedade ou atributo, como as primitives já fazem); só o runtime lê o catálogo gerado, e `web/tests/architecture.test.ts` reprova qualquer outro módulo que o importe — resolvendo o specifier contra o arquivo, para que `./generated.js` e `../i18n/generated.js` sejam a mesma acusação;
- chave ausente é erro (`MissingMessageError`), nunca a chave crua na tela; `fallbackLocale` (default `pt-BR`) é o caminho de resiliência de produção de `I18N_STANDARD.md` §8;
- placeholder declarado sem valor também é erro (`MissingPlaceholderError`), e a passada de substituição é a rede de segurança para um catálogo cujo texto discorde da própria declaração.

Plural usa variantes estruturadas por categoria CLDR (`chave.one`, `chave.other`), selecionadas por `Intl.PluralRules`: o zero é `one` em `pt-BR` e `other` em `en-US`, e comparar o número com um daria a resposta errada. Nenhuma mensagem do catálogo usa variantes hoje; o que existe é o mecanismo, provado com catálogo injetado.

Formatação implícita (`toLocaleString()` e companhia, sem argumentos) é proibida por gate: ela usaria o locale do navegador de quem lê em vez do locale em que a página foi renderizada.

Testes: `web/tests/i18n` (os dois locales, plural zero/one/other, BRL/USD/JPY/BHD, instante e fuso com transição de DST, lista, relative time, chave ausente, placeholder malicioso, troca de locale sem reload, separação entre páginas) e `web/tests/i18n/types.test.ts`, que usa `@ts-expect-error` para provar que as declarações geradas continuam literais — se uma delas deixar de ser erro, o `tsc` reprova a expectativa não usada.

### 10.2 Pseudo-locale, direção e o gate de texto

Os documentos que uma pessoa lê são renderizados pelo servidor, então é no lado do servidor que a resiliência do layout e a ausência de texto fora do catálogo têm de ser provadas — e é isso que P18-T10 acrescenta em três peças.

**Pseudo-locale derivado, somente em build que o pede.** `internal/i18n/pseudo.go` deriva um catálogo do locale default: cada mensagem sai entre `⟦ ⟧`, com as letras acentuadas e o texto maior (a derivação é proporcional à mensagem inteira, placeholders incluídos, e os placeholders são copiados caractere a caractere — renomear `{max}` para `{máx}` quebraria a única build que o renderiza). O catálogo só é **registrado** com a tag de build `pseudolocale` (`internal/i18n/pseudo_enabled.go`): o binário entregue não carrega o locale, porque não o compila, e `internal/i18n/pseudo_disabled_test.go` prova a ausência no build padrão enquanto `pseudo_enabled_test.go` prova o registro no build com a tag. A derivação é pura e determinística, e o teste unitário do catálogo cobre todas as mensagens — uma tradução nova entra no gate no mesmo commit em que entra em `locales/`.

**Direção do documento.** Toda tag `<html>` declara `lang` e `dir`, e o `dir` é calculado da mesma expressão que o `lang` — `dir="{{dir .Lang}}"` —, então os dois não podem discordar. `dir` é uma **função de template** (`websurface.Funcs`), não um campo de dados: um campo a mais é um campo a menos em alguma página nova. `internal/i18n.Direction` decide pela língua (subtag primária) e pelo script explícito quando existe (`ar`, `he`, `fa`, `az-Arab`), e o default é `ltr`: uma tag desconhecida nunca vira um layout da direita para a esquerda.

**O gate.** `tools/i18naudit` (`make audit-i18n`, dentro de `make verify`) varre a árvore entregue e falha fechado em três regras: prosa em nó de texto fora do catálogo; `<html>` com `lang` literal, sem `dir` ou com `dir` derivado de outro campo; e propriedade **física** em folha entregue (`margin-left`, `left:`, `text-align: right`) em vez da lógica (`margin-inline-start`, `inset-inline-start`, `text-align: start`). O scanner lê o código-fonte, não o build: layout e texto são propriedades da árvore que vai para produção. Ele nunca reescreve nada, e a correção é sempre do autor, no commit que introduziu o documento.

O que essa peça encontrou e corrigiu: as cinco páginas de destino dos links transacionais (`internal/identity/adapters/http`) eram fontes portuguesas hardcoded com `lang="pt-BR"` fixo — uma página que uma pessoa abre, num idioma que ninguém escolheu e que nenhum catálogo conhecia. Hoje são um documento e a seção `auth.landing.*` do catálogo, com a mesma regra de fallback das outras superfícies.

**No navegador.** `tools/e2e/specs/localization.spec.js` dirige as páginas com `Accept-Language: qps-Ploc` (o harness compila o binário com a tag e falha de imediato se ele não servir o pseudo-locale) e verifica quatro propriedades por documento: o locale chegou (`lang` e os marcadores), a direção está declarada, nada transborda (ninguém passa da viewport e o documento não rola para o lado, em 1280px e em 360px) e a estrutura acessível continua de pé (toda referência de `label`/`aria-*` resolve, todo controle tem nome acessível). Uma execução de controle no locale default prova que os marcadores significam o catálogo pseudo, e não um colchete qualquer.

## 11. Orçamentos iniciais

Orçamentos são guardrails a validar:

- JavaScript inicial por página pública: alvo máximo de 50 KB comprimidos;
- CSS inicial: alvo máximo de 40 KB comprimidos;
- nenhuma dependência remota bloqueando render;
- zero layout shift introduzido por componente sem reserva de espaço;
- interações principais acompanhadas por Core Web Vitals reais.

Exceções exigem medida, justificativa e revisão.

### 11.1 O gate que mede

`make audit-web` (`tools/webaudit`) mede o build entregue em `web/dist` e falha fechado. Cinco perguntas, cada uma com o artefato que interroga:

1. **orçamentos** — a soma dos bytes comprimidos de cada módulo do fecho de uma página pública (`web/dist/pages/*.js` e tudo que ele importa) contra os 50 KB, e o total das folhas de estilo do build contra os 40 KB. A medida é por resposta, não por concatenação: o browser busca um módulo por request, cada um com seu próprio fluxo de compressão, então o que se soma é o que o visitante baixa — nunca o peso de um bundle, que não existe neste pipeline. A compressão é gzip: brotli exigiria uma dependência que a política de dependências não admite, e o orçamento é guarda de regressão, não promessa sobre o que a borda negocia. "KB" é lido como 1.000 bytes, a leitura mais restrita.
2. **specifiers** — todo import do grafo entregue é relativo, resolve para um arquivo que o manifest publica e fica dentro do build: nada de origem externa (`https://…`, `//…`), de caminho absoluto, de bare specifier (o browser não tem resolver nem diretório de pacotes) nem de caminho não publicado.
3. **csp** — o código entregue não carrega `eval`, `new Function`, `document.write`, `javascript:` nem sink de HTML (`innerHTML`, `outerHTML`, `insertAdjacentHTML`), e a política que o binário entrega (lida do próprio `internal/platform/securityheaders`) continua sem `'unsafe-inline'`, sem `'unsafe-eval'` e sem origem alguma. Os comentários são apagados antes da leitura — este repositório documenta as regras nos módulos que as obedecem, e o gate não pode reprovar a própria documentação — preservando as linhas do arquivo original, para que a violação aponte a linha certa.
4. **network** — `fetch`, `XMLHttpRequest`, `WebSocket`, `EventSource` e `sendBeacon` aparecem somente sob `web/src/core/`: é o core que carrega timeout, CSRF, identidade de request e Problem Details, e uma página que busca por conta própria ignora todas essas garantias.
5. **build** — o manifest existe, todo arquivo que ele declara existe e há ao menos um entry em `pages/`. Um gate que não consegue ver o que mede não prova nada: arquivo declarado e ausente é erro, não skip.

O digest de cada registro do manifest é o contrato do servidor (`internal/platform/assets`, endereço imutável e `ETag`) e é verificado onde é usado; este gate mede conteúdo, não identidade.

O gate roda no `make verify`; os testes de `tools/webaudit` provam cada regra sobre um build de fixture, de modo que uma sonda que quebra uma regra falha nomeando o módulo e a linha, e o build real volta a passar quando ela sai.

## 12. Testes

- unidades para funções puras e state machines;
- contract tests do client HTTP;
- testes em browser real para lifecycle de Custom Elements;
- E2E para jornadas críticas;
- auditoria do build entregue para orçamentos e dependências (`make audit-web`);
- axe ou auditoria equivalente no CI sem virar dependência de runtime;
- screenshots para regressões de layout em páginas essenciais;
- teste com JavaScript desabilitado para fluxos progressivos prometidos.

## 13. Definição de pronto

Um componente só está pronto quando possui contrato, tipos estritos, teclado, estados completos, cancelamento de efeitos, CSS isolado, testes relevantes e nenhum dado privado em logs ou DOM público.
