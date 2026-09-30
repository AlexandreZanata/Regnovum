# ADR-015 — Fontes determinísticas de teste e o portão de efeitos

## Status

Aceito (P22-T02). Substitui, no [ADR-012](ADR-012-clock-random-ids-ports.md), a
forma do portão de efeitos e a regra de que stubs determinísticos vivem apenas
junto de cada teste.

## Contexto

A P22-T02 pede que o backend garanta portas determinísticas para tempo, UUID/IDs,
nonce e aleatoriedade **onde eles afetam regra**, com produção usando fontes
criptograficamente corretas e testes recebendo fontes explícitas; e que cenários
de expiração e ordenação sejam reproduzíveis com **seed registrada**.

O ADR-012 já tinha criado os ports (`ports.Clock`, `ports.Random`,
`ports.IDGenerator`) e um portão de arquitetura. Duas medições mostraram que a
garantia registrada ali não era a garantia real:

1. **O portão casava chamadas, não leituras.** Ele só reconhecia `time.Now()` e
   `rand.*()`; `Now: time.Now` (valor, em vez de chamada) e `time.Since(...)`
   passavam. A auditoria da P22-T02 encontrou cinco desses na árvore entregue —
   `backpressure` (três sítios), `ratelimit`, `turnstile` (dois sítios) — e,
   atrás do furo, um defeito de regra: o verificador de webhook da Stripe já
   recebia um `ports.Clock` e mesmo assim media a idade do evento com
   `time.Since`, de modo que a janela de tolerância (a defesa contra replay,
   THR-PAY-*) ignorava o relógio injetado e não era decidível com um instante
   fixo.
2. **Stub ao lado de cada teste não tem seed.** Um stub de relógio escrito
   dentro de um arquivo de teste resolve o determinismo daquele teste e não
   resolve o que a fase pede: uma falha que alguém reproduza. Sem um lugar
   compartilhado onde a seed é registrada, a única resposta honesta a "esse
   cenário é reproduzível?" era "rode de novo e veja".

## Decisão

**1. O portão lê expressões, com vocabulário fechado.** `effectViolation` em
`internal/architecture_test.go` julga o *seletor*, não a chamada, e o conjunto
do que ele proíbe é explícito e pequeno:

- `time.Now` — chamada **ou** valor (`Now: time.Now` é a mesma leitura escrita
  de dois jeitos);
- `time.Since` e `time.Until` — `Now`, disfarçado: subtraem o relógio da máquina
  de um instante injetado;
- todo seletor de um leitor de entropia (`crypto/rand`, `math/rand`,
  `math/rand/v2`), inclusive `rand.Reader` segurado sem chamada;
- todo seletor de uma biblioteca de identificadores (`github.com/google/uuid`,
  `github.com/gofrs/uuid`, `github.com/satori/go.uuid`), inclusive o construtor
  segurado sem chamada. Diferente de `time`, aqui **não há metade pura** a
  preservar: gerar um identificador é entropia com nome, e o produto mantém
  seus identificadores opacos (`ports.IDGenerator` em produção, com
  `clockseed.RandomIDs`, e `testsource.NewIDs` nos cenários), de modo que
  nenhum consumidor precisa da biblioteca nem para ler um valor.

Ficam **deliberadamente** fora: `time.Unix`, `Parse`, `Add`, `Sub`, `Before`,
`After` e durações (puros), e `Sleep`, `After`, `NewTimer`, `NewTicker`, `Tick`,
`AfterFunc` — esperar não é ler, e proibir `time.Sleep` trocaria uma garantia
real por uma errada. Um teste prova que a árvore de fato usa timers para esperar,
para que a permissão não seja uma regra sobre código que não existe mais.

O verificador é exercitado sobre fontes sintéticas — uma tabela de fontes que
precisa ser recusada e uma que precisa passar —, porque um portão cujos dentes
só são observados na árvore entregue não se distingue de um portão que não casa
nada.

**2. Dois donos do efeito, e o segundo não alcança produção.**
`internal/platform/clockseed` (produção) e `internal/platform/testsource`
(fontes determinísticas de teste) são a allowlist. `testsource` não é uma
conveniência: um cenário que pede "expire a janela" precisa de relógio e
entropia que ele controla, e não pode pedir emprestado o relógio de produção sem
voltar a depender da máquina. Um teste de arquitetura prova que nenhum arquivo
não-teste sob `internal/` ou `cmd/` importa `testsource`.

**3. Não há mais padrão silencioso para o relógio do sistema.** Os relógios
injetáveis de `backpressure`, `ratelimit` e `turnstile` continuam aceitando um
cliente que não injeta nada, mas o valor padrão passa a ser
`clockseed.SystemClockNow` — a leitura continua existindo, no único pacote
auditado para ela. A janela do webhook da Stripe passa a ser julgada pelo relógio
injetado (`v.clock.Now().Sub(eventTime)`), e um relógio ausente é recusado em
`NewWebhookVerifier` (`ErrMissingClock`): uma regra de segurança que não sabe
dizer "agora" não pode ser aplicada.

O nonce foi auditado junto: em `internal/platform/mfa` ele já vinha de uma fonte
injetada e `NewSealer` recusa um leitor ausente, então o que faltava ali era a
descrição — o comentário do `Seal` dizia que o nonce vinha "do sistema", e o
pacote não lê fonte nenhuma por conta própria.

