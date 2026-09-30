# Inventário de cobertura semântica

**Status:** relatório gerado por `tools/qualityinventory` — não editar à mão; rode `make quality-inventory-write` e revise o diff.

**Documentos cruzados:** [catalog.json](../../quality/catalog.json) · [evidence.json](../../quality/evidence.json) · [REQUIREMENTS.md](../REQUIREMENTS.md)

**Famílias lidas:** rotas do contrato (`api/openapi.json`), migrations (`internal/platform/dbmigrate/migrations/`), casos de uso (`internal/<módulo>/application/`), tipos de job (`internal/jobs/domain/types.go`) e comandos de CLI (`cmd/arena/main.go`), além das referências de teste que os três documentos citam.

---

## 1. Resumo

| Medida | Valor |
| --- | --- |
| Regras no catálogo | 95 |
| Regras críticas (Q0) | 39 |
| **Cobertas** | **95** |
| **Ausentes** | **0** |
| Identidades de evidência | 11 |
| Citações resolvidas | 991 de 991 |
| **Obsoletas** | **0** |
| Artefatos lidos | 252 |
| **Órfãos** | **10** |

Cobertura é contada **por regra, nunca por linha**: uma regra está coberta quando um teste que a prova, uma rota, um caso de uso ou uma migration que a carrega, ou uma identidade de evidência que a declara existe de verdade no checkout. Um arquivo executado por um teste não torna coberta nenhuma regra.

## 2. As seis famílias

| Família | Artefatos | Rastreados | Órfãos | Citações | Não resolvidas |
| --- | --- | --- | --- | --- | --- |
| Rotas do contrato | 82 | 82 | 0 | 93 | 0 |
| Migrations | 32 | 32 | 0 | 77 | 0 |
| Casos de uso | 125 | 122 | 3 | 123 | 0 |
| Tipos de job | 6 | 6 | 0 | 0 | 0 |
| Comandos de CLI | 7 | 0 | 7 | 0 | 0 |
| Referências de teste | 0 | 0 | 0 | 698 | 0 |

Um artefato é rastreado quando um documento o cita pelo nome **ou** quando uma regra do catálogo cobre o pacote que o possui — o segundo critério é grosseiro de propósito, porque uma rota pertence ao módulo que a serve e esse módulo é o que uma regra alcança. O que ninguém cita e nenhuma regra cobre é a superfície que a qualidade ainda não alcança: um achado, não um portão.

A família **Referências de teste** não tem universo de artefatos: uma referência é uma citação, não algo que o inventário enumere, e por isso ela aparece nas duas últimas colunas.

## 3. Coberto

- **Q0**: 39 regra(s)
- **Q1**: 47 regra(s)
- **Q2**: 9 regra(s)

