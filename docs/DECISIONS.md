# Log de decisões de produto

Este arquivo registra decisões que alteram comportamento ou incentivos. Cada entrada deve manter contexto, decisão, consequência e condição de revisão.

## PD-001 — Métrica central factual

**Data:** 2026-09-16

**Status:** aceita

**Contexto:** likes e seguidores premiam popularidade, não contribuição para reflexão.

**Decisão:** tratar mudanças declaradas e atribuições a argumentos como resultado central; não criar score universal.

**Consequência:** a métrica depende de eventos raros e exige defesa contra reciprocidade e contas múltiplas.

**Revisar quando:** o piloto mostrar frequência e qualidade reais das atribuições.

## PD-002 — Escolha anônima não entra no agregado

**Data:** 2026-09-16

**Status:** aceita

**Contexto:** o visitante deve experimentar o produto sem login, mas votos anônimos são baratos de manipular.

**Decisão:** escolha local libera resultado, porém só posição confirmada por conta elegível integra o agregado oficial.

**Consequência:** haverá uma etapa adicional ao cadastrar e diferença entre experiência local e dado público.

**Revisar quando:** testes mostrarem abandono excessivo ou outra forma confiável de elegibilidade.

## PD-003 — Argumentos imutáveis no MVP

**Data:** 2026-09-16

**Status:** aceita

**Contexto:** edições silenciosas quebram atribuições, respostas e hashes; histórico de versões aumenta escopo.

**Decisão:** oferecer prévia e tornar argumento imutável após publicação. Autor pode retirá-lo da exibição.

**Consequência:** erros exigem retirada e nova publicação; é necessário comunicar isso com clareza.

**Revisar quando:** erros de boa-fé produzirem fricção relevante e houver capacidade de implementar revisões públicas.

## PD-004 — Ordenação temporal como padrão

**Data:** 2026-09-16

**Status:** aceita

**Contexto:** ordenar por persuasão desde o início cria vantagem cumulativa e incentiva manipulação.

**Decisão:** separar argumentos por relação com a afirmação e usar ordem temporal explícita no MVP. Persuasão é dado e filtro, não ranking padrão.

**Consequência:** argumentos excelentes podem exigir descoberta manual.

**Revisar quando:** volume por Arena tornar navegação temporal insuficiente.

## PD-005 — Member substitui a franquia Free

**Data:** 2026-09-16

**Status:** hipótese adotada

**Contexto:** “30.000 INK/mês” é ambíguo sobre somar ou substituir 5.000 Free.

**Decisão:** Member oferece 30.000 INK totais por período, não 35.000.

**Consequência:** comunicação e histórico de saldo devem deixar a regra evidente.

**Revisar quando:** houver teste de preço e compreensão.

## PD-006 — Lançamento por comunidade controlada

**Data:** 2026-09-16

**Status:** aceita

**Contexto:** abrir todos os temas e cadastros antes de aprender cria risco desproporcional de moderação e métricas vazias.

**Decisão:** iniciar com entrevistas, protótipo e piloto por convite; liberar autoatendimento progressivamente.

**Consequência:** crescimento inicial será intencionalmente limitado.

**Revisar quando:** o piloto cumprir critérios de saída definidos no MVP.

## PD-007 — Posições individuais privadas por padrão

**Data:** 2026-09-16

**Status:** aceita

**Contexto:** expor posição e mudança ligadas ao username aumenta risco de assédio e torna socialmente mais caro admitir mudança. O valor central pode ser entregue por agregados e contagens.

**Decisão:** publicar argumentos, agregados de posições e contagens de influência. Manter posição individual, histórico de mudança e identidade de quem atribuiu influência privados por padrão.

**Consequência:** auditoria pública não poderá reconstruir votos individuais; controles internos e metodologia pública precisarão sustentar a confiança sem expor participantes.

