# ADR-018 — Scanner DAST fixado

## Status

Aceito (P26-T10). Estreita o [ADR-011](ADR-011-dependency-admission-policy.md)
para varredura dinâmica, como o [ADR-016](ADR-016-static-analysis-toolchain.md)
fez para análise estática e o [ADR-017](ADR-017-mutation-testing-tool.md) para
mutação.

## Contexto

A P26-T10 pede um scanner DAST **avaliado por ADR** e depois fixado, rodando
contra stack descartável com perfis anonymous/user/admin, OpenAPI e corpus
próprio, sem chamadas externas. A matriz ampla de varredura roda somente na
P45; esta decisão fixa o instrumento, o portão que impede regressão até lá e
o que ele provou na árvore de hoje.

### O que foi avaliado

1. **OWASP ZAP (externo, JVM/Docker) recusado.** Exige imagem de terceiros,
   egresso para atualizar regras e minutos por varredura; nenhuma das três
   cabe na esteira hermética (`test-offline` prova que PR não usa rede) nem
   na política de dependências (runtime zero de terceiros no caminho de
   entrega). Seria o instrumento certo para uma superfície pública em
   produção, não para um gate de microtarefa.
2. **Nuclei com templates públicos recusado.** Templates versionados fora
   da árvore quebram reprodutibilidade byte a byte (a mesma razão que
   fixou o gremlins por `go run` com versão); além disso os templates
   genéricos não conhecem Problem Details, `arena_session` nem idempotência.
3. **Scanner próprio em Go stdlib, `tools/dast`, aceito.** Zero dependência
   nova (stdlib já homologada), rotas vindas do `api/openapi.json` da
   árvore, corpus próprio versionado junto, determinístico (sem relógio,
   sem rede, sem aleatoriedade) e com fixtures nas duas direções: um mux
   vulnerável que cada regra é obrigada a acusar e um mux correto que
   passa limpo. O preço é escopo consciente: ele julga transporte e
   protocolo (5xx, reflexão, bypass de auth, redirect aberto, headers,
   cookies), nunca regra de negócio — essa é da matriz T02.

### O que foi medido

Na árvore do commit que abriu a tarefa, contra o servidor de jornadas
sobre PostgreSQL descartável com Stripe simulado:

1. **Achou um 500 real antes de qualquer fixture: busca sem idioma.**
   `GET /api/v1/search/arenas?q=hello` (sem `language`) respondia 500
   `search_error`: com o filtro vazio a cláusula sumia mas o `$2`
   continuava ligado, quebrando a query irmã da de argumentos (que trata
   `''` explicitamente). Corrigido espelhando o padrão irmão
   (`AND ($2 = '' OR ...)`), com os pacotes de search verdes.
2. **Achou o cancelamento que quebrava o CHECK.** `POST
   /api/v1/me/deletion/cancel` sem motivo passava pela validação e morria
   em 500 na constraint `canceled_check`. O caso de uso agora recusa
   motivo vazio com 400 `invalid_cancel_reason` (ordem preservada:
   estado inexistente continua 409/404 como os testes cravavam).
3. **O scanner tentou se deslogar no meio do scan.** As sondas de payload
   levavam o cookie das contas inclusive em `/auth/logout`, matando a
   sessão e convertendo o resto em 401 falsos. Operações que destroem a
   credencial são puladas com perfil e documentadas; a sessão pertence à
   matriz T01, que é dona de credenciais.
4. **Após os dois fixes, a árvore limpa zera critical/high** com perfis
   anonymous/user/admin: 2 waivers válidos restam (endpoint de sinais sem
   composição, que panica por use case nil em servidor de teste; cookie
   CSRF legível por JS por desenho do double-submit), ambos com dono,
   motivo e expiração 2026-12-31.

## Decisão

- O scanner é `go run ./tools/dast -target <loopback> -openapi
  api/openapi.json [-user-cookie ...] [-admin-cookie ...]`; alvo fora de
  loopback é recusa de configuração, nunca varredura.
- `make dast` roda os testes do motor (fixtures nas duas direções,
  waivers, guarda de egresso, loader OpenAPI, contrato de exit codes).
- Varredura integral da árvore com credenciais é sob demanda e matriz na
  P45; não entra em `quick-verify` (sobe servidores e leva minutos) nem
  em `verify` (esteira de release, gate da P45).
- Severidades: critical (5xx/transporte, reflexão) e high (bypass de
   auth, redirect aberto, cookie com Domain) bloqueiam; medium reporta.
- Waiver válido exige regra conhecida, path, motivo, dono e expiração
  futura; expirado/desconhecido/sem dono/sem motivo recusa a execução, e
  waiver sem achado correspondente é violação `stale-waiver`.
- Regras fechadas hoje: `no-5xx`, `no-reflection`, `auth-bypass`,
  `unexpected-write`, `open-redirect`, `security-headers`,
  `cookie-flags`. Adicionar sonda é adicionar regra aqui e no mapa do
  motor, com fixture que a acende.

## Consequências

- `quality/dast-waivers.json` nasce com 2 entradas e expira em
  2026-12-31: sem re-triagem, o portão volta a falhar sozinho.
- Rotas sem composição em nenhum processo (portal, sinais de atribuição)
  continuam fora do mux de produção; a de sinais panica se montada sem o
  caso de uso — lacuna registrada para a T12, coberta pelo waiver até lá.
- A matriz T02 segue dona de autorização de negócio; o DAST nunca decide
  sobre 403 vs 404.
