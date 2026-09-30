# ADR-017 — Ferramenta de mutação fixada

## Status

Aceito (P24-T10). Estreita o [ADR-011](ADR-011-dependency-admission-policy.md)
para ferramentas de mutação, como o [ADR-016](ADR-016-static-analysis-toolchain.md)
fez para análise estática.

## Contexto

A P24-T10 pede que a ferramenta de mutação Go seja **avaliada por ADR** e
depois fixada, com operadores úteis selecionados, alvos Q0/Q1 medidos e a
demonstração de que um mutante perigoso morre. A matriz completa de
mutação/cobertura roda somente na P45; esta decisão fixa o instrumento com
que a T10 mede e o portão que impede regressão até lá.

### O que foi medido

As medições foram feitas na árvore do commit que abriu a tarefa, com o Go
que o `go.mod` declara (`go1.27.1`), contra os pacotes `domain` e
`application` dos onze módulos da fase.

1. **`go-gremlins/gremlins v0.6.0` lê a árvore.** Resolvido por
   `go run github.com/go-gremlins/gremlins/cmd/gremlins@<versão>`, como o
   staticcheck do ADR-016: nada é instalado, nenhum `require` entra no
   `go.mod`, e o gate começa perguntando a versão resolvida. Alternativas
   recusadas: `go-mutesting` (sem manutenção desde 2019, anterior aos
   generics e ao `go vet` atual) e um mutador próprio em stdlib (duplicaria
   o analisador, o executor e o relatório que o gremlins já publica em
   JSON legível por máquina).
2. **O coeficiente de timeout baixo esconde sobreviventes.** Com
   `--timeout-coefficient 30`, `internal/identity/application` media 86
   mortos, 0 vivos e 30 timed-out. Com `--timeout-coefficient 100`, os
   mesmos 120 mutantes resolvem em 105 mortos e 11 vivos, sem timeouts:
   19 mortes lentas e 11 sobreviventes lentos estavam arquivados como
   timeout. Um gate que aceita timeout sem triagem aceita sobrevivente
   sem ver. O pino é 100, e **todo timeout fora do manifesto é recusa**.
3. **Os operadores default medem lacunas reais; os opt-in ficam para a
   P45.** `ARITHMETIC_BASE`, `CONDITIONALS_BOUNDARY`,
   `CONDITIONALS_NEGATION`, `INCREMENT_DECREMENT` e `INVERT_NEGATIVES`
   produziram, cada um, mutantes mortos e mutantes vivos que viraram
   testes (fronteiras de comprimento, janelas de tempo, guardas de
   elegibilidade, verificação de assinatura). Os seis operadores opt-in
   (`INVERT_ASSIGNMENTS`, `INVERT_BITWISE`, `INVERT_BWASSIGN`,
   `INVERT_LOGICAL`, `INVERT_LOOPCTRL`, `REMOVE_SELF_ASSIGNMENTS`) ficam
   desligados e registrados como avaliação pendente da P45: ligá-los sem
   triagem seria contar mutante que ninguém julgou.
4. **O escopo medido esteriliza as sete áreas críticas.** Quinze pacotes
   `domain`/`application` (identity, wallet, billing/domain,
   moderation, profiles/application, transparency, positions/application,
   arguments/application, arenas/application, persuasion/application)
   medem 100% de eficácia após esta tarefa: cada sobrevivente morreu por
   teste novo ou entrou no manifesto com prova. `billing/application`
   (o processador de webhook, 52 sobreviventes iniciais) foi incluído de
   propósito: adiar o núcleo do webhook esvaziaria a área. Sete pacotes
   (profiles/domain, arenas/domain, arguments/domain,
   persuasion/domain, positions/domain, jobs/domain, jobs/application)
   ficam diferidos à P45 com motivo e gate registrados — adiamento
   explícito, nunca silêncio.
5. **Custo medido: ~3 minutos.** Os quinze pacotes somam ~2.300 mutantes e
   o `make audit-mutations` completo roda em ~3 minutos com 8 workers
   nesta máquina (cronometrado). É teste caro, não gate barato: ele entra
   em `make verify`, não em `make quick-verify`, cujo desenho documentado
   recusa o barato antes do caro. A matriz completa da P45 reexecuta tudo
   com os operadores opt-in avaliados.

## Decisão

**1. A ferramenta é o gremlins v0.6.0, resolvido por `go run`, com os
operadores default ligados e os opt-in desligados.** Pinos no `Makefile`
(`GREMLINS_MODULE`, `GREMLINS_VERSION`) e no registro
(`quality/mutations.json`); o portão recusa divergência entre os dois e
uma versão resolvida diferente da fixada. Coeficiente 100, 8 workers,
saída JSON por pacote: sem esses três, a medição não é a mesma.

**2. Cada alvo medido tem risco Q0/Q1 herdado do módulo no catálogo, e
cada área crítica tem arquivos com zero sobrevivente.** O registro lista,
por pacote, o risco, as áreas e o estado (`measured` ou
`deferred-p45` com motivo e gate). Todo pacote `domain`/`application`
dos onze módulos está em uma das duas listas: um pacote em nenhuma é
recusa (`unknown-target`). Os limiares são Q0 ≥ 90% e Q1 ≥ 80% sobre os
medidos; as áreas autorização, wallet, billing, webhook, idempotência,
moderação e privacidade exigem zero vivos não-manifestados nos arquivos
nomeados.

**3. Equivalente e hang entram no manifesto com prova, nunca em
silêncio.** Cada entrada nomeia pacote, arquivo, tipo de mutante,
classe (`equivalent` ou `hangs`), motivo e dono. O portão exige que cada
entrada case com ao menos um mutante vivo (equivalente) ou timed-out
(hang) do relatório fresco — entrada sem mutante é recusa (o código
mudou e a prova caducou) — e recusa todo vivo ou timed-out sem entrada.
Exemplos aceitos: fatiamento identidade (`s[:256]==s`), guarda
inalcançável (teto total antes do limite de domínio), ramo morto por
pré-validação do chamador, laço que diverge monotonicamente. Exclusões
de gerado/boilerplate seguem a mesma regra por manifesto explícito; a
lista atual é vazia.

**4. O portão nunca escreve e nunca confia em número antigo.** Ele
executa a ferramenta, lê o JSON de cada alvo, calcula eficácia por
risco, cobra os limiares, a lista zero e o manifesto, e imprime o
resumo. Pontuação registrada em documento seria promessa; a medida é
feita a cada execução.

## Consequências

- `make audit-mutations` entra em `make verify` (e na tabela do
  `tools/ciaudit` como trigésimo gate); `docs/CI.md` e `docs/STACK.md`
  registram o custo e o pino.
- A P45 avalia os seis operadores opt-in, mede os sete pacotes
  diferidos e decide a certificação sobre a matriz completa.
- Se o teto de 12 minutos do job rápido for estendido, o mesmo registro
  já declara o escopo que entraria nele sem mudar o portão.
