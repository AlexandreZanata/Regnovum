# Auditoria de segurança (P20-T04)

**Escopo da fase:** executar o modelo de ameaças, autorização negativa, cache, CSRF, sessões, MFA, Stripe, double spend, IDOR, segredos, dependências e container; revisão manual das fronteiras de confiança.

**Método:** a fase não pede um segundo modelo de ameaças — ela pede que o modelo existente ([THREAT_MODEL.md](THREAT_MODEL.md), metodologia STRIDE) seja **executado** e que cada afirmação de mitigação seja amarrada a um caminho do repositório ou a um comando que roda. Este documento é o registro dessa execução. O portão é `make security-audit` (`tools/secaudit`), que:

- resolve cada caminho de evidência citado aqui e recusa o que não existe;
- roda o comando de cada uma das doze áreas e recusa se qualquer um falhar;
- confere que o conjunto de ameaças do registro é exatamente o conjunto do modelo, que nenhuma severidade foi rebaixada, e que nenhuma ameaça crítica aparece como `monitored` (regra do próprio modelo, §1);
- aplica o piso da fase: **nenhum finding crítico ou alto aberto**, e todo finding médio ou acima com **owner e aceite explícito**;
- varre a árvore em busca de segredo estrutural (arquivo `.env` rastreado, bloco de chave privada, `.env` fora do `.gitignore`).

**Data da execução:** 2026-09-22 · **Commit auditado:** o head da branch da fase no momento do gate (impresso pelo run).

---

## 1. Fronteiras de confiança — revisão manual (TB-01 a TB-06)

O que segue é a leitura humana exigida pela fase: qual é a fronteira, o que o código e a configuração realmente fazem nela, o que provou que fazem, e qual é a nota de residual.

**TB-01 — Cliente ↔ Borda (`[Browser] → Cloudflare`).** Todo payload, header, cookie e endereço de origem é tratado como não confiável: a borda autentica o visitante (Turnstile), e o que a aplicação vê do cliente é o que o Caddy **observou**, não o que o cliente afirmou. Provado por `deploy/caddy/Caddyfile` + `tools/caddyaudit/verify.sh` (gate `caddy-verify`): um peer que forja `CF-Connecting-IP`, `X-Forwarded-For` e `X-Real-IP` não sobrevive — o upstream recebe o endereço do próprio edge e os dois headers de valor único ausentes —, e o mesmo gate prova o **controle positivo** (com todas as faixas confiadas o header é acreditado), o que separa "a recusa é a decisão de confiança" de "o mecanismo nunca foi fiado". Residual: a lista de faixas do Cloudflare é uma decisão datada (o gate a lê do arquivo e o teste estrutural a mantém sob revisão).

**TB-02 — Borda ↔ Caddy.** `strict_sni_host on`, piso de TLS 1.2, listener sem QUIC, tempo de `read_header` e de `idle` orçados, e o caminho de admin do Caddy **não publicado** (o gate mostra que `/config/*` responde 404 no edge e que `docker port` publica 80/443 e nada mais). Residual: a recusa do tráfego direto ao host pertence ao firewall em frente ao contêiner, que não vive neste repositório — registrado em [DEPLOYMENT.md](DEPLOYMENT.md) §3.1.

**TB-03 — Caddy ↔ Backend Go.** A aplicação não confia no proxy para nada que decida segurança: CSRF por duplo envio assinado (HMAC-SHA256 sobre o token, comparação em tempo constante) **mais** comparação de `Origin`/`Referer`; `httpcache.Private` em toda resposta privada; limites de corpo nos adapters; e a validação de sessão consulta o banco em cada requisição. **Nota de residual registrada, não escondida:** `security.New` é composto com `AllowedOrigins` vazio e `RequireOrigin` falso (`internal/bootstrap/participation.go`), então a comparação de origem aceita `r.Host` como mesma origem e tolera a ausência de `Origin`/`Referer` quando o token do duplo envio confere. Quem decide é o **duplo envio** — o token é assinado, mora num cookie `SameSite=Lax` que só o próprio host escreve e é comparado em tempo constante com o header —, e a comparação de origem é defesa em profundidade. A confiança em `r.Host` só é segura porque **a porta da aplicação nunca é publicada** (provado por `tools/composeaudit/verify.sh`) e o edge recusa SNI desconhecido (TB-02); é por isso que ela é aceitável aqui e não seria num host alcançável direto. Residual: baixo, com a dependência declarada.

