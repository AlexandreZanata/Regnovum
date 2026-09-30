# Documentação do Regnovum

Esta pasta contém a definição atual do produto. Os documentos distinguem deliberadamente:

**Regnovum** é o nome público decidido em 2026-09-23. O nome anterior ainda aparece em identificadores técnicos e evidências históricas, que não foram alterados pela renomeação documental. As [diretrizes do Reino](reino/README.md) e a [Carta Econômica](reino/CARTA_ECONOMICA.md) descrevem uma direção futura; as regras implantadas continuam identificadas abaixo.

- **Princípio:** compromisso duradouro, alterado apenas com justificativa pública forte.
- **Decisão:** regra adotada para a versão atual, passível de revisão documentada.
- **Hipótese:** suposição que precisa ser validada com pesquisa ou uso real.
- **Questão aberta:** decisão ainda não tomada.

Quando houver conflito, a ordem de precedência é:

1. [Constituição](CONSTITUTION.md);
2. [Regras de negócio](BUSINESS_RULES.md);
3. documento específico do tema;
4. [Visão do produto](PRODUCT.md);
5. materiais históricos.

Quem chega agora começa pelo [README da raiz](../README.md): ele é o handoff do backend — arquitetura final, configuração, desenvolvimento, teste e operação com os comandos reais —, e o que ele afirma é conferido contra a árvore por `make handoff-check` e seguido num checkout limpo por `make handoff-walkthrough`.

## Produto

- [PRODUCT.md](PRODUCT.md) — problema, proposta de valor, público e posicionamento.
- [CONSTITUTION.md](CONSTITUTION.md) — compromissos fundamentais da plataforma.
- [BUSINESS_RULES.md](BUSINESS_RULES.md) — regras, estados, invariantes e casos-limite.
- [MVP.md](MVP.md) — escopo estrito e critérios de sucesso da primeira versão.
- [ROADMAP.md](ROADMAP.md) — sequência de validação e evolução.
- [reino/](reino/README.md) — Constituição funcional do Reino, Carta Econômica, decretos conceituais e questões abertas.

## Operação e confiança

- [MONETIZATION.md](MONETIZATION.md) — INK, Arena Pass, assinatura e preços.
- [MODERATION.md](MODERATION.md) — política e processo de moderação.
- [PRIVACY.md](PRIVACY.md) — minimização, direitos e separação de dados.
- [TRANSPARENCY.md](TRANSPARENCY.md) — métricas públicas, histórico e exportações.
- [BUILD_IN_PUBLIC.md](BUILD_IN_PUBLIC.md) — como o projeto será construído publicamente.

## Tecnologia

- [STACK.md](STACK.md) — stack aprovada e política de versões.
- [ARCHITECTURE.md](ARCHITECTURE.md) — arquitetura inicial e limites dos módulos.
- [FRONTEND.md](FRONTEND.md) — componentes TypeScript nativos e CSS.
- [BACKEND.md](BACKEND.md) — domain, application, ports e adapters.
- [SCALABILITY.md](SCALABILITY.md) — caminho mensurável para alta escala.
- [SLO.md](SLO.md) — SLOs e budgets por jornada: baseline medido vs compromisso (P28-T01).
- [SECURITY.md](SECURITY.md) — requisitos técnicos de segurança.
- [DEPLOYMENT.md](DEPLOYMENT.md) — topologia, ambientes, backup e evolução.
- [RUNBOOKS.md](RUNBOOKS.md) — alertas iniciais, thresholds e runbooks de incidente.
- [CI.md](CI.md) — verificação completa de release: gates exigidos, jobs, ações pinadas e orçamento do pipeline.
- [MIGRATION_AUDIT.md](MIGRATION_AUDIT.md) — evidência do ciclo de vida das migrations, gerada por `tools/migrationaudit` (`make migration-audit`): tempos, dataset, locks observados, o resultado de cada regra e as exceções declaradas.
- [SECURITY_AUDIT.md](SECURITY_AUDIT.md) — execução da auditoria de segurança do lançamento (P20-T04) por `tools/secaudit` (`make security-audit`): as doze áreas rodadas, as seis fronteiras de confiança revisadas à mão, os achados com dono e aceite e o registro executável que o portão lê.
- [PRIVACY_AUDIT.md](PRIVACY_AUDIT.md) — revisão de privacidade e moderação do lançamento (P20-T06) por `tools/privacyaudit` (`make privacy-audit`): as sete áreas de ciclo de vida de dados, as duas contas sintéticas que procuram mistura de dados, as allowlists das exportações conferidas contra o código, a tabela de retenção conferida contra o cronograma que o job aplica e os achados com dono e aceite.
- [DISASTER_DRILL.md](DISASTER_DRILL.md) — evidência do exercício de desastre e carga (P20-T05), gerada pelo próprio exercício (`make disaster-drill`) e julgada por `tools/drillaudit`: o backup restaurado num ambiente isolado com os scripts da operação, a comparação do ledger antes e depois, RPO e RTO medidos, o baseline de carga registrado e o que os dois provedores fizeram com os seus jobs/financial rows quando ficaram inalcançáveis.
- [I18N_AUDIT.md](I18N_AUDIT.md) — auditoria final de internacionalização (P20-T09) por `tools/i18nrelease` (`make i18n-audit`, dentro de `make verify`): cobertura das 200 mensagens nos dois locais pela segunda medição independente do gerador, snapshots de email, códigos estáveis de Problem Details, SEO e `hreflang`, cache sem mistura de locale, texto hardcoded, pseudo-locale — e as jornadas de navegador dirigidas em **cada** idioma, com a cobrança do idioma declarado em cada página. Traz os achados com dono e a revisão humana do `en-US`, que segue pendente.
- [RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md) — verificação final reproduzível do backend (P20-T07), gerada por `tools/releaseverify` (`make release-verify`): o checkout limpo do commit, as dependências instaladas só pelos lockfiles, cada comando da fase rodado **duas vezes**, a imagem e o smoke, o `git fsck`, o veredito do portão de release e as limitações reais.
- [COMMITS.md](COMMITS.md) — Conventional Commits, scopes e versionamento.
- [ADRs](adr/README.md) — decisões arquiteturais e suas consequências.

## Aprendizado e governança

- [GOVERNANCE.md](GOVERNANCE.md) — decisões humanas do lançamento: o que está decidido, onde cada decisão é aplicada e o que ainda bloqueia o release (`make release-gate`).
- [METRICS.md](METRICS.md) — métricas de produto e guardrails.
- [RISKS_AND_ASSUMPTIONS.md](RISKS_AND_ASSUMPTIONS.md) — riscos, hipóteses e plano de validação.
- [DECISIONS.md](DECISIONS.md) — log de decisões de produto.
- [GLOSSARY.md](GLOSSARY.md) — vocabulário comum.

## História

- [HISTORY.md](HISTORY.md) — linha do tempo pública da construção (P00–P20): o que cada fase entregou, com que evidência e em qual PR, e como o trabalho acontece; atualizada ao fim de cada fase e espelhada na wiki.

## Regra de manutenção

Toda mudança que altere incentivos, elegibilidade, contagem pública, moderação, preço, reputação ou privacidade deve atualizar os documentos afetados e acrescentar uma entrada em `DECISIONS.md` no mesmo conjunto de mudanças.
