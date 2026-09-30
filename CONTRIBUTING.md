# Como contribuir

Obrigado por contribuir com o Regnovum. O projeto está em descoberta e definição arquitetural; mudanças devem preservar a distinção entre princípio, decisão e hipótese.

## Antes de começar

1. Leia a [constituição](docs/CONSTITUTION.md).
2. Consulte as [regras de negócio](docs/BUSINESS_RULES.md).
3. Verifique o [roadmap](docs/ROADMAP.md) e as decisões existentes.
4. Para vulnerabilidades, siga [SECURITY.md](SECURITY.md), não abra issue pública.

## Propostas

Uma proposta deve explicar:

- problema observado;
- evidência disponível;
- mudança sugerida;
- alternativas consideradas;
- consequências e riscos;
- como verificar o resultado.

Mudanças de produto atualizam `docs/DECISIONS.md`. Mudanças técnicas relevantes criam ou substituem um ADR em `docs/adr`.

## Pull requests

- mantenha escopo pequeno e revisável;
- não misture formatação geral com mudança funcional;
- adicione ou atualize testes quando houver código;
- atualize documentação e changelog afetados;
- informe riscos de segurança, privacidade, migração e rollback;
- não inclua dados reais, credenciais ou dumps;
- confirme que conteúdo gerado foi revisado por uma pessoa responsável.

## Commits

Use o padrão descrito em [docs/COMMITS.md](docs/COMMITS.md). O resumo é `type(scope): descrição`, seguindo Conventional Commits.

## Código futuro

As convenções detalhadas serão adicionadas antes do primeiro código de produção. Até lá, a arquitetura aprovada em `docs/STACK.md` e `docs/ARCHITECTURE.md` é a referência.