**TB-04 — Adapters ↔ Domínio.** `internal/architecture_test.go` recusa a direção das dependências e o relógio/aleatoriedade fora de `platform` (`TestNoDirectClockOrRandomOutsidePlatform`), e `internal/architecture_boundaries_test.go` (P23-T01) recusa o tipo de fornecedor atravessando a fronteira, com uma linha por dependência homologada (`TestOwnedDependenciesStayWithTheirOwners`: o SDK de pagamento, o driver PostgreSQL, a ferramenta de migration, `golang.org/x/crypto` e a biblioteca Unicode), a direção dentro do módulo (`TestLayerDependenciesPointInward`) e o adapter instanciado fora do composition root (`TestOnlyTheCompositionRootWiresAdapters`). É uma asserção de compilação e de grafo de imports, não de prosa.

**TB-05 — Backend ↔ PostgreSQL.** Duas provas independentes: as queries tipadas do `sqlc` (aplicação nunca monta SQL por concatenação) e o **ledger de privilégios medido** pela auditoria de migrations ([MIGRATION_AUDIT.md](MIGRATION_AUDIT.md)): a role de runtime `arena_app` lê todas as tabelas, não tem `TRUNCATE`, `REFERENCES` nem `TRIGGER` em nenhuma, não pode apagar em nome próprio nas tabelas de histórico, e o ledger financeiro (`wallet_transactions`, `wallet_operations`) é append-only para ela. Residual: baixo.

**TB-06 — Gateways ↔ Backend.** O webhook da Stripe é verificado por HMAC-SHA256 sobre o **corpo bruto**, com janela de tolerância de 5 minutos, recusa de carimbo no futuro e comparação em tempo constante (`hmac.Equal`); a concessão de benefício acontece no processamento do evento autenticado e nunca na navegação para a página de sucesso; o `event_id` é registrado em transação com restrição de unicidade. Residual: baixo.

---

## 2. As doze áreas, uma por uma

Cada área tem um comando que o portão **roda** (não uma promessa) e, quando o que interessa só existe na esteira completa, um job do CI nomeado — e o portão confere que esse job existe no workflow.

| Área | O que foi medido | Execução | Também na esteira |
| --- | --- | --- | --- |
| `threat-model` | O modelo e a matriz andam juntos: mesmo conjunto de IDs, sem duplicata, nenhuma ameaça crítica com evidência apenas manual. | `go test -count=1 ./internal/security/...` | — |
| `negative-authorization` | A superfície administrativa recusa anônimo, recusa quem não tem a capacidade, exige segundo fator recente e mantém o gate imediato à mudança de papel. | `go test -count=1 ./internal/moderation/adapters/http/... ./internal/jobs/adapters/http/...` | — |
| `cache` | Política por superfície: privado é `private, no-store, no-cache, must-revalidate` + `Pragma: no-cache`; público é limitado e nunca quando há `Set-Cookie`; as suítes das superfícies afirmam o header. | `go test -count=1 ./internal/platform/httpcache/... ./internal/arguments/adapters/http/... ./internal/wallet/adapters/http/... ./internal/positions/adapters/http/... ./internal/transparency/adapters/http/...` | — |
| `csrf` | Duplo envio com token assinado, cookie `SameSite=Lax`, comparação em tempo constante, métodos seguros isentos e mutantes exigidos; origem `null` recusada. | `go test -count=1 ./internal/platform/security/...` | — |
| `sessions` | Token opaco com hash SHA-256 no banco, política de inatividade de 24 h e vida absoluta de 14 dias, rotação no login e na troca, revogação de **todas** as sessões no logout e na redefinição de senha. | `go test -count=1 ./internal/identity/application/... ./internal/identity/adapters/http/...` | — |
| `mfa` | Segundo fator no enrolamento, confirmação, códigos de recuperação e *step-up*; janela de 15 minutos para as ações sensíveis; a superfície administrativa exige o fator recente. | `go test -count=1 ./internal/identity/... ./internal/moderation/...` | — |
| `stripe` | Assinatura sobre o corpo bruto, tolerância de 5 minutos, carimbo no futuro recusado, idempotência por `event_id` e concessão apenas no webhook. | `go test -count=1 ./internal/billing/...` | — |
| `double-spend` | Débito e publicação na mesma transação, `SELECT … FOR UPDATE` no saldo, `CHECK (balance >= 0)` no schema e chave de idempotência com `ON CONFLICT … DO NOTHING` — medido com o detector de corrida. | `go test -count=1 -race ./internal/arguments/adapters/postgres/... ./internal/wallet/adapters/postgres/...` | — |
| `idor` | Leitura e escrita de argumento, arena e faturamento conferem a titularidade no servidor; recurso alheio não vira 200. | `go test -count=1 ./internal/arguments/adapters/http/... ./internal/arenas/adapters/http/... ./internal/billing/adapters/http/...` | — |
| `secrets` | Redação de valores e de endereços no log; varredura estrutural da árvore (`.env` rastreado, bloco de chave privada, `.env` fora do `.gitignore`) feita pelo próprio portão. | `go test -count=1 ./internal/platform/logging/...` | `source-scans` (gitleaks) |
| `dependencies` | Vulnerabilidades conhecidas nas dependências Go pelo `govulncheck`, e a procedência de cada dependência, action e imagem: o componente sem aprovação, a versão que se moveu sem o registro, a licença fora do que a classe homologa e a lista de materiais que discorda da árvore. | `make vuln`, `make audit-deps` | `source-scans` (`govulncheck`, `npm audit`), `quick` (`make audit-deps` em cada pull request) |
| `container` | As regras da receita e do artefato (base por digest, sem `ADD`/`COPY . .`, `USER` não-root, sem compilador, cache, fonte ou credencial na imagem). | `go test -count=1 ./tools/imageaudit/...` | `image` (`make image-verify`), `image-scan` (trivy) |

