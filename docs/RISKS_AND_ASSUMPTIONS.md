# Riscos, hipóteses e validação

**Status:** registro vivo

**Ordem recomendada:** validar risco de valor antes de risco de escala

## 1. Riscos críticos

### R1 — Mudança de opinião é rara

Se poucos participantes mudarem, a proposta central e a reputação ficam vazias.

**Teste inicial:** Arenas facilitadas com entrevista posterior. Observar não só mudança final, mas refinamento de confiança; considerar futuramente uma escala de confiança sem adicioná-la ao MVP antes de evidência.

### R2 — Atribuição não representa influência real

Amizades, reciprocidade e campanhas podem transformar reconhecimento em moeda social.

**Mitigação:** autoatribuição proibida, pessoas distintas na manchete, padrões suspeitos revisáveis, ausência de recompensa financeira e transparência de metodologia.

### R3 — O resultado prévio vaza ou o bloqueio irrita

Resultados podem circular externamente, e usuários podem escolher aleatoriamente apenas para desbloquear.

**Teste:** comparar compreensão e abandono; explicar por que a escolha vem primeiro; permitir “indeciso”. Não prometer eliminar efeito manada, apenas reduzi-lo no fluxo principal.

### R4 — Amostra é interpretada como opinião pública

Percentuais visualmente fortes podem parecer pesquisa representativa.

**Mitigação:** tamanho da amostra, linguagem “participantes desta Arena”, aviso persistente e nenhuma extrapolação populacional.

### R5 — INK cria paywall de expressão

Cobrar por texto pode favorecer renda, punir idiomas ou incentivar mensagens excessivamente curtas.

**Teste:** piloto sem cobrança real, simulação de consumo e entrevistas. Monitorar quem esgota franquia e que conteúdo deixa de publicar. Ajustar franquia antes de adicionar planos.

### R6 — Temas controversos excedem capacidade de moderação

Uma pequena equipe pode ser sobrecarregada por assédio, ilegalidade e recursos.

**Mitigação:** lançamento por convite, categorias limitadas, criação manual de Arenas no piloto e limites de crescimento ligados à capacidade operacional.

### R7 — Sybil e brigading distorcem resultados

Email verificado não equivale a uma pessoa. Coordenação externa é barata.

**Mitigação:** limites, sinais de idade de conta, revisão de anomalias, transparência de limitações e invalidação auditável. Não prometer “uma pessoa, um voto” sem prova correspondente.

### R8 — A afirmação é ambígua

Pessoas respondem a interpretações diferentes e mudanças perdem significado.

**Mitigação:** guia de formulação, prévia, exemplos, revisão manual inicial e declaração imutável.

### R9 — Reputação gera performance em vez de escuta

Autores podem perseguir mudanças fáceis ou temas polarizados.

**Mitigação:** métricas desagregadas, nenhum score universal, nenhuma conversão financeira, ranking temporal padrão e guardrails de concentração.

### R10 — Complexidade prematura impede aprendizado

Construir billing, multilíngue, exportação, moderação e observabilidade antes de validar o loop pode consumir meses.

**Mitigação:** piloto concierge e protótipo clicável antes do SaaS completo.

### R11 — Arenas vazias não demonstram valor

Sem argumentos suficientes dos diferentes lados, o primeiro visitante não encontra motivo para permanecer ou mudar.

**Mitigação:** lançar poucas Arenas por vez, recrutar participantes com perspectivas diferentes e preparar contribuições iniciais autênticas — identificadas por seus autores, nunca fabricadas pela plataforma.

### R12 — Nome e marca precisam de validação

Regnovum é o nome escolhido. A mudança reduz o risco cultural associado ao nome anterior, mas não comprova disponibilidade de marca, domínio ou boa compreensão em todos os mercados. Uma identidade ainda não validada pode afetar confiança, aquisição e parceiros comerciais.

**Mitigação:** testar compreensão e pronúncia em pt-BR e en-US, fazer busca jurídica de marca e verificar domínios antes de investir em identidade visual ou aquisição. Disponibilidade de domínio não equivale a liberação de marca.

## 2. Hipóteses priorizadas

### Devem ser testadas antes de implementar monetização completa

1. Usuários entendem e aceitam a posição prévia.
2. Leem argumentos de mais de uma posição.
3. Conseguem identificar argumentos que contribuíram para mudanças.
4. Voltam para participar de outra Arena.
5. Criadores conseguem formular afirmações claras com orientação.

### Podem ser testadas durante o MVP público

1. INK reduz spam e mantém acesso suficiente.
2. Arena Pass filtra criação de baixa qualidade sem bloquear bons criadores.
3. Member tem valor recorrente.
4. Preços funcionam nos dois mercados.
5. Métricas factuais produzem reconhecimento saudável.

## 3. Plano de pesquisa inicial

### Entrevistas de problema

Realizar de 12 a 20 entrevistas com participantes e criadores potenciais. Perguntar sobre episódios reais de discussão e mudança; não vender a solução cedo demais.

Perguntas úteis:

- Conte sobre a última vez que você mudou de opinião após uma discussão.
- Como encontrou o argumento decisivo?
- O que torna um debate online confiável ou inútil?
- Você registraria sua posição publicamente? O que impediria isso?
- Como gostaria de reconhecer quem ajudou sem “curtir” a pessoa inteira?

### Protótipo

Testar cinco telas: Arena bloqueada, escolha, agregado, argumentos e mudança com atribuição. Medir compreensão e hesitações, não estética.

### Piloto concierge

Selecionar temas que permitam discordância de boa-fé, tenham participantes com conhecimento distribuído e não exijam moderação extrema logo no primeiro teste.

## 4. Questões bloqueadoras antes do beta público

- idade mínima e política para menores — **resolvida:** dezoito anos em todos os mercados, sem exceção (`GOVERNANCE.md`, `age-minimum`);
- tratamento jurídico de conteúdo público após exclusão;
- termos, privacidade e base legal por tratamento — **em aberto:** a decisão sobre termos de uso e aviso de privacidade no beta público é o bloqueio `terms-of-use` de `GOVERNANCE.md`;
- categorias de conteúdo de alto risco;
- regra de região e moeda;
- política de reembolso e chargeback;
- responsabilidade operacional por moderação e incidentes;
- significado público exato de “minds changed”.

## 5. Revisão

Cada risco deve ganhar evidência, responsável e próxima data de revisão quando o trabalho começar. Hipóteses invalidadas devem permanecer no histórico; apagar uma hipótese fracassada prejudica o propósito de build in public.
