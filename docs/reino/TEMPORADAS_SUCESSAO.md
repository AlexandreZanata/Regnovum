# Adendo — temporadas de 90 dias e sucessão econômica

**Versão:** v0.1, ajuste solicitado pelo titular em 2026-09-30. **Estado:** lógica futura; não altera saldos, contratos ou permissões em produção. Complementa a [Carta Econômica](CARTA_ECONOMICA.md), as [Diretrizes](DIRETRIZES.md) e o [contrato do ledger](LEDGER_CONTRACT.md). Em matéria de temporada e titularidade do cargo real, este adendo substitui a interpretação de Genesis global perpétuo e de Rei imutável.

## 1. Decisões expressas e desenho recomendado

As decisões expressas desta solicitação são: **TEMP-01**, temporada de 90 dias; **TEMP-02**, reinício econômico no estado inicial; **TEMP-03**, preservação dos dados e destaques históricos, incluindo a pessoa mais rica; **TEMP-04**, sucessão automática quando um participante ultrapassa a riqueza da Coroa. Fonte: solicitação do titular nesta data; aprovação da intenção de produto, sem autorização de lançamento. As propostas Q01–Q36 mantêm seus estados de ratificação anteriores.

As definições operacionais abaixo são o desenho recomendado para executar essas decisões sem apagar direitos ou permitir tomada das credenciais da plataforma. Antes de implementação financeira ativa, registrar em adendo de ratificação o aceite dos critérios de riqueza, desempate, titular inicial e contrato de expiração; não usar este pedido para aprovar implicitamente números pendentes de Q20/Q21/Q23 ou os produtos excluídos.

## 2. Propriedade, escassez e poder dentro do jogo

Regnovum continua sendo **um único Reino**. Temporadas são ciclos sucessivos desse domínio, não reinos paralelos ou uma conversão dos debates em competição financeira. "Destruir o Reino" encerra seu estado econômico/político sazonal; não apaga contas, conteúdo ou fatos históricos.

Adesão voluntária e contratos publicados antes do uso orientam a propriedade **por prazo definido**: o participante adquire INK de uma temporada identificada e sabe quando sua utilidade acaba. O feudalismo inspira o cargo real, a corte e os livros; a autoridade deriva da Carta aceita. A inspiração Bitcoin é oferta limitada, unidades inteiras e contabilidade verificável **dentro de cada temporada**. Reinício periódico não é uma propriedade do Bitcoin: INK não é BTC, não promete seu lastro, resgate ou escassez global perpétua.

O **operador** mantém as responsabilidades técnicas, contratuais e de segurança do serviço. O **Rei sazonal** é um participante investido em função de jogo. Ele não recebe banco, chave privada de provedor, admin global, dados pessoais, poder sobre ativos externos ou capacidade de alterar as próprias regras de sucessão. O Tesouro é patrimônio institucional da Coroa; seu titular econômico não muda para a pessoa que ocupa o trono.

## 3. Calendário, identidade e versões

Cada temporada tem `season_id` imutável, ordinal, `starts_at`, `ends_at`, versão da Carta, política econômica e hash do manifesto inicial. Duração exata: `90 × 24 × 60 × 60 = 7.776.000 segundos`, em UTC; intervalo `[starts_at, ends_at)`. Não usar três meses civis nem relógio do browser. O fim coincide com o início **programado** da sucessora; indisponibilidade operacional não prorroga o prazo econômico.

Máquina de estados: `PREPARED → ACTIVE → CLOSING → SEALED → ARCHIVED`. Freeze de segurança é controle separado, não licença para estender prazo. Há no máximo uma temporada econômica `ACTIVE`. Se o fechamento falhar, o serviço mantém leitura/histórico e suspende novas mutações; a temporada seguinte não fica ativa até o selo válido da anterior. Seu prazo programado continua visível; não simular temporadas intermediárias vazias nem encurtar/estender o calendário por recuperação silenciosa.

O manifesto é publicado antes da abertura. O Rei sazonal não altera duração, oferta, riqueza elegível, desempate ou termos já aceitos durante a temporada. Uma nova política exige versão prospectiva e aceite onde necessário; não reescreve contratos em curso.

## 4. Oferta fixa por temporada, sem continuidade monetária entre elas