---

## 3. Execução do modelo de ameaças

O bloco executável ao fim deste documento carrega, por ameaça, o veredito e a evidência. Os vereditos, com o que cada um significa:

- **`mitigated`** — todas as mitigações declaradas no modelo existem no código e estão cobertas pela execução citada;
- **`accepted`** — o controle existe, mas uma mitigação declarada está ausente ou mais fraca, e o residual está aceito num finding com owner, data de aceite e trabalho seguinte nomeado;
- **`monitored`** — o controle é procedimento ou observação, não mecanismo (proibido para ameaça crítica pela §1 do modelo);
- **`open`** — há finding aberto no nível da ameaça. Nenhum.

O que a execução confirmou, além do que o modelo já declarava:

- **THR-WAL-02** deixou de ser uma promessa sobre privilégios: a auditoria de migrations mediu o catálogo e provou que o runtime não tem `UPDATE` nem `DELETE` no ledger financeiro, que é exatamente o que a ameaça declara. A evidência da ameaça hoje aponta também para [MIGRATION_AUDIT.md](MIGRATION_AUDIT.md).
- **THR-ADM-01/THR-ADM-02** são medidos na superfície administrativa de moderação e na superfície operacional de jobs (`RequireRole` + *step-up* + auditoria na mesma transação), e as duas suítes incluem o caso negativo (anônimo e papel insuficiente). A pendência de composição da superfície de jobs está nos limites (§5) e não esconde nenhuma rota exposta: hoje ela **não** está montada em `arena server`.
- **THR-CACHE-01** é medido por `internal/platform/httpcache` mais as suítes de cada superfície, e a política tem a decisão codificada que mais importa: uma resposta pública que ganhe `Set-Cookie` cai para `Private`.

---

## 4. Achados

O registro executável traz o mesmo conteúdo em forma de bloco; aqui está a leitura.

**SEC-01 · `__Host-` ausente no cookie de sessão · severidade Média · status `accepted`.**
O modelo declara, em THR-AUTH-01, "cookies com atributos `HttpOnly`, `Secure`, `SameSite=Lax` e prefixo `__Host-` em produção". Os três atributos existem e são testados; o **prefixo não existe em lugar nenhum** — o cookie é `arena_session` (`internal/platform/security/cookies.go`), host-only (a composição não define `Domain`) e sem prefixo. O prefixo `__Host-` é o que impede um cookie escrito por um subdomínio irmão de ser aceito pelo host, e é justamente contra *cookie tossing* / fixação de sessão que ele é declarado. Owner: titular do repositório. Aceite explícito para o beta (2026-09-22): a sessão continua sendo validada no banco em cada requisição, o token não é transferível e a rotação no login já derrota a fixação — o residual é de profundidade, não de portão. Trabalho seguinte nomeado: **P20-T04A** renomeia o cookie para `__Host-arena_session` (prefixo só em produção, com o contrato e as suítes acompanhando) e atualiza a prosa do modelo no mesmo commit.

