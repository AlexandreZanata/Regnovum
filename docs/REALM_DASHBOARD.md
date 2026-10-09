# Painel do Reino — P60-T01

`GET /` é a entrada funcional do Regnovum. Não há hero promocional, catálogo
inicial de ícones ou demonstrações. O servidor entrega arenas reais, filtros,
busca e paginação antes de executar JavaScript. A rota é exata: endereços
inexistentes continuam respondendo 404.

## Componentes reutilizáveis

Os templates em `internal/arenas/adapters/html/home/` definem campo de busca,
navegação, ação rápida, card de arena, cabeçalho de painel, painel do Reino,
painel de temporada, card de cargo e painéis de contratos/disputas. Recebem
contextos tipados de `home_components.go`. Conteúdo público passa pelo escape
de `html/template`; não há interpolação de HTML confiável nem `innerHTML`.

`ga-realm-navigation` adota a navegação SSR em Light DOM. Faz a melhoria do menu
mobile, mantém `aria-expanded`, fecha com Escape e cancela listeners ao sair.
Sem JavaScript, links permanecem acessíveis. Os cargos usam `details/summary`
nativos; cinco aparecem inicialmente e outros doze ficam no diretório expansível.

O CSS tem tokens locais, layers, container query nos cards, fontes nativas e
layouts para desktop, tablet e celular. Catálogos SSR `server-home` pt-BR/en-US são regenerados
pelo gerador oficial. Namespaces `server-*` são validados normalmente e emitidos apenas no catálogo Go,
para preservar o orçamento de JavaScript das outras páginas. Nenhuma biblioteca
de runtime foi adicionada.

## Dados e limites

`ComposeArenaLifecycle` injeta o feed e a busca existentes. A página passa os
cursores opacos aos casos de uso; nunca os decodifica. Resultados de busca e
filtros ficam na URL. A busca é textual; as categorias são filtros do feed.
`locale` escolhe a interface; `language` filtra o conteúdo, independentemente.
Páginas seguintes preservam ambos, categoria e busca. Erros de validação
respondem 400; indisponibilidade responde 503 com ação de recuperação.

Não se expõem agregados de posição no feed, saldo pessoal, placar universal de
reputação ou contagens inventadas. Arenas levam à participação existente e ao
documento público. Entrar/criar conta e transparência apontam a rotas reais.
Contratos, disputas e temporadas continuam explicitamente indisponíveis;
a interface não ativa módulos staged. As ilustrações dos cargos são referência
visual, nunca autoridade ou permissão.

## Verificação

Testes HTTP cobrem conteúdo real do feed, busca, XSS, cursor/idioma/categoria,
SSR sem JS, vazios, falhas, dependências e assets ausentes, 404 fora da raiz.
O pipeline de imagens prova preservação de bytes, hash, MIME e cache.
A prova de navegador do painel vive em `tools/e2e/specs/realm.spec.js`.
