# Política de preço e ponto de cobrança

**Versão:** v0.1 (proposta, sem ratificação; pendente como Q21/Q22/Q24)
**Fonte:** [RESPOSTAS.md](RESPOSTAS.md) itens 21, 22, 24; [DECISOES_VIGENTES.md](DECISOES_VIGENTES.md) Q21, Q22, Q24 (todas `PENDENTE`); [TEMPO_ECONOMICO.md](TEMPO_ECONOMICO.md) §5
**Estado:** especificação. Nada aqui é tabela vigente, preço aprovado ou
autorização para cobrar. A tabela vigente até ratificação é a do produto
atual (débito de INK por clusters de grafema na publicação de argumentos);
a tabela Genesis em milliINK abaixo existe somente como proposta a
aprovar — e tabela sem aprovação falha o portão documental.

## 1. Unidade de texto canônico

O objeto precificado é o texto final canônico: normalizado (quebras
convertidas, bordas aparadas), medido em clusters de grafema (UAX #29,
caracteres percebidos, não bytes nem runas) e com hash canônico
registrado. Orçamento, medição e cobrança falam da mesma sequência de
bytes; duas representações do "mesmo texto" com bytes distintos são dois
textos. Rascunho, digitação e edição não são objeto precificável.

## 2. Tabela vigente e tabela proposta

**Vigente (produto atual):** 1 INK por cluster de grafema do conteúdo
publicado, espaços e quebras incluídos, limitado a 3.000 clusters. Vale
até que o titular aprove expressamente a tabela Genesis.

**Proposta Genesis (pendente):** valores em milliINK por faixas de
clusters, com versão de tabela (`tabela vN`), vigência e fórmula de
arredondamento para baixo à subunidade. Enquanto `N` não for aprovado,
a tabela proposta não existe para efeito de cobrança: mantém-se a atual
e qualquer gate que encontre referência a tabela futura sem aprovação
falha. Preço futuro não aprovado nunca herda vigência por omissão.

## 3. Cotação antes do aceite e freeze da intenção

Antes do aceite, o serviço mostra quantidade, preço de referência,
arredondamento e total, calculados sobre o texto canônico final e a
tabela vigente na aceitação (`accepted_at`). A intenção congela esses
termos: texto alterado depois do aceite é nova intenção, com nova
medição e nova cotação — nunca ajuste silencioso da anterior.

## 4. Cobrança só no commit de publicação

Débito e publicação nascem juntos no commit transacional; fora dele não
há cobrança. Cada estado tem exatamente um custo:

| Estado | Custo |
|---|---|
| rascunho (não publicado) | zero |
| texto alterado antes do aceite | zero até aqui; nova medição e nova cotação ao aceitar |
| publicação confirmada | custo único do texto canônico final |
| retry da mesma intenção (mesma chave) | o mesmo custo, uma única vez |
| timeout ou resposta perdida após o commit | efeito original devolvido pela chave de idempotência, sem segunda cobrança |

## 5. Proibições

- Nenhuma cobrança sem tabela vigente aprovada; tabela proposta sem aprovação bloqueia, não vigora.
- Nenhum débito fora do commit de publicação; retry, timeout, digitação e texto alterado não cobram.
- Nenhum arredondamento para cima e nenhuma distribuição acima do orçamento; sobras permanecem no Tesouro.
- Nenhum preço inferido de proposta, exemplo ou rascunho: parâmetro sugerido é sugestão até ratificação expressa.