**SEC-02 · O modelo nomeia uma superfície administrativa que não existe · severidade Baixa · status `accepted`.**
THR-ADM-01 descreve o isolamento das rotas administrativas "sob namespace `/api/v1/admin/*` com middleware dedicado (`RequireRole(RoleAdmin)`)". O contrato servido (`api/openapi.json`) não tem nenhuma rota sob `/api/v1/admin/*`: a superfície administrativa de HTTP é `/api/v1/moderation/*`, com autorização por capacidade (`internal/moderation/domain/policy.go`), e a administração de papéis é comando (`arena admin bootstrap|revoke`, P19-T09). O controle existe e é mais estreito do que a prosa (capacidades em vez de um papel só); o que está errado é a **descrição**. Owner: titular. Aceite (2026-09-22): nenhum controle deixa de existir por causa disso, e a correção é de documentação. Trabalho seguinte: a prosa do modelo é corrigida na revisão de privacidade e moderação (**P20-T06**), junto do que mais o documento declarar de forma mais ampla do que o servido.

---

## 5. Limites desta auditoria (registrados, não escondidos)

1. **A auditoria executa e confere; ela não substitui a esteira.** Container e dependências têm na esteira o que não roda aqui: `make image-verify` (imagem de verdade, filesystem somente leitura, migrations de dentro, smoke) e o `trivy` do job `image-scan`; o `gitleaks` do job `source-scans` cobre o que uma varredura de árvore estrutural não vê (valor de segredo dentro de um arquivo legítimo). O portão **confere que esses jobs existem pelo nome** e recusa se um deles desaparecer do workflow; ele não os roda.
2. **A superfície operacional de jobs é auditada no adapter, não na composição.** `internal/jobs/adapters/http` tem a matriz negativa, o `RequireRole` e o *step-up* testados (P15-T06), mas **não** está montada em `arena server` nem no contrato — a pendência está em [DEPLOYMENT.md](DEPLOYMENT.md) §5. Consequência para esta auditoria: quando a superfície for fiada, o que já está provado passa a valer para uma rota alcançável; nada aqui afirma que ela esteja exposta hoje.
3. **THR-CACHE-02 continua `procedure:` na matriz.** O que existe automatizado é a metade do edge (`caddy-verify`: headers de cliente removidos, o edge define `X-Forwarded-Host`/`X-Forwarded-Proto` a partir do que observou, link e resposta não carregam host injetado). O procedimento completo — inclusive um `X-Forwarded-Host` forjado explicitamente — segue manual, e esta auditoria registra o que o gate mede em vez de promovê-lo a `test:`.
4. **A varredura de segredos do portão é estrutural e limitada por tipo de arquivo.** Ela recusa `.env` rastreado (exceto `.env.example` e equivalentes), `.env` fora do `.gitignore`, e cabeçalho de chave privada em arquivo cujo tipo nunca guarda uma como texto — `.pem`, `.key`, `.crt`, `.p12`, `.json`, `.yaml`, `.toml`, `.ini`, `.txt` e afins. Fonte Go, Markdown e shell ficam de fora por construção, porque **nomeiam** o cabeçalho legitimamente: os scanners deste próprio repositório o fazem, e uma regra que os recusasse tornaria os scanners incomitáveis. A varredura de **valor**, em qualquer arquivo, é do `gitleaks` (limite 1).
5. **`AllowedOrigins` vazio e `RequireOrigin` falso são decisão, não esquecimento** — está na revisão de TB-03 com o motivo e o residual. Se a intenção for exigir origem em produção, é mudança de comportamento e de composição, não desta auditoria.
6. **`make vuln` exige um `govulncheck` construído com a toolchain do projeto.** O binário tem de ser instalado com o Go que o `go.mod` declara (`GOTOOLCHAIN=go1.27.1 go install golang.org/x/vuln/cmd/govulncheck@v1.8.0`); um binário construído com uma versão anterior não processa os pacotes e o gate fica vermelho **por ambiente**, não por vulnerabilidade. Foi o que aconteceu nesta execução — e é o tipo de vermelho que se resolve reinstalando a ferramenta, nunca reduzindo limiar.
7. **Nenhum limiar foi reduzido, nenhum teste pulado, nenhum snapshot aceito, nenhum retry, nenhum waiver.** As duas severidades que o piso da fase manda não deixar em aberto (Crítica e Alta) têm **zero** findings abertos; os dois findings são Média e Baixa, os dois `accepted`, com owner, data e trabalho seguinte.

