# Inventário e direitos legados

**Versão:** v0.1 (proposta, sem ratificação; pendente como Q05/Q19/Q30)
**Fonte:** [RESPOSTAS.md](RESPOSTAS.md) itens 5, 19, 30; [DECISOES_VIGENTES.md](DECISOES_VIGENTES.md) Q05, Q19, Q30 (todas `PENDENTE`); [CARTA_ECONOMICA.md](CARTA_ECONOMICA.md) §10; [MONETIZATION.md](../MONETIZATION.md) §2–§5; [BUSINESS_RULES.md](../BUSINESS_RULES.md) §9
**Responsável:** titular do repositório (aprovação expressa por item; sem aprovação em nome do titular)
**Estado:** especificação. Nada aqui converte, expira, confisca ou transfere direito algum. O livro legado segue intacto e o produto econômico segue desativado até P44: sem rota, sem caso de uso, sem migration e sem código que mova saldo nesta fase. Nenhum saldo real foi lido neste planejamento: o inventário abaixo cita apenas schema, contratos de domínio e regras publicadas, nunca linhas de conta. Enquanto houver pendência crítica, as fases dependentes param; proposta não ratificada não vira regra por inferência.

## 1. Inventário por contrato (schema, sem dados)

- **Franquia e INK comprado (ledger legado):** `app.wallet_accounts` (`balance_free` FREE_INK consumido primeiro, `balance_purchased` PURCHASED_INK consumido depois, ambos nunca negativos) e `app.wallet_transactions` (append-only, deltas assinados por bucket, `amount <> 0`, uma operação toca cada bucket no máximo uma vez) — `00008_wallet_ledger_schema.sql`; âncora de ciclo da franquia — `00009_wallet_free_cycle_anchor.sql`. Domínio: `internal/wallet/domain/bucket.go` (`FREE_INK` antes de `PURCHASED_INK`), `allocation.go` (free-first), `period.go` (franquia Free 5.000/ Member 30.000 totais por período, sem acumular, expira no período).
- **Arena Pass:** `app.arena_pass_lots` (origem, quantidade, expiração imutável opcional) e `app.arena_pass_consumptions` (append-only, uma linha por Arena publicada) — `00011_arena_pass_schema.sql`. Regra: rascunho nunca consome; publicação bem-sucedida consome exatamente um; passes comprados não expiram inicialmente; passe Member é franquia do período e não acumula; passes não se transferem nem viram INK.
- **Assinatura Member:** `app.stripe_customers`, `app.subscriptions` (espelho do provedor; franquias do período concedidas deste estado, nunca de visita ao browser), `app.checkout_intents`, `app.stripe_events` (idempotência por `event_id`), `app.billing_reconciliation_runs`/`_findings` — `00020_billing_stripe_schema.sql`. Domínio: `internal/billing/domain/grant.go` (`INK`, `ARENA_PASS`, `MEMBER`).
- **Refund e chargeback:** `app.billing_refunds` (uma linha por objeto de refund/disputa do provedor, quantidades compensatórias, flag de revisão humana) — `00021_billing_refunds.sql`; domínio `internal/billing/domain/refund.go`; INK comprado não utilizado pode sair por reembolso, franquia não tem valor de reembolso, parte consumida exige regra de fraude e suporte sem saldo negativo automático para boa-fé.

## 2. Opções por direito (sem conversão automática)

O [adendo sazonal](TEMPORADAS_SUCESSAO.md) não acrescenta expiração a direitos legados. Conversão voluntária para INK de temporada precisa mostrar o término e consumir o crédito convertido uma única vez; o reset não cria uma nova concessão do mesmo crédito. Refund/obrigação fiat antiga mantém vínculo ao recibo original e não é liquidado por INK novo por inferência.

Toda transição exige aceite expresso e versionado da nova Carta onde aplicável (Q05, proposta); quem recusar conserva acesso razoável a histórico, exportação, recurso e liquidação de direitos anteriores.

- **Permanência:** manter o direito no livro legado com utilidade e prazo dos termos aceitos.
- **Opt-in explícito:** conversão com equivalência e taxa divulgadas, custeada pelo Tesouro Genesis com débito simultâneo; só com estoque livre, ou a promessa é encerrada prospectivamente, nunca simulada por mint.
- **Reembolso:** proporcional e contratual pela trilha do provedor, com revisão humana; benefício consumido não vira dívida implícita sem base.

## 3. Matriz de transição

| Caso | Permanência | Opt-in | Reembolso | Conversão automática |
|---|---|---|---|---|
| Conta ativa com franquia do período | usa a franquia no prazo; expira sem acumular | só com aceite + equivalência publicada | sem reembolso de franquia | nenhuma |
| Conta recusante da nova Carta | conserva histórico, exportação, recurso e liquidação | sem aceite não há conversão | reembolso proporcional do contratado, se devido | nenhuma |
| Saldo FREE_INK não usado | vale até o fim do período | opt-in encerra o crédito legado na mesma intenção | sem reembolso | nenhuma |
| Saldo FREE_INK usado | consumo já ocorrido permanece válido | nada a converter | nada a devolver | nenhuma |
| Saldo PURCHASED_INK não usado | permanece após cancelamento | opt-in com taxa aprovada, sem criar INK | reembolso pode retirar o não utilizado | nenhuma |
| Saldo PURCHASED_INK usado | consumo válido, sem dívida implícita para boa-fé | nada a converter | regra de fraude e suporte, sem negativo automático | nenhuma |
| Assinatura Member ativa | benefícios até o fim do período pago | novas franquias pós-Genesis só do Tesouro | cancela renovação futura, sem reembolso de franquia | nenhuma |
| Assinatura cancelada ou em falha | conteúdo publicado permanece; expira a franquia | sem estoque não há promessa simulada | reembolso conforme contrato e provedor | nenhuma |
| Passe comprado | não expira inicialmente; consome um por publicação | opt-in preserva quantidade e prazo | restituição auditada só em erro da plataforma ou moderação revertida | nenhuma |
| Passe de franquia Member | vale no período, não acumula | idem, distinguível do comprado | sem reembolso | nenhuma |
| Refund financeiro | retira INK não utilizado pela trilha do provedor | vinculado à mesma intenção | proporcional, com revisão humana | nenhuma |
| Chargeback contestado | terceiro de boa-fé conserva; operador suporta e cobra do devedor por via contratual | sem conversão forçada | sem recuperação automática de inocentes | nenhuma |

## 4. Estratégia expand/contract (planejada, P33-T07)

Somente migrations aditivas e reversíveis; leitura dupla com o livro legado como verdade até o corte; ensaio em cópias sintéticas com comparação de checksums e app anterior operável na janela declarada; rollback pela estratégia documentada, sem `down` destrutivo; saldo ambíguo ou órfão bloqueia a migração. Nenhum dado pessoal entra no relatório de inventário.

## 5. Proibições

- Nenhuma conversão automática de livro legado em moeda Genesis.
- Nenhuma franquia pós-Genesis sem débito simultâneo do Tesouro; sem estoque, encerrar prospectivamente, nunca cunhar.
- Nenhuma leitura de saldo real ou PII neste planejamento; só schema e contratos.
- Nenhum `UPDATE`/`DELETE` de lançamento legado; correção só por evento novo vinculado.
