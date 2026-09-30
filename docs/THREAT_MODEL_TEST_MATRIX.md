# Matriz executável do threat model

Esta matriz é o índice versionado usado pelo gate `make test-security`. Cada `THR-*`
do `docs/THREAT_MODEL.md` aparece exatamente uma vez. Evidência `test:` aponta para
uma suíte automatizada; `procedure:` aponta para um procedimento manual versionado.
A ausência de uma linha, uma ameaça desconhecida ou uma ameaça crítica sem evidência
bloqueia o gate.

| ID | Severidade | Evidência | Resíduo |
|---|---|---|---|
| THR-AUTH-01 | Crítica | test: `internal/platform/security`, `internal/platform/security/lifecycle_test.go`, `internal/identity/application`, `internal/contract/auth_attack_matrix_test.go` | Baixo |
| THR-AUTH-02 | Média | test: `internal/identity/adapters/http`, `internal/platform/ratelimit`, `internal/identity/application`, `internal/contract/auth_attack_matrix_test.go` | Baixo |
| THR-AUTH-03 | Alta | test: `internal/platform/security/csrf_test.go` | Baixo |
| THR-AUTH-04 | Alta | test: `internal/identity/application/password_reset_test.go`, `internal/identity/adapters/postgres/password_reset_test.go`, `internal/contract/auth_attack_matrix_test.go` | Baixo |
| THR-WAL-01 | Crítica | test: `internal/arguments/adapters/postgres/integrity_test.go`, `internal/wallet/adapters/postgres/repository_test.go` | Baixo |
| THR-WAL-02 | Crítica | test: `internal/platform/postgres/wallet_test.go`, `internal/wallet/adapters/postgres/repository_test.go` | Baixo |
| THR-WAL-03 | Alta | test: `internal/arenas/adapters/http/challenge_http_test.go`, `internal/arguments/adapters/http/ratelimit_test.go`, `internal/billing/adapters/http/ratelimit_test.go` | Médio |
| THR-STRIPE-01 | Crítica | test: `internal/billing/adapters/stripe/webhook_test.go`, `internal/billing/application/process_webhook_test.go`, `internal/contract/webhook_adversarial_test.go` | Baixo |
| THR-STRIPE-02 | Crítica | test: `internal/billing/application/process_webhook_test.go`, `internal/contract/webhook_adversarial_test.go` | Baixo |
| THR-STRIPE-03 | Alta | test: `internal/billing/application/process_webhook_test.go`, `internal/billing/application/settle_checkout_test.go`, `internal/contract/webhook_adversarial_test.go` | Baixo |
| THR-PERS-01 | Alta | test: `internal/persuasion/application/record_attributions_test.go`, `internal/persuasion/application/moderate_attribution_test.go` | Médio |
| THR-PERS-02 | Média | test: `internal/positions/adapters/http/positions_http_test.go`, `internal/positions/application/read_position_test.go` | Baixo |
| THR-MOD-01 | Alta | test: `internal/moderation/application/file_report_test.go`, `internal/moderation/adapters/http/ratelimit_test.go`, `internal/contract/authorization_matrix_test.go` | Baixo |
| THR-MOD-02 | Alta | test: `internal/moderation/application/review_test.go`, `internal/moderation/adapters/postgres/review_test.go`, `internal/audit/adapters/postgres/recorder_test.go`, `internal/contract/privileged_controls_test.go` | Baixo |
| THR-MOD-03 | Média | test: `internal/moderation/adapters/http/handler_test.go`, `internal/moderation/adapters/postgres/reports_test.go` | Baixo |
| THR-CACHE-01 | Crítica | test: `internal/arguments/adapters/http/arguments_http_test.go`, `internal/wallet/adapters/http/wallet_http_test.go`, `internal/positions/adapters/http/positions_http_test.go`, `internal/transparency/adapters/http/handler_test.go`, `internal/contract/http_trust_boundaries_test.go` | Baixo |
| THR-CACHE-02 | Alta | procedure: `docs/SECURITY.md` §5; execute against the configured reverse proxy before release; review-by 2026-12-31 | Baixo |
| THR-ADM-01 | Crítica | test: `internal/moderation/adapters/http/handler_test.go`, `internal/jobs/adapters/http/handler_test.go`, `internal/moderation/application/authorizer_test.go`, `internal/contract/authorization_matrix_test.go`, `internal/contract/privileged_controls_test.go` | Baixo |
| THR-ADM-02 | Crítica | test: `internal/wallet/application/adjust_ink_test.go`, `internal/audit/adapters/postgres/recorder_test.go`, `internal/contract/privileged_controls_test.go` | Baixo |

## Regra do gate

O teste estrutural exige que o conjunto de IDs seja igual ao conjunto de ameaças do
modelo, sem duplicatas na matriz. Para cada ameaça crítica, a evidência precisa ser
automatizada (`test:`); procedimentos manuais não substituem regressão automatizada
para riscos críticos. Cada alvo `test:` precisa existir como arquivo ou pacote, de
modo que ameaça nova sem teste e referência pendurada falham. Procedimento carrega
`review-by AAAA-MM-DD` futuro: risco não automatizável bloqueia certificação ao
vencer. O `make test-security` executa todos os pacotes citados na matriz.