`INK@season_id` é a identidade completa do ativo. Cada temporada possui **um** Genesis de oferta `S` ratificada, inicialmente integral no Tesouro; contas de participantes começam em zero, partições usam a política inicial aprovada. As propostas atuais de S e milliINK continuam pendentes até sua ratificação específica.

Em cada snapshot confirmado do livro de uma temporada: `Σ custódias(season_id) = S(season_id)`. Após seu Genesis, transferências têm soma zero; não há mint/burn, negativo, lançamento editado ou segunda Genesis nessa temporada. Um novo livro pode ter seu próprio Genesis; isso não autoriza adicionar S ao livro antigo. Histórico de várias ofertas não é oferta gastável acumulada.

No encerramento, os saldos finais são **preservados e tornam-se não gastáveis**. Não zerar linhas, confiscar para simular reinício, apagar diário ou converter saldo antigo em saldo novo. Nova temporada começa sem riqueza herdada, yield, dívida interna transportada ou multiplicador de vencedor. Prestígio histórico não cria INK nem privilégio econômico automático. Conta, conteúdo publicado, recibos, provas e identidade continuam existentes conforme a política de dados.

Todas as custódias, intenções, quotes, holds, contratos econômicos, recibos e chaves idempotentes carregam `season_id`. Origem e destino devem pertencer à mesma temporada. A mesma string de idempotência em outra temporada não pode relançar ou redirecionar uma intenção anterior: o recibo original continua preso ao livro original.

## 5. Fechamento, obrigações e dinheiro externo

1. Ao atingir `ends_at`, impedir novas admissões econômicas e revogar a autoridade real sazonal para novas intervenções. O banco ordena a barreira com transações já admitidas, por geração e registro durável; não inferir ordem de commit de um sequence ou timestamp. Operação admitida antes do limite pode concluir no livro antigo antes do selo, dentro do timeout transacional publicado, sem esperar rede/provedor sob lock. Requisição no limite é recusada.
2. Capturar `cutoff_revision` e o snapshot da competição após drenar as operações admitidas. Conservar o fato de uma execução tardia já admitida; não deslocá-la para outra temporada.
3. Cancelar intenções não liquidadas, liberar reservas e encerrar escrows pela **cláusula terminal aceita**. Contrato interno não pode exigir prestação ou saldo INK depois do fim sem política explícita anterior; condição indeterminada, litígio ou dever não resolvido bloqueia o selo, não autoriza distribuir o dinheiro a terceiros. Liquidação autorizada em `CLOSING` não participa da disputa pelo trono nem do ranking de corte.
4. Conciliar diário, projeções, estoque, terceiros, pagamentos externos e checkpoints. Selar o arquivo com hashes e evidências; só então permitir a abertura da sucessora por comando idempotente autorizado. Crash/replay não podem produzir dois livros ativos, dois Geneses ou duas liberações de escrow.

Compra fiat tem temporada, consentimento, validade e prazo de liquidação limitado por `ends_at`. O webhook atrasado de uma compra não entregue segue reembolso/reconciliação no provedor: **não** concede INK na nova temporada nem reabre gasto na antiga. Refund/chargeback posterior ao selo é incidente/obrigação externa vinculada ao recibo original, com registro corretivo separado; nunca mint na temporada atual ou subtração de terceiro inocente. Não prometer que um reset apaga dívida em fiat, dever do operador ou obrigação legal.

Direitos anteriores que não tinham expiração sazonal — INK legado comprado, passes, períodos Member pagos — conservam seu contrato. Opt-in para uso sazonal mostra o fim e consome o direito convertido **uma única vez**, sem nova conversão grátis em cada reset. Serviços de assinatura não econômicos podem atravessar o calendário; benefícios INK precisam de política sazonal aceita. Sem termos ratificados de venda/expiração/reembolso, checkout sazonal permanece bloqueado; não criar uma percentagem de reembolso ou uma janela de vendas por inferência.

### 5.1. TEMP-06 — cláusula terminal e desbloqueio de P46-T07

**Decisão de implementação (2026-09-30):** a pedido do titular para desbloquear P46-T07, este contrato fica definido para desenvolvimento/testes inertes. TEMP-06 passa de BLOQUEADO a PLANEJADO nesse escopo. Não é certificação ou autorização de lançamento, não ratifica outros TEMP/Q e não altera contratos já aceitos. Aplicação a participantes exige termos publicados/aceitos antes do financiamento e gate de release.

