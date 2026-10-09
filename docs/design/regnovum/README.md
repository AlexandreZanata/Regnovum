# Assets do Reino — P60

O painel usa as ilustrações geradas para o Regnovum: 17 retratos de cargos,
cinco cenas, brasão, wordmark, moeda INK, ícones e insígnias.

- `web/public/realm/`: 148 arquivos otimizados WebP/SVG para o produto.
- `docs/design/regnovum/originals/`: 22 originais PNG, preservados para exportação.
- `docs/design/regnovum/data/`: prompts, resoluções, tokens e conceitos visuais.

`make build-web` publica cópias com hash no manifesto. O servidor aceita apenas
as imagens declaradas, com MIME explícito, ETag e cache imutável. Os originais
PNG não são enviados ao navegador. Retratos pequenos usam WebP de 128 px;
arte dos cards usa 768 px, dimensões reservadas e carregamento lazy.

As imagens são ilustrações geradas por IA, não fotografias de usuários nem
identidades reais. As descrições do kit registram conceitos documentais; não
constituem concessão de cargo. A descrição de Rei no catálogo do produto segue
`docs/reino/TEMPORADAS_SUCESSAO.md`: Coroa sazonal distinta do operador técnico.
Nenhuma permissão ou ativação depende de uma imagem.
