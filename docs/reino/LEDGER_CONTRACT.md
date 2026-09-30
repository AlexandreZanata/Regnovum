# Contrato de custódia e ledger

**Versão:** v0.1 (proposta, sem ratificação; pendente como Q08/Q11/Q20/Q21/Q23/Q24/Q28/Q30)
**Fonte:** [RESPOSTAS.md](RESPOSTAS.md) itens 8, 11, 20, 21, 23, 24, 28, 30; [DECISOES_VIGENTES.md](DECISOES_VIGENTES.md) Q08, Q11, Q20, Q21, Q23, Q24, Q28, Q30 (todas `PENDENTE`); [CARTA_ECONOMICA.md](CARTA_ECONOMICA.md) §1–§2, §4; [TEMPO_ECONOMICO.md](TEMPO_ECONOMICO.md) §1, §4; [PRECIFICACAO.md](PRECIFICACAO.md) §4
**Responsável:** titular do repositório (aprovação expressa por item; sem aprovação em nome do titular)
**Estado:** especificação. Nada aqui é regra ativa, saldo vigente ou autorização para cunhar, cobrar, transferir ou liquidar. O produto econômico segue desativado até P44: sem rota, sem caso de uso, sem migration e sem código que mova saldo nesta fase. Enquanto houver pendência crítica, as fases dependentes param; proposta não ratificada não vira regra por inferência.

## 1. Contas de custódia exclusivas

Toda unidade Genesis vive em uma e apenas uma custódia. Partições mutuamente exclusivas:

- Tesouro (custódia única do Genesis) com subcontas internas exclusivas: Reserva Soberana, Estoque Comercial, Caixa Operacional, Obrigações e Empenhos. Subconta interna nunca se soma ao Tesouro.
- Contas de usuários, escrows e cauções, cofres de contrato, principal de Título se autorizado.
- `disponível = saldo real − reservas − obrigações exigíveis − empenhos`, sem contagem dupla.

Soma S (proposta, pendente de Q20/Q21): `S = 2.100.000.000 INK`; **sugestão** pendente `= 2.100.000.000.000 milliINK` com `1 INK = 1.000 milliINK`. Antes da ratificação, S é especificação pendente e o ledger atual segue inalterado.

## 2. Dupla entrada e identidade

Todo movimento tem origem, destino, montante inteiro, tipo, causa, instantes (`accepted_at` decide regras/preço, `posted_at` nasce do relógio do banco dentro da transação) e identidade única de transação. Débito e crédito são inseparáveis: ambos ocorrem ou nenhum. Repetir a mesma intenção com a mesma chave devolve o efeito original, sem segunda transferência. Saldo é projeção reconstituível do ledger append-only, nunca valor editado; `UPDATE`/`DELETE` de lançamento é vedado.

## 3. Oferta Genesis, reserva, obrigações e empenho

O único evento de criação é o Genesis integral no Tesouro; após ele não há emissão. Venda, Migalha, rendimento ou decreto exigem empenho atômico prévio sobre saldo realmente livre; estoque zero interrompe a venda sem emissão para suprir. Obrigação é reivindicação sobre INK, não INK adicional. Livro externo de fiat e livro interno de INK são distintos: cobrança externa não cria INK e recebimento válido só autoriza transferir estoque existente.

## 4. Exemplos que preservam S (um resultado cada)

Convenção: saldos inteiros em INK; `S` constante em cada snapshot confirmado. O mesmo INK nunca aparece em duas custódias.

**Compra.** Estado inicial: Tesouro `2.100.000.000`, Usuária Ana `0`. Fiat externo liquidado fora do ledger autoriza transferir estoque existente. Lançamento único: Tesouro `−1.000`, Ana `+1.000`. Estado final: Tesouro `2.099.999.000`, Ana `1.000`. Soma `2.100.000.000`. Idempotência por intenção mais evento externo: replay devolve o efeito, sem segunda transferência.

**Escrita.** Ana publica texto canônico de 50 clusters; tabela vigente na aceitação: `50 INK`. Lançamento único com a publicação: Ana `−50`, Tesouro `+50`. Final: Tesouro `2.099.999.050`, Ana `950`. Soma preservada. Rascunho, digitação, retry antes do commit e texto alterado não cobram; resposta perdida após o commit devolve o efeito pela chave.

**Dízimo.** Bruno paga `1.000` a Carla em comércio formal liquidado. Taxa `floor(1.000 × 10 / 100) = 100`. Lançamentos atômicos: Bruno `−1.000`, Carla `+900`, Tesouro `+100`. Soma preservada. Presente sem contraprestação não sofre Dízimo; comércio disfarçado de presente é recusado como classificação, sem confisco automático.

**Escrow.** Diego bloqueia `500` para condição aceita: Diego `−500`, Escrow-E1 `+500` (bloqueio, não gasto nem propriedade livre da Coroa). Condição cumprida: Escrow-E1 `−500`, Elisa `+500`. Condição frustrada: Escrow-E1 `−500`, Diego `+500`. Em qualquer instante, os `500` estão em uma só custódia.

**Refund.** Reembolso do Dízimo acima desfaz pagamento e Dízimo na mesma trilha, por lançamentos novos vinculados: Carla `−900` para Bruno `+900`; Tesouro `−100` para Bruno `+100`. Soma preservada. Terceiro de boa-fé em transação final válida conserva o recebido; o operador suporta a diferença e cobra do devedor por via contratual, sem inventar INK.

**Decreto.** Ato real prospectivo transfere `200` do Tesouro Livre para Fernanda, com origem explícita Tesouro Livre, motivo e revisão. Tesouro `−200`, Fernanda `+200`. Soma preservada. Decreto sem origem legítima, com INK de escrow, de terceiro inocente ou indisponível é recusado; Coroa financia com patrimônio disponível.

## 5. Correção compensatória e estado congelado

Correção é novo lançamento vinculado ao original, com causa e vínculo causal, nunca edição ou apagamento. Divergência entre projeção, custódia e oferta (`Σ ≠ S`) congela todas as mutações econômicas até resolução auditada; não há ajuste para fechar a soma. Evento tardio não reabre semana selada sem lançamento corretivo atual.

## 6. Contraexemplos recusados

- Mint fora do Tesouro ou segunda Genesis após o evento único.
- A mesma unidade em duas partições ou subconta somada ao total.
- Arredondamento para cima, distribuição acima do orçamento ou fração sub-milliINK paga.
- Cobrança fora do commit de publicação; retry ou timeout antes do commit cobrando.
- Reembolso sem reversão do Dízimo na mesma trilha.
- Escrow liberado sem condição aceita ou tratado como receita livre.
- Decreto com custódia alheia, saldo indisponível ou efeito retroativo prejudicial.
- `UPDATE`/`DELETE` de lançamento, saldo negativo oculto ou INK inventado para reparar estorno.

## 7. Proibições

- Nenhum movimento sem custódia única de origem e destino legítimos; nenhuma contagem dupla.
- Nenhuma correção por edição; só lançamento compensatório vinculado.
- Nenhum decreto com patrimônio alheio; só patrimônio disponível do Tesouro.
- Nenhuma migration ou saldo real tocado por este documento; livro legado segue intacto até P31-T07 e transição com opt-in.