**Revisar quando:** pesquisa mostrar demanda segura por endosso público opcional. Opt-in, se criado, não pode tornar-se requisito para contagem.

## PD-008 — Idade mínima de dezoito anos

**Data:** 2026-09-22

**Status:** aceita

**Contexto:** a idade mínima e o tratamento de menores estavam entre as questões bloqueadoras do beta público, e a recomendação conservadora anterior admitia exceção por mercado.

**Decisão:** dezoito anos, em todos os mercados, sem exceção por mercado e sem fluxo para menores. O produto não coleta data de nascimento para checar idade: a declaração vive no cadastro e nos termos, e a verificação é responsabilidade de quem os aceita.

**Consequência:** não existe consentimento parental a implementar nem fluxo que incentive a participação de menores; um mercado que exigir tratamento diferente é decisão nova, não ajuste de texto.

**Revisar quando:** um mercado exigir tratamento diferente para menores.

## PD-009 — Licença do código do projeto é AGPL-3.0

**Data:** 2026-09-22

**Status:** aceita

**Contexto:** o repositório não declarava a licença do próprio código, e a política de licenças existente governa apenas as **dependências** — inclusive proibindo copyleft forte no runtime.

**Decisão:** o código do projeto é licenciado em AGPL-3.0, identificador SPDX `AGPL-3.0-only`. A proibição de dependências copyleft fortes continua valendo e não é afetada: são os dois lados de uma mesma política, não uma contradição.

**Consequência:** quem oferecer o serviço pela rede, modificado, precisa publicar as modificações. O produto continua comercial; a contrapartida é que a operação do serviço modificado fica aberta. Falta o arquivo `LICENSE` com o texto canônico, que depende do nome do titular do direito autoral.

**Revisar quando:** houver decisão de relicenciar; a mudança é nova entrada aqui e em `GOVERNANCE.md`.

## PD-010 — Pedidos de titular por alias dedicado

**Data:** 2026-09-22

**Status:** aceita

**Contexto:** os direitos do titular exigem um canal público e rastreável, distinto do canal privado de segurança.

**Decisão:** pedidos de acesso, correção, exclusão e informação sobre tratamento, e o contato de privacidade, chegam por um alias de e-mail dedicado, criado e mantido pelo proprietário. O endereço não é inventado em documento: é publicado quando o alias existir.

**Consequência:** o canal está decidido e ainda não é alcançável; enquanto não existir, o item correspondente do checklist de pré-beta permanece aberto.

**Revisar quando:** o alias existir e for publicado nos dois idiomas, ou se o volume de pedidos exigir outro processo.

## PD-011 — Retenção em vigor ratificada como política

**Data:** 2026-09-22

**Status:** aceita

**Contexto:** as janelas de retenção já rodavam no código, mas como implementação; `PRIVACY.md` §5 exigia revisão jurídica registrada para mudá-las.

**Decisão:** ratificar como política aprovada as janelas em vigor: `tokens` e `sessions` 30 dias após o término, `abuse_signals` 7 dias (anonimização), `exports` 24 horas, `referential_logs` e `billing` retidos como evidência sem prazo. Retenção legal ativa continua suspendendo a ação e as classes continuam independentes.

**Consequência:** mudar uma janela ou uma ação deixa de ser ajuste de código e passa a ser mudança de política, com decisão nova registrada. A revisão jurídica do cronograma é pendência do checklist de pré-beta e não altera a política em vigor até produzir uma decisão.

**Revisar quando:** a revisão jurídica concluir, ou quando uma classe nova passar a existir.

## PD-012 — Brasil e internacional desde o beta

**Data:** 2026-09-22

**Status:** aceita

**Contexto:** o README declarava mercados iniciais como intenção, sem decisão do proprietário; a transferência internacional de dados aparecia como pré-requisito apenas de operar fora do Brasil.

**Decisão:** o beta atende Brasil **e** internacional, em português do Brasil e inglês dos Estados Unidos.

