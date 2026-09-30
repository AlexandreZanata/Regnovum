# ADR-016 — Toolchain de análise estática fixada

## Status

Aceito (P23-T02). Estreita o [ADR-011](ADR-011-dependency-admission-policy.md)
para as ferramentas de análise e substitui a previsão de `golangci-lint` de
[STACK.md](../STACK.md) §6.

## Contexto

A P23-T02 pede que a toolchain estática seja **avaliada por ADR** e depois
fixada, com regras para correctness, shadowing, error handling, nilness,
context, SQL/HTTP e segurança, e com a proibição de silenciar uma regra
globalmente para obter verde. O `Makefile` tinha `lint` como alvo **pendente**:
`make verify` o listava como não criado, nenhum job o chamava e
[docs/CI.md](../CI.md) §6 registrava a ausência.

### O que foi medido

As medições foram feitas na árvore do commit que abriu a fase, com o Go que o
`go.mod` declara (`go1.27.1`):

1. **`go vet ./...` é limpo.** Nenhum achado na árvore entregue.
2. **O staticcheck instalado na máquina é inútil aqui.** Ele foi construído com
   `go1.25` e o próprio módulo dele fixa `go1.26.8`; as duas versões recusam um
   módulo que declara `go1.27.1` (`package requires newer Go version go1.27`).
   Um analisador estático lê os dados de exportação da biblioteca padrão que
   analisa, então **um analisador construído com Go mais antigo não mede esta
   árvore**: ele mede outra linguagem.
3. **Com o Go fixado, o staticcheck v0.7.0 também não lê a árvore.** O
   `honnef.co/go/tools@v0.7.0` (2026.1) falha os mesmos dados de exportação. A
   leitura só funciona a partir do **`v0.8.1`** (2026.2.1).
4. **A árvore tem 35 achados do `v0.8.1`**, nenhum deles em código de produto
   novo: 5 em produto (`U1000` de código morto, `SA1012` de contexto nulo e
   `ST1005` de mensagem de erro), 14 em arquivos de teste e 19 em `tools/**`.
   O sinal de "a árvore é limpa" que uma execução anterior produziu era
   artefato de cache de build velho — e é exatamente por isso que o pino é
   verificado no início do gate em vez de presumido.
5. **O `golangci-lint` instalado recusa a árvore por versão** e é um agregador:
   ele não acrescenta análise que os dois anteriores não façam, e acrescenta um
   arquivo de configuração onde o silenciamento por regra é barato.
6. **Nenhum check dos analisadores cobre shadowing.** O `v0.8.1` publica 149
   checks e nenhum deles julga identificador sombreado. A fase nomeia a família;
   o gate registra a lacuna em vez de fingir que a cobre.

## Decisão

**1. A toolchain são dois analisadores, e os dois usam a toolchain da árvore.**
`go vet ./...` com o Go de `go.mod` e `staticcheck` na versão fixada em
`STATICCHECK_VERSION` (`v0.8.1`), resolvido por
`go run honnef.co/go/tools/cmd/staticcheck@<versão>` com
`GOTOOLCHAIN=$(STATICCHECK_TOOLCHAIN)` — lido do `go.mod`, porque um pino
escrito duas vezes é um pino que deriva. `make lint` executa o portão
`tools/staticaudit`, e o gate **começa perguntando ao analisador resolvido a
versão dele**: uma versão que diverge do pino é recusa. Um analisador que deriva
do pino analisa outra linguagem.

O `golangci-lint` **não é admitido**: ele agrega os mesmos checks, não cobre
shadowing que o staticcheck não cobre, e traz uma superfície de configuração
cujo caminho mais barato é desligar uma regra — a operação que esta tarefa
existe para tornar visível. A previsão de §6 de STACK.md passa a nomear o
staticcheck fixado.

O analisador **não é instalado**: o módulo é resolvido por `go run` (do proxy do
Go ou do cache de módulos) na mesma versão que o `Makefile` declara. Instalar
fora da árvore criaria uma segunda versão a manter de acordo com o pino — e a
medição que abriu esta tarefa é exatamente o custo disso: o binário instalado na
máquina, construído com `go1.25`, não lê esta árvore, e o gate ficaria vermelho
por ambiente em vez de por achado. É o mesmo precedente de `sqlc@v1.29.0` e
`govulncheck@v1.8.0` no que diz respeito a fixar a versão, com uma diferença: o
que entra no repositório é só o portão (`tools/staticaudit`), que é biblioteca
padrão.