| Regra | Classe | Âncoras | Testes | Evidências |
| --- | --- | --- | --- | --- |
| `QUAL-REQ-ARG-01` | Q1 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-ARG-02` | Q1 | route, migration, use-case, test | 4 | — |
| `QUAL-REQ-ARG-03` | Q1 | route, migration, use-case, test | 2 | — |
| `QUAL-REQ-ARG-04` | Q1 | route, use-case, test | 4 | EVD-ARGUMENTS-FUZZ-01 |
| `QUAL-REQ-ARG-05` | Q1 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-ARG-06` | Q1 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-ARG-07` | Q1 | migration, use-case, test | 3 | — |
| `QUAL-REQ-ARG-08` | Q1 | route, migration, use-case, test | 4 | — |
| `QUAL-REQ-ARN-01` | Q1 | route, migration, use-case, test | 2 | — |
| `QUAL-REQ-ARN-02` | Q0 | route, migration, use-case, test | 8 | — |
| `QUAL-REQ-ARN-03` | Q1 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-ARN-04` | Q1 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-ARN-05` | Q1 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-ARN-06` | Q1 | route, migration, use-case, test | 4 | — |
| `QUAL-REQ-ARN-07` | Q2 | route, migration, use-case, test | 4 | — |
| `QUAL-REQ-AUD-01` | Q0 | migration, use-case, test | 7 | — |
| `QUAL-REQ-AUTH-01` | Q0 | route, migration, use-case, test | 7 | EVD-IDENTITY-FUZZ-01 |
| `QUAL-REQ-AUTH-02` | Q0 | route, migration, use-case, test | 6 | — |
| `QUAL-REQ-AUTH-03` | Q0 | route, migration, use-case, test | 6 | — |
| `QUAL-REQ-AUTH-04` | Q0 | route, migration, use-case, test | 9 | — |
| `QUAL-REQ-BIL-01` | Q0 | route, migration, use-case, test | 7 | — |
| `QUAL-REQ-BIL-02` | Q0 | route, migration, use-case, test | 4 | — |
| `QUAL-REQ-BIL-03` | Q0 | migration, use-case, test | 7 | EVD-BILLING-SECURITY-01 |
| `QUAL-REQ-BIL-04` | Q0 | migration, use-case, test | 5 | EVD-BILLING-INTEGRATION-01 |
| `QUAL-REQ-BIL-05` | Q0 | migration, use-case, test | 5 | — |
| `QUAL-REQ-DISC-01` | Q2 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-DISC-02` | Q2 | route, migration, use-case, test | 3 | EVD-CONTRACT-CONTRACT-01 |
| `QUAL-REQ-DISC-03` | Q2 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-DISC-04` | Q2 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-ENT-01` | Q0 | route, migration, use-case, test | 7 | — |
| `QUAL-REQ-ENT-02` | Q0 | route, migration, use-case, test | 6 | — |
| `QUAL-REQ-I18N-01` | Q1 | route, use-case, test | 4 | — |
| `QUAL-REQ-I18N-02` | Q1 | route, migration, use-case, test | 3 | EVD-JOURNEYS-E2E-01 |
| `QUAL-REQ-I18N-03` | Q1 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-I18N-04` | Q1 | use-case, test | 2 | — |
| `QUAL-REQ-INV-01` | Q1 | migration, test | 4 | — |
| `QUAL-REQ-INV-02` | Q1 | migration, test | 2 | — |
| `QUAL-REQ-INV-03` | Q1 | migration, test | 4 | — |
| `QUAL-REQ-INV-04` | Q1 | migration, test | 5 | EVD-POSITIONS-PROPERTY-01 |
| `QUAL-REQ-INV-05` | Q1 | migration, test | 5 | — |
| `QUAL-REQ-INV-06` | Q1 | migration, test | 5 | — |
| `QUAL-REQ-INV-07` | Q0 | migration, test | 7 | EVD-WALLET-INTEGRATION-01 |
| `QUAL-REQ-INV-08` | Q0 | migration, test | 7 | — |
| `QUAL-REQ-INV-09` | Q0 | migration, test | 5 | EVD-MODERATION-INTEGRATION-01 |
| `QUAL-REQ-INV-10` | Q2 | migration, test | 2 | — |
| `QUAL-REQ-MOD-01` | Q1 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-MOD-02` | Q0 | route, migration, use-case, test | 6 | — |
| `QUAL-REQ-MOD-03` | Q0 | route, migration, use-case, test | 5 | EVD-MODERATION-INTEGRATION-01 |
| `QUAL-REQ-MOD-04` | Q0 | route, migration, use-case, test | 5 | — |
| `QUAL-REQ-PERS-01` | Q1 | route, migration, use-case, test | 4 | — |
| `QUAL-REQ-PERS-02` | Q1 | route, migration, use-case, test | 4 | — |
| `QUAL-REQ-PERS-03` | Q1 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-PERS-04` | Q1 | route, migration, use-case, test | 2 | — |
| `QUAL-REQ-PERS-05` | Q1 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-PERS-06` | Q1 | route, migration, use-case, test | 4 | — |
| `QUAL-REQ-PERS-07` | Q1 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-PERS-08` | Q1 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-POS-01` | Q1 | route, migration, use-case, test | 4 | — |
| `QUAL-REQ-POS-02` | Q1 | route, migration, use-case, test | 4 | — |
| `QUAL-REQ-POS-03` | Q1 | route, migration, use-case, test | 4 | — |
| `QUAL-REQ-POS-04` | Q0 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-POS-05` | Q1 | route, migration, use-case, test | 2 | — |
| `QUAL-REQ-POS-06` | Q1 | route, use-case, test | 2 | — |
| `QUAL-REQ-POS-07` | Q1 | route, use-case, test | 2 | — |
| `QUAL-REQ-POS-08` | Q1 | route, migration, use-case, test | 4 | — |
| `QUAL-REQ-POS-09` | Q1 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-POS-10` | Q1 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-PRIV-01` | Q0 | route, migration, use-case, test | 9 | — |
| `QUAL-REQ-PROF-01` | Q1 | route, migration, use-case, test | 6 | — |
| `QUAL-REQ-PROF-02` | Q1 | migration, use-case, test | 4 | — |
| `QUAL-REQ-PROF-03` | Q2 | route, migration, use-case, test | 3 | — |
| `QUAL-REQ-TRP-01` | Q2 | route, migration, use-case, test | 4 | — |
| `QUAL-REQ-TRP-02` | Q2 | migration, use-case, test | 3 | — |
| `QUAL-REQ-WAL-01` | Q0 | route, migration, use-case, test | 4 | EVD-WALLET-INTEGRATION-01 |
| `QUAL-REQ-WAL-02` | Q0 | route, use-case, test | 5 | — |
| `QUAL-REQ-WAL-03` | Q0 | route, migration, use-case, test | 7 | — |
| `QUAL-REQ-WAL-04` | Q0 | route, migration, use-case, test | 5 | — |
| `QUAL-REQ-WAL-05` | Q0 | route, migration, use-case, test | 5 | — |
| `QUAL-REQ-WAL-06` | Q0 | migration, use-case, test | 5 | — |
| `QUAL-THR-ADM-01` | Q0 | test | 9 | — |
| `QUAL-THR-ADM-02` | Q0 | test | 10 | — |
| `QUAL-THR-AUTH-01` | Q0 | test | 8 | — |
| `QUAL-THR-AUTH-03` | Q0 | test | 3 | EVD-SECURITY-UNIT-01 |
| `QUAL-THR-AUTH-04` | Q0 | test | 6 | — |
| `QUAL-THR-CACHE-01` | Q1 | test | 4 | — |
| `QUAL-THR-CACHE-02` | Q1 | test | 3 | — |
| `QUAL-THR-MOD-01` | Q1 | test | 4 | — |
| `QUAL-THR-MOD-02` | Q0 | test | 8 | EVD-MODERATION-INTEGRATION-01 |
| `QUAL-THR-PERS-01` | Q1 | test | 4 | — |
| `QUAL-THR-STRIPE-01` | Q0 | test | 7 | EVD-BILLING-SECURITY-01 |
| `QUAL-THR-STRIPE-02` | Q0 | test | 6 | — |
| `QUAL-THR-STRIPE-03` | Q0 | test | 6 | — |
| `QUAL-THR-WAL-01` | Q0 | test | 7 | EVD-WALLET-INTEGRATION-01 |
| `QUAL-THR-WAL-02` | Q0 | test | 5 | EVD-PLATFORM-INTEGRATION-01 |
| `QUAL-THR-WAL-03` | Q0 | test | 3 | — |

