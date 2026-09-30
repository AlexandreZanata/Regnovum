# ADR-012 — Ports de clock, aleatoriedade e identificadores

**Status:** aceito em parte — a forma do portão de efeitos e a regra sobre stubs determinísticos foram revistas pelo [ADR-015](ADR-015-deterministic-test-sources.md)

**Data:** 2026-09-16

## Contexto

Casos de uso precisam de tempo corrente, aleatoriedade criptográfica e identificadores opacos. Ler `time.Now` e o leitor `crypto/rand` direto no domínio e na aplicação torna testes não determinísticos — relógio avança, entropia não se repete — e espalha o acesso ao efeito por camadas que deveriam ser puras. O plano (P02-T02) exige ports mínimos para esses efeitos, com implementações concretas em platform, e proíbe service locator.

## Decisão

Criar o package `internal/ports` com três interfaces pequenas e orientadas ao consumidor:

- `Clock` — `Now() time.Time`;
- `Random` — `Read([]byte) (int, error)` sobre fonte criptográfica;
- `IDGenerator` — `NewID() string` opaco e resistente a colisão.

As implementações de produção ficam em `internal/platform/clockseed`: `System` (relógio real arredondado ao microssegundo), `CryptoRandom` (leitor `crypto/rand`) e `RandomIDs` (128 bits de entropia + timestamp de segundo + checksum, URL-safe, seguro para concorrência). Stubs determinísticos vivem em `internal/platform/testsource`, alcançáveis somente de arquivos de teste — o portão de arquitetura prova o fecho, e por isso eles podem ser compartilhados sem virarem estado global (ver [ADR-015](ADR-015-deterministic-test-sources.md)). Nenhum service locator: construtores e casos de uso recebem os ports como parâmetros comuns.

A partir desta decisão, a busca `rg 'time\.Now|rand\.' internal` só admite ocorrências em adapters documentados e `internal/platform` (hoje `clockseed`); o teste `internal/architecture_test.go` passa a enforcement automático.

## Alternativas

- `time.Now` direto nos casos de uso com injeção tardia: não obriga o desenho até o primeiro teste determinístico falhar; rejeitada por espalhar o efeito.
- Relógio global mutável em package compartilhado: vira estado escondido entre testes paralelos; rejeitada.
- Gerador de IDs único global (tipo UUID v4 de biblioteca): exigiria dependência externa não aprovada e esconderia o formato; rejeitada — o formato fica selado dentro de `clockseed`.
- Service locator / container de dependências: contra a arquitetura hexagonal documentada; rejeitada.

## Consequências

- testes de aplicação e domínio são determinísticos por construção, sem `time.Sleep` nem entropia real;
- o grafo de imports mantém `adapters → application → domain`; `platform` e `cmd/bootstrap` compõem as implementações;
- a busca `rg 'time\.Now|rand\.' internal` permanece verificável no CI via teste de arquitetura, não por revisão manual;
- `RandomIDs` usa formato próprio selado; consumidores tratam o identificador como opaco, e o formato só muda com nova decisão.

## Revisão

Reavaliar quando o primeiro adapter real (PostgreSQL, Stripe, Resend) precisar de clock ou aleatoriedade próprios, ou quando `RandomIDs` não atender a um requisito de formato externo.