**Referente:** escrow financeiro do `TradeContract` em `internal/commerce`. `charter` oferece Carta/aceites de P46-T05, não o contrato comercial. `disputes` oferece procedimento/decisão de P39, não um segundo ledger/escrow persistido. Aceitar a Carta não aceita automaticamente contrato comercial.

**Termos obrigatórios:** temporada, comprador, prestador, principal inteiro, vencimento até `ends_at`, versão/hash da política terminal e evidência de aceite de ambas as partes antes do financiamento. Persistir/vincular à intenção e livro. Ausência/divergência impede novo financiamento sazonal; migration não completa aceites antigos. Não admitir nova prestação INK exigível depois do fim.

**Classificação após barreira e drenagem de P46-T09:**

- `released`, `refunded`, `resolved`: preservar recibo, sem novo efeito terminal. Refund posterior autorizado é evento vinculado separado, não reabertura do escrow.
- `accepted`: pagar ao prestador somente com aceite válido do comprador admitido antes do cutoff e ausência comprovada de litígio/impedimento. Usar Dízimo/arredondamento vigentes, sem criar alíquota.
- `funded`, sem entrega aceita e comprovadamente incontroverso: a cláusula previamente aceita cancela obrigação sazonal e devolve principal ainda retido ao comprador. Depósito/devolução de escrow não é prestação liquidada e não cria Dízimo.
- `expired`: executar somente resolução competente, final e persistida que determine release/refund. Vencimento sozinho não escolhe beneficiário; sem resolução final, bloquear.
- Litígio/recurso tempestivo pendente, decisão conflitante/não final, titularidade incerta, termos/aceites ausentes ou integridade divergente: preservar custódia e retornar `BLOCKED`. Estado desconhecido/fonte indisponível também bloqueia; ausência de dados não prova ausência de litígio.

**Contrato indecidível** é essa categoria BLOCKED: falta evidência final/verificável para escolher destinação. Não é julgamento por IA. Resolver pelo procedimento competente de P39 ou acordo válido das partes, nunca discricionariedade do Rei/worker. T07 entrega classificação/resultado bloqueante; T09 impede selo enquanto houver BLOCKED; T10 não abre sucessora sem selo.

Pagamento/devolução usa somente saldo efetivamente retido no livro original, sem duplicar parcelas liquidadas. Não adicionar liquidação parcial se o modelo não a suporta: divergência principal/custódia/recibos bloqueia. Sem carry-over, mint, confisco para Coroa, eliminação de litígio ou cancelamento de obrigação externa. Contratos legados sem esta cláusula conservam regras e não são terminalizados automaticamente.

**Dois liberadores** = duas execuções autorizadas concorrentes no mesmo escrow, por exemplo dois `ReleaseContractUseCase`; não significa duas assinaturas de aprovação. T09 inclui dois workers de fechamento. Decisão terminal/legs são uma transação, com lock/CAS e unicidade terminal por escrow/temporada compartilhada entre pagamento/devolução. Replay idêntico retorna recibo; payload/destinação conflitante é recusado. Chaves diferentes de release/refund não permitem dois efeitos. Crash antes do commit = zero efeito; depois = um recuperável por replay.

**Limite de T07:** adaptar admissão sazonal, consumidores, termos/classificação e preservar atomicidade/idempotência existentes. Worker, barreira global, terminalização em CLOSING, selo, sucessora e suas provas pertencem a T09/T10. Não criar tribunal novo nem ativar produto.

## 6. Migalhas, fluxo e estado que reinicia

Proposta recomendada: uma elegibilidade de Migalhas **por pessoa por temporada**, incluindo contas preexistentes que ingressem nela; nunca exigir trabalho/publicação. Isso altera o alcance da proposta Q25 e precisa de aceite explícito antes de ativação. Sinais antifraude e sanções de segurança continuam globais: criar conta ou mudar temporada não remove impedimento.

R4, IRR, distribuição, estoque, empenhos, Patentes, corte e cargos sazonais têm nova dimensão de temporada. R4 só usa semanas ISO completas e seladas **inteiramente contidas** na temporada; nenhuma semana anterior alimenta a seguinte. Sem história completa aplicável, usar a política ratificada de histórico insuficiente. Semana final parcial não ganha uma distribuição extra por ocasião do reset. Sobra permanece no livro encerrado; não vira doação ao livro novo.