**Consequência:** o mapeamento de onde os dados são processados, os mecanismos de transferência internacional e a lista de subprocessadores passam a ser pré-requisito do próprio beta, e não de uma etapa seguinte.

**Revisar quando:** a lista de subprocessadores for publicada, ou se um mercado exigir tratamento próprio.

## PD-013 — Um achado registrado vale mais do que um modelo editado

**Data:** 2026-09-22 · **Fase:** P20-T04

O modelo de ameaças declara, em `THR-AUTH-01`, que os cookies de sessão carregam o prefixo `__Host-`. O prefixo não existe no código: o cookie é `arena_session`, host-only, e a composição não define `Domain`. A auditoria executou o modelo inteiro e registrou a diferença como achado **SEC-01** (Média), aceito pelo titular com o trabalho seguinte nomeado (`P20-T04A`), em vez de corrigir a prosa do modelo para descrever o que existe ou de implementar o prefixo dentro de uma microtarefa de auditoria. O mesmo vale para **SEC-02**: `THR-ADM-01` nomeia um namespace `/api/v1/admin/*` que o contrato servido não tem — a superfície real é `/api/v1/moderation/*` com capacidades, e a administração de papéis é comando — e o achado fica para a revisão de privacidade e moderação corrigir a descrição.

**Decisão:** uma diferença entre o que o modelo declara e o que o código faz entra em `docs/SECURITY_AUDIT.md` §4 com dono, data e aceite, e o portão passa a recusar se ela desaparecer sem que o trabalho seguinte a feche. **Consequência:** se o desejo for o prefixo ou o namespace como o modelo o descreve, isso é trabalho nomeado, não ajuste de redação — e a fase proíbe reduzir limiar ou criar waiver para obter verde.

**Revisar quando:** `P20-T04A` implementar o prefixo do cookie e a revisão de privacidade corrigir a prosa de `THR-ADM-01`.

## PD-014 — A auditoria de segurança roda na publicação, não no merge

**Data:** 2026-09-22 · **Fase:** P20-T04

`make security-audit` ficou fora de `make verify` deliberadamente. Ele é a soma das doze áreas da fase, e duas delas já estão na esteira como jobs próprios (`make test-security` no `foundation`, `make vuln` no `source-scans`); as outras — sessões, MFA, Stripe, IDOR, cache, CSRF e container — rodam dentro dele, com `-race` onde importa. Colocá-lo em `verify` faria cada merge pagar o custo de um release e confundiria dois portões com respostas diferentes.

**Decisão:** o alvo existe no `Makefile` e é do operador, como o `release-gate` das decisões humanas; o **registro** entra em `verify` pela porta que já existe, porque `TestDeliveredRegisterStands` audita o documento entregue dentro de `go test ./tools/secaudit/...`. **Consequência:** uma evidência citada que deixa de existir quebra o CI no próximo merge que a tocar, e nenhum merge passa a pagar `make vuln` duas vezes.

**Revisar quando:** a verificação reproduzível da fase (P20-T07) decidir se o portão completo vira job.

## PD-015 — Nome público Regnovum

**Data:** 2026-09-23

**Status:** aceita

**Contexto:** o titular decidiu substituir o nome público anterior do produto. A identidade do Reino e as regras de negócio não dependem da grafia antiga.

**Decisão:** o produto passa a se chamar **Regnovum** em toda a documentação narrativa ativa.

**Consequência:** títulos, apresentações e referências ao produto são atualizados. Caminho do repositório, URL remota, módulo Go, nomes de pacote, binário, variáveis, imagens, catálogos de interface e evidências históricas não são renomeados automaticamente; mudanças nesses identificadores exigem alteração técnica separada e verificação própria. Textos históricos permanecem rastreáveis.

**Revisar quando:** a marca ou o domínio exigir ajuste, ou quando houver decisão de migrar os identificadores técnicos e a apresentação em runtime.
