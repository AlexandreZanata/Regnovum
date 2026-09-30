# Transparência

**Status:** contrato público inicial

## 1. Objetivo

A transparência do Regnovum deve permitir que uma pessoa entenda o que as métricas significam, como o conteúdo é ordenado e quando uma regra mudou, sem expor dados pessoais ou facilitar abuso.

## 2. Página pública

A rota futura `/transparency` deve apresentar, por período e quando houver volume seguro:

- contas participantes elegíveis;
- Arenas publicadas, fechadas, restritas e removidas;
- argumentos publicados e retirados;
- mudanças de posição;
- mudanças com atribuição;
- pessoas distintas reconhecidas como influentes;
- INK de franquia emitido, expirado e consumido;
- INK comprado, consumido, reembolsado e ajustado;
- Arena Passes emitidos, comprados e consumidos;
- denúncias, ações, recursos e reversões;
- eventos invalidados por abuso.

Receita bruta e MRR são opcionais. Se publicados, devem declarar moeda, impostos, reembolsos, taxas e metodologia para não produzir números enganosos.

## 3. Metodologia obrigatória

Cada métrica pública deve informar:

- definição;
- período e fuso horário;
- população incluída e excluída;
- frequência de atualização;
- se o dado é exato, atrasado ou estimado;
- mudanças de metodologia.

Baixos volumes devem ser omitidos ou agrupados para reduzir reidentificação.

## 4. Resultados de uma Arena

Toda visualização de resultado deve mostrar:

- número de participantes elegíveis;
- distribuição atual e, quando útil, inicial;
- momento da última atualização;
- idioma e estado da Arena;
- aviso de que é uma amostra autoselecionada da plataforma;
- link para a regra de elegibilidade;
- quantidade de eventos invalidados, apenas quando a divulgação não ajudar atacantes.

## 5. Ordenação

O MVP usa listas separadas por relação com a afirmação e ordenação temporal explícita. Se houver nova ordenação, a página pública deve documentar sinais usados, sinais proibidos, desempate e data de vigência.

Receita, plano, compra de INK ou Arena Pass nunca podem ser sinais de ranking.

## 6. Histórico de regras

Mudanças relevantes em contagem, reputação, monetização, moderação e privacidade devem registrar:

- versão e data;
- regra anterior;
- nova regra;
- motivação;
- métricas afetadas;
- se houve recálculo histórico.

## 7. Exportação pública de Arena

Cada Arena terá exportação JSON versionada contendo apenas dados públicos. Deve incluir `schema_version`, identidade e estado da Arena, conteúdo público, posições e mudanças agregadas, argumentos, fontes, contagens válidas de influência e datas. Não deve incluir posição individual, histórico individual de mudança ou identidade de quem atribuiu influência.

Identificadores devem ser estáveis. Integridade futura pode usar assinaturas, encadeamento de hashes ou logs de transparência, sem exigir blockchain.

## 8. Limites de transparência

Transparência não exige publicar:

- dados pessoais;
- regras antifraude detalhadas que tornem evasão trivial;
- denúncias ou evidências privadas;
- segredos e detalhes que comprometam segurança;
- dados cuja divulgação viole lei ou direitos de terceiros.

Quando algo não puder ser divulgado, a plataforma deve explicar a categoria da restrição sempre que for seguro.