A sanção de segurança, obrigação externa, retenção de prova e recurso não desaparecem no reinício. Cargos/status sazonais expiram; reputação e fatos auditáveis anteriores permanecem separados por temporada. Vencedor anterior recebe reconhecimento histórico, sem poder ou saldo atual implícito.

## 7. O que significa ter mais dinheiro que a Coroa

Comparar INK da **mesma temporada**, em milliINK inteiro, confirmado e sem preço de mercado. Proposta de política `wealth-v1`:

`A(b) = soma do INK liquidado de propriedade econômica incondicional do beneficiário b`.

`L(b) = obrigações monetárias registradas e gravames ainda não descontados de A(b)`.

`W(b) = max(0, A(b) − L(b))`, com aritmética verificada, custódia/beneficiário únicos e sem dupla dedução.

Custódia de terceiro, principal emprestado com obrigação equivalente, escrow de propriedade condicional, pagamento não confirmado, crédito futuro, fiat/BTC, Patente, reputação e riqueza histórica não são riqueza elegível. Reserva do próprio beneficiário sem dívida **não** muda propriedade: a Coroa não reduz W movendo saldo entre Reserva Soberana, Estoque Comercial ou Caixa Operacional. Recursos de terceiros ficam excluídos de A, não também subtraídos de L. Empréstimos/dívidas externos desconhecidos e colusão fora do serviço não são detectáveis por essa fórmula; não prometer que o algoritmo os elimina.

`C = W(COROA institucional)` e `P(u) = W(participante u)`. Coroa, cofres e contas técnicas não concorrem como cidadãos. Todas as carteiras pessoais do mesmo participante são agregadas uma vez. O Rei atual não adiciona C ao próprio P. Reclassificação de bolso ou mudança de ocupante não reduz o limiar artificialmente.

## 8. Sucessão automática e desempate

Pré-condições: temporada `ACTIVE` e instante do banco dentro de `[starts_at, ends_at)`, controles Q0 verdes, fonte econômica reconciliada, candidato vivo/habilitado para o jogo, Carta e termos da função aceitos, MFA configurado. Worker ausente não prorroga poderes após o limite. Não excluir por simples denúncia ou decisão interessada do Rei. Conta inelegível pode conservar patrimônio/histórico sem receber prerrogativa.

Após um evento econômico confirmado que altere patrimônio/obrigações/elegibilidade, o sistema avalia automaticamente o snapshot correspondente. **Somente `P(u) > C`** habilita uma tomada; igualdade não basta. Entre candidatos elegíveis, vence o maior P. Empate conserva o Rei atual se estiver entre os líderes; sem incumbente empatado, ganha quem alcançou o valor atual primeiro em revisão econômica confirmada, depois ID interno como desempate final. Gastar e voltar àquele valor registra nova revisão de alcance, sem reutilizar uma antiguidade perdida. Nenhum sorteio, voto, taxa de coroação ou aprovação discricionária do perdedor.

Se ninguém supera C, o Rei atual permanece até expiração, renúncia ou impedimento. Cair abaixo de C não causa vacância sozinho. Outro candidato só substitui um Rei conquistado se superar C e superar P do incumbente elegível; igualdade conserva incumbência. No início, o Rei vem do `initial_monarch_account_id` previamente publicado; recomenda-se o fundador habilitado, não herança automática do campeão anterior. Ausência/impedimento usa regência técnica **limitada**, sem autorizar um admin a fingir que venceu o algoritmo.

Cada sucessão registra temporada, revisão econômica, política, valores P/C, antecessor, sucessor, motivo e novo `reign_version`. Registro e revogação/concessão dos poderes são atômicos. O algoritmo pode rodar por outbox durável após commit, sem intervenção humana; enquanto houver revisão relevante ainda não aplicada, novas ações reais aguardam o avaliador. Nunca autorizar ato com snapshot stale. Prazo de processamento será medido e fixado antes de lançamento, não uma promessa de tempo instantâneo.

