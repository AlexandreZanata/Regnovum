# External approvals and pre-conditions (P59-T03)

**Status:** acompanhamento, não portão executável. Nenhuma aprovação desta lista
foi concedida até este commit; inferência do agente nunca conta como aprovação.

**Objetivo:** nomear cada decisão humana, revisão independente ou credencial
operacional que ainda bloqueia o release e dizer onde o bloqueio é aplicado —
em complemento a [GOVERNANCE.md](../GOVERNANCE.md), que já registra as sete
decisões do titular com o auditor `governanceaudit` (idade mínima, licença,
contato legal/privacidade, retenção, mercados, termos de uso, canal de
segurança). Este arquivo cobre o que o GOVERNANCE não cobre: ratificações de
qualidade, parâmetros sazonais, jurídico por mercado, revisão independente e
pré-condições operacionais.

## Tabela de aprovações

| # | Item | Quem decide | Critério / artefato | Estado |
|---|------|-------------|---------------------|--------|
| 1 | Ratificação Q01–Q36 | titular do repositório | `docs/DECISOES_VIGENTES.md` com status APROVADA por qualidade; suíte integral no SHA final | nenhuma qualidade ratificada |
| 2 | Parâmetros TEMP | titular do repositório | `docs/reino/TEMPORADAS_SUCESSAO.md` com parâmetros INTENÇÃO promovidos a decisão; depende de P39 → P46 → P40 → P41 → P42 → P43 → P47 → P44 → P45 | INTENÇÃO apenas |
| 3 | Jurídico por mercado | assessoria por país | parecer por mercado cobrindo idade, retenção, tributos e contato legal; mercados seguem `GOVERNANCE.md` | ausente |
| 4 | Revisão independente (IREV) | revisor independente | laudo IREV sem achados bloqueadores | ausente |
| 5 | Revisão humana en-US | revisor bilíngue | catálogo `en-US` revisado contra `pt-BR` (nenhuma chave `reviewed:false` pendente de humano) | pendente (pré-release) |
| 6 | Credenciais de provider | operador técnico | Stripe, Resend, Sentry e PostHog configurados via Secrets/ambiente (nunca no repositório); smoke de cada integração verde | pré-requisito operacional |
| 7 | Publicação histórica | operador técnico | `HISTORY.md` + espelho wiki publicados via `./.local/git-flow.sh wiki` após cada merge de fase | por fase, até P59 |

## Onde cada bloqueio é aplicado

- Itens 1–2: `make release-gate` recusa sem `DECISOES_VIGENTES.md` APROVADA e
  sem a cadeia de dependências cumprida; itens 3–4 seguem a mesma trilha de
  gate final pós-merge.
- Item 5: `make i18n-check` enumera as chaves pendentes; o portão de release
  exige catálogo revisado.
- Item 6: nenhum segredo no repositório (`grep` por `sk_live`, `re_`,
  `phc_`, DSN real); as integrações têm smoke próprio antes do release.
- Item 7: conferido no encerramento de cada fase (regra de registro
  histórico do [AGENTS.md](../../AGENTS.md)).

## Regras que nunca mudam

- Nenhuma qualidade, parâmetro TEMP ou item jurídico é considerado aprovado
  por inferência do agente, por texto descritivo ou por passagem de teste
  automatizado.
- Ativação de rotas staged e ativação de economia real têm runbooks próprios
  ([RUNBOOKS.md](../RUNBOOKS.md) §6) e exigem, além desta lista, as
  pré-condições humanas e jurídicas nomeadas em cada runbook.
