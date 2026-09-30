# Tempo econômico canônico

**Versão:** v0.1 (proposta, sem ratificação; pendente como Q06/Q22/Q25/Q31)
**Fonte:** [RESPOSTAS.md](RESPOSTAS.md) itens 6, 22, 25, 31; [DECISOES_VIGENTES.md](DECISOES_VIGENTES.md) Q06, Q22, Q25, Q31 (todas `PENDENTE`)
**Estado:** especificação. Nada aqui é regra ativa, parâmetro vigente ou
autorização para implementar, cobrar ou liquidar. Cada exemplo abaixo tem
exatamente um resultado; ambiguidade restante é lacuna a fechar, nunca
interpretação livre.

## 1. Instantes e precisão

Todo instante persistido é UTC com precisão explícita (segundos para
vigências, microssegundos para lançamentos, conforme a coluna). Quatro
instantes distintos viajam com cada fato econômico, cada um com um dono:

| Instante | Dono | Significado |
|---|---|---|
| `occurred_at` | origem informante | quando o fato aconteceu no mundo (pode ser impreciso) |
| `accepted_at` | serviço | quando a intenção foi aceita (regras e preços desta hora valem) |
| `posted_at` | banco de dados | relógio do banco lido **dentro** da transação do lançamento |
| `settled_at` | serviço | quando o efeito se tornou visível e final |

`posted_at` nunca é prometido como "timestamp exato do commit": o
PostgreSQL não o fornece no protocolo escolhido, e o evento só se torna
visível quando a transação commita. O relógio do browser nunca decide
saldo, preço, período ou vigência.

## 2. Intervalos `[início, fim)`

**Ajuste sazonal:** temporadas duram exatamente 7.776.000 segundos em UTC, não três meses civis. O [adendo](TEMPORADAS_SUCESSAO.md) define `season_id`, barreira de fecho e `cutoff_revision`. Uma intenção antiga não adquire direito a crédito na sucessora por webhook tardio; quote e liquidação são limitados pelo fim da temporada. As regras dos exemplos abaixo continuam distinguindo aceitação/postagem, mas não autorizam efeito econômico entre temporadas.

Vigências, benefícios, quarentenas e janelas são intervalos semiabertos:
no instante exato de `fim`, a oferta expirou. Exemplo canônico: benefício
válido em `[2026-09-01T00:00:00Z, 2026-10-01T00:00:00Z)` está ativo em
`2026-09-30T23:59:59Z` e expirado em `2026-10-01T00:00:00Z` — um único
resultado, sem "tolerância de fronteira".

## 3. Exemplos resolvidos (um resultado cada)

**Transação em voo na meia-noite.** Intenção aceita em
`2026-12-31T23:59:59Z` (`accepted_at` em 2026), lançada com `posted_at`
`2027-01-01T00:00:01Z`. Resultado: o fato pertence à janela de 2026 pela
aceitação (preço e regras de 2026 valem) e ao ledger de 2027 pela
postagem. Aceitação e postagem nunca se misturam na mesma pergunta.

**Limite exato.** Assinatura válida até `2026-10-01T00:00:00Z`:
requisição autenticada em `2026-09-30T23:59:59.999999Z` passa;
em `2026-10-01T00:00:00Z` é recusada (§2).

**DST.** Períodos de cobrança são durações de instantes, não dias de
parede: `2026-03-08T00:00:00-05:00` a `2026-03-09T00:00:00-04:00`
(virada de horário de verão nos EUA) dura exatamente 23 horas. Fuso
horário formata exibição; nunca decide duração, período ou vigência.

**Ano bissexto.** Âncora mensal legada em dia 31: competência de
fevereiro de 2024 (bissexto) fecha em `2024-02-29T23:59:59Z`; em 2025,
ano comum, fecha em `2025-02-28T23:59:59Z` (clamp ao último dia, sem
deslocar a âncora de março).

**Semana 53.** O calendário semanal é ISO 8601 em UTC, sem depender do
fuso do usuário: `2020-W53` existe (segunda `2020-12-28` a domingo
`2021-01-03`) e recebe lançamentos normalmente; anos sem W53 não aceitam
a etiqueta, e a barreira de fecho (§4) a rejeita como semana inexistente.

**Clock skew.** Timestamp de provedor fora da tolerância de skew não
recalibra o serviço: vendas novas com fonte dessincronizada além do
limite são suspensas até a fonte voltar; vendas já aceitas seguem os
termos da aceitação.

**Webhook tardio.** Evento do provedor chegando após a liquidação é
reconciliado pela chave de idempotência como duplicata tardia: confirma
o efeito original, nunca cria segundo efeito e nunca reabre a janela de
aceitação.

**Retry após commit.** Resposta perdida depois do commit: a repetição da
mesma intenção com a mesma chave devolve o efeito original, sem segunda
cobrança. O que decide é a chave, não a contagem de tentativas.

## 4. Fecho de semana e barreira

O fecho de época espera e ordena transações em voo antes de selar a
semana (barreira de concorrência); evento tardio não reabre semana
selada sem lançamento corretivo atual e vinculado. Migalhas usam apenas
as até quatro semanas ISO completas seladas anteriores; `N=0` implica
distribuição zero.

## 5. Meses legados e versões

Mensal legado preserva a âncora contratual (exemplo do §3, ano
bissexto). Versão de Carta e tabela de preço são as vigentes na
**aceitação** (`accepted_at`), nunca as do momento da exibição, do
webhook ou da liquidação: intenção aceita dentro do prazo preserva os
termos até o prazo contratual de liquidação; intenção expirada exige
nova cotação, com `observed_at`, fontes, versão, `expires_at` e hash.

## 6. Proibições

- Nenhuma regra usa relógio do cliente para saldo, preço, período ou vigência.
- Nenhum documento afirma "timestamp exato do commit": afirma-se `posted_at` lido na transação.
- Nenhuma cotação sem `expires_at` é válida; validade checa-se na aceitação, não no webhook tardio.
- Nenhum `time.Sleep` prova sincronização em teste ou job.
