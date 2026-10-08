# Runbooks de incidente

**Status:** inicial (P19-T06). Os *thresholds* são ponto de partida, não promessa:
só viram garantia depois de tráfego real observado (ver "Regra dos thresholds").

Este documento é operacional. Ele responde a três perguntas, para cada alerta:

1. **o que o sinal significa** (e o que ele *não* significa);
2. **qual o threshold inicial** que dispara o alerta;
3. **qual a ação** — o que fazer nos primeiros minutos, como diagnosticar sem
   destruir nada, e o que corrigir para o incidente não voltar.

Fontes de verdade que este documento usa e não substitui:
[DEPLOYMENT.md](DEPLOYMENT.md) (topologia, ambientes, backup, telemetria),
[SECURITY.md](SECURITY.md) (requisitos técnicos),
[BACKEND.md](BACKEND.md) (arquitetura) e
[ROLE_MODEL.md](ROLE_MODEL.md) (papéis do banco).

## 1. Como os alertas são expressos

O processo publica **métricas RED/USE** no registry (P19-T05) e as serve em
texto Prometheus **apenas no listener administrativo** (`ARENA_ADMIN_ADDR`),
junto de `/debug/pprof/`. Não há Prometheus dentro da stack: o coletor é do
operador, o alvo é o `/metrics` do listener administrativo e as expressões
abaixo são PromQL sobre esse alvo.

Duas limitações que o operador precisa conhecer antes de confiar num alerta:

- **O endpoint é loopback.** `ARENA_ADMIN_ADDR` exige host loopback
  (`127.0.0.1`, `localhost` ou `::1`) e a imagem é *distroless*: não há shell,
  `curl` nem `wget` dentro do contêiner, e publicar a porta não ajudaria — um
  *bind* em loopback dentro do contêiner não é alcançável pela porta publicada.
  Em desenvolvimento (processo local) o endereço é alcançável direto; em
  produção o coletor precisa compartilhar o *network namespace* do processo
  (`network_mode: "service:app"` num sidecar, ou `nsenter` no host). **Fiar
  esse coletor é da implantação (P19-T07)** e está registrado como pendência.
- **Não há log de acesso por requisição ainda.** O processo registra eventos de
  ciclo de vida (subida, parada) e reporta *panics* ao Sentry; o 5xx
  operacional vem das métricas e do Sentry, não de uma linha de access log.

Os alertas de **host** (disco, certificado, firewall de origem) não têm métrica
de aplicação: vêm do coletor do host, do proxy ou do provedor de borda.

## 2. Severidade e resposta

| Nível | Significado | Resposta |
|---|---|---|
| **P1** | perda ou indisponibilidade de dados/serviço; ou risco de perda | acordar humano, mitigar agora, registrar incidente |
| **P2** | degradação mensurável com usuário afetado | responder no horário comercial, mitigar no mesmo dia |
| **P3** | sinal de tendência; nada quebrado ainda | abrir tarefa, observar, corrigir na semana |

Regra de ouro: **antes de qualquer ação destrutiva, faça a mitigação reversível
primeiro.** Operações marcadas com **⚠ destrutivo** nunca são o primeiro passo.

## 3. Sumário dos alertas

