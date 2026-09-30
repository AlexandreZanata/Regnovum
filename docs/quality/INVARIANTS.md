# Matriz de invariantes do backend

**Status:** inventário versionado de `quality/invariants.json` — não editar o JSON à mão sem rodar o portão; o loader é `tools/qualityinvariants`.
**Fase:** P24-T01 — validação profunda das regras de negócio.

A matriz liga cada invariante Q0/Q1 ao catálogo executável (`quality/catalog.json`) e aos testes que provam os quatro casos: válido, inválido, limite e replay. Transição persistida exige os quatro; linha não persistida exige válido e inválido, com limite/replay quando a regra tem limiar ou repetição. Cada um dos doze módulos carrega ao menos uma prova de estado inalcançável. Retirar uma transição da matriz falha a cobertura semântica, porque o portão exige que toda regra Q0/Q1 esteja ligada a ao menos uma linha.

## 1. Módulos e cobertura

| Módulo | Linhas | Persistidas | Inalcançável provado |
| --- | --- | --- | --- |
| identity | 7 | 2 | sim |
| profiles | 4 | 1 | sim |
| wallet | 6 | 6 | sim |
| entitlements | 3 | 2 | sim |
| arenas | 11 | 0 | sim |
| positions | 14 | 1 | sim |
| arguments | 13 | 2 | sim |
| persuasion | 11 | 1 | sim |
| billing | 10 | 5 | sim |
| moderation | 7 | 2 | sim |
| transparency | 5 | 2 | sim |
| jobs | 2 | 0 | sim |

Totais: **98 linhas** (86 ligadas uma a uma às regras Q0/Q1 do catálogo mais 12 provas de inalcançável, uma por módulo), **29 persistidas com quatro casos**, **12 provas de inalcançável**. As linhas extras `platform` (3) e `i18n` (2) mantêm a ligação total Q0/Q1; o portão exige os doze acima, sem proibir os adjacentes.

`entitlements` mapeia para `internal/billing` (lotes de Arena Pass e concessões); `transparency` inclui a trilha de auditoria (`internal/audit`) como prova pública.

## 2. Colunas da matriz

Cada linha carrega: `transition`, `precondition`, `postcondition`, `global_invariant`, `prohibited_effect`, `catalog`, `persisted`, `unreachable` e `tests.{valid,invalid,limit,replay}` no formato `caminho::Teste`.

- `precondition` diz quem pode mover e o que a recusa parece.
- `postcondition` diz o que a repetição faz.
- `global_invariant` diz o que a concorrência faz e a propriedade de segurança que nunca cai.
- `prohibited_effect` diz o vazamento ou a quebra que é recusa.

## 3. Validação mínima da tarefa

- Toda transição persistida possui válido, inválido, limite e replay que resolvem na árvore (`func Nome(` no arquivo citado).
- Estados inalcançáveis são testados: `removed -> published`, `revoked -> authenticated`, saldo negativo, lote expirado consumível, mesma posição, recursão além de um nível, auto-atribuição, webhook sem HMAC, self-review, exportação de rascunho/removida e retry de vivo.
- Retirar uma transição falha: `go test ./tools/qualityinvariants/ -run TestRemovingATransitionFailsCoverage` fica vermelho com `invariants-coverage-missing`, e `go run ./tools/qualityinvariants -root .` recusa a árvore.

## 4. Uso

```bash
go run ./tools/qualityinvariants -root .
go test ./tools/qualityinvariants/ -count=1
```

O portão nunca escreve e nunca executa teste: ele julga referências, pacotes, ligação ao catálogo, cobertura por módulo e provas de inalcançável. Modelos independentes (T03–T09), fuzz (T02), mutação (T10) e floors (T11) constroem sobre esta matriz sem um runner monolítico.
