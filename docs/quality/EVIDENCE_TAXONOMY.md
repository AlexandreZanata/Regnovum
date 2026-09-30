# Taxonomia e identidade de evidências de teste

**Status:** padrão obrigatório para o programa de qualidade (P21–P30)

**Documentos de referência:** [QUALITY_CHARTER.md](QUALITY_CHARTER.md) · [catalog.json](../../quality/catalog.json) · [evidence.json](../../quality/evidence.json) · [evidence.schema.json](../../quality/evidence.schema.json) · [CI.md](../CI.md)

---

## 1. Por que a classificação é dado declarado

Um teste não diz o que é. O nome `TestRepository_ApplyDebitConcurrentNoDoubleSpend` não prova que o teste roda contra um PostgreSQL de verdade, e o dia em que alguém o renomear não muda nada do que ele faz — mas mudaria tudo do que uma ferramenta que lesse o nome concluiria. Por isso o tipo de uma evidência, o ambiente em que ela roda e a classe das regras que ela prova são **declarados no registro** (`quality/evidence.json`) e julgados contra os registros do checkout, nunca inferidos do código.

O registro é o contrato entre quem escreve o teste e quem lê o relatório de cobertura. Ele diz, para cada identidade: **que tipo de teste é**, **em que suíte roda**, **em que ambiente**, **quais regras do catálogo prova** e **em que classe de risco**. `tools/qualitytaxonomy` é quem o executa, e `quality/evidence.schema.json` é a metade legível do mesmo contrato — a concordância entre os dois é conferida pela própria ferramenta.

## 2. Os onze tipos

| Tipo | Rótulo | Onde roda | O que prova |
| --- | --- | --- | --- |
| `unit` | Teste unitário | `unit` | Comportamento de uma unidade isolada, sem serviço externo. |
| `property` | Teste de propriedade | `unit` | Uma propriedade que vale para qualquer entrada gerada, não para os casos que alguém lembrou de escrever. |
| `fuzz` | Teste de fuzz | `unit` | Que a entrada malformada é recusada sem pânico, limite estourado ou aceitação silenciosa. |
| `integration` | Teste de integração | `postgres`, `docker` | Que o comportamento se sustenta contra o serviço real — o banco, o daemon, a imagem. |
| `contract` | Teste de contrato | `unit` | Que a superfície pública (rotas, schemas, campos expostos) é a que foi prometida. |
| `e2e` | Jornada de navegador | `browser` | Que a jornada inteira funciona para quem usa, no navegador, com o frontend entregue. |
| `security` | Teste de segurança | `unit`, `postgres`, `network` | Que o controle de segurança recusa o que tem de recusar — e que a recusa é a que foi declarada. |
| `mutation` | Teste de mutação | `unit`, `postgres` | Que o teste falha quando o código é defeituoso: cobre a força da suíte, não a cobertura. |
| `performance` | Teste de desempenho | `network`, `browser` | Que o orçamento declarado é respeitado sob carga medida. |
| `chaos` | Exercício de caos | `docker` | Que a falha de um componente é absorvida sem perda silenciosa. |
| `recovery` | Exercício de recuperação | `docker` | Que a operação volta dos dados que foram restaurados, com o ledger igual. |

Todo tipo tem rótulo próprio e ao menos um ambiente. Um tipo sem rótulo é uma falha de teste — um vocabulário que um relatório em português não consegue imprimir é um vocabulário só para máquinas.

## 3. Os cinco ambientes

O ambiente é **o que a máquina precisa ter** para o teste significar alguma coisa, e é ele que decide se o rótulo do tipo é honesto:

- `unit` — o binário de teste, nada além;
- `postgres` — um PostgreSQL 18 real e descartável;
- `browser` — Chromium, através de `tools/e2e`;
- `docker` — o daemon: imagens, compose, ingress, backups;
- `network` — um provedor falso local, ou uma instância servida.

**A combinação é conferida.** Chamar de `unit` um teste que só existe contra o banco é recusado: `integration` roda em `postgres` ou `docker` e em nenhum outro lugar. É essa tabela — e não a boa vontade de quem escreve — que impede um teste de integração de ser contado como barato.

## 4. Identidade

- Suíte: `SUITE-<MÓDULO>-<AMBIENTE>` — onde as evidências rodam, com o comando que as executa e o que a máquina precisa ter.
- Evidência: `EVD-<MÓDULO>-<TIPO>-<NN>` — o conjunto de testes que prova as regras declaradas.

Uma suíte pode não ter dono. O dono é exigido **da suíte que prova regra crítica** (`Q0`) e de nenhuma outra: um teste crítico cuja suíte ninguém responde é um teste que ninguém revisita, e essa condição é condicional de verdade — não cabe num `required` de schema, então ela é um código de recusa.

## 5. O que o classificador recusa

`make quality-taxonomy` não é um relatório: é um portão. Ele recusa, com código próprio:

- **identidade** — id fora da convenção, id duplicado, campo obrigatório ausente, campo obrigatório em branco, lista vazia, tipo ou ambiente fora do vocabulário, campo que o contrato não declara;
- **coerência** — tipo incompatível com o ambiente da suíte, suíte que não existe, suíte que nenhuma evidência classifica, regra que o catálogo não declara, referência de teste que não resolve na árvore (a função que o arquivo não declara, o arquivo que não existe, o caminho fora do checkout);
- **classe** — classe declarada diferente da mais severa entre as regras provadas, nos dois sentidos: nem mais branda, nem mais estrita. A classe é **lida do catálogo**, então a declaração não pode amaciá-la;
- **registro** — o registro ausente, um catálogo ausente ou sem regra, e a discordância entre `quality/evidence.schema.json` e o vocabulário do próprio carregador (campos exigidos, rigidez, tipos e ambientes).

A ferramenta nunca escreve e nunca executa teste. O que ela produz é o julgamento que o inventário da P21-T06 vai ler.

## 6. Onde está ligado

`make quality-taxonomy` entra em `make verify` porque é gate de merge: evidência que ninguém encontra é evidência que não guarda nada. A regra de manutenção do [CI.md](../CI.md) §8 vale aqui como para os outros: gate novo entra no `Makefile` e nos mesmos jobs, e `make audit-ci` recusa o contrário.

## 7. Como acrescentar uma evidência

1. Escreva o teste e confirme que ele prova o que você acha que prova (a classe decide se você precisa de mais: `Q0` pede evidência proporcional).
2. Declare a suíte, se a dela ainda não existir — com ambiente e comando reais.
3. Declare a evidência: tipo, suíte, regras do catálogo, os testes que a carregam e o que ela prova em uma frase.
4. Rode `make quality-taxonomy`. A classe você não declara por conta própria: declare a mais severa e deixe o catálogo discordar se ela estiver errada.

Contagens não são repetidas neste documento de propósito: quantas evidências existem, quantos tipos estão classificados e quantas regras o catálogo tem são coisas que o próprio portão imprime, e um número copiado aqui envelheceria em silêncio.