## 4. Ausente

Nenhuma regra está sem âncora: toda regra do catálogo é provada por algo que existe no checkout.

## 5. Obsoleto

Nenhuma citação aponta para o vazio: tudo o que os três documentos nomeiam existe no checkout.

## 6. Órfão

**Comandos de CLI** (7)

- `admin` — pacote `cmd/arena`
- `help` — pacote `cmd/arena`
- `migrate` — pacote `cmd/arena`
- `projections` — pacote `cmd/arena`
- `server` — pacote `cmd/arena`
- `version` — pacote `cmd/arena`
- `worker` — pacote `cmd/arena`

**Casos de uso** (3)

- `internal/notifications/application/deliver.go` — pacote `internal/notifications`
- `internal/notifications/application/notify.go` — pacote `internal/notifications`
- `internal/notifications/application/ports.go` — pacote `internal/notifications`

Um comando de operador ou um tipo de job pode ser órfão sem ser defeito: o que este relatório afirma é que nenhuma regra de negócio o alcança, e é a quem lê que cabe decidir se deveria.

## 7. O que este relatório decide

`make quality-inventory` **falha** quando uma regra não tem âncora alguma, quando uma citação não resolve e quando o relatório versionado diverge do que a árvore gera. Ele **não** falha por órfão: a lista é o achado, e transformá-la em portão exigiria uma política que a fase não deu.
