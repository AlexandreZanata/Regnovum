# Carta Econômica do Regnovum — base funcional

**Fonte:** *Constituição Monetária do Reino — v1.0*, anexada pelo titular em 2026-09-23 antes da renomeação do produto para **Regnovum**.

**Estado:** direção monetária do Reino, documentada como lógica de negócio. Não é o funcionamento atualmente implantado nem uma oferta pública de investimento, aposta, resgate em Bitcoin ou moeda conversível. As fórmulas marcadas como **sugestão** no anexo continuam propostas. Operações com direitos financeiros ou risco regulatório dependem de definições e análise especializadas antes de qualquer oferta.

## 1. Constituição monetária

O INK passa a ser concebido pela nova Carta como **unidade econômica interna oficial do Reino**. Isso substitui, como direção futura de produto, a concepção atual de INK exclusivamente como capacidade de publicação. A mudança não converte automaticamente o saldo de ninguém: o produto em operação continua regido por [MONETIZATION.md](../MONETIZATION.md) e seus contratos até que a compatibilização seja decidida e comunicada.

Os quatro princípios constitucionais da Carta são:

1. O único evento de criação é o **Genesis de 2.100.000.000 INK**; após ele não há emissão adicional.
2. Nenhum INK é queimado ou desaparece por uso, expiração, falha ou sanção; ele muda de custódia ou titularidade.
3. Cada unidade pertence a um titular ou a um cofre definido do Reino, e é contabilizada **uma só vez**.
4. Pagamento por serviço próprio da Arena transfere INK do pagador ao Tesouro Real, de onde poderá circular novamente.

Genesis é um **evento futuro definido pela Carta**, não algo que esta documentação declara já executado. O Rei tem prerrogativa sobre a economia interna, mas não pode transferir valor que não esteja disponível em conta de origem legítima. Não há comando real de mint ou burn após Genesis.

## 2. Invariante e custódia

Em qualquer estado válido depois de Genesis:

`Σ saldos de todas as contas de custódia mutuamente exclusivas = 2.100.000.000 INK`.

As contas de custódia incluem Tesouro, contas de usuários, escrows/cauções, contratos, apostas se forem autorizadas, títulos e outros cofres. **Reserva Soberana, Estoque Comercial, Caixa Operacional e Obrigações** são classificações internas do Tesouro; não se somam novamente a ele. Uma obrigação contratual é uma reivindicação sobre INK, não INK adicional. Principal de Título bloqueado é contado no cofre uma vez e continua devido ao titular; não pode ser contado também no saldo disponível do usuário ou do Tesouro.

Se a reconciliação encontrar soma diferente da oferta Genesis, movimentações econômicas devem ser suspensas até investigação e correção auditada. Uma correção precisa ser uma transferência rastreável entre origens e destinos existentes, nunca criação contábil para “fechar” a soma.

Todo movimento tem origem, destino, montante, tipo, causa, instante e identidade única de transação. Débito e crédito são inseparáveis: ambos ocorrem, ou nenhum. Repetir uma confirmação de compra/publicação não pode produzir segunda transferência. Saldo é projeção do Livro Razão Real, não valor editado diretamente. Um decreto segue a mesma regra de origem e destino.

## 3. Referência de preço e dinheiro externo

A Carta fixa a **regra de referência de venda pela Coroa**: `1 INK = preço de referência de 1 satoshi`. Para cotação BTC/BRL, o preço teórico em reais é `BTC_BRL / 100.000.000` por INK. A razão de referência de um sat por INK não é promessa de entregar Bitcoin, lastro em BTC, paridade de mercado, direito de resgate ou conversão reversa. O valor em reais varia com a cotação, e a compra precisa mostrar valor final, impostos, taxas e quantidade de INK antes do aceite.

Há dois livros distintos:

- **Externo:** pagamento fiat, estorno, imposto, despesa e receita da empresa.
- **Interno:** transferência de INK existente entre Tesouro, usuário e cofres.

Uma venda só pode transferir INK que esteja no **Estoque Vendável** do Tesouro. O recebimento de dinheiro externo não altera a oferta Genesis. Cobrança externa sem transferência interna concluída, ou transferência interna sem confirmação válida de cobrança, exige resolução explícita; não se presume “compra completa” apenas porque um dos livros mudou.

A Carta propõe `EstoqueVendável = Tesouro - ReservaSoberana - Obrigações`, limitado ao INK efetivamente livre e disponibilizado para venda. Valores bloqueados em títulos, escrows ou contratos não podem ser vendidos. Se o estoque disponível for zero, a venda é interrompida; não há emissão para suprir a falta. A **Reserva Soberana de 20%** e o limite de até **80% para operações ordinárias** são sugestões do anexo, não parâmetros homologados.