Uma credencial/sessão do ex-Rei não continua real porque ainda autentica a conta. Cada ato revalida `(season_id, reign_version, competência)` no efeito; aprovações pendentes de outro reinado precisam de nova validação. Sucessão não transfere o Tesouro à carteira vencedora, não confisca o perdedor, não apaga obrigações e não desfaz ato final válido.

## 9. Poder conquistado, incentivos e possibilidade de vitória

O Rei dirige apenas funções de jogo e orçamento institucional definidos previamente pela Carta, com auditabilidade e dupla conferência onde exigida. Não pode cunhar, alterar esta regra, antecipar/adiar reset, se conceder estoque, impedir rival por riqueza ou decidir caso em que tem interesse. Ato de benefício próprio, perdão/cargo econômico para contornar impedimento ou apropriação de custódia alheia é recusado; reparação legítima exige procedimento independente.

A possibilidade é **rara pelo estado econômico**, não por aleatoriedade secreta. Inicialmente C=S e P=0. Ela surge se circulação/atividade concentrarem patrimônio legítimo em alguém acima da Coroa. Simulação deve verificar se venda, custos, Dízimo e Migalhas tornam a vitória possível; não prometer uma probabilidade pequena sem dados nem reduzir estoque público ocultamente para fabricar uma coroação. Compra legítima de INK pode contribuir para riqueza e isso deve ser comunicado: o jogo não promete uma competição imune a poder de compra.

Exemplo com a oferta sugerida: Coroa `700 milhões`, Ana `800 milhões`, demais participantes `600 milhões` de INK, soma `2,1 bilhões`, sem obrigações. Ana supera a Coroa e sucede automaticamente. Depois do ato, Ana continua com 800 milhões e a Coroa com 700 milhões. Se Ana tivesse 700 milhões, a igualdade não lhe daria o trono. Valores são exemplo, não novos parâmetros aprovados.

## 10. Memória da temporada e privacidade

No corte, gravar classificação de riqueza P, pessoa mais rica, último Rei, sequência de reinados/duração, métricas econômicas, versão das regras e `cutoff_revision`. **Pessoa mais rica e último Rei são fatos distintos**; um prêmio não afirma o outro. Valores condicionais e operações de fechamento não fabricam riqueza competitiva. Se houver empate, conservar a lista empatada; desempate técnico só determina um destaque principal, sem inventar superioridade financeira.

Arquivo público exibe temporada, pseudônimo/alias permitido, colocações, reinados e agregados; saldos pessoais exatos dependem da política de publicação aceita. Íntegra financeira fica com titular e auditor autorizado. Contas apagadas/anonimizadas mantêm fato histórico sob alias não identificável quando houver base de retenção. Hash não substitui controle de acesso nem impede descarte de PII.

Fraude/erro comprovado após o corte produz **edição histórica corretiva vinculada**, com motivo, autor e snapshot original preservado onde lícito; não reedita o ledger selado nem concede saldo/poder na temporada corrente. Ranking provisório não é certificado como final antes da reconciliação.

## 11. Invariantes para requisitos e testes

- **TEMP-05:** um Genesis por temporada; soma S individual, zero transferência entre temporadas e zero carry-over.
- **TEMP-06:** corte `[início,fim)`, barreira de transações, fechamento recuperável e no máximo uma temporada ativa.
- **TEMP-07:** expiração aceita antes da aquisição; legados e obrigações externas preservados; evento tardio não concede INK atual.
- **TEMP-08:** cada valor contado uma vez por propriedade econômica; dívidas/terceiros não dão trono; bolsas institucionais não mudam limiar por reclassificação.
- **TEMP-09:** sucessão determinística `P>C`, empate conservador, uma autoridade vigente e nenhum veto do perdedor.
- **TEMP-10:** versão de reinado/temporada no efeito; ex-Rei, worker atrasado e decreto não bypassam os controles.
- **TEMP-11:** ranking de corte e histórico corrigível sem apagar fatos, sem saldo/poder herdado ou vazamento pessoal.
- **TEMP-12:** nova temporada não remove sanção de segurança, fraude, direito de defesa/export ou produto deliberadamente desativado.

Mapear esses IDs a requisitos, ameaças e testes nas fases locais de adaptação; eles não substituem as decisões Q01–Q36 pendentes. A execução integral da certificação permanece na fase final de release, após todas as fases e ajustes planejados concluídos.