**2. Cada família tem dono, e o dono é provado por fixture.** O portão carrega
uma tabela de famílias e, para cada uma, ou manda o analisador sobre uma fixture
que **precisa** ser recusada com o check nomeado, ou nomeia o teste que já a
recusa:

| Família | Dono | Prova |
| --- | --- | --- |
| correctness | `go vet` (printf) | `tools/staticaudit/testdata/correctness` |
| error handling | staticcheck `ST1008` | `tools/staticaudit/testdata/error_handling` |
| nilness | staticcheck `SA5000` | `tools/staticaudit/testdata/nilness` |
| context | staticcheck `SA1012` | `tools/staticaudit/testdata/context` |
| SQL | portão de arquitetura (P23-T01) | `TestOwnedDependenciesStayWithTheirOwners` |
| HTTP | portão de arquitetura (P23-T01) | `TestLayerDependenciesPointInward` |
| security | portão de arquitetura (P23-T01) | `TestTheBoundaryRulesRefuseFixtures` |
| shadowing | **ninguém** | lacuna registrada e impressa pelo gate |

As fixtures vivem sob `testdata/`, que o `go` pula em padrões terminados em
`./...`, então elas nunca são julgadas como código entregue: elas existem para
ser recusadas. Uma regra que parar de morder fica vermelha **nomeando a
família** em vez de virar uma execução limpa, e uma família cujo dono é um teste
tem a existência do teste verificada — uma alegação de cobertura não sobrevive
ao código que a fazia verdadeira.

SQL, HTTP e segurança não viram checks do staticcheck porque **nenhum analisador
sabe qual pacote pode falar SQL**: isso é fronteira, e a fronteira já tem dono
desde a P23-T01. A família de segurança também inclui o que não é análise
estática (CVE de dependência é `make vuln`, auditoria de release é
`make security-audit`), e o gate nomeia isso em vez de sugerir que um check o
cobre.

**3. O débito existente entra num baseline que só encolhe, e cada linha tem dono
e motivo.** `quality/lint-baseline.json` é versionado, com esquema declarado, e a
identidade de uma entrada é **(arquivo, check, mensagem) com a contagem** — a
linha não faz parte dela, porque mover o código não muda o achado. O gate recusa
nos quatro sentidos: achado novo, achado que cresceu, entrada que o documento não
tem, e **entrada que a árvore não produz mais**. Cada entrada nomeia a tarefa que
paga a dívida (as cinco restantes da própria fase 23) e o motivo do aceite; o
baseline existe para que a árvore entre no gate **antes** de a dívida estar
paga, com a dívida visível, e não para que a dívida desapareça.

**4. A supressão é local, nomeia o check, carrega o motivo — e é julgada.** O
gate aceita `//lint:ignore <CHECK> <motivo>` e recusa:

- `//lint:file-ignore`: um arquivo silenciado não é um achado silenciado;
- uma diretiva sem motivo;
- `//nolint:staticcheck`: vocabulário de **outro** linter. O analisador fixado
  não lê essa forma, então o comentário silencia nada enquanto **parece**
  silenciar — era o caso de três testes desta árvore, que passaram a usar a
  diretiva que o analisador realmente lê;
- uma diretiva que o analisador **leu e nunca precisou** (a resposta
  `this linter directive didn't match anything`): uma supressão que ninguém
  precisa se lê exatamente como uma que funciona, e só uma das duas é honesta.

As diretivas são lidas dos **comentários do arquivo parseado**, nunca dos bytes
da linha: um teste que cita `//lint:ignore SA1012 motivo` dentro de uma string
está descrevendo uma diretiva, não escrevendo uma — o scanner por bytes reprovava
os testes do próprio gate antes de reprovar um achado real, e foi assim que o
defeito apareceu.

**5. O gate entra em `make verify`, num job e na tabela do `tools/ciaudit`.**
Análise estática é gate de merge: ela roda no `foundation`, que já tem Go. O
`tools/ciaudit` ganha a linha `lint` na tabela de gates obrigatórios, então um
`lint` que saia do `Makefile` ou do job deixa de passar em silêncio.

## Alternativas