## 4. Fluxos econômicos do Reino

O modelo distingue quatro trajetórias, sempre conservando a oferta:

1. **Coroa → cidadão:** venda de INK existente, Migalhas, subsídio, prêmio, presente real ou rendimento pago do Tesouro.
2. **Cidadão → Coroa:** escrita, publicação, taxa institucional, Patente de Nobreza ou outra prestação do próprio Reino; o INK retorna integralmente ao Tesouro quando a regra assim definir.
3. **Cidadão → cidadão:** pagamento por serviço ou transferência pessoal. Comércio formal através da infraestrutura do Reino sujeita-se ao Dízimo; simples transferência pessoal pode ter outro tratamento.
4. **Cidadão → cofre → cidadão/destinatário devido:** caução, contrato, garantia, Título ou eventual aposta; bloqueio não é gasto nem propriedade livre da Coroa.

O **Dízimo Real de 10%** é a regra declarada pela Carta para pagamento econômico formal entre cidadãos. Exemplo: de 1.000 INK pagos por serviço, 900 chegam ao prestador e 100 ao Tesouro. O escopo exato de incidência, arredondamento, reembolso e prevenção de disfarce de comércio como “transferência pessoal” ainda precisam de definição. Transferência pessoal gratuita é apenas possibilidade descrita no anexo, não autorização operacional atual.

**Taxa** e **caução** não se confundem. Taxa consumida vai ao Tesouro; caução é bloqueada em cofre e retorna ou é destinada conforme condição aceita/decidida. Em ambos os casos, cada transferência deve preservar o Livro Razão. A Carta define três formas de retorno à Coroa: serviço próprio integral, Dízimo sobre comércio e retorno patrimonial especial (por exemplo, confisco ou saldo residual de conta morta). Essa classificação impede somar o mesmo fluxo duas vezes nas métricas.

## 5. Escrita e serviços próprios

Escrever gasta INK **somente na publicação confirmada**, não por tecla, rascunho, texto apagado ou tentativa falha. O preço deve ser mostrado antes do aceite. Publicação e transferência ao Tesouro são uma única operação lógica; falha em publicar não cobra, e falha em cobrar não publica como se tivesse sido paga.

O anexo **sugere**, mas não aprova como tabela final, fórmulas como `1 + ceil(caracteres/500)` para comentário, `3 + ceil(caracteres/500)` para postagem e `5 + ceil(caracteres/400)` para argumento/resposta formal. Também sugere taxas para desafio, mediação, disputa, mercado, torneio e Feudo. Essas sugestões não substituem o preço atual de `1 INK por grapheme cluster` do argumento. É necessário decidir qual é a unidade contada, quais tipos de conteúdo existirão e se as franquias e Arena Passes atuais continuarão.

## 6. Migalhas da Coroa

Migalhas são distribuição inicial de INK **já existente no Tesouro** a novos Camponeses elegíveis. A Carta as separa de remuneração por tarefa: posts, likes, horas online, sequência diária e publicidade assistida **não** são condição de recebimento. A proposta é uma época semanal para todos os novos elegíveis da mesma semana.

As fórmulas transcritas da Carta são:

- `R4 = mediana do refluxo regular das quatro semanas anteriores`;
- `T = Tesouro Livre`;
- `M = min(0,02% × T, 0,005% × T + 5% × R4)` — orçamento semanal;
- `Migalha individual = min(100 INK, M / N)`, com `N` novos elegíveis.

O gasto total nunca pode superar `M` ou o INK livre do Tesouro. Parte não distribuída permanece nele. Eventos extraordinários, como confisco e morte de conta, são **sugeridos** para exclusão do refluxo regular, para não elevar artificialmente distribuições futuras.

**Lacuna aritmética expressa:** o ledger atual usa INK inteiro, mas exemplos da Carta resultam em `0,5` e `0,05 INK` por pessoa. Sem subunidade definida, esses pagamentos são impossíveis. Não se deve arredondar para cima, produzir INK fracionário oculto nem excluir elegíveis por omissão. A regra de divisibilidade, arredondamento, sobras e `N=0` precisa de decisão antes de considerar a fórmula executável.

## 7. Títulos da Coroa e Patentes de Nobreza