| # | Alerta | Sinal | Threshold inicial | Ação curta | Runbook |
|---|---|---|---|---|---|
| R1 | Erros 5xx | `http_requests_total{status=~"5.."}` / total | razão > 1% em 5 min **e** ≥ 5 erros (P1 se > 5%) | confirmar no Sentry, isolar rota, reverter imagem se ligado a deploy | [R1](#r1--erros-5xx) |
| R2 | Disco | uso do filesystem / inodes | > 80% (P2) / > 90% (P1); inodes > 85% | achar o consumidor, girar/expirar log, nunca apagar WAL | [R2](#r2--disco) |
| R3 | Conexões de DB | `db_pool_connections{state}` vs `max`, `db_pool_empty_acquires_total`, `db_pool_canceled_acquires_total` | acquired/max > 0,8 por 5 min, ou qualquer `canceled` novo (P2) | achar query longa, checar vazamento de conexão, não subir `max_connections` às cegas | [R3](#r3--conexões-de-db) |
| R4 | Backup / arquivamento WAL | `pg_stat_archiver.failed_count` e idade do WAL arquivado | qualquer `failed_count` novo, ou último arquivo > 10 min (P1) | ler o motivo, restaurar o arquivamento, só então reter | [R4](#r4--backup-e-arquivamento-do-wal) |
| R5 | Webhook de pagamento | `app.stripe_events` em `failed`/`received`/`processing` | qualquer evento não-processado > 15 min, ou `failed` novo (P1 se dinheiro envolvido) | reenviar pelo provedor após corrigir a causa, reconciliar a janela | [R5](#r5--webhook-de-pagamento) |
| R6 | Atraso da fila | `jobs_lag_seconds`, `jobs_queue{state="due_now"}`, `jobs_oldest_dead_seconds` | lag > 300 s (P2), > 900 s (P1); `due_now` > 0 sem consumo por 5 min | checar worker vivo, diagnosticar a carga presa, retomar | [R6](#r6--atraso-da-fila-job-lag) |
| R7 | Email transacional | `jobs_processed_total{type="email_delivery",outcome="failed"}` e mortos | > 3 falhas em 15 min, ou qualquer morto `email_delivery` (P2) | ver o código do erro, checar provedor/cota, reenviar | [R7](#r7--email-transacional) |
| R8 | Certificado TLS | validade do certificado servido | < 21 dias (P3), < 7 dias (P2) | renovar/rotacionar o secret e recarregar o Caddy | [R8](#r8--certificado-tls) |
| R9 | Bypass de origem | requisição chegando à origem fora da borda | qualquer peer que não seja da borda (P1) | fechar o firewall, provar de novo com `make caddy-verify` | [R9](#r9--bypass-de-origem) |

## R1 — Erros 5xx

**Sinal.** `http_requests_total{status=~"5.."}` dividido pelo total da mesma
janela. Cada requisição conta uma vez por método, rota casada e status; o
`route` é o padrão do mux (`/thing/{id}`), nunca a URL — um estranho não pode
criar uma série por caminho. Um *panic* de handler é contado como **500** e
reportado ao Sentry.

**Threshold inicial.** P2 quando a razão passa de **1% em 5 min com pelo menos
5 erros**; P1 quando passa de **5%** ou quando a rota principal (login,
participação, checkout) está inteiramente vermelha.

**Primeiros 5 minutos.**

```promql
# razão de 5xx na janela
sum(rate(http_requests_total{status=~"5.."}[5m]))
  / clamp_min(sum(rate(http_requests_total[5m])), 1)

# qual rota está falhando
sum by (route, status) (rate(http_requests_total{status=~"5.."}[5m]))
```

```bash
# o processo serve o listener administrativo; o endereço é ARENA_ADMIN_ADDR
curl --silent --show-error "http://127.0.0.1:9090/metrics" | grep '^http_requests_total'
# o Sentry do erro: agrupe por release; um pico depois de um deploy é o primeiro suspeito
```

**Diagnóstico (não destrutivo).**

```bash
docker compose -f compose.production.yaml ps
docker compose -f compose.production.yaml logs --since 15m app | grep -i -E 'panic|error'
docker compose -f compose.production.yaml logs --since 15m caddy | grep -i -E ' 5[0-9][0-9] '
```

**Mitigação.** Se o pico coincide com um deploy, **reverta a imagem para o
digest anterior** com `./deploy/deploy.sh rollback --env-file .env.production
--health-url https://<site>` (P19-T07): ele promove o digest que o arquivo de
estado registra como anterior e só considera a reversão feita depois de a
prontidão responder 200. É reversível e não mexe em dados — e o schema fica
onde o passo *expand* o deixou, porque o runner não tem caminho de volta
(docs/DEPLOYMENT.md §5). Se a rota quebrada é de leitura, ela pode ser
desabilitada no edge (Caddy) enquanto a causa é corrigida.

**Correção.** Reler `http_requests_total` por rota e status para saber se o
erro é de uma rota ou de toda a superfície; se for *panic*, o Sentry aponta o
ponto e a correção vira um teste de regressão (o panic precisa ser reproduzido
antes de fechar o incidente).

## R2 — Disco

**Sinal.** Uso do filesystem e de inodes no host que roda PostgreSQL, o
contêiner da aplicação e o armazenamento local de trabalho do backup.

**Threshold inicial.** P2 acima de **80%**; P1 acima de **90%** ou acima de
**85% de inodes** (um filesystem com inodes esgotados falha mesmo com espaço
livre). O crescimento do WAL e o diretório de dados do banco são os primeiros
suspeitos.

**Primeiros 5 minutos.**

```bash
df -h          # espaço por filesystem
df -i          # inodes: um volume cheio de arquivos pequenos falha sem estar "cheio"
docker system df
docker compose -f compose.production.yaml ps
```

**Regras que não podem ser quebradas.** O consumo é governado, não adivinhado:

- os logs dos contêineres têm **rotação declarada** (`max-size: 10m`,
  `max-file: 5` no `compose.production.yaml`); um contêiner sem esse par é um
  processo que enche o disco onde o banco vive;
- o WAL é arquivado e a **retenção** é decidida por `deploy/backup/retention.sh`
  (`BACKUP_KEEP_DAYS`, `BACKUP_KEEP_MIN`);
- o `wal_keep_size` do serviço `db` mantém uma reserva que o servidor não
  recicla.

**Nunca** apague arquivos de `pg_wal/`, `pgdata/` ou do bucket de backup para
liberar espaço: isso é a diferença entre um incidente de disco e uma perda de
dados. A liberação correta é expirar via `retention.sh` (**sem** `--apply`
primeiro) e girar log.

**Mitigação.** Rotação/expiração de log; se o espaço é do banco, aumentar o
volume ou arquivar e reter com o pipeline de backup — nesta ordem.

## R3 — Conexões de DB

**Sinal.** USE do pool: `db_pool_connections{state="total|acquired|idle|max"}`,
`db_pool_empty_acquires_total` (saturação: um acquire encontrou o pool vazio),
`db_pool_canceled_acquires_total` (erro: um acquire desistiu esperando) e
`db_pool_acquire_seconds_total` (espera acumulada). Os contadores vêm do próprio
`pgxpool.Stat`, não de uma segunda cópia.

**Threshold inicial.** P2 quando `acquired/max` fica acima de **0,8 por 5 min**,
ou quando **qualquer** `canceled` novo aparece (um acquire desistiu), ou quando
`empty_acquires` cresce de forma sustentada. `canceled` é o sinal mais forte:
usuários estão recebendo erro de espera.

**Primeiros 5 minutos.**

```promql
db_pool_connections{state="acquired"} / clamp_min(db_pool_connections{state="max"}, 1)
increase(db_pool_empty_acquires_total[5m])
increase(db_pool_canceled_acquires_total[5m])
```

```bash
# quantas conexões e o que estão fazendo (somente leitura)
docker compose -f compose.production.yaml exec db \
  psql -U arena -d arena --tuples-only --command \
  "SELECT state, count(*) FROM pg_stat_activity WHERE datname = 'arena' GROUP BY state ORDER BY state;"

# consultas mais longas naquele instante
docker compose -f compose.production.yaml exec db \
  psql -U arena -d arena --tuples-only --command \
  "SELECT pid, state, wait_event_type, now() - query_start AS running_for, left(query, 80)
     FROM pg_stat_activity
    WHERE datname = 'arena' AND state <> 'idle'
    ORDER BY running_for DESC NULLS LAST LIMIT 20;"

# teto do servidor
docker compose -f compose.production.yaml exec db \
  psql -U arena -d arena --tuples-only --command "SHOW max_connections;"
```

**Mitigação.** Se há uma consulta travada, o caminho é investigar o plano e a
transação; **não** matar processo (`pg_terminate_backend`) como primeiro passo,
porque ele pode abortar uma transação financeira no meio. Se o vazamento é do
processo, reiniciar `app`/`worker` libera o pool — aberto, `max_connections`
**não** é ajustado às cegas: ele é o teto que a soma de todos os pools não pode
ultrapassar.

**Correção.** Um `canceled` recorrente sem query longa é vazamento de conexão
ou pool pequeno para a concorrência: medir `acquire_seconds_total` e o número
de instâncias antes de mexer em `ARENA_DB_MAX_CONNS`.

## R4 — Backup e arquivamento do WAL

**Sinal.** `pg_stat_archiver`: `archived_count`, `failed_count`,
`last_archived_wal`, `last_failed_wal`, `last_failed_time`. O arquivamento é a
base do PITR; uma falha silenciosa significa que a recuperação para um instante
começou a não existir.

**Threshold inicial.** **P1** quando `failed_count` aumenta, ou quando o último
WAL arquivado tem mais de **10 min** (o `archive_timeout` é 300 s; a meta de RPO
de 15 min só se sustenta com arquivamento vivo), ou quando a retenção removeu
abaixo do piso (`BACKUP_KEEP_MIN`).

**Primeiros 5 minutos.**

```bash
docker compose -f compose.production.yaml exec db \
  psql -U arena -d arena --tuples-only --command \
  "SELECT archived_count, failed_count, last_archived_wal, last_failed_wal, last_failed_time
     FROM pg_stat_archiver;"
```

O motivo da falha está no log do próprio arquivador (que carrega
`BACKUP_S3_*` e a chave): credencial expirada, endpoint inacessível, bucket
cheio. Ver [DEPLOYMENT.md](DEPLOYMENT.md) §7 para o desenho do pipeline.

**Retenção — atenção ao comando destrutivo.**

```bash
# dry-run (padrão; não apaga nada)
BACKUP_S3_ENDPOINT=… BACKUP_S3_BUCKET=… BACKUP_S3_ACCESS_KEY=… BACKUP_S3_SECRET_KEY=… \
BACKUP_ENCRYPTION_KEY_FILE=/run/secrets/backup_key \
  deploy/backup/retention.sh

# ⚠ destrutivo: só depois do dry-run revisado
# … retention.sh --apply
```

**Mitigação.** Restaurar o arquivamento primeiro (a credencial/endpoint é o mais
comum). Só depois discutir retenção — **nunca** apagar o que sobrou para
"liberar espaço" (é o caminho oposto: R2 aponta para cá).

**Prova periódica.** O exercício de restauração é manual e sob demanda:
`make backup-verify` (requer daemon Docker). Ele restaura num cluster vazio e
compara checksums. **Backup que nunca foi restaurado não é backup** — a
recuperação por linha do tempo (troca de timeline) e a leitura a partir de uma
segunda cópia fora do host continuam pendências registradas.

## R5 — Webhook de pagamento

**Sinal.** `app.stripe_events`, ancorado no `stripe_event_id` do provedor
(idempotência) e com ciclo de vida por CHECK + trigger
(`received` → `processing` → `processed`/`failed`/`ignored`), com `attempts` e
`last_error`. Benefício só é concedido por webhook autenticado — um evento preso
é um pagamento que não virou direito.

**Threshold inicial.** **P2** com qualquer evento não-processado
(`received`/`processing`/`failed`) há mais de **15 min**; **P1** se o evento
preso é de dinheiro (checkout concluído, renovação, reembolso) e há usuário
afetado. Um `failed` novo é sempre alerta.

**Primeiros 5 minutos.**

```bash
docker compose -f compose.production.yaml exec db \
  psql -U arena -d arena --tuples-only --command \
  "SELECT status, count(*), max(attempts) AS max_attempts
     FROM app.stripe_events GROUP BY status ORDER BY status;"

docker compose -f compose.production.yaml exec db \
  psql -U arena -d arena --tuples-only --command \
  "SELECT stripe_event_id, event_type, status, attempts, last_error, received_at
     FROM app.stripe_events
    WHERE status IN ('received','processing','failed')
    ORDER BY received_at ASC LIMIT 20;"
```

**Mitigação.** Corrigir a causa (assinatura/segredo, rota, tempo) e **reenviar
o evento pelo provedor** (Stripe: Workbench → Webhooks → *Resend*), o que
exercita o caminho real e é idempotente pelo `stripe_event_id`. A reconciliação
de billing (`app.billing_reconciliation_runs` e as consultas
`ListUnprocessedStripeEvents`/`ListSubscriptionsForReconciliation`) é como se
encontra o que a janela perdeu.

**Correção.** Um `last_error` recorrente é defeito de parsing/assinatura ou de
handlers: cada formato de evento precisa de teste próprio antes do incidente
ser considerado fechado.

## R6 — Atraso da fila (job lag)

**Sinal.** USE da fila durável: `jobs_queue{state="queued|leased|due_now|dead"}`,
`jobs_lag_seconds` (espera do job *due* mais antigo — o número que um alerta
observa), `jobs_oldest_dead_seconds` (há quanto tempo um operador pode agir e
não agiu) e `jobs_health_scrape_errors_total`. O mesmo dado está na superfície
operacional autenticada `GET /api/v1/admin/jobs/health`.

**Threshold inicial.** **P2** quando `jobs_lag_seconds` passa de **300 s**;
**P1** acima de **900 s**, ou quando `jobs_queue{state="due_now"}` fica acima de
zero **sem** consumo (nenhum `jobs_processed_total` avançando) por 5 min — a
fila está parada.

**Primeiros 5 minutos.** A leitura direto do banco (somente leitura; a
consulta não seleciona coluna de payload):

```bash
docker compose -f compose.production.yaml exec db \
  psql -U arena -d arena --tuples-only --command \
  "SELECT
     count(*) FILTER (WHERE state = 'queued') AS queued,
     count(*) FILTER (WHERE state = 'leased') AS leased,
     count(*) FILTER (WHERE state = 'dead')   AS dead,
     count(*) FILTER (WHERE state = 'queued' AND available_at <= now()) AS due_now,
     now() - min(available_at) FILTER (WHERE state = 'queued' AND available_at <= now()) AS lag
   FROM app.jobs;"
```

> **Pendência (achada no tabletop de 2026-09-22):** a superfície HTTP
> operacional (`internal/jobs/adapters/http`: `GET /api/v1/admin/jobs/health`,
> `GET /api/v1/admin/jobs/dead`, `POST /api/v1/admin/jobs/{id}/retry`) existe e
> tem testes próprios (P15-T06), mas **não está composta** em `arena server` nem
> declarada no contrato/registry — hoje ela responde **404**. Enquanto isso, o
> banco é o caminho de leitura, e o *retry* de um job morto é uma operação
> pendente de *wiring*. Fiar essa superfície (e pôr o alerta de fila no
> coletor) é trabalho de implantação/observabilidade, não deste documento.

```bash
docker compose -f compose.production.yaml ps worker
docker compose -f compose.production.yaml logs --since 15m worker | tail -50
```

**Diagnóstico de carga presa.** `GET /api/v1/admin/jobs/dead` lista os mortos
(sem o payload, por construção) com `last_error_code`. Um tipo desconhecido
aparece como `JOB_UNKNOWN_TYPE`: é um workload **sem handler registrado**, não
uma fila ociosa. Um job preso em `leased` além do lease é recuperado pelo
próprio worker; `reclaimed` no log de parada conta isso.

**Mitigação.** Garantir o `worker` vivo e com os handlers registrados; um job
morto é devolvido à fila por `POST /api/v1/admin/jobs/{id}/retry` **com motivo
declarado** — e só depois de a causa raiz ser corrigida, porque o orçamento de
tentativas é renovado.

**Correção.** Lag crescente com `due_now` baixo é throughput insuficiente
(concorrência/limite); lag com `due_now` alto é falta de worker. São correções
diferentes — meça antes de escolher.

## R7 — Email transacional

**Sinal.** `jobs_processed_total{type="email_delivery",outcome="failed"}` e
`jobs_processing_seconds{type="email_delivery"}`, mais os jobs mortos do tipo
`email_delivery`. Em produção, o fluxo **enfileira** a mensagem e o worker a
entrega; um cadastro cujo link não sai não é uma jornada.

**Threshold inicial.** **P2** com mais de **3** falhas de entrega em 15 min, ou
com **qualquer** job `email_delivery` morto — um morto é uma mensagem que nunca
chegou.

**Primeiros 5 minutos.**

```bash
# falhas e mortos do workload de email (sem payload)
docker compose -f compose.production.yaml exec db \
  psql -U arena -d arena --tuples-only --command \
  "SELECT state, count(*) FROM app.jobs WHERE type = 'email_delivery' GROUP BY state ORDER BY state;"

docker compose -f compose.production.yaml exec db \
  psql -U arena -d arena --tuples-only --command \
  "SELECT id, attempts, max_attempts, last_error_code, updated_at
     FROM app.jobs
    WHERE type = 'email_delivery' AND state = 'dead'
    ORDER BY updated_at ASC LIMIT 20;"
```

O `last_error_code` diz se é credencial/ remetente (provedor recusou), cota
(*rate limit*) ou endereço inválido. Verificar também a caixa do provedor
(Resend → Emails) e se `ARENA_EMAIL_FROM` continua sendo um remetente
autorizado.

**Mitigação.** Corrigir a causa (chave, remetente, domínio verificado) e
reenviar o job morto pela superfície operacional. **Nunca** entregar para um
endereço que o provedor marcou como *bounce*/*complaint*: reputação de domínio
é o ativo.

**Correção.** Falhas por cota pedem *batching* ou ritmo; falhas por conteúdo
pedem validação do template. Registre qual foi: são correções em lugares
diferentes.

## R8 — Certificado TLS

**Sinal.** Validade do certificado que o Caddy **serve** de fato. O certificado
de origem chega como *secret* montado (`origin_certificate`/`origin_key`) e o
Caddyfile fixa piso de TLS 1.2/1.3.

**Threshold inicial.** **P3** abaixo de **21 dias**; **P2** abaixo de **7 dias**.
O alerta olha o certificado servido, não o arquivo guardado: um par trocado mas
não recarregado é indistinguível de um par vencido.

**Primeiros 5 minutos.**

```bash
# o que está sendo servido agora
SITE="${COMPOSE_SITE_ADDRESS:-arena.example}"
openssl s_client -connect "$SITE:443" -servername "$SITE" </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer -dates

# o que o Caddy logou sobre o certificado
docker compose -f compose.production.yaml logs --since 24h caddy | grep -i -E 'certificate|tls'
```

**Mitigação.** Rotacionar o secret e recarregar o Caddy
(`docker compose -f compose.production.yaml exec caddy caddy reload --config /etc/caddy/Caddyfile`).
**Nunca** substitua a validação do certificado por "confiar em tudo": um
`insecure` ligado para "resolver rápido" é a porta de entrada de um
interceptador (ver [THREAT_MODEL.md](THREAT_MODEL.md)).

**Correção.** A renovação precisa de agenda (Origin CA tem validade longa, mas
finita) e de um alerta que sobreviva a quem renovou na última vez — é o alerta
que não pode depender de memória.

## R9 — Bypass de origem

**Sinal.** Requisição chegando à origem **fora da borda**. O desenho é: a borda
(Cloudflare) aceita o público; a origem só é alcançável pela borda
([DEPLOYMENT.md](DEPLOYMENT.md) §3). O Caddy confia apenas em *proxies*
declarados e deriva o IP do cliente de um **único** header de valor
(`CF-Connecting-IP`), remontando `X-Forwarded-For` para o processo — e a
aplicação ignora `CF-Connecting-IP`/`X-Real-IP` do cliente, porque um valor sem
cadeia não distingue quem escreveu (ver `docs/SECURITY.md` e o pacote
`internal/platform/clientip`).

**Threshold inicial.** **P1** com **qualquer** requisição observada na origem
cujo *peer* não seja da faixa da borda — "um pouco de bypass" é um caminho
aberto, e ele contorna WAF, rate limit e Turnstile de uma vez.

**Primeiros 5 minutos.**

```bash
# a prova de que o header forjado não sobrevive ao edge
make caddy-verify

# as regras do firewall do host (somente leitura)
sudo nft list ruleset 2>/dev/null || sudo iptables -S 2>/dev/null

# o que chegou à origem
docker compose -f compose.production.yaml logs --since 1h caddy | tail -100
```

Na borda, conferir a lista de IPs de origem autorizados e o *rate* de requisições
diretas ao IP do host (Cloudflare → Analytics → Origin). A allowlist de
*peers* é decisão de **firewall**, não do servidor web — o Caddy não pode
impedir o que já chegou.

**Mitigação.** Restringir o firewall do host às faixas publicadas pela borda,
mantendo 80/443 abertos **somente** para elas; se a origem responde no IP nu,
isso é a confirmação do bypass.

**Correção.** Registrar os endereços observados e a hora; revisar como a faixa
foi aplicada (uma regra por host é uma regra que pode faltar num host novo).
`make caddy-verify` é a re-prova de que o spoof de header continua rejeitado.

## R10 — Smoke do frontend com e sem JavaScript

**Sinal.** Release tocando `web/` ou incidente de renderização. O desenho é:
o backend serve HTML semântico íntegro sem JS (progressive enhancement), e o
harness do plano de fechamento prova a jornada com JS — as duas metades têm
prova própria, e nenhuma é inferida da outra.

**Com JavaScript (harness).** Executar `make frontend-coverage` com
`FRONTEND_BASE_URL` apontando ao ambiente. Verde exige: rotas/áreas cobertas
sem queda, jornada real (navegação + formulário válido com sucesso visível),
jornada negativa (submissão inválida com erro anunciado) e os 15 staged
recusados nominalmente.

**Sem JavaScript (degradação).** Com JS desabilitado, cada rota principal
deve entregar HTML semântico legível: conteúdo principal presente, navegação
por links funcional, formulários submetem via POST nativo e erros do servidor
voltam como HTML com o resumo de erros. `ga-toast` ausente é esperado (é
transiente); `ga-error-summary` e `ga-busy` têm equivalente SSR e devem
aparecer.

**Threshold inicial.** **P1** com qualquer rota em branco sem JS, formulário
que só funciona com JS, ou erro sem anúncio — release bloqueado.

**Correção.** Registrar em `docs/HISTORY.md`; re-prova é o smoke R10 verde
nas duas metades.

## 4. Tabletop

Exercício de mesa executado em 2026-09-22, com os comandos **não destrutivos**
dos runbooks rodados de verdade (não só lidos): um PostgreSQL 18.4 descartável
(a mesma imagem *por digest* do `compose.production.yaml`) recebeu as 32
migrations pelo runner da aplicação, o processo `arena server` subiu contra ele
com o listener administrativo em loopback, e os comandos foram executados e
observados. Nenhuma operação destrutiva foi usada; o cluster e o contêiner foram
removidos ao final.

### A. R1 — Erros 5xx (prova do sinal)

```bash
curl --silent http://127.0.0.1:59090/metrics | grep '^http_requests_total'
```

Saída observada:

```text
http_requests_total{method="GET",route="unmatched",status="200"} 2
http_requests_total{method="GET",route="unmatched",status="404"} 1
```

`/health/live` e `/health/ready` responderam 200 e um caminho inexistente
respondeu 404, e as três requisições apareceram **na mesma janela**, separadas
por status — a razão de 5xx da expressão é calculável a partir daqui.

**Achados:** (1) as rotas de saúde são contadas como `route="unmatched"` porque
o handler de plataforma não registra o padrão no `Request.Pattern`; um operador
que filtra por rota precisa saber disso antes de estranhar o rótulo; (2) sem log
de acesso, o 5xx depende das métricas e do Sentry, e **se o coletor do listener
administrativo não estiver fiado, não há alerta de 5xx** — a pendência mais
importante desta tarefa.

### B. R3 — Conexões de DB (prova do sinal)

```bash
curl --silent http://127.0.0.1:59090/metrics | grep -E '^(db_pool|jobs_)'
```

```text
db_pool_connections{state="acquired"} 0
db_pool_connections{state="idle"} 2
db_pool_connections{state="max"} 10
db_pool_connections{state="total"} 2
db_pool_acquires_total 4
db_pool_empty_acquires_total 0
db_pool_canceled_acquires_total 0
db_pool_acquire_seconds_total 2.386e-06
jobs_health_scrape_errors_total 0
jobs_queue{state="dead"} 0
jobs_queue{state="due_now"} 0
jobs_queue{state="leased"} 0
jobs_queue{state="queued"} 0
jobs_lag_seconds 0
jobs_oldest_dead_seconds 0
```

```bash
psql ... -c "SELECT state, count(*) FROM pg_stat_activity WHERE datname = 'arena' GROUP BY state ORDER BY state;"
psql ... -c "SHOW max_connections;"
```

```text
 active |     1
 idle   |     2

 100
```

**Achado:** numa leitura ociosa o pool está em `2/10` e os contadores de
saturação/erro são zero — o alerta de R3 é "quando muda", não "quando é
diferente de zero".

### C. R6 — Atraso da fila (prova do sinal, e um defeito de expectativa)

A consulta de saúde da fila (a mesma do `GetQueueHealth`) rodou contra o cluster
de exercício e devolveu `0 | 0 | 0 | 0 | (lag vazio)` com `app.jobs` vazia; as
métricas `jobs_queue`, `jobs_lag_seconds` e `jobs_oldest_dead_seconds` foram
publicadas com zero. Um cluster ocioso reporta 0 — o alerta só tem valor com
carga real, e a leitura é cacheada por scrape (uma query por scrape).

**O achado que mais mudou o documento:** o runbook partia do pressuposto de que
`GET /api/v1/admin/jobs/health` estaria disponível. Executado contra o processo
real, o caminho respondeu:

```text
admin jobs health (sem sessão): status 404
type: text/plain; charset=utf-8
```

A superfície operacional de jobs **existe e tem teste próprio** (P15-T06), mas
**não está composta** em `arena server` nem declarada no contrato/registry: hoje
responde 404. O runbook foi corrigido para ler do banco e a pendência ficou
registrada em R6.

### D. R2 — Disco (prova do sinal)

```bash
df -h / ; df -i / ; docker system df
```

```text
/dev/mapper/data-root  460G  379G   58G  87% /
/dev/mapper/data-root 30670848 4168144 26502704   14% /

TYPE            TOTAL     ACTIVE    SIZE      RECLAIMABLE
Images          55        18        14.42GB   7.79GB (54%)
Containers      27        24        411.9MB   320.1MB (77%)
Local Volumes   150       14        7.89GB    6.505GB (82%)
Build Cache     23        0         710.2MB    502.5kB
```

**Achado:** o host de exercício já lê **87%** — acima do P2 (80%) do runbook e
abaixo do P1 (90%): o threshold é alcançável na prática, não hipotético. E o
`df` do host não mostra o que os volumes/imagens do Docker consomem;
`docker system df` é o segundo comando obrigatório, não opcional.

### E. R4, R5 e R7 — Backup, webhook e email (prova do sinal)

```bash
psql ... -c "SELECT archived_count, failed_count, last_archived_wal, last_failed_wal, last_failed_time FROM pg_stat_archiver;"
psql ... -c "SHOW archive_mode;"
psql ... -c "SELECT status, count(*), max(attempts) FROM app.stripe_events GROUP BY status;"
psql ... -c "SELECT state, count(*) FROM app.jobs WHERE type = 'email_delivery' GROUP BY state;"
```

Saída observada, respectivamente: `0 | 0 | | | ` com `archive_mode = off`;
sem linhas em `app.stripe_events`; sem linhas para `email_delivery`. Todas as
consultas rodaram sem erro em um cluster com as migrations aplicadas.

**Achado de R4 (o mais importante):** o servidor de exercício **não arquiva**
(`archive_mode = off`, porque o arquivamento é declarado no serviço `db` do
compose) e ainda assim `pg_stat_archiver` devolve `failed_count = 0`. O alerta
não pode ser "`failed_count` é zero": precisa observar **"arquivou nos últimos N
minutos"**, senão um servidor que nunca arquivou se apresenta como verde.

### F. Superfície administrativa (prova de que não é pública)

```bash
curl --silent -o /dev/null -w '%{http_code}\n' http://127.0.0.1:58080/metrics
curl --silent -o /dev/null -w '%{http_code}\n' http://127.0.0.1:58080/debug/pprof/
```

Ambos responderam **404** no endereço público, enquanto o mesmo `/metrics`
respondeu 200 no listener loopback (`127.0.0.1:59090`). As métricas e os
perfis não estão expostos ao público.

### Pendências que o exercício registrou

Pertencem a tarefas seguintes, não a este documento: **fiar o coletor** do
listener administrativo (loopback num contêiner *distroless*, P19-T07); **fiar a
superfície HTTP operacional de jobs** (`internal/jobs/adapters/http`) e o alerta
de fila; e **fiar o alerta de origem** no firewall.

## 5. Regra dos thresholds

Um *threshold* inicial é uma hipótese com data. Ele muda quando:

- o tráfego real mostra que a janela é ruidosa (alarga) ou lenta (encurta);
- um incidente **passou** sem disparar (threshold estava alto) ou disparou
  **sem** incidente (threshold estava baixo);
- a topologia mudou (novo serviço, novo volume, novo provedor).

Mudar um threshold **não** é sucesso de gate: é decisão operacional e fica
registrada com a medição que a motivou. Nunca reduza um *threshold* para "ficar
verde".

## 6. Gates de ativação futura (staged e economia)

Dois portões separados, ambos bloqueados até as aprovações humanas e
jurídicas de [EXTERNAL_APPROVALS.md](quality/EXTERNAL_APPROVALS.md). Nenhum
passo aqui é executado por inferência do agente.

**Ativação de rotas staged (15 rotas).** Pré-condições: Q/TEMP ratificadas,
jurídico por mercado, IREV sem achados bloqueadores, smoke R10 verde no
ambiente-alvo. Passos: ativar uma rota por vez no harness sintético separado
(nunca pelo tráfego real); conferir jornada real e negativa da rota;
registrar a ativação em `docs/HISTORY.md`. Reversão: voltar a rota a staged
e repetir o smoke.

**Ativação de economia real (INK/moeda).** Pré-condições: as mesmas acima,
mais credenciais de provider via Secrets (item 6 de EXTERNAL_APPROVALS) e
`economycertify` verde no ambiente-alvo. Passos: liberar em janela de
observação; conciliar ledger append-only contra extratos; qualquer
divergência = reversão imediata e incidente P0. Valores sempre em minor units
com ISO de moeda; nenhuma operação financeira sem transação explícita.