---

## 6. Registro executável

O bloco abaixo é lido por `tools/secaudit`. Ele é a forma mecânica do que este documento diz, e o portão recusa quando os dois discordam (uma ameaça fora do registro, uma severidade rebaixada, um caminho de evidência que não existe, uma área sem execução nem job nomeado, um finding aberto em Crítica ou Alta, um aceite sem data ou sem owner).

```json
{
  "version": 1,
  "audited_on": "2026-09-22",
  "threats": [
    {
      "id": "THR-AUTH-01",
      "severity": "Crítica",
      "verdict": "accepted",
      "finding": "SEC-01",
      "evidence": [
        "internal/platform/security/cookies.go",
        "internal/platform/security/cookies_test.go",
        "internal/platform/security/auth.go",
        "internal/platform/security/lifecycle_test.go",
        "internal/identity/application/rotate_session.go",
        "internal/identity/application/complete_password_reset.go",
        "internal/identity/application/logout.go"
      ],
      "note": "Token opaco com hash no banco, cookie HttpOnly/Secure/SameSite=Lax, rotação e revogação de todas as sessões no logout e na redefinição de senha: implementados e testados. O prefixo __Host- que o modelo declara não existe em lugar nenhum — é o finding SEC-01, aceito pelo titular em 2026-09-22 com a microtarefa P20-T04A nomeada."
    },
    {
      "id": "THR-AUTH-02",
      "severity": "Média",
      "verdict": "mitigated",
      "evidence": [
        "internal/identity/adapters/http",
        "internal/platform/ratelimit",
        "internal/identity/application"
      ],
      "note": "Resposta uniforme em login e recuperação (mesmo formato RFC 9457 para conta existente e inexistente), limite de ritmo por IP e conta, desafio Turnstile após falhas consecutivas e Argon2id com parâmetros calibrados."
    },
    {
      "id": "THR-AUTH-03",
      "severity": "Alta",
      "verdict": "mitigated",
      "evidence": [
        "internal/platform/security/csrf.go",
        "internal/platform/security/csrf_test.go",
        "internal/platform/security/manager.go"
      ],
      "note": "Duplo envio com token assinado por HMAC-SHA256, comparação em tempo constante, cookie SameSite=Lax, métodos seguros isentos e mutantes exigidos, origem nula recusada. A comparação de origem é defesa em profundidade: quem decide é o duplo envio (ver TB-03)."
    },
    {
      "id": "THR-AUTH-04",
      "severity": "Alta",
      "verdict": "mitigated",
      "evidence": [
        "internal/identity/application/request_password_reset.go",
        "internal/identity/adapters/postgres/password_reset_test.go",
        "internal/identity/application/complete_password_reset.go"
      ],
      "note": "Token de recuperação com CSPRNG, uso único, 15 minutos de validade, invalidação dos demais tokens e de todas as sessões ativas no consumo."
    },
    {
      "id": "THR-WAL-01",
      "severity": "Crítica",
      "verdict": "mitigated",
      "evidence": [
        "db/queries/wallet.sql",
        "internal/arguments/adapters/postgres/integrity_test.go",
        "internal/wallet/adapters/postgres/repository_test.go"
      ],
      "note": "Débito e publicação na mesma transação, SELECT ... FOR UPDATE no saldo, CHECK (balance >= 0) e chave de idempotência com ON CONFLICT DO NOTHING. A área double-spend roda a suíte com -race no portão."
    },
    {
      "id": "THR-WAL-02",
      "severity": "Crítica",
      "verdict": "mitigated",
      "evidence": [
        "internal/platform/postgres/wallet_test.go",
        "internal/wallet/adapters/postgres/repository_test.go",
        "docs/MIGRATION_AUDIT.md",
        "tools/migrationaudit/ledger.go"
      ],
      "note": "O ledger é append-only e o privilégio é medido, não prometido: a auditoria de migrations lê o catálogo e prova que arena_app não tem UPDATE nem DELETE em wallet_transactions e wallet_operations, nem TRUNCATE/REFERENCES/TRIGGER em nenhuma tabela."
    },
    {
      "id": "THR-WAL-03",
      "severity": "Alta",
      "verdict": "mitigated",
      "evidence": [
        "internal/arenas/adapters/http/challenge_http_test.go",
        "internal/arguments/adapters/http/ratelimit_test.go",
        "internal/billing/adapters/http/ratelimit_test.go"
      ],
      "note": "Grant atrelado a conta verificada e desafio de borda, INK promocional não sacável e sem rota de transferência entre contas, limite de ritmo por IP. Residual declarado pelo modelo: Médio."
    },
    {
      "id": "THR-STRIPE-01",
      "severity": "Crítica",
      "verdict": "mitigated",
      "evidence": [
        "internal/billing/adapters/stripe/webhook.go",
        "internal/billing/adapters/stripe/webhook_test.go",
        "internal/billing/application/process_webhook_test.go"
      ],
      "note": "HMAC-SHA256 sobre o corpo bruto, tolerância de 5 minutos, carimbo no futuro recusado, comparação em tempo constante (hmac.Equal) e unicidade do event_id no banco."
    },
    {
      "id": "THR-STRIPE-02",
      "severity": "Crítica",
      "verdict": "mitigated",
      "evidence": [
        "internal/billing/application/process_webhook_test.go",
        "internal/billing/adapters/http/billing.go"
      ],
      "note": "A concessão acontece no processamento do webhook autenticado; a página de sucesso é informativa e a autorização de recursos lê o estado gravado."
    },
    {
      "id": "THR-STRIPE-03",
      "severity": "Alta",
      "verdict": "mitigated",
      "evidence": [
        "internal/billing/application/process_webhook_test.go",
        "internal/billing/application/settle_checkout_test.go",
        "db/queries/billing.sql"
      ],
      "note": "Reenvio do mesmo event_id é reconhecido e responde 200 sem reexecutar mutação no ledger."
    },
    {
      "id": "THR-PERS-01",
      "severity": "Alta",
      "verdict": "mitigated",
      "evidence": [
        "internal/persuasion/application/record_attributions_test.go",
        "internal/persuasion/application/moderate_attribution_test.go",
        "internal/persuasion/domain"
      ],
      "note": "Autoatribuição recusada no servidor, elegibilidade por permanência e mudança real de posição exigida. Residual declarado pelo modelo: Médio."
    },
    {
      "id": "THR-PERS-02",
      "severity": "Média",
      "verdict": "mitigated",
      "evidence": [
        "internal/positions/adapters/http/positions_http_test.go",
        "internal/positions/application/read_position_test.go",
        "internal/platform/httpcache/policy.go"
      ],
      "note": "Agregado oculto antes do registro da posição inicial do próprio usuário, e as respostas que dependem da sessão saem com private, no-store."
    },
    {
      "id": "THR-MOD-01",
      "severity": "Alta",
      "verdict": "mitigated",
      "evidence": [
        "internal/moderation/application/file_report_test.go",
        "internal/moderation/adapters/http/ratelimit_test.go",
        "internal/moderation/domain/report.go"
      ],
      "note": "Denúncia não remove conteúdo: ela enfileira para triagem humana, com limite de ritmo e janela de duplicidade por usuário."
    },
    {
      "id": "THR-MOD-02",
      "severity": "Alta",
      "verdict": "mitigated",
      "evidence": [
        "internal/moderation/application/review.go",
        "internal/moderation/adapters/postgres/review_test.go",
        "internal/audit/adapters/postgres/recorder_test.go"
      ],
      "note": "Toda mutação de moderação grava evento imutável com moderador, alvo, justificativa tipada e snapshot; a gravação e a mutação compartilham a transação, e a trilha é append-only para o runtime (medido em MIGRATION_AUDIT.md)."
    },
    {
      "id": "THR-MOD-03",
      "severity": "Média",
      "verdict": "mitigated",
      "evidence": [
        "internal/moderation/adapters/http/handler.go",
        "internal/moderation/adapters/http/handler_test.go",
        "internal/moderation/adapters/postgres/reports_test.go"
      ],
      "note": "Identidade do denunciante e conteúdo de tickets nunca aparecem na resposta de usuário comum; a suíte inclui o caso negativo (TestModerationReportFlowHidesEvidence)."
    },
    {
      "id": "THR-CACHE-01",
      "severity": "Crítica",
      "verdict": "mitigated",
      "evidence": [
        "internal/platform/httpcache/policy.go",
        "internal/platform/httpcache/policy_test.go",
        "internal/arguments/adapters/http/arguments_http_test.go",
        "internal/wallet/adapters/http/wallet_http_test.go",
        "internal/positions/adapters/http/positions_http_test.go",
        "internal/transparency/adapters/http/handler_test.go"
      ],
      "note": "Toda superfície autenticada sai com private, no-store, no-cache, must-revalidate mais Pragma: no-cache; o público é limitado e cai para Private quando a resposta carrega Set-Cookie."
    },
    {
      "id": "THR-CACHE-02",
      "severity": "Alta",
      "verdict": "mitigated",
      "evidence": [
        "deploy/caddy/Caddyfile",
        "tools/caddyaudit/verify.sh",
        "internal/platform/httpcache/policy.go"
      ],
      "note": "O edge remove os headers do cliente que poderiam mentir sobre o cliente, define X-Forwarded-Host/Proto a partir do que observou e o gate caddy-verify prova isso com um peer que forja headers, incluindo o controle positivo. A matriz ainda marca esta ameaça como procedure: e a auditoria registra o que o gate mede em vez de promover a evidência (limite 3)."
    },
    {
      "id": "THR-ADM-01",
      "severity": "Crítica",
      "verdict": "accepted",
      "finding": "SEC-02",
      "evidence": [
        "internal/moderation/adapters/http/handler.go",
        "internal/moderation/adapters/http/handler_test.go",
        "internal/moderation/application/authorizer.go",
        "internal/moderation/application/authorizer_test.go",
        "internal/jobs/adapters/http/handler_test.go"
      ],
      "note": "Autorização por capacidade conferida no banco a cada requisição, segundo fator recente exigido para as ações sensíveis e matriz negativa (anônimo, papel insuficiente, sessão velha) nas duas superfícies. O namespace que o modelo nomeia (/api/v1/admin/*) não existe: a superfície é /api/v1/moderation/* e a administração de papéis é comando. É o finding SEC-02, aceito em 2026-09-22."
    },
    {
      "id": "THR-ADM-02",
      "severity": "Crítica",
      "verdict": "mitigated",
      "evidence": [
        "internal/wallet/application/adjust_ink_test.go",
        "internal/audit/adapters/postgres/recorder_test.go",
        "internal/audit/domain"
      ],
      "note": "Ajuste administrativo de saldo exige justificativa tipada, roda em transação com a gravação do evento de auditoria e falha por inteiro se a trilha não gravar; a trilha é append-only para o runtime."
    }
  ],
  "areas": [
    {
      "key": "threat-model",
      "execution": "go test -count=1 ./internal/security/...",
      "evidence": ["docs/THREAT_MODEL.md", "docs/THREAT_MODEL_TEST_MATRIX.md", "internal/security/threatmodel_test.go"],
      "note": "O modelo e a matriz têm o mesmo conjunto de IDs, sem duplicata, e nenhuma ameaça crítica depende de procedimento manual."
    },
    {
      "key": "negative-authorization",
      "execution": "go test -count=1 ./internal/moderation/adapters/http/... ./internal/jobs/adapters/http/...",
      "evidence": ["internal/moderation/adapters/http/handler_test.go", "internal/jobs/adapters/http/handler_test.go", "internal/moderation/application/authorizer_test.go"],
      "note": "Anônimo, papel insuficiente e sessão sem segundo fator recente recusados; o gate acompanha a mudança de papel imediatamente."
    },
    {
      "key": "cache",
      "execution": "go test -count=1 ./internal/platform/httpcache/... ./internal/arguments/adapters/http/... ./internal/wallet/adapters/http/... ./internal/positions/adapters/http/... ./internal/transparency/adapters/http/...",
      "evidence": ["internal/platform/httpcache/policy.go", "internal/platform/httpcache/policy_test.go"],
      "note": "Política única para as superfícies de entrada e asserção do header em cada uma."
    },
    {
      "key": "csrf",
      "execution": "go test -count=1 ./internal/platform/security/...",
      "evidence": ["internal/platform/security/csrf.go", "internal/platform/security/csrf_test.go"],
      "note": "Inclui a recusa de origem nula e a assimetria entre método seguro e mutante."
    },
    {
      "key": "sessions",
      "execution": "go test -count=1 ./internal/identity/application/... ./internal/identity/adapters/http/...",
      "evidence": ["internal/identity/domain/policies.go", "internal/identity/application/rotate_session.go", "internal/identity/application/logout.go"],
      "note": "Política de inatividade e vida absoluta, rotação e revogação em massa; a listagem de sessões expira o que passou do prazo."
    },
    {
      "key": "mfa",
      "execution": "go test -count=1 ./internal/identity/... ./internal/moderation/...",
      "evidence": ["internal/identity/application/mfa.go", "internal/moderation/domain/policy.go", "internal/moderation/adapters/http/handler.go"],
      "note": "Enrolamento, confirmação, códigos de recuperação e step-up com janela de 15 minutos; a superfície administrativa consulta mfa_verified_at da sessão."
    },
    {
      "key": "stripe",
      "execution": "go test -count=1 ./internal/billing/...",
      "evidence": ["internal/billing/adapters/stripe/webhook.go", "internal/billing/adapters/stripe/webhook_test.go"],
      "note": "Corpo bruto, tolerância, futuro recusado e idempotência."
    },
    {
      "key": "double-spend",
      "execution": "go test -count=1 -race ./internal/arguments/adapters/postgres/... ./internal/wallet/adapters/postgres/...",
      "evidence": ["db/queries/wallet.sql", "internal/platform/postgres/wallet_test.go"],
      "note": "O detector de corrida roda sobre o caminho que debita e publica."
    },
    {
      "key": "idor",
      "execution": "go test -count=1 ./internal/arguments/adapters/http/... ./internal/arenas/adapters/http/... ./internal/billing/adapters/http/...",
      "evidence": ["internal/arguments/adapters/http/arguments_http_test.go", "internal/arenas/adapters/http/arenas_http_test.go"],
      "note": "Escrita e leitura de recurso alheio recusadas no servidor; a titularidade é conferida no caso de uso, não no cliente."
    },
    {
      "key": "secrets",
      "execution": "go test -count=1 ./internal/platform/logging/...",
      "deferred_to": ["source-scans"],
      "evidence": ["internal/platform/logging/logging.go", ".gitignore"],
      "note": "Redação de valor e de endereço no log; a varredura estrutural da árvore é feita pelo próprio portão e a varredura de valor é do gitleaks na esteira."
    },
    {
      "key": "dependencies",
      "execution": "make vuln",
      "deferred_to": ["source-scans", "quick"],
      "evidence": ["go.mod", "go.sum", "quality/dependencies.json", "quality/sbom.json", "docs/DEPENDENCIES.md"],
      "note": "govulncheck sobre todos os pacotes e, no caminho rápido de cada pull request, a procedência: `make audit-deps` recusa a dependência que ninguém aprovou — inclusive a transitiva —, a versão que se moveu sem o registro, a licença fora do que a classe homologa e a lista de materiais que discorda da árvore. A revisão de dependências que a esteira de release prometia é hoje um portão local que roda antes dos testes caros; `npm audit` continua na esteira."
    },
    {
      "key": "container",
      "execution": "go test -count=1 ./tools/imageaudit/...",
      "deferred_to": ["image", "image-scan"],
      "evidence": ["Dockerfile", ".dockerignore", "tools/imageaudit/audit.go"],
      "note": "As regras da receita e do artefato rodam aqui; a constrói-e-sobe de verdade e o scan da imagem são jobs da esteira."
    }
  ],
  "findings": [
    {
      "id": "SEC-01",
      "title": "O prefixo __Host- declarado em THR-AUTH-01 não existe no cookie de sessão",
      "severity": "Média",
      "status": "accepted",
      "owner": "titular do repositório",
      "accepted_on": "2026-09-22",
      "threat": "THR-AUTH-01",
      "evidence": ["internal/platform/security/cookies.go", "internal/bootstrap/participation.go"],
      "plan": "P20-T04A renomeia o cookie para __Host-arena_session em produção (sem Domain), com o contrato, as suítes e a prosa do modelo acompanhando no mesmo commit."
    },
    {
      "id": "SEC-02",
      "title": "O modelo descreve um namespace administrativo (/api/v1/admin/*) que não existe no que é servido",
      "severity": "Baixa",
      "status": "accepted",
      "owner": "titular do repositório",
      "accepted_on": "2026-09-22",
      "threat": "THR-ADM-01",
      "evidence": ["api/openapi.json", "internal/moderation/domain/policy.go", "cmd/arena/admin.go"],
      "plan": "P20-T06 corrige a prosa do modelo (namespace e middleware) para descrever a superfície real: /api/v1/moderation/* com capacidades, mais o comando arena admin."
    }
  ]
}
```