**Títulos da Coroa** são proposta de bloqueio voluntário de INK com principal a devolver no vencimento e participação variável em receitas futuras. O principal permanece em cofre e não pode financiar gasto ordinário do Rei. O rendimento, se houver, sairia do Tesouro, nunca de emissão; pode ser zero. A Carta traz `Y = min(5% × R4, limite_de_rendimento)` e divisão proporcional ao principal ponderado pelo prazo. `limite_de_rendimento`, disponibilidade do Tesouro, arredondamento, resgate antecipado, inadimplência e direitos em caso de morte de conta não estão definidos. Prazos/pesos e teto de 20% dos INKs economicamente ativos são **sugestões**. Nenhum Título deve ser tratado como disponível ou vendido enquanto essas condições e sua classificação jurídica não forem resolvidas.

**Patentes de Nobreza** (Barão a Duque) são status sociais, não Títulos financeiros. A compra transfere o preço ao Tesouro; a Patente não concede voto, verdade, reputação, poder de Árbitro ou autoridade sobre terceiros. Preços, quantidades máximas por população, leilões e privilégios visuais são **sugestões**. Se a conta morrer, o anexo prevê retorno da Patente à Coroa; o destino de INK pago e eventual disputa de compra precisa ser comunicado nos termos aplicáveis.

## 8. Contas mortas, atos reais e terceiros

O anexo destina ao Tesouro o saldo **residual** de conta morta, após liquidação de contratos, cauções, escrows e obrigações. Isso não autoriza confiscar imediatamente saldos bloqueados de terceiros, principal de Títulos ou INK comprado sob promessas vigentes. Ordem de liquidação, recurso, reversão por Benção Real, créditos externos e direitos do titular permanecem perguntas abertas.

O Rei pode vender, distribuir, cobrar, subsidiar ou confiscar **INK existente** por ato identificado, mantendo origem, destino e motivo. Poder real não supera a conservação monetária nem transforma obrigação de terceiro em patrimônio livre do Tesouro. Um decreto econômico deve deixar explícito se usa Tesouro Livre, transferência de outro titular, liberação de cofre ou compensação, e seguir a [diretriz de decretos](DECRETOS_REAIS.md).

## 9. Transparência e métricas

O painel econômico proposto mostra oferta fixa, Tesouro, INK em circulação, saldos bloqueados por categoria, vendas, Dízimo, Migalhas e refluxo sem expor saldos individuais ou dados de pagamento. Cada métrica deve ser derivada do mesmo Livro Razão e não contar subcontas do Tesouro duas vezes.

O **Índice de Refluxo Real** do anexo é `INK retornado ao Tesouro / INK colocado em circulação pelo Tesouro` no período. Não se presume limite de 100%: o mesmo INK pode circular e retornar várias vezes. A Carta ainda não define se a contagem é fluxo bruto ou líquido, qual janela, como tratar devoluções nem o caso de denominador zero. “Velocidade do INK” é número de mudanças de titularidade por período; movimentos entre cofres do mesmo beneficiário não devem ser chamados automaticamente de circulação econômica.

## 10. Compatibilidade e limites desta documentação

O sistema atual documenta **franquias mensais de 5.000/30.000 INK**, compra de pacotes com preços fiat fixos, Arena Pass e INK não transferível/não apostável. A Carta nova determina oferta fixa, venda apenas de estoque existente, referência em sat e circulação econômica. Essas duas semânticas **não podem ser misturadas silenciosamente**. Em particular, concessão mensal sem débito do Tesouro violaria a oferta fixa após Genesis; franquia não utilizada não poderia simplesmente desaparecer na expiração; compra de pacote com preço fixo pode divergir da regra de referência em sat. Saldos já pagos e benefícios contratados exigem tratamento explícito antes de qualquer transição. Nenhum saldo existente foi migrado por este documento.

A Carta menciona apostas, Títulos com rendimento, transferências e preço referenciado a BTC. Essas descrições **não** concluem que tais produtos podem ser ofertados em todos os mercados. A [CVM explica que o enquadramento de ativos e contratos depende de sua função](https://www.gov.br/cvm/pt-br/acesso-a-informacao-cvm/perguntas-frequentes-da-cvm/criptoativos-quando-se-aplicam-as); a [Secretaria de Prêmios e Apostas descreve autorização para apostas de quota fixa no Brasil](https://www.gov.br/fazenda/pt-br/composicao/orgaos/secretaria-de-premios-e-apostas/apostas-de-quota-fixa). Isso não classifica juridicamente o INK, os Títulos ou um mercado específico do Regnovum. Requer análise especializada por produto e país antes de oferta pública; o fato de não haver saque ou lastro em BTC não basta, sozinho, para dispensá-la.

As lacunas materiais e aritméticas estão reunidas em [QUESTOES_ABERTAS.md](QUESTOES_ABERTAS.md). Esta Carta documenta **lógica de negócio**, sem plano de implementação, alteração de código ou mudança de contrato em produção.