- **`golangci-lint` como alvo do `make lint`**: rejeitada. Ele agrega os mesmos
  analisadores, não cobre a lacuna de shadowing, e o arquivo de configuração dele
  é onde desligar uma regra custa uma linha sem dono nem motivo. Um agregador
  cujo caminho barato é o silenciamento não é a ferramenta certa quando a tarefa
  é justamente tornar o silenciamento visível.
- **Admitir um terceiro analisador só para shadowing** (por exemplo `gosec` ou um
  linter dedicado): rejeitada por ora. Shadowing não tem check nos analisadores
  fixados, e a fase pede que a decisão seja **avaliada**, não que a cobertura seja
  comprada a qualquer preço. A lacuna fica declarada e impressa por toda execução
  do gate; abri-la é um ADR novo que justifique a dependência.
- **Rodar o staticcheck instalado na máquina, sem pino**: rejeitada, e a medição
  é a razão. O binário instalado aqui foi construído com `go1.25` e **não lê** um
  módulo que declara `go1.27.1`; a primeira execução que "passou" com dois achados
  era cache de build velho, não uma árvore limpa. Um gate que aceita o que estiver
  instalado mede a máquina, não o commit.
- **Bater o martelo de débito em zero nesta tarefa**: rejeitada. Os 35 achados se
  distribuem por três tarefas seguintes da mesma fase (T03 mede complexidade e
  duplicação, T04 remove código morto, T05 cuida de erros, contextos e recursos;
  T07 julga a qualidade dos testes). Zerar aqui significaria **tocar código de
  produto que outras tarefas são donas de tocar** — e um baseline com dono e
  motivo é mais honesto que uma tarefa que invade o escopo das vizinhas.
- **Silenciar por regra para obter verde**: proibida pela tarefa e recusada pelo
  gate: a única forma de aceitar um achado é uma linha de baseline com dono e
  motivo, revisável num diff.

## Consequências

- `make lint` deixa de ser um alvo pendente e passa a exercer a análise estática
  sobre a árvore, dentro de `make verify`, no job `foundation` e na tabela do
  `tools/ciaudit` (22 gates);
- uma versão de analisador divergente **falha nomeando o pino e a versão
  resolvida**, em vez de medir outra linguagem;
- cada família de regra tem prova executável, e a família sem dono (shadowing) é
  impressa por toda execução — uma lacuna que se lê como lacuna;
- a dívida estática existente passa a ser visível, com dono (as tarefas restantes
  da fase 23) e motivo, num arquivo versionado que só encolhe, e um achado que
  desaparece **exige** que a linha saia;
- o vocabulário de supressão é o do analisador fixado; a forma
  `//nolint:staticcheck`, que já existia em três testes, foi convertida porque
  não silenciava nada do que dizia silenciar;
- custo: uma ferramenta externa resolvida por `go run` com a versão e a
  toolchain fixadas, um portão de biblioteca padrão (`tools/staticaudit`), um
  baseline versionado, quatro fixtures e a responsabilidade de manter o baseline
  encolhendo;
- a primeira execução de `make lint` resolve o módulo do analisador do proxy do
  Go (ou do cache de módulos) e o constrói no cache de build; não exige
  instalação nenhuma, e é por isso que nem o operador nem o CI têm uma segunda
  versão do analisador a manter em acordo com o pino.

## Evidências

- `make lint` verde sobre a árvore da fase: `go vet ./...` limpo, staticcheck
  `v0.8.1` com 35 achados aceitos por 34 entradas de baseline, as quatro famílias
  com fixture recusando o check nomeado, as três supressões locais aceitas e a
  lacuna de shadowing impressa;
- fixtures: `tools/staticaudit/testdata/{correctness,error_handling,nilness,context}`;
- porta de supressão convertida: `internal/notifications/adapters/outbox/outbox_test.go`,
  `internal/notifications/adapters/resend/resend_test.go`,
  `internal/notifications/application/deliver_test.go`;
- medições do contexto acima (vet limpo, recusa por toolchain, `v0.7.0` inútil e
  `v0.8.1` necessário, 35 achados) no registro da fase.

## Revisão

Reavaliar quando a dívida do baseline chegar a zero (as cinco tarefas restantes
da fase 23 nomeiam cada entrada), quando os analisadores fixados publicarem um
check de shadowing — a lacuna declarada aqui deixa de existir e o gate passa a
ter um dono para a família —, ou quando uma segunda linguagem entrar no
repositório (o pino é da toolchain do Go, e uma árvore com TypeScript no
`foundation` pede a mesma decisão para o lado do `tsc`).