**4. A seed é registrada.** `ARENA_TEST_SEED` fixa a seed da execução; sem ela, o
padrão é uma constante (`testsource.DefaultSeed`) e não uma leitura do relógio.
`SeedFor(t)` registra no log a seed em uso em **todo** caminho, e uma seed que
não parseia **falha** a execução em vez de cair no padrão — uma execução que
acredita estar reproduzindo uma falha e está rodando outra coisa é pior que uma
execução que diz que não pode. As fontes (`Clock`, `Random`, `NewIDs`) são
funções puras da seed, e os cenários de expiração (`turnstile`, janela de uso
único) e de ordenação (`clockseed`, instante que o identificador declara) são
gerados do seed e replicados a partir dele.

**5. O ciclo de vida de um teste tem dono, e o ambiente é efeito dele
(P22-T06).** Uma fonte determinística resolve *o que* o cenário lê; ela não
resolve *o que sobrevive* ao cenário. `internal/platform/testguard` é o dono
desse outro lado: ele entrega o recurso (diretório, listener, conexão,
goroutine, timer, processo, variável) e, quando o teste termina, pergunta se ele
acabou — e se quem o acabou foi o teste ou o próprio guarda, porque "o guarda
fechou" é justamente o vazamento que uma suíte verde esconde. O relato nomeia a
regra e a linha que criou o recurso, e uma fixture que vaza de propósito
(`testguard/testdata/leak`, fora de `./...` porque ela *deve* falhar) é a
falsificação: `make test-isolation` exige que a execução dela seja vermelha
e que cada regra declarada apareça por nome.

O **ambiente** entra no vocabulário do portão por causa disso. Ele já era lido
diretamente por `os.Getenv`/`LookupEnv`/`Setenv`/`Unsetenv` e o portão não
conhecia `os.Environ` — a leitura que compara as duas fotografias do ambiente foi
quem mostrou o furo. Agora `Environ` é vocabulário fechado e `testguard` é o
terceiro dono declarado, com a mesma prova de sempre: nenhum arquivo não-teste
sob `internal/` ou `cmd/` o importa.

O que sobrevive ao *processo* inteiro não é pergunta de teste nenhum, e por isso
o outro lado do gate é um instrumento separado: `tools/isolationaudit` fotografa
a máquina antes e depois de uma execução (bancos descartáveis do harness,
conexões presas a eles, diretórios do guarda), compara as duas e recusa o que
cresceu. Nenhuma suíte consegue responder por si mesma a essa pergunta — o
processo que responde precisa sobreviver ao processo que ele julga.

## Alternativas

- **Manter a busca textual e ampliar a allowlist por pacote**: rejeitada. O
  problema era a *forma* da leitura, não o lugar: `Now: time.Now` num pacote
  permitido é a mesma leitura que num pacote proibido, e a allowlist por pacote
  esconderia a próxima que aparecesse dentro dele.
- **Exigir o relógio em todos os construtores (assinatura com erro)**: adiada,
  não rejeitada. Ela alcança quatro construtores e dezenas de sítios de teste, e
  o que compra — nenhum padrão silencioso — já é obtido por o padrão ser lido no
  dono auditado e o portão não admitir outra leitura. Fica como revisão deste
  ADR quando a composição passar a exigir o relógio.
- **Um relógio global mutável de teste**: rejeitada no ADR-012 e continua
  rejeitada: vira estado escondido entre testes paralelos, que é exatamente o que
  a P22-T06 vai atacar.
- **Uma flag `-seed` no binário de teste**: rejeitada. O CI e o operador já têm
  o ambiente; uma flag a mais só existiria dentro de `go test` e não no comando
  que reproduz o cenário por fora.
- **Uma fonte "falsa" que se declara segura**: rejeitada. `testsource.Random` é
  documentadamente **não** criptográfico, e o portão que impede a importação fora
  de teste é o que impede que ele vire uma fonte de produção com nome de teste.

## Consequências

- uma falha de cenário é reproduzível: `ARENA_TEST_SEED=<seed> go test ./...`, com
  a seed impressa em toda execução que a usa;
- o portão de efeitos tem vocabulário declarado e testado por mutação sintética,
  então afrouxá-lo para fazer um pacote compilar passa a ser uma mudança visível
  (a tabela de fontes aceitas e recusadas);
- a garantia "nenhum efeito fora do dono" passa a valer para leituras que não se
  escrevem como chamada — inclusive as três que existiam e o portão anterior não
  via — e para a geração de identificadores por biblioteca, que a árvore não usa
  e que agora não entra sem aparecer na tabela de fontes recusadas;
- a janela de tolerância do webhook passa a ser decidível com um instante fixo,
  com teste dos dois lados da borda, e um relógio ausente é recusa em vez de
  panique no primeiro webhook da release;
- custo: uma entrada a mais em cada allowlist do portão de arquitetura (efeitos e
  variáveis de ambiente), dois pacotes de suporte a teste, e a responsabilidade de
  mantê-los fora de produção, provada por teste;
- o ciclo de vida de cada teste passa a ser medível (regra, recurso e linha),
  uma fixture prova que o detector morde, e o que sobra de uma execução inteira
  deixa de ser invisível: `make test-isolation` roda a suíte com ordem
  embaralhada e `-parallel=16` e compara a máquina antes e depois (`test-isolation`
  fora de `make verify` por rodar a suíte inteira, como `testenv-verify`).

## Revisão

Reavaliar quando o relógio passar a ser exigido na composição (a alternativa
adiada acima), quando um adapter precisar de relógio próprio e documentar a
necessidade, ou quando a P22-T04 introduzir datasets determinísticos que passem a
ser a fonte dos cenários. A T04 e a T05 consumiram a última: os datasets e os
simuladores usam as fontes desta decisão, e a T06 acrescentou o dono do ciclo de
de vida sem abrir uma segunda forma de ler o relógio — o orçamento de espera do
guarda é um timer, não uma leitura (`waitFor` deixou de comparar instantes), e o
portão continua recusando `time.Now`/`Since`/`Until` fora dos dois donos.
