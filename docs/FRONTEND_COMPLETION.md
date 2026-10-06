# Conclusão do frontend — direção aprovada em 2026-10-06

**Estado: plano de implementação, não release certificada.** A revisão foi solicitada pelo titular para completar o frontend consumindo todas as rotas aplicáveis do Regnovum. Não aprova parâmetros Q/TEMP, não ativa economia e não modifica regras do Reino.

## Baseline verificado

Main `3f4522b`: backend e preparação da qualidade/release mergeados segundo histórico de PRs; P45-T01–T04 concluídas, sem certificado/tag. Em 2026-10-06, antes desta revisão, nenhum PR ou issue estava aberto. 36 Q continuam PENDENTE; aprovação de produto não deve ser inferida de código implementado.

O contrato `api/openapi.json` descreve 82 operações/70 paths. As declarações dos arquivos `routes.go` somam 100 operações: mais 15 em fragmentos staged (metering 4, commerce 2, disputes 5, seasons 4) e 3 operações administrativas de jobs ainda fora do contrato publicado. Trata-se de inventário estático, não prova de montagem ou de cobertura de UI. P48 reconcilia também montagens dinâmicas e ingressos provider que não estejam nesse levantamento.

O frontend tem clients auth/arenas/positions/arguments, páginas de autenticação/participação e componentes de leitura pontuais. `cmd/arena/main.go` compõe principalmente AccountSurface e ParticipationSurface; declarações/handlers/unit tests de outros módulos não garantem que suas rotas sejam servidas. Por isso, o plano começa pelo contrato e pela composição real, não por redesign visual.

## Entregas planejadas

- **P48:** Inventário executável de rotas e cobertura — 4 microtarefas.
- **P49:** Integração real das superfícies MVP no servidor — 9 microtarefas por família.
- **P50:** Shell profissional, navegação e autenticação — 4 microtarefas.
- **P51:** Conta, perfil, privacidade e segurança — 5 microtarefas.
- **P52:** Descoberta, busca e autoria das Arenas — 5 microtarefas.
- **P53:** Debate, posições, respostas e influência — 5 microtarefas.
- **P54:** Carteira, passes, Member e cobrança — 4 microtarefas.
- **P55:** Moderação, transparência e operação restrita — 4 microtarefas.
- **P56:** Contratos staged e harness real sem ativação — 4 microtarefas.
- **P57:** Temporadas, medição, comércio e disputas staged — 4 microtarefas.
- **P58:** Aceite integral do frontend e acabamento — 5 microtarefas.
- **P59:** Candidato final com frontend e retomada da certificação — 4 microtarefas.

12 fases, 57 microtarefas; sequência P48→P59, uma tarefa/commit, um PR por fase. IDs já entregues permanecem intactos. Próxima tarefa P48-T01; instruções executáveis permanecem locais/ignoradas pelo Git.

## Critério de frontend completo

Toda operação declarada/publicada tem audience, disponibilidade, dono e destino verificados. API browser requer client tipado + página/componente/form + jornada positiva e negativa no servidor real. HTML é link/form servido, não client JSON artificial. Não contar um wrapper isolado, mock ou snapshot como integração.

Probes, endpoints internos, CLI e webhooks de provedor não são consumidos pelo frontend; possuem exclusão justificada e teste de fronteira. Administração recebe painel restrito com autorização/MFA no efeito, sem promoção de admin pelo browser. As 15 operações staged recebem consumo de interface validado por harness real sintético, separado do servidor entregue; indisponibilidade em produção é explícita, não saldo/sucesso fictício.

Não inventar endpoints para Genesis, fechamento de temporada, transferência/escrow comercial, decretos, produtos diferidos ou edição de perfil que hoje só tem leitura. Uma nova rota aplicável entra automaticamente na cobertura: o número 100 é fotografia inicial, não meta que permita esconder rotas novas.

Stack preservada: TypeScript 7 pinado → ESM, HTML/CSS nativos, Custom Elements desacoplados; nenhum runtime/framework/bundler novo. Reusar core HTTP, i18n e primitives/componentes. Datas/quantias/custos/direitos vêm do servidor; não float financeiro, segredo em storage, HTML inseguro ou cálculo de autoridade no client.

Cada jornada cobre carregando/vazio/erro/desabilitado/sucesso confirmado, timeout/abort/stale request, duplo envio, 401/403/409 e negativas pertinentes, teclado/foco e mobile. pt-BR/en-US, pseudo-locale, CSS lógico, conteúdo não traduzido automaticamente, privacidade/no-store e budgets existentes permanecem obrigatórios.

## Validação, integração e release

Testes direcionados por tarefa, PG real quando necessário, geração sem drift e `make quick-verify` + CI rápido no PR. Não executar matriz integral por microtarefa/fase comum, nem adiar falha direcionada conhecida. Fases P48–P59 devem estar mergeadas antes da matriz final; P45-preparação não é refeita, seu gate é retomado no SHA final atualizado após P59.

A release ainda exige ratificação das decisões, parecer independente real, aprovação jurídica/tributária por país, revisão editorial en-US e matriz completa em dois ambientes limpos independentes. P59 atualiza inventário/manifesto, depois congela-se o SHA e reutiliza-se o procedimento final de P45. Mudança de SHA invalida evidência anterior. Nenhuma tag, deploy ou ativação monetária automática. Código integrado, frontend implementado, produto certificado e produto ativado são estados diferentes.
